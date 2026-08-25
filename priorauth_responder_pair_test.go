package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// priorauth_responder_pair_test.go — the PAIRING nothing else in this module covers: the
// published PA CLIENT (RunPriorAuth / ResumePriorAuth) driven into the published Responder,
// in-process, over a Hub that behaves like the real one.
//
// Why it has to exist. Every other test on each side supplies its OWN half. The client tests
// (priorauth_test.go) answer the client from a fake substrate that fabricates payer responses
// and never runs the Responder. The Responder tests (responder_pa_test.go) feed the Responder
// request bytes assembled by test helpers that re-implement the client's bundle shape rather
// than calling the client. So a change to what the client EMITS can break what the Responder
// ACCEPTS and both suites stay green — which is exactly what happened: the client began
// emitting absolute bundle references and the Responder's own subject fence refused them with
// 403, caught only by a Docker-gated deployment step.
//
// The two things this pins are therefore different in kind:
//
//  1. The pair still round-trips. Client → Hub → Responder → client, CRD then DTR then PAS,
//     pended, then resumed to approved. That is the half that catches a fence which refuses
//     our own client.
//  2. The bytes the client puts on the wire carry what a REAL Da Vinci payer requires to
//     answer them at all. That is the half a mock Responder can never catch, because a mock
//     accepts whatever it is handed. Each assertion below corresponds to a specific 400 a
//     live reference payer returned when the property was missing, named at its assertion.

// pairHub is a Hub stand-in that behaves like the real one on the path that matters: it takes
// the sealed envelope the client POSTs to /route, attaches the transport assertion the
// Responder requires, forwards the bytes UNCHANGED to the Responder's inbound handler, and
// relays the answer back verbatim.
//
// It also keeps a copy of each leg's plaintext, opened with the RESPONDER's own keys. That is
// not the Hub reading mail it cannot read — the real Hub is payload-blind and this one changes
// nothing — it is the test standing at the payer's door with the payer's key so it can assert
// on the bytes the payer actually received. Asserting on what the client BUILT instead would
// let a mutation between build and send pass unnoticed.
type pairHub struct {
	t           *testing.T
	h           *paTestHarness
	responder   *httptest.Server
	openWith    Identity
	mu          chan struct{} // 1-slot mutex; the client is sequential, this only guards the map
	byLeg       map[string][]byte
	legsInOrder []string
}

func newPairHub(t *testing.T, h *paTestHarness, responder *httptest.Server, responderIdent Identity) *pairHub {
	p := &pairHub{
		t: t, h: h, responder: responder, openWith: responderIdent,
		mu: make(chan struct{}, 1), byLeg: map[string][]byte{},
	}
	p.mu <- struct{}{}
	return p
}

func (p *pairHub) route(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	env, err := DecodeEnvelope(body)
	if err != nil {
		http.Error(w, "decode", http.StatusBadRequest)
		return
	}
	if plain, oerr := Open(env, p.openWith.EncPub, p.openWith.EncPriv); oerr == nil {
		<-p.mu
		leg := env.Metadata.TransactionType
		if _, seen := p.byLeg[leg]; !seen {
			p.legsInOrder = append(p.legsInOrder, leg)
		}
		p.byLeg[leg] = append([]byte(nil), plain...)
		p.mu <- struct{}{}
	}

	req, err := http.NewRequest(http.MethodPost, p.responder.URL+"/substrate/inbound", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Assertion",
		makeHubAssertion(p.t, p.h.hubPriv, "hub", p.h.responderID, p.h.now, 2*time.Minute, "jti-"+env.Metadata.CorrelationID))
	resp, err := p.responder.Client().Do(req)
	if err != nil {
		http.Error(w, "forward", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, MaxRequestBytes))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(answer)
}

// leg returns the plaintext the Responder received for a transaction type.
func (p *pairHub) leg(t *testing.T, txType string) []byte {
	t.Helper()
	<-p.mu
	defer func() { p.mu <- struct{}{} }()
	b, ok := p.byLeg[txType]
	if !ok {
		t.Fatalf("no %q leg reached the responder (legs seen: %v)", txType, p.legsInOrder)
	}
	return b
}

// pairBundle is the subset of a PAS Claim Bundle these assertions read.
type pairBundle struct {
	Entry []struct {
		FullUrl  string          `json:"fullUrl"`
		Resource json.RawMessage `json:"resource"`
	} `json:"entry"`
}

func parsePairBundle(t *testing.T, raw []byte) pairBundle {
	t.Helper()
	var b pairBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("parse bundle: %v", err)
	}
	return b
}

// resourceOfType returns the FIRST entry of the given resourceType, plus its fullUrl.
func (b pairBundle) resourceOfType(t *testing.T, want string) (map[string]any, string) {
	t.Helper()
	for _, e := range b.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(e.Resource, &probe) != nil || probe.ResourceType != want {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(e.Resource, &m); err != nil {
			t.Fatalf("parse %s: %v", want, err)
		}
		return m, e.FullUrl
	}
	t.Fatalf("bundle carries no %s entry", want)
	return nil, ""
}

// fullUrls returns every entry fullUrl in the bundle — the set a payer resolves an absolute
// bundle-internal reference against.
func (b pairBundle) fullUrls() map[string]bool {
	set := map[string]bool{}
	for _, e := range b.Entry {
		if e.FullUrl != "" {
			set[e.FullUrl] = true
		}
	}
	return set
}

// refAt reads a nested "…reference" string, e.g. refAt(claim, "insurer").
func refAt(t *testing.T, node map[string]any, field string) string {
	t.Helper()
	sub, _ := node[field].(map[string]any)
	if sub == nil {
		t.Fatalf("no %q object on the resource", field)
	}
	ref, _ := sub["reference"].(string)
	return ref
}

// claimItemProductCode returns Claim.item[0].productOrService's first coding code.
func claimItemProductCode(t *testing.T, claim map[string]any) string {
	t.Helper()
	items, _ := claim["item"].([]any)
	if len(items) == 0 {
		t.Fatal("Claim carries no item[]")
	}
	item0, _ := items[0].(map[string]any)
	pos, _ := item0["productOrService"].(map[string]any)
	codings, _ := pos["coding"].([]any)
	if len(codings) == 0 {
		t.Fatal("Claim.item[0].productOrService carries no coding")
	}
	c0, _ := codings[0].(map[string]any)
	code, _ := c0["code"].(string)
	return code
}

// claimHasInfoChangedOnEveryItem reports whether every Claim.item carries the PAS
// infoChanged item extension — the element that tells a payer the amendment carries NEW
// information and must be re-evaluated rather than answered from the prior decision.
func claimHasInfoChangedOnEveryItem(t *testing.T, claim map[string]any) bool {
	t.Helper()
	items, _ := claim["item"].([]any)
	if len(items) == 0 {
		t.Fatal("Claim carries no item[]")
	}
	for _, it := range items {
		im, _ := it.(map[string]any)
		exts, _ := im["extension"].([]any)
		found := false
		for _, e := range exts {
			em, _ := e.(map[string]any)
			if url, _ := em["url"].(string); strings.Contains(url, "infoChanged") {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestPriorAuthClientIntoResponder_PendedThenResumed drives the published PA client into the
// published Responder and asserts BOTH halves described in this file's header: the pair
// round-trips, and the bytes that reach the payer's door carry what a real Da Vinci payer needs
// in order to answer them.
//
// The order is HCPCS G0151, deliberately NOT the procedure code the Claim builder uses as its
// internal placeholder — otherwise the "the Claim asks for the order that was actually placed"
// assertion would pass no matter what.
func TestPriorAuthClientIntoResponder_PendedThenResumed(t *testing.T) {
	h, responderIdent, senderIdent := newPAHarness(t)
	_, responderSrv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})

	hub := newPairHub(t, h, responderSrv, responderIdent)
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", hub.route)
	hubSrv := httptest.NewServer(hubMux)
	t.Cleanup(hubSrv.Close)

	ep := Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL}
	payer := Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub}

	// A prior surgery with no operative report is what this test binary's policy pends on, so
	// the submit pends and the resume — which attaches the report — approves.
	req := PriorAuthRequest{
		Member: "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
		Clinical:         DemoLumbarContextPriorSurgery(),
		ProcedureSystem:  systemHCPCS,
		ProcedureCPT:     "G0151",
		ProcedureDisplay: "Home health physical therapy, each 15 minutes",
		DiagnosisICD10:   "M54.16",
	}

	// ---- half 1: the pair round-trips ----

	res, err := senderIdent.RunPriorAuth(context.Background(), hubSrv.Client(), ep, payer, req)
	if err != nil {
		t.Fatalf("RunPriorAuth into the published Responder: %v", err)
	}
	if res.Outcome != "pended" {
		t.Fatalf("submit outcome = %q, want pended", res.Outcome)
	}
	if res.Resume == nil {
		t.Fatal("pended result carries no resume handle")
	}

	amended, err := senderIdent.ResumePriorAuth(context.Background(), hubSrv.Client(), ep, payer,
		*res.Resume, demoSupplementalReport())
	if err != nil {
		t.Fatalf("ResumePriorAuth into the published Responder: %v", err)
	}
	if amended.Outcome != "approved" {
		t.Fatalf("after resume outcome = %q, want approved", amended.Outcome)
	}

	// ---- half 2: the bytes carry what a real payer requires ----

	t.Run("crd-prefetch-coverage-names-its-payer", func(t *testing.T) {
		// A payer's coverage-requirements service reads Coverage.payor to decide whose rules
		// apply, and answers 400 "Coverage resource lacks valid payer identifier" when the
		// payor is an Organization reference it cannot identify. This is the FIRST leg, so
		// getting it wrong fails the prior-auth before any of the rest runs.
		covJSON, _, ok := conformantOrderSelectCoverageAndPatient(hub.leg(t, "crd-order-select"))
		if !ok {
			t.Fatal("CRD order-select request carries no Coverage prefetch")
		}
		payer, ok := ParsePayerIdentifier(covJSON, nil)
		if !ok {
			t.Fatalf("Coverage.payor yields no payer identity — a real payer refuses this at leg 1; coverage=%s", covJSON)
		}
		if payer.System == "" || payer.Value == "" {
			t.Errorf("payer identity is %+v, want both system and value populated", payer)
		}
	})

	t.Run("submit-bundle-is-payer-resolvable", func(t *testing.T) {
		b := parsePairBundle(t, hub.leg(t, "pas-claim"))
		urls := b.fullUrls()

		// A real payer resolves Claim.insurer against the bundle it was handed, and answers
		// 400 "Resource Organization/payer not found, specified in path: Claim.insurer" when
		// the reference names nothing in it.
		claim, _ := b.resourceOfType(t, "Claim")
		insurer := refAt(t, claim, "insurer")
		if !urls[insurer] {
			t.Errorf("Claim.insurer = %q resolves to no bundle entry; entries are %v", insurer, keysOf(urls))
		}
		// resourceOfType fails the test when the entry is absent, which is the assertion:
		// the insurer reference above has nothing to resolve TO without it.
		b.resourceOfType(t, "Organization")

		// The Coverage the payer reads to decide whose rules apply must name a payer it can
		// identify; an unidentified payor is answered 400 "Coverage resource lacks valid payer
		// identifier".
		cov, _ := b.resourceOfType(t, "Coverage")
		payors, _ := cov["payor"].([]any)
		if len(payors) == 0 {
			t.Fatal("Coverage carries no payor")
		}
		p0, _ := payors[0].(map[string]any)
		payorRef, _ := p0["reference"].(string)
		if !urls[payorRef] {
			t.Errorf("Coverage.payor = %q resolves to no bundle entry; entries are %v", payorRef, keysOf(urls))
		}

		// A code-keyed payer decides on Claim.item.productOrService. If the builder's internal
		// placeholder survives, the determination that comes back is a determination about a
		// different service, with no error anywhere.
		if got := claimItemProductCode(t, claim); got != "G0151" {
			t.Errorf("Claim.item[0].productOrService = %q, want the ORDER's own code G0151 — the payer would decide on the wrong service", got)
		}
	})

	t.Run("amendment-is-a-conformant-claim-update", func(t *testing.T) {
		b := parsePairBundle(t, hub.leg(t, "pas-claim-update"))
		urls := b.fullUrls()

		claim, _ := b.resourceOfType(t, "Claim")

		// A real payer finds the authorization being amended through
		// Claim.related[].claim.reference, and requires that prior Claim to be present in the
		// bundle — otherwise 400 "The prior Claim referenced in Claim.related.claim must be
		// included in the Bundle".
		related, _ := claim["related"].([]any)
		if len(related) == 0 {
			t.Fatal("amended Claim carries no related[] — the payer cannot find the authorization being amended")
		}
		rel0, _ := related[0].(map[string]any)
		relClaim, _ := rel0["claim"].(map[string]any)
		priorRef, _ := relClaim["reference"].(string)
		if priorRef == "" {
			t.Fatal("Claim.related[0].claim carries no reference — an identifier alone does not resolve for a payer that reads .reference")
		}
		if !urls[priorRef] {
			t.Errorf("Claim.related[0].claim.reference = %q resolves to no bundle entry; entries are %v", priorRef, keysOf(urls))
		}

		// And it must be told the information CHANGED, or it carries the prior decision
		// forward and the amendment comes back still pended.
		if !claimHasInfoChangedOnEveryItem(t, claim) {
			t.Error("amended Claim.item carries no infoChanged extension — a real payer keeps its prior decision and never re-evaluates")
		}
	})
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
