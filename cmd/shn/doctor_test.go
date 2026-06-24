package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	"golang.org/x/crypto/nacl/box"
)

// fakeSandbox stands up the full sandbox discovery surface `shn doctor` probes:
// accountsvc /discovery, authz /authorize + /pubkey, registrar /holders, and hub
// /route. The /authorize + /route legs reimplement just enough of the substrate to
// drive RunEligibility hermetically (mirrors sdk/originate_test.go's fakeSubstrate,
// which is unexported in package shnsdk so cannot be reused here directly).
type fakeSandbox struct {
	signPriv     ed25519.PrivateKey // authz token signing key
	signPub      ed25519.PublicKey  // served at /pubkey
	payerEnc     *[32]byte          // payer X25519 priv (opens the request)
	payerPub     *[32]byte          // payer X25519 pub (seal target; served in /holders)
	payerID      string
	now          time.Time
	requesterEnc *[32]byte // dev identity enc pub, so the payer seals the response back

	// Test knobs.
	discWireVersion string           // overrides advertised wireProtocolVersion
	holders         []map[string]any // overrides the /holders feed
	personas        []shnsdk.DiscoveryPersona
	forceCovered    *bool // if set, /route returns this coverage regardless of member
	// paDeny makes the PAS leg return a denied ClaimResponse (explicit A3 signal),
	// driving the priorauth command's denied / nonzero-exit path.
	paDeny bool
	// paPended makes the PAS submit PEND (Bundle + Task) and the ClaimUpdate APPROVE —
	// the pended→resume→approved path.
	paPended bool
	// paUC08Denied makes the PAS submit return the real PAS A3 denied ClaimResponse
	// (reviewActionCode A3) — the denied path the widened ParseClaimResponse classifies.
	paUC08Denied bool
	// paNotRequired makes the CRD leg say PA is NOT required, short-circuiting to
	// Outcome "no-pa-required". With a persona that expects "approved" this is a clean
	// outcome MISMATCH (no error), exercising doctor's exitOutcome PA branch.
	paNotRequired bool

	routeHits int32 // atomically counted /route calls (proves version-check short-circuit)
}

// paResponseOp mirrors hubsvc.responseOp: the response-leg op keyed by the request
// envelope's TransactionType, for the PA legs (CRD→DTR→PAS) plus eligibility. RunPriorAuth
// drives the conformant -native legs (the minimized leg names were deleted in convergence Phase C).
var paResponseOp = map[string]string{
	"coverage-eligibility":    "eligibility-response",
	"crd-order-select":        "crd-cards",
	"dtr-questionnaire-fetch": "dtr-questionnaire",
	"pas-claim":               "pas-response",
	"pas-claim-update":        "pas-update-response",
}

func (f *fakeSandbox) mint(tok shnsdk.Token) shnsdk.Token {
	c := tok
	c.Signature = nil
	b, _ := json.Marshal(c)
	tok.Signature = ed25519.Sign(f.signPriv, b)
	return tok
}

func (f *fakeSandbox) authorizeHandler(w http.ResponseWriter, r *http.Request) {
	var req shnsdk.AuthorizeRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, shnsdk.MaxRequestBytes))
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	holder := "dev-provider"
	if req.Frame == "payer-coverage" {
		holder = f.payerID
	}
	tok := f.mint(shnsdk.Token{
		Operation:     req.Operation,
		Scope:         "coverage",
		Subject:       req.SubjectPCI,
		Frame:         req.Frame,
		CorrelationID: req.CorrelationID,
		Holder:        holder,
		PayloadHash:   req.PayloadHash,
		Expiry:        f.now.Add(time.Hour),
	})
	_ = json.NewEncoder(w).Encode(map[string]any{"token": tok})
}

func (f *fakeSandbox) pubkeyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"pubkey": base64.StdEncoding.EncodeToString(f.signPub),
	})
}

func (f *fakeSandbox) holdersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.holders)
}

func (f *fakeSandbox) routeHandler(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&f.routeHits, 1)
	body, _ := io.ReadAll(io.LimitReader(r.Body, shnsdk.MaxRequestBytes))
	env, err := shnsdk.DecodeEnvelope(body)
	if err != nil {
		http.Error(w, "decode", http.StatusBadRequest)
		return
	}
	plain, err := shnsdk.Open(env, f.payerPub, f.payerEnc)
	if err != nil {
		http.Error(w, "open", http.StatusBadRequest)
		return
	}
	corr := env.Metadata.CorrelationID
	txType := env.Metadata.TransactionType
	var inTok shnsdk.Token
	_ = json.Unmarshal([]byte(env.Metadata.AuthzToken), &inTok)

	respOp, ok := paResponseOp[txType]
	if !ok {
		http.Error(w, "unknown tx", http.StatusBadGateway)
		return
	}
	payload := f.payloadFor(txType, plain)

	respMeta := shnsdk.Metadata{
		Sender:          f.payerID,
		Recipient:       env.Metadata.Sender,
		TransactionType: txType,
		AuthorityFrame:  "payer-coverage",
		Timestamp:       f.now.UTC().Format(time.RFC3339),
		CorrelationID:   corr,
	}
	respEnv, err := shnsdk.Seal(respMeta, payload, f.requesterEnc)
	if err != nil {
		http.Error(w, "seal", http.StatusInternalServerError)
		return
	}
	respHash := sha256.Sum256(respEnv.Ciphertext)
	respTok := f.mint(shnsdk.Token{
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
	out, _ := shnsdk.EncodeEnvelope(respEnv)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// payloadFor is the in-process payer adjudicator: eligibility → a coverage response
// (covered unless the sealed request names a NOTCOVERED member, or forceCovered pins
// it); the three PA legs → CRD cards (PA required) → the lumbar-MRI questionnaire →
// an approved (or, when paDeny, denied) ClaimResponse. Mirrors the SDK's own
// paFakeSubstrate.payloadFor so the doctor fake drives the same RunPriorAuth wiring.
func (f *fakeSandbox) payloadFor(txType string, reqPlain []byte) []byte {
	switch txType {
	case "coverage-eligibility":
		covered := !strings.Contains(strings.ToUpper(string(reqPlain)), "NOTCOVERED")
		if f.forceCovered != nil {
			covered = *f.forceCovered
		}
		if covered {
			return []byte(`{"resourceType":"CoverageEligibilityResponse","status":"active","purpose":["benefits"],` +
				`"outcome":"complete","patient":{"reference":"Patient/X"},` +
				`"insurance":[{"coverage":{"reference":"Coverage/X"},"inforce":true}]}`)
		}
		return []byte(`{"resourceType":"CoverageEligibilityResponse","status":"active","purpose":["benefits"],` +
			`"outcome":"complete","disposition":"member not enrolled","patient":{"reference":"Patient/X"},` +
			`"insurance":[{"coverage":{"reference":"Coverage/X"},"inforce":false}]}`)
	case "crd-order-select":
		if f.paNotRequired {
			return []byte(`{"cards":[{"summary":"no PA","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`)
		}
		return []byte(`{"cards":[{"summary":"PA verdict","indicator":"info",` +
			`"extension":{"covered":"covered","paNeeded":"auth-needed","questionnaires":["` + shnsdk.SupportedQuestionnaireCanonical + `"]}}]}`)
	case "dtr-questionnaire-fetch":
		// §6.2: uniform leg shape — the substrate returns a $questionnaire-package
		// collection Bundle; RunPriorAuth extracts the bare Questionnaire.
		q := []byte(`{"resourceType":"Questionnaire","url":"` + shnsdk.SupportedQuestionnaireCanonical + `",` +
			`"status":"active","item":[` +
			`{"linkId":"conservative-therapy-weeks","type":"integer"},` +
			`{"linkId":"neuro-deficit","type":"boolean"},` +
			`{"linkId":"prior-imaging","type":"boolean"}]}`)
		pkg, err := shnsdk.BuildQuestionnairePackage(q)
		if err != nil {
			panic("doctor fake: wrap questionnaire package: " + err.Error())
		}
		return pkg
	case "pas-claim":
		if f.paPended {
			return []byte(`{"resourceType":"Bundle","type":"collection","entry":[` +
				`{"resource":{"resourceType":"ClaimResponse","status":"active","use":"preauthorization",` +
				`"outcome":"queued","patient":{"reference":"Patient/X"},"created":"` + f.now.UTC().Format(time.RFC3339) +
				`","insurer":{"reference":"Organization/payer"}}},` +
				`{"resource":{"resourceType":"Task","status":"requested","intent":"order",` +
				`"for":{"reference":"Patient/X"},"authoredOn":"` + f.now.UTC().Format(time.RFC3339) + `",` +
				`"input":[{"type":{"text":"operative-diagnostic-report"},"valueString":"operative-diagnostic-report"}]}}]}`)
		}
		if f.paUC08Denied {
			return []byte(`{"resourceType":"ClaimResponse","status":"active","type":{"coding":[{"code":"professional"}]},` +
				`"use":"preauthorization","patient":{"reference":"Patient/X"},"created":"` + f.now.UTC().Format(time.RFC3339) +
				`","insurer":{"reference":"Organization/payer"},"outcome":"complete","disposition":"denied: insufficient conservative therapy",` +
				`"item":[{"itemSequence":1,"adjudication":[{"category":{"coding":[{"code":"submitted"}]},"extension":[` +
				`{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[` +
				`{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode",` +
				`"valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A3","display":"Not Certified"}]}}]}]}]}],` +
				`"processNote":[{"number":1,"type":"print","text":"Appeal window: 30 days."}]}`)
		}
		if f.paDeny {
			return []byte(`{"resourceType":"ClaimResponse","status":"active","type":{"coding":[{"code":"professional"}]},` +
				`"use":"preauthorization","patient":{"reference":"Patient/X"},"created":"` +
				f.now.UTC().Format(time.RFC3339) + `","insurer":{"reference":"Organization/payer"},` +
				`"outcome":"error","disposition":"denied"}`)
		}
		return []byte(`{"resourceType":"ClaimResponse","status":"active","type":{"coding":[{"code":"professional"}]},` +
			`"use":"preauthorization","patient":{"reference":"Patient/X"},"created":"` +
			f.now.UTC().Format(time.RFC3339) + `","insurer":{"reference":"Organization/payer"},` +
			`"outcome":"complete","preAuthRef":"PA-APPROVED-123",` +
			`"preAuthPeriod":{"end":"2026-12-31"}}`)
	case "pas-claim-update":
		// The amend approves (the operative report cleared the pend).
		return []byte(`{"resourceType":"ClaimResponse","status":"active","type":{"coding":[{"code":"professional"}]},` +
			`"use":"preauthorization","patient":{"reference":"Patient/X"},"created":"` +
			f.now.UTC().Format(time.RFC3339) + `","insurer":{"reference":"Organization/payer"},` +
			`"outcome":"complete","preAuthRef":"PA-APPROVED-AMEND","preAuthPeriod":{"end":"2026-12-31"}}`)
	}
	return []byte(`{}`)
}

// start wires the four fakes onto a single httptest server (one origin: doctor
// treats --discovery as the accountsvc base, and discovery's endpoints point back
// at this same origin). Returns the server.
func (f *fakeSandbox) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorizeHandler)
	mux.HandleFunc("/pubkey", f.pubkeyHandler)
	mux.HandleFunc("/holders", f.holdersHandler)
	mux.HandleFunc("/route", f.routeHandler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wire := shnsdk.WireProtocolVersion
	if f.discWireVersion != "" {
		wire = f.discWireVersion
	}
	mux.HandleFunc("/discovery", func(w http.ResponseWriter, r *http.Request) {
		disc := shnsdk.Discovery{
			Sandbox:             true,
			SyntheticDataOnly:   true,
			WireProtocolVersion: wire,
			Endpoints: shnsdk.DiscoveryEndpoints{
				Hub:       srv.URL,
				Authz:     srv.URL,
				Registrar: srv.URL,
			},
			AuthzPublicKeyURL: srv.URL + "/pubkey",
			SandboxResponders: []shnsdk.DiscoveryResponder{{Role: "payer", HolderID: f.payerID}},
			SandboxPersonas:   f.personas,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(disc)
	})
	return srv
}

// newFakeSandbox builds a fully-wired healthy sandbox plus a dev keys dir, returning
// the sandbox, the dev's holder id, and the keys dir. The dev id is present+active in
// /holders by default.
func newFakeSandbox(t *testing.T) (*fakeSandbox, string, string) {
	t.Helper()
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen sign key: %v", err)
	}
	payerPub, payerPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen payer enc: %v", err)
	}
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	devID := "dev-provider"
	dir := t.TempDir()
	// keygen writes the dev's keys so loadIdentity works inside doctor.
	if _, stderr, code := runCLI("keygen", "--id", devID, "--role", "provider", "--base-url", "https://dev.example.com", "-out", dir); code != 0 {
		t.Fatalf("keygen exit=%d stderr=%s", code, stderr)
	}
	devMan, err := readManifest(dir)
	if err != nil {
		t.Fatalf("read dev manifest: %v", err)
	}

	f := &fakeSandbox{
		signPriv: signPriv, signPub: signPub,
		payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now,
		personas: []shnsdk.DiscoveryPersona{
			{MemberID: "MBR-COVERED", DOB: "1975-04-02", Family: "Johansson", ExpectedEligibility: "covered", ExpectedPriorAuth: "approved"},
			{MemberID: "MBR-NOTCOVERED", DOB: "1975-04-02", Family: "Johansson", ExpectedEligibility: "not-covered"},
		},
	}
	f.holders = []map[string]any{
		{"id": "payer", "role": "payer", "encPub": base64.StdEncoding.EncodeToString(payerPub[:]), "signPub": base64.StdEncoding.EncodeToString(signPub), "baseURL": "https://payer.example.com"},
		{"id": devID, "role": "provider", "encPub": devMan.EncPub, "signPub": devMan.SignPub, "baseURL": devMan.BaseURL},
	}
	return f, devID, dir
}

// doctorClock pins the doctor's eligibility legs to the sandbox clock. doctor reads it
// to let tests drive a fixed-clock substrate.
func init() { doctorClock = func() time.Time { return time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC) } }

func TestDoctor_HappyPath(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	// The sandbox enc pub must be the dev's actual pub so the response seals back.
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOK {
		t.Fatalf("doctor exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "PASS") {
		t.Errorf("stdout should report PASS: %s", stdout)
	}
	// Both personas exercised.
	if !strings.Contains(stdout, "MBR-COVERED") || !strings.Contains(stdout, "MBR-NOTCOVERED") {
		t.Errorf("stdout should mention both personas: %s", stdout)
	}
	// The prior-auth leg ran (after eligibility) and approved.
	if !strings.Contains(stdout, "priorauth MBR-COVERED") || !strings.Contains(stdout, "approved") {
		t.Errorf("stdout should report the priorauth approved line: %s", stdout)
	}
}

// TestDoctor_PriorAuthMismatch: the payer says PA is NOT required, so the round-trip
// returns "no-pa-required" while the persona expects "approved" — a clean outcome
// MISMATCH (no error) → doctor exits exitOutcome with a got/want message.
func TestDoctor_PriorAuthMismatch(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	f.paNotRequired = true

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOutcome {
		t.Fatalf("doctor exit=%d, want exitOutcome=%d\nstdout=%s\nstderr=%s", code, exitOutcome, stdout, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout+stderr), "priorauth") || !strings.Contains(stdout+stderr, "want") {
		t.Errorf("mismatch message should mention priorauth + got/want: %s %s", stdout, stderr)
	}
}

// TestDoctor_EligibilityBeforePriorAuth proves PA runs AFTER eligibility: when an
// eligibility outcome mismatches, doctor exits the eligibility code and the PA line
// never appears (the run returns on the first eligibility failure).
func TestDoctor_EligibilityBeforePriorAuth(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	// Force every persona covered → MBR-NOTCOVERED fails its eligibility expectation
	// BEFORE any PA leg can run.
	yes := true
	f.forceCovered = &yes

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOutcome {
		t.Fatalf("doctor exit=%d, want exitOutcome=%d\nstdout=%s\nstderr=%s", code, exitOutcome, stdout, stderr)
	}
	// The PA line must NOT appear: eligibility failed first, so PA never ran.
	if strings.Contains(stdout, "priorauth") {
		t.Errorf("priorauth must not run when eligibility fails first: %s", stdout)
	}
}

func TestDoctor_VersionUnsupported_FailsBeforeEligibility(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	f.discWireVersion = "9.9.9"
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitVersionUnsup {
		t.Fatalf("doctor exit=%d, want exitVersionUnsup=%d\nstdout=%s\nstderr=%s", code, exitVersionUnsup, stdout, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout+stderr), "upgrade") {
		t.Errorf("expected an upgrade message: %s %s", stdout, stderr)
	}
	if got := atomic.LoadInt32(&f.routeHits); got != 0 {
		t.Errorf("version check must run BEFORE any eligibility leg; /route hit %d times", got)
	}
}

func TestDoctor_PayerAbsentFromHolders(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	// Drop the payer from /holders, keep the dev.
	f.holders = f.holders[1:]

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitSandboxHealth {
		t.Fatalf("doctor exit=%d, want exitSandboxHealth=%d\nstdout=%s\nstderr=%s", code, exitSandboxHealth, stdout, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout+stderr), "sandbox") {
		t.Errorf("message should blame the sandbox: %s %s", stdout, stderr)
	}
}

func TestDoctor_DevIDAbsentFromHolders(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	// Keep the payer, drop the dev.
	f.holders = f.holders[:1]

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitDevRegistration {
		t.Fatalf("doctor exit=%d, want exitDevRegistration=%d\nstdout=%s\nstderr=%s", code, exitDevRegistration, stdout, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout+stderr), "register") {
		t.Errorf("message should blame registration: %s %s", stdout, stderr)
	}
}

func TestDoctor_OutcomeMismatch(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	srv := f.start(t)
	id, _ := loadIdentity(dir, devID)
	f.requesterEnc = id.EncPub
	// Force the payer to return covered for EVERY persona, so the not-covered persona
	// mismatches its expected outcome.
	yes := true
	f.forceCovered = &yes

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOutcome {
		t.Fatalf("doctor exit=%d, want exitOutcome=%d\nstdout=%s\nstderr=%s", code, exitOutcome, stdout, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout+stderr), "want") {
		t.Errorf("outcome mismatch should report got/want: %s %s", stdout, stderr)
	}
}

// TestDoctor_PriorAuthPendedResume: a pended persona pends, doctor resumes with the
// sandbox supplemental, and the post-amend outcome is approved → exitOK.
func TestDoctor_PriorAuthPendedResume(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	f.paPended = true
	f.personas = []shnsdk.DiscoveryPersona{
		{MemberID: "MBR-UC04", DOB: "1982-11-03", Family: "Chen", ExpectedEligibility: "covered", ExpectedPriorAuth: "pended", ExpectedAfterAmend: "approved"},
	}
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOK {
		t.Fatalf("doctor exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "priorauth MBR-UC04: pended") {
		t.Errorf("stdout should report the pended line: %s", stdout)
	}
	if !strings.Contains(stdout, "after amend approved") {
		t.Errorf("stdout should report the post-amend approved line: %s", stdout)
	}
}

// TestDoctor_PriorAuthDenied: a denied persona (A3 review action) → exitOK (the outcome
// matches the persona's ExpectedPriorAuth "denied").
func TestDoctor_PriorAuthDenied(t *testing.T) {
	f, devID, dir := newFakeSandbox(t)
	f.paUC08Denied = true
	f.personas = []shnsdk.DiscoveryPersona{
		{MemberID: "MBR-UC08", DOB: "1971-02-09", Family: "Okafor", ExpectedEligibility: "covered", ExpectedPriorAuth: "denied"},
	}
	srv := f.start(t)
	id, err := loadIdentity(dir, devID)
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	f.requesterEnc = id.EncPub

	stdout, stderr, code := runCLI("doctor", "--discovery", srv.URL, "--id", devID, "-keys", dir)
	if code != exitOK {
		t.Fatalf("doctor exit=%d (want %d)\nstdout=%s\nstderr=%s", code, exitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "priorauth MBR-UC08: denied") {
		t.Errorf("stdout should report the denied line: %s", stdout)
	}
}
