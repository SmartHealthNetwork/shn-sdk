package shnsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// Each leg has two names in the window between wire protocols 1.1.0 and
// 2.0.0: its flat name and the same name with its workstream prefix. The SDK
// compares a leg by either and sends its prefixed name.

func TestSameLeg(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"pas-claim", "pas-claim", true},
		{"pas-claim", "pa.pas-claim", true},
		{"pa.pas-claim", "pas-claim", true},
		{"pa.pas-claim", "pa.pas-claim", true},
		{"federated-query", "fqs.federated-query", true},
		{"pas-claim", "pas-claim-update", false},
		{"pa.pas-claim", "pa.pas-claim-update", false},
		{"pa.federated-query", "federated-query", false}, // the wrong prefix names no leg
		{"pas-claim", "pa.nowhere", false},
		{"", "", false},
	} {
		if got := sameLeg(c.a, c.b); got != c.want {
			t.Errorf("sameLeg(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDiscovery_SharesWireProtocol(t *testing.T) {
	for _, c := range []struct {
		name     string
		disc     Discovery
		versions []string
		want     bool
	}{
		{"the network's version", Discovery{WireProtocolVersion: "1.1.0"}, []string{"1.1.0"}, true},
		{"a version the network accepts", Discovery{WireProtocolVersion: "2.0.0", WireProtocolVersionsAccepted: []string{"1.1.0", "2.0.0"}}, []string{"1.1.0"}, true},
		{"one of several the SDK speaks", Discovery{WireProtocolVersion: "2.0.0", WireProtocolVersionsAccepted: []string{"2.0.0"}}, []string{"1.1.0", "2.0.0"}, true},
		{"none shared", Discovery{WireProtocolVersion: "2.0.0", WireProtocolVersionsAccepted: []string{"2.0.0"}}, []string{"1.1.0"}, false},
		{"accepted names other versions only", Discovery{WireProtocolVersion: "3.0.0", WireProtocolVersionsAccepted: []string{"2.0.0"}}, []string{"1.1.0"}, false},
		{"a descriptor with no version", Discovery{}, []string{""}, false},
		{"no versions", Discovery{WireProtocolVersion: "1.1.0"}, nil, false},
	} {
		if got := c.disc.SharesWireProtocol(c.versions); got != c.want {
			t.Errorf("%s: SharesWireProtocol(%q) = %v, want %v", c.name, c.versions, got, c.want)
		}
	}
	// The SDK's own versions are shared with a network that speaks today's.
	if !(Discovery{WireProtocolVersion: WireProtocolVersion}).SharesWireProtocol(WireProtocolVersions()) {
		t.Fatal("the SDK does not share a wire version with a network speaking WireProtocolVersion")
	}
}

// legAnswer is what a Responder's answer to one leg says about how it handled
// the leg: the HTTP status, the name the answer gives the leg, the operation
// its response leg is authorized for, and the frame's status, headers and body.
type legAnswer struct {
	status       int
	name, respOp string
	frameStatus  int
	frameHeaders map[string]string
	frameBody    string
}

func answerLeg(t *testing.T, h *paTestHarness, srv *httptest.Server, name, op, corr string, payload []byte) legAnswer {
	t.Helper()
	envBytes, hubHdr := h.buildForwardEnv(t, name, op, corr, payload)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	a := legAnswer{status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		a.frameBody = string(body)
		return a
	}
	env, err := DecodeEnvelope(body)
	if err != nil {
		t.Fatalf("%s: decode answer: %v", name, err)
	}
	a.name = env.Metadata.TransactionType
	var tok Token
	if err := json.Unmarshal([]byte(env.Metadata.AuthzToken), &tok); err != nil {
		t.Fatalf("%s: answer token: %v", name, err)
	}
	a.respOp = tok.Operation
	hdr, frameBody, err := DecodeHTTPFrame(h.openResponse(t, body))
	if err != nil {
		t.Fatalf("%s: answer frame: %v", name, err)
	}
	a.frameStatus, a.frameHeaders, a.frameBody = hdr.Status, hdr.Headers, string(frameBody)
	return a
}

// TestResponder_EitherLegName: the Responder handles a request naming its leg
// flat or prefixed as the same leg: the same request operation is pinned, the
// same handler answers, the same contract version is stamped, and the response
// leg is authorized for the same operation. The answer names the leg as the
// request did.
func TestResponder_EitherLegName(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &paTestAdjudicator{now: h.now}
	srv := h.refusalResponder(t, responderIdent, adj, func(string) []string { return []string{MessageFrameV1} }, nil)

	for i, flat := range slices.Sorted(maps.Keys(responderReqOp)) {
		prefixed, ok := PrefixedTransactionType(flat)
		if !ok || prefixed == flat {
			t.Fatalf("fixture: %s has no prefixed name", flat)
		}
		got := map[string]legAnswer{}
		for _, name := range []string{flat, prefixed} {
			got[name] = answerLeg(t, h, srv, name, responderReqOp[flat], fmt.Sprintf("leg-%d-%s", i, name), []byte(`{}`))
		}
		a, b := got[flat], got[prefixed]
		if a.status != http.StatusOK || a.respOp != responseOp(flat) {
			t.Fatalf("%s: fixture: the flat request was not answered on its response leg: %+v", flat, a)
		}
		if a.name != flat || b.name != prefixed {
			t.Errorf("%s: the answers name the leg %q and %q, want %q and %q", flat, a.name, b.name, flat, prefixed)
		}
		a.name, b.name = "", ""
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("%s: the leg is handled differently by its name\n flat:     %+v\n prefixed: %+v", flat, a, b)
		}
	}
	// A success is stamped with the leg's contract version under either name.
	stamps := map[string]string{}
	for _, name := range []string{"crd-order-select", "pa.crd-order-select"} {
		a := answerLeg(t, h, srv, name, "crd-order-select", "leg-crd-stamp-"+name, buildConformantCRD(t, "MBR-001", "72148"))
		if a.frameStatus/100 != 2 {
			t.Fatalf("fixture: the CRD request named %s did not succeed: %+v", name, a)
		}
		stamps[name] = a.frameHeaders[FrameHeaderContractVersion]
	}
	if stamps["crd-order-select"] == "" || stamps["crd-order-select"] != stamps["pa.crd-order-select"] {
		t.Errorf("the CRD success is stamped %q flat and %q prefixed, want the leg's contract version for both", stamps["crd-order-select"], stamps["pa.crd-order-select"])
	}
}

// TestResponder_LegNameRefusals: a name that is neither form of a leg this
// Responder serves is refused as today, before any token work; the operation
// header stays the DTR leg's under its prefixed name too.
func TestResponder_LegNameRefusals(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &paTestAdjudicator{now: h.now}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	for i, name := range []string{"", "pa.federated-query", "fqs.federated-query", "pa.nowhere", "pa.", "PA.pas-claim", "pa.pa.pas-claim"} {
		envBytes, hubHdr := h.buildForwardEnv(t, name, "pas-submit", fmt.Sprintf("refuse-%d", i), []byte(`{}`))
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "unknown transaction type")
	}

	req := orderSelectFromRecords(t, []byte(`{"resourceType":"ServiceRequest","id":"sr","subject":{"reference":"Patient/p1"}}`), "p1")
	envBytes, hubHdr := h.buildForwardEnv(t, "pa.crd-order-select", "crd-order-select", "crd-op-prefixed", frameDTROp(t, FrameOperationQuestionnairePackage, req))
	resp := postInbound(t, srv, envBytes, hubHdr)
	assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "operation header is not defined for this transaction type")

	pkg := packageRequest(t, QuestionnairePackageInputs{
		Coverages:      [][]byte{pkgCoverage("c1", "Patient/p1")},
		Orders:         [][]byte{pkgOrder("o1", "Patient/p1")},
		Questionnaires: []string{SupportedQuestionnaireCanonical},
		Context:        "assert-1",
	})
	answers := map[string]string{}
	for _, name := range []string{"dtr-questionnaire-fetch", "pa.dtr-questionnaire-fetch"} {
		envBytes, hubHdr := h.buildForwardEnv(t, name, "dtr-questionnaire-fetch", "dtr-op-"+name, frameDTROp(t, FrameOperationQuestionnairePackage, pkg))
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s with the operation header: %d %s, want the leg answered", name, resp.StatusCode, body)
		}
		answer := h.openResponse(t, body)
		if _, err := ExtractQuestionnaireFromPackage(answer); err != nil {
			t.Fatalf("%s with the operation header: the answer is not a package: %v", name, err)
		}
		answers[name] = string(answer)
	}
	if answers["dtr-questionnaire-fetch"] != answers["pa.dtr-questionnaire-fetch"] {
		t.Error("the DTR operation is answered differently by the leg's name")
	}
}

// TestOriginators_AnswerInEitherLegName: an originator accepts an answer that
// names its leg in either form, and refuses one that names another leg.
func TestOriginators_AnswerInEitherLegName(t *testing.T) {
	signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	id, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatal(err)
	}
	id.Clock = func() time.Time { return now }

	eligibility := func(answerName string) error {
		f := &fakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", covered: true, now: now,
			requesterEnc: id.EncPub, answerName: answerName}
		authz := httptest.NewServer(http.HandlerFunc(f.authorizeHandler))
		defer authz.Close()
		hub := httptest.NewServer(f.routeHandler())
		defer hub.Close()
		_, _, err := id.RunEligibility(context.Background(), authz.Client(), Endpoints{HubURL: hub.URL, AuthzURL: authz.URL},
			Payer{ID: "payer", Identity: CMSPayerIdentity, EncPub: payerPub, AuthzPub: signPub}, "9999999999", "MBR-1", "1975-04-02", "Johansson")
		return err
	}
	for _, name := range []string{"coverage-eligibility", "pa.coverage-eligibility"} {
		if err := eligibility(name); err != nil {
			t.Errorf("RunEligibility, answer named %q: %v", name, err)
		}
	}
	for _, name := range []string{"pas-claim", "pa.pas-claim", "fqs.coverage-eligibility", "pa.nowhere"} {
		if err := eligibility(name); err == nil || !strings.Contains(err.Error(), "response transactionType") {
			t.Errorf("RunEligibility, answer named %q: err = %v, want the answer's leg refused", name, err)
		}
	}

	priorAuth := func(answerName func(string) string) error {
		f := &paFakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", now: now, paRequired: true, answerName: answerName}
		pid, ep, payer, _ := newPATestRig(t, f)
		res, err := pid.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
		if err == nil && res.Outcome != "approved" {
			return fmt.Errorf("outcome %q", res.Outcome)
		}
		return err
	}
	if err := priorAuth(func(tx string) string { p, _ := PrefixedTransactionType(tx); return p }); err != nil {
		t.Errorf("RunPriorAuth, every answer prefixed: %v", err)
	}
	if err := priorAuth(func(tx string) string {
		if tx == "pas-claim" {
			return "pa.pas-claim-update"
		}
		return tx
	}); err == nil || !strings.Contains(err.Error(), "response transactionType") {
		t.Errorf("RunPriorAuth, the PAS answer naming another leg: err = %v, want it refused", err)
	}
}

// TestOriginators_SendPrefixedNames: every leg an originator sends names its
// leg by its workstream-prefixed name, and the outcome is the one the flat
// name reached.
func TestOriginators_SendPrefixedNames(t *testing.T) {
	signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	id, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatal(err)
	}
	id.Clock = func() time.Time { return now }

	f := &fakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", covered: true, now: now,
		requesterEnc: id.EncPub}
	authz := httptest.NewServer(http.HandlerFunc(f.authorizeHandler))
	defer authz.Close()
	hub := httptest.NewServer(f.routeHandler())
	defer hub.Close()
	covered, _, err := id.RunEligibility(context.Background(), authz.Client(), Endpoints{HubURL: hub.URL, AuthzURL: authz.URL},
		Payer{ID: "payer", Identity: CMSPayerIdentity, EncPub: payerPub, AuthzPub: signPub}, "9999999999", "MBR-1", "1975-04-02", "Johansson")
	if err != nil || !covered {
		t.Fatalf("RunEligibility: covered=%v err=%v", covered, err)
	}
	if f.statedName != "pa.coverage-eligibility" {
		t.Errorf("RunEligibility sent %q, want pa.coverage-eligibility", f.statedName)
	}

	pa := &paFakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", now: now, paRequired: true}
	pid, ep, payer, _ := newPATestRig(t, pa)
	res, err := pid.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, demoPARequest())
	if err != nil || res.Outcome != "approved" {
		t.Fatalf("RunPriorAuth: outcome=%q err=%v", res.Outcome, err)
	}
	want := []string{"pa.crd-order-select", "pa.dtr-questionnaire-fetch", "pa.pas-claim"}
	if !slices.Equal(pa.statedNames, want) {
		t.Errorf("RunPriorAuth sent %v, want %v", pa.statedNames, want)
	}
}
