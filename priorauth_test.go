package shnsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// paFakeSubstrate is a stand-in for the Authorization Framework + Hub + payer for
// the three PA legs (CRD order-select, DTR questionnaire-fetch, PAS submit), just
// enough to exercise RunPriorAuth hermetically (no internal/). It mints tokens with
// a test signing key and answers each leg in-process. The cross-module conformance
// test drives the SDK against the REAL substrate; this only proves the SDK's own
// orchestrator wiring without a network.
//
// The per-leg request→response op/frame pinning mirrors the substrate's
// hubsvc.requestOp/responseOp maps so VerifyBound (per leg) exercises the same
// bindings.
type paFakeSubstrate struct {
	signPriv     ed25519.PrivateKey
	payerEnc     *[32]byte
	payerPub     *[32]byte
	payerID      string
	now          time.Time
	requesterEnc *[32]byte

	// paRequired controls the CRD cards verdict. false → no-pa-required short-circuit.
	paRequired bool
	// malformCards makes the CRD leg return non-JSON, to drive a leg-attributed error.
	malformCards bool
}

// responseOpFor mirrors hubsvc.responseOp: the op the response-leg token is pinned
// to, keyed by the request envelope's TransactionType.
var paResponseOp = map[string]string{
	"coverage-eligibility":    "eligibility-response",
	"crd-order-select":        "crd-cards",
	"dtr-questionnaire-fetch": "dtr-questionnaire",
	"pas-claim":               "pas-response",
}

func (f *paFakeSubstrate) mint(tok Token) Token {
	tok.Signature = ed25519.Sign(f.signPriv, tokenSigningPayload(tok))
	return tok
}

func (f *paFakeSubstrate) authorizeHandler(w http.ResponseWriter, r *http.Request) {
	var req AuthorizeRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	holder := "ext-provider"
	if req.Frame == "payer-coverage" {
		holder = f.payerID
	}
	tok := f.mint(Token{
		Operation:     req.Operation,
		Scope:         "coverage",
		Subject:       req.SubjectPCI,
		Frame:         req.Frame,
		CorrelationID: req.CorrelationID,
		Holder:        holder,
		Expiry:        f.now.Add(time.Hour),
	})
	_ = json.NewEncoder(w).Encode(authorizeResp{Token: tok})
}

// payloadFor returns the response payload bytes for an opened request payload on the
// given transaction type. It is the in-process payer adjudicator: CRD → cards, DTR →
// the lumbar-MRI questionnaire, PAS → an approved ClaimResponse.
func (f *paFakeSubstrate) payloadFor(txType string, reqPlain []byte) []byte {
	switch txType {
	case "crd-order-select":
		if f.malformCards {
			return []byte("this is not json cards {{{")
		}
		canon := ""
		if f.paRequired {
			canon = `,"questionnaireCanonical":"` + SupportedQuestionnaireCanonical + `"`
		}
		return []byte(`{"cards":[{"summary":"PA verdict","indicator":"info",` +
			`"extension":{"shnPaRequired":` + boolStr(f.paRequired) + canon + `}}]}`)
	case "dtr-questionnaire-fetch":
		// §6.2: uniform leg shape — the substrate returns a $questionnaire-package
		// collection Bundle wrapping the lumbar-MRI questionnaire the SDK
		// FillQuestionnaire recognizes. RunPriorAuth extracts the bare Questionnaire.
		q := []byte(`{"resourceType":"Questionnaire","url":"` + SupportedQuestionnaireCanonical + `",` +
			`"status":"active","item":[` +
			`{"linkId":"conservative-therapy-weeks","type":"integer"},` +
			`{"linkId":"neuro-deficit","type":"boolean"},` +
			`{"linkId":"prior-imaging","type":"boolean"}]}`)
		pkg, err := BuildQuestionnairePackage(q)
		if err != nil {
			panic("priorauth fake: wrap questionnaire package: " + err.Error())
		}
		return pkg
	case "pas-claim":
		return []byte(`{"resourceType":"ClaimResponse","status":"active","type":{"coding":[{"code":"professional"}]},` +
			`"use":"preauthorization","patient":{"reference":"Patient/X"},"created":"` +
			f.now.UTC().Format(time.RFC3339) + `","insurer":{"reference":"Organization/payer"},` +
			`"outcome":"complete","preAuthRef":"PA-APPROVED-123",` +
			`"preAuthPeriod":{"end":"2026-12-31"}}`)
	}
	return []byte(`{}`)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// routeHandler mimics the Hub+payer for the PA legs: open the request, look up the
// per-leg response op, build the response payload, seal it back to the requester,
// and mint a response token bound to the response ciphertext (AI-2).
func (f *paFakeSubstrate) routeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
		env, err := DecodeEnvelope(body)
		if err != nil {
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		reqPlain, err := Open(env, f.payerPub, f.payerEnc)
		if err != nil {
			http.Error(w, "open", http.StatusBadRequest)
			return
		}
		corr := env.Metadata.CorrelationID
		txType := env.Metadata.TransactionType
		var inTok Token
		_ = json.Unmarshal([]byte(env.Metadata.AuthzToken), &inTok)

		respOp, ok := paResponseOp[txType]
		if !ok {
			http.Error(w, "unknown tx", http.StatusBadGateway)
			return
		}

		payload := f.payloadFor(txType, reqPlain)

		respMeta := Metadata{
			Sender:          f.payerID,
			Recipient:       env.Metadata.Sender,
			TransactionType: txType,
			AuthorityFrame:  "payer-coverage",
			Timestamp:       f.now.UTC().Format(time.RFC3339),
			CorrelationID:   corr,
		}
		respEnv, err := Seal(respMeta, payload, f.requesterEnc)
		if err != nil {
			http.Error(w, "seal", http.StatusInternalServerError)
			return
		}
		respHash := sha256.Sum256(respEnv.Ciphertext)
		respTok := f.mint(Token{
			Operation:     respOp,
			Scope:         "coverage",
			Subject:       inTok.Subject,
			Frame:         "payer-coverage",
			CorrelationID: corr,
			Holder:        f.payerID,
			PayloadHash:   hex.EncodeToString(respHash[:]),
			Expiry:        f.now.Add(time.Hour),
		})
		respTokJSON, _ := json.Marshal(respTok)
		respEnv.Metadata.AuthzToken = string(respTokJSON)
		out, _ := EncodeEnvelope(respEnv)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}
}

// paTestRig wires the fake substrate's authz + hub servers and returns an Identity +
// the wiring RunPriorAuth needs.
func newPATestRig(t *testing.T, f *paFakeSubstrate) (Identity, Endpoints, Payer, ed25519.PublicKey) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorizeHandler)
	authzSrv := httptest.NewServer(mux)
	t.Cleanup(authzSrv.Close)

	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", f.routeHandler())
	hubSrv := httptest.NewServer(hubMux)
	t.Cleanup(hubSrv.Close)

	id, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	id.Clock = func() time.Time { return f.now }
	f.requesterEnc = id.EncPub

	return id,
		Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
		Payer{ID: f.payerID, EncPub: f.payerPub, AuthzPub: signPubOf(f.signPriv)},
		signPubOf(f.signPriv)
}

func signPubOf(priv ed25519.PrivateKey) ed25519.PublicKey {
	return priv.Public().(ed25519.PublicKey)
}

func sandboxPARequest() PriorAuthRequest {
	cpt, display, icd10 := SandboxUC03Order()
	return PriorAuthRequest{
		Member:           "MBR-COVERED",
		DOB:              "1975-04-02",
		Family:           "Johansson",
		NPI:              "9999999999",
		Clinical:         SandboxUC03Context(),
		ProcedureCPT:     cpt,
		ProcedureDisplay: display,
		DiagnosisICD10:   icd10,
	}
}

// TestRunPriorAuth_Approved drives the full CRD→DTR→PAS happy path: PA required,
// questionnaire filled, claim approved.
func TestRunPriorAuth_Approved(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	f := &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now, paRequired: true,
	}
	id, ep, payer, _ := newPATestRig(t, f)

	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, sandboxPARequest())
	if err != nil {
		t.Fatalf("RunPriorAuth: %v", err)
	}
	if res.Outcome != "approved" {
		t.Errorf("Outcome = %q, want approved", res.Outcome)
	}
	if res.PreAuthRef == "" {
		t.Error("PreAuthRef is empty on an approved result")
	}
}

// TestRunPriorAuth_NoPARequired proves the CRD no-PA branch short-circuits: when the
// payer's cards say paRequired=false, RunPriorAuth returns "no-pa-required" WITHOUT
// touching the DTR or PAS legs (asserted by counting routed legs).
func TestRunPriorAuth_NoPARequired(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	f := &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now, paRequired: false,
	}

	// Count the routed legs by txType so we can prove DTR/PAS never run.
	var legTypes []string
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorizeHandler)
	authzSrv := httptest.NewServer(mux)
	defer authzSrv.Close()
	hubMux := http.NewServeMux()
	route := f.routeHandler()
	hubMux.HandleFunc("/route", func(w http.ResponseWriter, r *http.Request) {
		// Sniff the txType from the envelope before delegating.
		body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
		if env, err := DecodeEnvelope(body); err == nil {
			legTypes = append(legTypes, env.Metadata.TransactionType)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		route(w, r)
	})
	hubSrv := httptest.NewServer(hubMux)
	defer hubSrv.Close()

	id, _ := GenerateIdentity("ext-provider")
	id.Clock = func() time.Time { return now }
	f.requesterEnc = id.EncPub

	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient,
		Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
		Payer{ID: "payer", EncPub: payerPub, AuthzPub: signPubOf(signPriv)},
		sandboxPARequest())
	if err != nil {
		t.Fatalf("RunPriorAuth: %v", err)
	}
	if res.Outcome != "no-pa-required" {
		t.Errorf("Outcome = %q, want no-pa-required", res.Outcome)
	}
	if len(legTypes) != 1 || legTypes[0] != "crd-order-select" {
		t.Errorf("no-pa-required must hit ONLY the CRD leg; routed legs = %v", legTypes)
	}
}

// TestRunPriorAuth_LegAttributedError proves a leg failure surfaces a leg-attributed
// error naming the leg + step (e.g. "crd-order-select" / "parse cards"), so a caller
// can tell WHICH leg broke.
func TestRunPriorAuth_LegAttributedError(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	f := &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now, paRequired: true, malformCards: true,
	}
	id, ep, payer, _ := newPATestRig(t, f)

	_, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, sandboxPARequest())
	if err == nil {
		t.Fatal("RunPriorAuth should fail when the CRD leg returns malformed cards")
	}
	if !strings.Contains(err.Error(), "crd-order-select") {
		t.Errorf("error %q must name the failing leg (crd-order-select)", err.Error())
	}
}

func TestSandboxUC04Report(t *testing.T) {
	r := SandboxUC04Report()
	if r.ReportID == "" || r.CPT == "" || r.ProvenanceAgent == "" {
		t.Fatalf("SandboxUC04Report incomplete: %+v", r)
	}
}

func TestResumePriorAuth_RequiresProvenance(t *testing.T) {
	id := Identity{HolderID: "provider"} // no keys needed: must fail BEFORE any wire
	resume := PriorAuthResume{
		OriginalCorrelationID: "corr",
		PatientRef:            "Patient/MBR-UC04",
		CoverageRef:           "Coverage/MBR-UC04",
		SubjectPCI:            "pci:abc",
		QRJSON:                []byte(`{"resourceType":"QuestionnaireResponse"}`),
		SRJSON:                []byte(`{"resourceType":"ServiceRequest"}`),
	}
	bad := SupplementalReport{ReportID: "dr-x", CPT: "72148", Display: "MRI", ProvenanceAgent: ""}
	_, err := id.ResumePriorAuth(context.Background(), nil, Endpoints{}, Payer{}, resume, bad)
	if err == nil {
		t.Fatal("ResumePriorAuth with empty ProvenanceAgent: want a fail-loud error before sealing")
	}
	if !strings.Contains(err.Error(), "ProvenanceAgent") {
		t.Errorf("error = %v, want it to name the missing ProvenanceAgent (FR-32)", err)
	}
}

func TestSandboxContexts(t *testing.T) {
	uc04 := SandboxUC04Context()
	if !uc04.PriorSurgery {
		t.Error("SandboxUC04Context: PriorSurgery must be true (the pend trigger)")
	}
	if uc04.ConservativeTherapyWeeks != 6 {
		t.Errorf("SandboxUC04Context: weeks = %d, want 6 (approves after amend)", uc04.ConservativeTherapyWeeks)
	}
	uc08 := SandboxUC08Context()
	if uc08.ConservativeTherapyWeeks != 4 {
		t.Errorf("SandboxUC08Context: weeks = %d, want 4 (the deny variant)", uc08.ConservativeTherapyWeeks)
	}
	if uc08.PriorSurgery || uc08.HighDisability {
		t.Error("SandboxUC08Context: must not set PriorSurgery/HighDisability (those pend, not deny)")
	}
	for _, member := range []string{"MBR-COVERED", "MBR-UC04", "MBR-UC08"} {
		if _, ok := SandboxContextFor(member); !ok {
			t.Errorf("SandboxContextFor(%q): ok=false, want a context", member)
		}
	}
	if _, ok := SandboxContextFor("MBR-UNKNOWN"); ok {
		t.Error("SandboxContextFor(unknown): ok=true, want false")
	}
}
