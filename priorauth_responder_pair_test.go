package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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
// Responder requires, and forwards it to the Responder's inbound handler with one change, the
// one the real Hub makes: the leg's name, in either form, goes on as the flat name. It relays
// the answer back with the name the client stated restored, as the real Hub does.
//
// It also keeps a copy of each leg's plaintext, opened with the RESPONDER's own keys. That is
// not the Hub reading mail it cannot read — the real Hub is payload-blind and this one changes
// no payload byte — it is the test standing at the payer's door with the payer's key so it can assert
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
	// stated is each request's transaction type as the client stated it, in
	// order, before the stand-in resolves it.
	stated []string
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
	// As the network's Hub does: resolve the stated name to its leg, forward
	// the flat name, and return the answer under the name the requester
	// stated. Envelope metadata is not signed, so the rewrite breaks nothing.
	stated := env.Metadata.TransactionType
	<-p.mu
	p.stated = append(p.stated, stated)
	p.mu <- struct{}{}
	if flat, ok := FlatTransactionType(stated); ok {
		env.Metadata.TransactionType = flat
		if body, err = EncodeEnvelope(env); err != nil {
			http.Error(w, "encode", http.StatusInternalServerError)
			return
		}
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
	if respEnv, derr := DecodeEnvelope(answer); derr == nil && resp.StatusCode == http.StatusOK {
		// The real Hub refuses an answer that names another leg than its
		// request's, before it rewrites the name.
		if !sameLeg(respEnv.Metadata.TransactionType, stated) {
			http.Error(w, "response leg transaction-type mismatch", http.StatusBadGateway)
			return
		}
		respEnv.Metadata.TransactionType = stated
		if answer, err = EncodeEnvelope(respEnv); err != nil {
			http.Error(w, "encode", http.StatusInternalServerError)
			return
		}
	}
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

// countOfType counts the bundle's entries of the given resourceType.
func (b pairBundle) countOfType(want string) int {
	n := 0
	for _, e := range b.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(e.Resource, &probe) == nil && probe.ResourceType == want {
			n++
		}
	}
	return n
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

// claimHasInfoChangedOnAnyItem reports whether any Claim.item carries the PAS
// infoChanged item extension (the marker a payer re-evaluates an item on).
func claimHasInfoChangedOnAnyItem(t *testing.T, claim map[string]any) bool {
	t.Helper()
	items, _ := claim["item"].([]any)
	if len(items) == 0 {
		t.Fatal("Claim carries no item[]")
	}
	for _, it := range items {
		im, _ := it.(map[string]any)
		exts, _ := im["extension"].([]any)
		for _, e := range exts {
			em, _ := e.(map[string]any)
			if url, _ := em["url"].(string); strings.Contains(url, "infoChanged") {
				return true
			}
		}
	}
	return false
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
		ClaimFacts: testClaimFacts(),
		Member:     "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
		Provider:         testRequestingProvider(),
		Patient:          testMemberPatient("MBR-001"),
		Coverage:         testMemberCoverageSearch("MBR-001"),
		MemberIDSystem:   MemberSystem,
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
		*res.Resume, demoSupplementalReport(res.Resume.PatientRef))
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

	t.Run("every-leg-is-sent-prefixed", func(t *testing.T) {
		<-hub.mu
		got := slices.Clone(hub.stated)
		hub.mu <- struct{}{}
		want := []string{"pa.crd-order-select", "pa.dtr-questionnaire-fetch", "pa.pas-claim", "pa.pas-claim-update"}
		if !slices.Equal(got, want) {
			t.Errorf("the client sent %v, want %v", got, want)
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

		// The resume amends at PAS 2.0, whose request Bundle allows one Claim: the update
		// names the authorization it amends by the submission's correlation identifier in
		// Claim.related[0].claim, and the payer looks it up in its own records. No prior
		// Claim is carried, and nothing names one.
		related, _ := claim["related"].([]any)
		if len(related) == 0 {
			t.Fatal("amended Claim carries no related[] — the payer cannot find the authorization being amended")
		}
		rel0, _ := related[0].(map[string]any)
		relClaim, _ := rel0["claim"].(map[string]any)
		if ref, ok := relClaim["reference"]; ok {
			t.Errorf("Claim.related[0].claim.reference = %v, want none: PAS 2.0 names the prior by identifier; entries are %v", ref, keysOf(urls))
		}
		relID, _ := relClaim["identifier"].(map[string]any)
		if relID["system"] != "urn:shn:correlation" || relID["value"] != res.Resume.OriginalCorrelationID {
			t.Errorf("Claim.related[0].claim.identifier = %v, want urn:shn:correlation|%s, the submission's", relID, res.Resume.OriginalCorrelationID)
		}
		if n := b.countOfType("Claim"); n != 1 {
			t.Errorf("the amendment carries %d Claims, want the update alone", n)
		}

		// The infoChanged marker is derived from the submission, item by item:
		// this amendment re-sends the submitted item exactly as it was (its
		// new information is the operative report, which is not an item and
		// cannot carry the marker), so no item carries it. The reference payer
		// re-evaluates only items that carry it (hasInfoChanged), so it answers
		// this amendment with the decision it already holds for the item.
		if claimHasInfoChangedOnAnyItem(t, claim) {
			t.Error("an amended Claim.item sent before exactly as now carries infoChanged")
		}
		// The pend asks item 1 for a LOINC operative note; the report the
		// resume attaches is another LOINC report. The payer's own pend ties it to no
		// item, so nothing is linked.
		if _, ok := claim["supportingInfo"]; ok {
			t.Errorf("an answer the pend did not ask for is linked: supportingInfo %v", claim["supportingInfo"])
		}
	})

	t.Run("handle-keeps-the-payers-pend", func(t *testing.T) {
		pended, detail, err := ParsePendedResponseDetail(res.Resume.PendedResponse)
		if err != nil || !pended || len(detail.Tasks) == 0 || len(detail.Tasks[0].Items) == 0 || detail.Tasks[0].Items[0].Sequence != 1 {
			t.Fatalf("the resume handle does not keep the payer's pend as received: pended=%v err=%v detail=%+v", pended, err, detail)
		}
	})
}

// TestPriorAuthClientIntoResponder_ResumeLinksTheAskedReport: the payer's pend
// asks item 1 for exactly the report the resume attaches: the pend the handle
// keeps names the code the caller's report states. ResumePriorAuth's amendment links the
// report to item 1 through a new supportingInfo entry, and item 1 carries
// infoChanged "added".
func TestPriorAuthClientIntoResponder_ResumeLinksTheAskedReport(t *testing.T) {
	h, responderIdent, senderIdent := newPAHarness(t)
	_, responderSrv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
	hub := newPairHub(t, h, responderSrv, responderIdent)
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", hub.route)
	hubSrv := httptest.NewServer(hubMux)
	t.Cleanup(hubSrv.Close)
	ep := Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL}
	payer := Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub}
	req := PriorAuthRequest{
		ClaimFacts: testClaimFacts(),
		Member:     "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
		Provider: testRequestingProvider(), Patient: testMemberPatient("MBR-001"), Coverage: testMemberCoverageSearch("MBR-001"),
		MemberIDSystem: MemberSystem, Clinical: DemoLumbarContextPriorSurgery(),
		ProcedureSystem: systemHCPCS, ProcedureCPT: "G0151", ProcedureDisplay: "Home health physical therapy, each 15 minutes",
		DiagnosisICD10: "M54.16",
	}
	res, err := senderIdent.RunPriorAuth(context.Background(), hubSrv.Client(), ep, payer, req)
	if err != nil || res.Outcome != "pended" || res.Resume == nil {
		t.Fatalf("submit: outcome=%q err=%v", res.Outcome, err)
	}
	supp := demoSupplementalReport(res.Resume.PatientRef)
	var own struct {
		Code struct{ Coding []PASCoding } `json:"code"`
	}
	if err := json.Unmarshal(supp.Report, &own); err != nil || len(own.Code.Coding) == 0 {
		t.Fatalf("the report states no code: %v", err)
	}
	var pend map[string]any
	if err := json.Unmarshal(res.Resume.PendedResponse, &pend); err != nil {
		t.Fatal(err)
	}
	restated := 0
	for _, e := range pend["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		if r["resourceType"] != "Task" {
			continue
		}
		for _, in := range r["input"].([]any) {
			cc, _ := in.(map[string]any)["valueCodeableConcept"].(map[string]any)
			codings, _ := cc["coding"].([]any)
			for _, c := range codings {
				if cm := c.(map[string]any); cm["code"] == testOperativeNote.Code {
					cm["system"], cm["code"] = own.Code.Coding[0].System, own.Code.Coding[0].Code
					restated++
				}
			}
		}
	}
	if restated == 0 {
		t.Fatal("the pend names no operative-note attachment to restate")
	}
	res.Resume.PendedResponse, _ = json.Marshal(pend)
	if _, err := senderIdent.ResumePriorAuth(context.Background(), hubSrv.Client(), ep, payer, *res.Resume, supp); err != nil {
		t.Fatalf("resume: %v", err)
	}
	claim, _ := parsePairBundle(t, hub.leg(t, "pas-claim-update")).resourceOfType(t, "Claim")
	infos, _ := claim["supportingInfo"].([]any)
	items, _ := claim["item"].([]any)
	if len(infos) != 1 || len(items) != 1 {
		t.Fatalf("supportingInfo %v items %d, want one entry linked to the one item", infos, len(items))
	}
	ref, _ := infos[0].(map[string]any)["valueReference"].(map[string]any)["reference"].(string)
	if !strings.HasSuffix(ref, "/DiagnosticReport/"+conformantPASDRID) {
		t.Errorf("supportingInfo references %q, want the attached report", ref)
	}
	item := items[0].(map[string]any)
	if seq, _ := item["informationSequence"].([]any); len(seq) != 1 || seq[0] != float64(1) {
		t.Errorf("item 1 informationSequence %v, want [1]", item["informationSequence"])
	}
	var code string
	for _, e := range item["extension"].([]any) {
		if em := e.(map[string]any); em["url"] == pasInfoChangedExtensionURL {
			code, _ = em["valueCode"].(string)
		}
	}
	if code != pasInfoChangedAdded {
		t.Errorf("item 1 infoChanged %q, want added", code)
	}
}

// TestResumePriorAuthCarriesTheCallersReport is the tripwire for minted evidence: the
// DiagnosticReport an amendment carries to the payer is the caller's own, with nothing the
// caller did not supply. Its only changes are the bundle builder's own edits: the
// bundle-local id, the dropped meta.profile, and its subject reference made absolute against the
// bundle's base, as every reference in the bundle is. A report that states no effective date or
// category reaches the payer stating none.
func TestResumePriorAuthCarriesTheCallersReport(t *testing.T) {
	full := func(patientRef string) []byte { return testOwnReport("dr-own", patientRef) }
	bare := func(patientRef string) []byte {
		return []byte(`{"resourceType":"DiagnosticReport","id":"dr-own","status":"final",` +
			`"code":{"text":"operative report"},"subject":{"reference":"` + patientRef + `"}}`)
	}
	noID := func(patientRef string) []byte {
		return []byte(`{"resourceType":"DiagnosticReport","status":"final",` +
			`"code":{"text":"operative report"},"subject":{"reference":"` + patientRef + `"}}`)
	}
	for name, report := range map[string]func(string) []byte{"a full report": full, "a report stating no date or category": bare, "a report with no id": noID} {
		t.Run(name, func(t *testing.T) {
			h, responderIdent, senderIdent := newPAHarness(t)
			_, responderSrv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
			hub := newPairHub(t, h, responderSrv, responderIdent)
			hubMux := http.NewServeMux()
			hubMux.HandleFunc("/route", hub.route)
			hubSrv := httptest.NewServer(hubMux)
			t.Cleanup(hubSrv.Close)
			ep := Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL}
			payer := Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub}
			req := PriorAuthRequest{
				ClaimFacts: testClaimFacts(),
				Member:     "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
				Provider: testRequestingProvider(), Patient: testMemberPatient("MBR-001"),
				Coverage: testMemberCoverageSearch("MBR-001"), MemberIDSystem: MemberSystem,
				Clinical: DemoLumbarContextPriorSurgery(), ProcedureSystem: systemHCPCS,
				ProcedureCPT: "G0151", ProcedureDisplay: "Home health physical therapy, each 15 minutes",
				DiagnosisICD10: "M54.16",
			}
			res, err := senderIdent.RunPriorAuth(context.Background(), hubSrv.Client(), ep, payer, req)
			if err != nil || res.Outcome != "pended" || res.Resume == nil {
				t.Fatalf("submit: outcome %q, err %v", res.Outcome, err)
			}
			own := report(res.Resume.PatientRef)
			if _, err := senderIdent.ResumePriorAuth(context.Background(), hubSrv.Client(), ep, payer, *res.Resume,
				SupplementalReport{Report: own, ProvenanceAgent: ProvenanceIdentifier{System: "http://smarthealth.network/ids/holder", Value: "provider"}}); err != nil {
				t.Fatalf("resume: %v", err)
			}
			sent, _ := parsePairBundle(t, hub.leg(t, "pas-claim-update")).resourceOfType(t, "DiagnosticReport")
			var want map[string]any
			if err := json.Unmarshal(own, &want); err != nil {
				t.Fatal(err)
			}
			want["id"] = sent["id"]
			delete(want, "meta")
			sentSubject, _ := sent["subject"].(map[string]any)
			sentRef, _ := sentSubject["reference"].(string)
			if !strings.HasSuffix(sentRef, "/"+res.Resume.PatientRef) {
				t.Fatalf("the amendment's DiagnosticReport is about %q, not the caller's %q", sentRef, res.Resume.PatientRef)
			}
			want["subject"] = map[string]any{"reference": sentRef}
			if !reflect.DeepEqual(sent, want) {
				t.Fatalf("the amendment's DiagnosticReport is not the caller's own:\n sent %v\n own  %v", sent, want)
			}
			for _, k := range []string{"effectiveDateTime", "category"} {
				if _, stated := want[k]; !stated {
					if _, ok := sent[k]; ok {
						t.Errorf("the SDK filled in %s, which the caller's report does not state: %v", k, sent[k])
					}
				}
			}
		})
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestResumePriorAuth_PreReleaseHandleRefused: a resume handle written before
// shn-sdk v0.62.0 carries no claimFacts and no submission record. Resuming it
// would amend with facts this package supplies, so ResumePriorAuth refuses it
// before anything is built or sent, naming what the handle lacks — never
// amends with a default.
func TestResumePriorAuth_PreReleaseHandleRefused(t *testing.T) {
	h, responderIdent, senderIdent := newPAHarness(t)
	_, responderSrv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
	hub := newPairHub(t, h, responderSrv, responderIdent)
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", hub.route)
	hubSrv := httptest.NewServer(hubMux)
	t.Cleanup(hubSrv.Close)
	ep := Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL}
	payer := Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub}
	req := PriorAuthRequest{
		ClaimFacts: testClaimFacts(),
		Member:     "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
		Provider: testRequestingProvider(), Patient: testMemberPatient("MBR-001"), Coverage: testMemberCoverageSearch("MBR-001"),
		MemberIDSystem: MemberSystem, Clinical: DemoLumbarContextPriorSurgery(),
		ProcedureSystem: systemHCPCS, ProcedureCPT: "G0151", ProcedureDisplay: "Home health physical therapy, each 15 minutes", DiagnosisICD10: "M54.16",
	}
	res, err := senderIdent.RunPriorAuth(context.Background(), hubSrv.Client(), ep, payer, req)
	if err != nil || res.Resume == nil {
		t.Fatalf("pend: %v (resume %v)", err, res.Resume)
	}
	for name, tc := range map[string]struct {
		strip func(*PriorAuthResume)
		want  string
	}{
		"no claimFacts": {func(r *PriorAuthResume) { r.ClaimFacts = nil },
			"pas-update-submit: Claim.type, Claim.priority and Claim.item.category " + ErrPASClaimFactAbsent.Error() + " (expected from the resume handle's claimFacts, which a handle written before shn-sdk v0.62.0 does not carry)"},
		"no submission": {func(r *PriorAuthResume) { r.Submission = nil },
			"pas-update-submit: the prior Claim's type, priority, created and items " + ErrPASClaimFactAbsent.Error() + " (expected from the resume handle's submission record, which a handle written before shn-sdk v0.62.0 does not carry)"},
	} {
		t.Run(name, func(t *testing.T) {
			handle := *res.Resume
			// The handle as an earlier release wrote it: the JSON round trip
			// a stored handle takes, without the member it lacks.
			raw, err := json.Marshal(handle)
			if err != nil {
				t.Fatal(err)
			}
			var stored PriorAuthResume
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			tc.strip(&stored)
			// No endpoints: a refusal that came after anything was sent
			// would fail on the wire instead, with another error.
			_, err = senderIdent.ResumePriorAuth(context.Background(), nil, Endpoints{}, Payer{}, stored, demoSupplementalReport(handle.PatientRef))
			if err == nil || !errors.Is(err, ErrPASClaimFactAbsent) || err.Error() != tc.want {
				t.Fatalf("err = %v\nwant %s", err, tc.want)
			}
		})
	}
}

// rePendingAdjudicator decides the submit and every later amendment by the test
// policy, but pends the first amendment (its second call): a payer that still needs
// more after the first answer.
type rePendingAdjudicator struct {
	paTestAdjudicator
	calls int
}

func (a *rePendingAdjudicator) PriorAuth(qrJSON []byte, hasDR bool) (PASDecision, error) {
	a.calls++
	if a.calls == 2 { // the submit is call 1; the first amendment is call 2
		return testPendedDecision(), nil
	}
	return a.paTestAdjudicator.PriorAuth(qrJSON, hasDR)
}

// TestPriorAuthClientIntoResponder_RePendedResumesAgain drives a prior authorization the
// payer pends twice: on the submit, and again on the first amendment. The handle the
// re-pended ResumePriorAuth returns must resume again — it carries the requesting provider's
// and the member's coverage records the submission named, which the amendment must name
// again (a handle without them is refused before anything is sealed).
func TestPriorAuthClientIntoResponder_RePendedResumesAgain(t *testing.T) {
	h, responderIdent, senderIdent := newPAHarness(t)
	_, responderSrv := h.makeResponderSrv(t, responderIdent, &rePendingAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}})

	hub := newPairHub(t, h, responderSrv, responderIdent)
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", hub.route)
	hubSrv := httptest.NewServer(hubMux)
	t.Cleanup(hubSrv.Close)

	ep := Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL}
	payer := Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub}
	req := PriorAuthRequest{
		ClaimFacts: testClaimFacts(),
		Member:     "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
		Provider:         testRequestingProvider(),
		Patient:          testMemberPatient("MBR-001"),
		Coverage:         testMemberCoverageSearch("MBR-001"),
		MemberIDSystem:   MemberSystem,
		Clinical:         DemoLumbarContextPriorSurgery(),
		ProcedureSystem:  systemHCPCS,
		ProcedureCPT:     "G0151",
		ProcedureDisplay: "Home health physical therapy, each 15 minutes",
		DiagnosisICD10:   "M54.16",
	}

	res, err := senderIdent.RunPriorAuth(context.Background(), hubSrv.Client(), ep, payer, req)
	if err != nil {
		t.Fatalf("RunPriorAuth: %v", err)
	}
	if res.Outcome != "pended" || res.Resume == nil {
		t.Fatalf("submit = %q (resume %v), want pended with a resume handle", res.Outcome, res.Resume != nil)
	}
	// Each amendment is made a minute after the last, so the submit's own record
	// and an amendment's differ.
	senderIdent.Clock = func() time.Time { return h.now.Add(time.Minute) }
	again, err := senderIdent.ResumePriorAuth(context.Background(), hubSrv.Client(), ep, payer,
		*res.Resume, demoSupplementalReport(res.Resume.PatientRef))
	if err != nil {
		t.Fatalf("first ResumePriorAuth: %v", err)
	}
	if again.Outcome != "pended" || again.Resume == nil {
		t.Fatalf("first amendment = %q (resume %v), want pended with a resume handle", again.Outcome, again.Resume != nil)
	}
	if !bytes.Equal(again.Resume.ProviderJSON, res.Resume.ProviderJSON) || !bytes.Equal(again.Resume.CoverageJSON, res.Resume.CoverageJSON) {
		t.Errorf("re-pended handle does not carry the submission's provider and coverage records")
	}
	// It resumes the pend it was handed: the amendment's own pended answer.
	if bytes.Equal(again.Resume.PendedResponse, res.Resume.PendedResponse) || len(again.Resume.PendedResponse) == 0 {
		t.Errorf("re-pended handle does not carry the amendment's pended answer")
	}
	senderIdent.Clock = func() time.Time { return h.now.Add(2 * time.Minute) }
	final, err := senderIdent.ResumePriorAuth(context.Background(), hubSrv.Client(), ep, payer,
		*again.Resume, demoSupplementalReport(again.Resume.PatientRef))
	if err != nil {
		t.Fatalf("second ResumePriorAuth with the re-pended handle: %v", err)
	}
	if final.Outcome != "approved" {
		t.Fatalf("second amendment = %q, want approved", final.Outcome)
	}

	// Every amendment continues the original submission, as a Smart Gateway's own
	// amendment chain does (each amendment names the submit's correlation as the one
	// it amends): the re-pended handle keeps the submission record and the original
	// correlation, and the second amendment names the submit, not the first
	// amendment.
	if !reflect.DeepEqual(again.Resume.Submission, res.Resume.Submission) || again.Resume.OriginalCorrelationID != res.Resume.OriginalCorrelationID {
		t.Errorf("re-pended handle does not continue the original submission: submission %+v, correlation %q; want %+v, %q",
			again.Resume.Submission, again.Resume.OriginalCorrelationID, res.Resume.Submission, res.Resume.OriginalCorrelationID)
	}
	second := parsePairBundle(t, hub.leg(t, "pas-claim-update"))
	claim, _ := second.resourceOfType(t, "Claim")
	related, _ := claim["related"].([]any)
	if len(related) == 0 {
		t.Fatal("the second amendment's Claim carries no related[]")
	}
	rel0, _ := related[0].(map[string]any)
	relClaim, _ := rel0["claim"].(map[string]any)
	relID, _ := relClaim["identifier"].(map[string]any)
	if relID["system"] != "urn:shn:correlation" || relID["value"] != res.Resume.OriginalCorrelationID {
		t.Errorf("the second amendment names %v, want the submit's correlation %q", relID, res.Resume.OriginalCorrelationID)
	}
}
