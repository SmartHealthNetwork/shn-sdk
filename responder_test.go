package shnsdk

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- test authz stub ----

// testAuthzServer returns an httptest.Server that answers POST /authorize with
// a signed Token. It stamps Holder from the X-Holder-Assertion header (decoded
// without verifying the signature) so the minted token carries the correct
// Holder for VerifyBound checks. This mirrors the real framework behaviour:
// the caller authenticates with its own holder assertion and the framework
// stamps the minted token with that holder ID.
func testAuthzServer(t *testing.T, signPriv ed25519.PrivateKey, now time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/authorize" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		holderID := "unknown"
		if hdrVal := r.Header.Get("X-Holder-Assertion"); hdrVal != "" {
			if raw, err := base64.StdEncoding.DecodeString(hdrVal); err == nil {
				var a Assertion
				if err := json.Unmarshal(raw, &a); err == nil {
					holderID = a.HolderID
				}
			}
		}
		var req AuthorizeRequest
		body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tok := Token{
			Operation:     req.Operation,
			Scope:         "coverage",
			Subject:       req.SubjectPCI,
			Frame:         req.Frame,
			CorrelationID: req.CorrelationID,
			Holder:        holderID,
			PayloadHash:   req.PayloadHash,
			Expiry:        now.Add(time.Hour),
		}
		tok.Signature = ed25519.Sign(signPriv, tokenSigningPayload(tok))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authorizeResp{Token: tok})
	}))
}

// ---- forward builder ----

// buildValidForward constructs a fully valid inbound forward envelope sealed to
// responderEncPub, with a correct inbound authz token signed by authzSignPriv and
// a valid X-Hub-Assertion header signed by hubPriv.
func buildValidForward(
	t *testing.T,
	responderID string,
	responderEncPub *[32]byte,
	authzSignPriv ed25519.PrivateKey,
	hubPriv ed25519.PrivateKey,
	now time.Time,
) (envBytes []byte, corrID string, sender Identity, hubAssertion string, subjectPCI string) {
	t.Helper()

	sender, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatalf("GenerateIdentity sender: %v", err)
	}
	sender.Clock = func() time.Time { return now }

	member := "MBR-001"
	subjectPCI = ResolvePCI(member, "1975-04-02", "Johansson")
	fhirPayload, err := BuildEligibilityRequest(member, "9999999999", now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequest: %v", err)
	}

	corrID = "corr-responder-test-1"
	meta := Metadata{
		Sender:          sender.HolderID,
		Recipient:       responderID,
		TransactionType: "coverage-eligibility",
		AuthorityFrame:  "provider-tpo",
		Timestamp:       now.UTC().Format(time.RFC3339),
		CorrelationID:   corrID,
	}
	env, err := Seal(meta, fhirPayload, responderEncPub)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	ctHash := sha256.Sum256(env.Ciphertext)
	inTok := Token{
		Operation:     "eligibility-inquiry",
		Scope:         "coverage",
		Subject:       subjectPCI,
		Frame:         "provider-tpo",
		CorrelationID: corrID,
		Holder:        sender.HolderID,
		PayloadHash:   hex.EncodeToString(ctHash[:]),
		Expiry:        now.Add(time.Hour),
	}
	inTok.Signature = ed25519.Sign(authzSignPriv, tokenSigningPayload(inTok))
	inTokJSON, err := json.Marshal(inTok)
	if err != nil {
		t.Fatalf("marshal inTok: %v", err)
	}
	env.Metadata.AuthzToken = string(inTokJSON)

	envBytes, err = EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	hubAssertion = makeHubAssertion(t, hubPriv, "hub", responderID, now, 2*time.Minute, "jti-fwd-1")
	return envBytes, corrID, sender, hubAssertion, subjectPCI
}

// ---- test Adjudicator ----

// testAdjudicator is the B1 eligibility adjudicator. The PA-chain methods
// use demo defaults so existing eligibility tests are unaffected.
type testAdjudicator struct {
	covered bool
	reason  string
}

func (a *testAdjudicator) Eligibility(_ string) (bool, string) {
	return a.covered, a.reason
}

func (a *testAdjudicator) OrderSelect(cpt string) (bool, string) {
	if cpt == "72148" {
		return true, SupportedQuestionnaireCanonical
	}
	return false, ""
}

func (a *testAdjudicator) Questionnaire(canonical string) ([]byte, bool) {
	if canonical == SupportedQuestionnaireCanonical {
		return demoLumbarQuestionnaire(), true
	}
	return nil, false
}

func (a *testAdjudicator) PriorAuth(qrJSON []byte, hasDR bool) (PASDecision, error) {
	return testPriorAuthDecision(qrJSON, hasDR, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC))
}

// paTestAdjudicator is the Adjudicator the PA-chain tests plug into the Responder.
//
// It exists to EXERCISE the Responder's PAS response shapes (approved / pended /
// approved-on-amend / denied) — it is not, and never was, a payer. The package used to
// export a policy for this; it left the published API
// with the retired payer stub (spec R2/§13.2), so the fixture the Responder tests need lives
// here, in the test binary, where nobody can mistake it for a reference implementation.
type paTestAdjudicator struct {
	now time.Time
}

// testPriorAuthDecision is this test binary's stand-in PA policy: fewer than six weeks of
// conservative therapy denies; a prior surgery with no operative DiagnosticReport pends
// (and approves once the amend supplies one); anything else approves. Deliberately tiny —
// the assertions it serves are about the Responder's WIRE behaviour, not about policy.
func testPriorAuthDecision(qrJSON []byte, hasDiagnosticReport bool, now time.Time) (PASDecision, error) {
	var qr struct {
		Item []testQRItem `json:"item"`
	}
	if err := json.Unmarshal(qrJSON, &qr); err != nil {
		return PASDecision{Outcome: PASDenied}, fmt.Errorf("test adjudicator: parse QR: %w", err)
	}
	weeks, priorSurgery := 0, false
	for _, it := range flattenTestQRItems(qr.Item) {
		if len(it.Answer) == 0 {
			continue
		}
		switch it.LinkId {
		case "conservative-therapy-weeks":
			if it.Answer[0].ValueInteger != nil {
				weeks = *it.Answer[0].ValueInteger
			} else if it.Answer[0].ValueDecimal != nil {
				weeks = int(*it.Answer[0].ValueDecimal)
			}
		case "prior-surgery":
			if it.Answer[0].ValueBoolean != nil {
				priorSurgery = *it.Answer[0].ValueBoolean
			}
		}
	}
	switch {
	case priorSurgery && !hasDiagnosticReport:
		return PASDecision{Outcome: PASPended, NeededItems: []string{"operative-diagnostic-report"}}, nil
	case weeks < 6:
		return PASDecision{Outcome: PASDenied}, nil
	default:
		return PASDecision{
			Outcome:    PASApproved,
			PreAuthRef: fmt.Sprintf("PA-%012x", now.Unix()),
			ValidUntil: now.AddDate(0, 0, 90).Format("2006-01-02"),
		}, nil
	}
}

// testQRItem is the QR item shape testPriorAuthDecision reads. Named (not inline) because
// QuestionnaireResponse.item is self-recursive on BOTH nesting axes.
type testQRItem struct {
	LinkId string `json:"linkId"`
	Answer []struct {
		ValueInteger *int         `json:"valueInteger"`
		ValueDecimal *float64     `json:"valueDecimal"`
		ValueBoolean *bool        `json:"valueBoolean"`
		Item         []testQRItem `json:"item"`
	} `json:"answer"`
	Item []testQRItem `json:"item"`
}

// flattenTestQRItems returns every item at every depth, on both nesting axes.
func flattenTestQRItems(items []testQRItem) []testQRItem {
	var out []testQRItem
	for _, it := range items {
		out = append(out, it)
		out = append(out, flattenTestQRItems(it.Item)...)
		for _, a := range it.Answer {
			out = append(out, flattenTestQRItems(a.Item)...)
		}
	}
	return out
}

func (a *paTestAdjudicator) Eligibility(_ string) (bool, string) { return true, "" }

func (a *paTestAdjudicator) OrderSelect(cpt string) (bool, string) {
	if cpt == "72148" || cpt == "G0151" {
		return true, SupportedQuestionnaireCanonical
	}
	return false, ""
}

func (a *paTestAdjudicator) Questionnaire(canonical string) ([]byte, bool) {
	if canonical == SupportedQuestionnaireCanonical {
		return demoLumbarQuestionnaire(), true
	}
	return nil, false
}

func (a *paTestAdjudicator) PriorAuth(qrJSON []byte, hasDR bool) (PASDecision, error) {
	return testPriorAuthDecision(qrJSON, hasDR, a.now)
}

// ---- helpers ----

func postInbound(t *testing.T, srv *httptest.Server, body []byte, hubAssertion string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/substrate/inbound", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if hubAssertion != "" {
		req.Header.Set("X-Hub-Assertion", hubAssertion)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ---- TestResponder_Eligibility_BothBranches ----

func TestResponder_Eligibility_BothBranches(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	authzPub, authzPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen authz key: %v", err)
	}
	hubPub, hubPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen hub key: %v", err)
	}

	authzSrv := testAuthzServer(t, authzPriv, now)
	defer authzSrv.Close()

	responderID := "payer-responder"
	responderIdent, err := GenerateIdentity(responderID)
	if err != nil {
		t.Fatalf("GenerateIdentity responder: %v", err)
	}
	responderIdent.Clock = func() time.Time { return now }

	for _, tc := range []struct {
		name    string
		covered bool
		reason  string
	}{
		{"covered", true, ""},
		{"not-covered", false, "not a member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envBytes, corrID, sender, hubAssertion, subjectPCI :=
				buildValidForward(t, responderID, responderIdent.EncPub, authzPriv, hubPriv, now)

			senderEncPub := sender.EncPub
			r, err := NewResponder(ResponderConfig{
				Identity:        responderIdent,
				AuthzURL:        authzSrv.URL,
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc: func(holderID string) (*[32]byte, bool) {
					if holderID == sender.HolderID {
						return senderEncPub, true
					}
					return nil, false
				},
				Adjudicator: &testAdjudicator{covered: tc.covered, reason: tc.reason},
				Clock:       func() time.Time { return now },
				Client:      authzSrv.Client(),
			})
			if err != nil {
				t.Fatalf("NewResponder: %v", err)
			}
			srv := httptest.NewServer(r.Handler())
			defer srv.Close()

			resp := postInbound(t, srv, envBytes, hubAssertion)
			body := readBody(t, resp)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}

			respEnv, err := DecodeEnvelope(body)
			if err != nil {
				t.Fatalf("DecodeEnvelope: %v", err)
			}

			// Open with the SENDER's keys (response is addressed back to sender).
			plaintext, err := Open(respEnv, sender.EncPub, sender.EncPriv)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}

			covered, reason, err := ParseEligibilityResponse(plaintext)
			if err != nil {
				t.Fatalf("ParseEligibilityResponse: %v", err)
			}
			if covered != tc.covered {
				t.Errorf("covered = %v, want %v", covered, tc.covered)
			}
			if !tc.covered && reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}

			// Verify the response token: payer-coverage / eligibility-response,
			// bound to responder id as holder and to sha256hex(ciphertext).
			var respTok Token
			if err := json.Unmarshal([]byte(respEnv.Metadata.AuthzToken), &respTok); err != nil {
				t.Fatalf("unmarshal response token: %v", err)
			}
			respHash := sha256.Sum256(respEnv.Ciphertext)
			if err := VerifyBound(
				respTok,
				authzPub,
				now,
				"payer-coverage",
				"eligibility-response",
				corrID,
				responderID, // Holder = responder's own ID (from its assertion)
				subjectPCI,
				hex.EncodeToString(respHash[:]),
			); err != nil {
				t.Errorf("response token VerifyBound: %v", err)
			}
		})
	}
}

// ---- TestResponder_RejectionRows ----

// mutateEnv decodes envBytes, applies mut, re-signs the authz token with
// authzPriv using the overridden fields, and returns the re-encoded bytes.
// corrID, sender, subject, frame, op are taken from the existing envelope
// unless the mutation changes them.
func mutateThenSign(
	t *testing.T,
	envBytes []byte,
	authzPriv ed25519.PrivateKey,
	now time.Time,
	mut func(env *Envelope),
) []byte {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		t.Fatalf("unmarshal env: %v", err)
	}
	mut(&env)
	// Re-mint token bound to current ciphertext so the body is consistent.
	ctHash := sha256.Sum256(env.Ciphertext)
	inTok := Token{
		Operation:     "eligibility-inquiry",
		Scope:         "coverage",
		Subject:       "pci:x",
		Frame:         "provider-tpo",
		CorrelationID: env.Metadata.CorrelationID,
		Holder:        env.Metadata.Sender,
		PayloadHash:   hex.EncodeToString(ctHash[:]),
		Expiry:        now.Add(time.Hour),
	}
	inTok.Signature = ed25519.Sign(authzPriv, tokenSigningPayload(inTok))
	inTokJSON, _ := json.Marshal(inTok)
	env.Metadata.AuthzToken = string(inTokJSON)
	b, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	return b
}

func TestResponder_RejectionRows(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	authzPub, authzPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen authz key: %v", err)
	}
	hubPub, hubPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen hub key: %v", err)
	}

	authzSrv := testAuthzServer(t, authzPriv, now)
	defer authzSrv.Close()

	responderID := "payer-responder"
	responderIdent, err := GenerateIdentity(responderID)
	if err != nil {
		t.Fatalf("GenerateIdentity responder: %v", err)
	}
	responderIdent.Clock = func() time.Time { return now }

	// Build a canonical valid forward once; individual rows mutate from it.
	validEnvBytes, _, sender, _, _ :=
		buildValidForward(t, responderID, responderIdent.EncPub, authzPriv, hubPriv, now)

	senderEncPub := sender.EncPub

	makeResponder := func(t *testing.T) (*Responder, *httptest.Server) {
		t.Helper()
		r, err := NewResponder(ResponderConfig{
			Identity:        responderIdent,
			AuthzURL:        authzSrv.URL,
			AuthzPub:        authzPub,
			HubTransportPub: hubPub,
			ResolveEnc: func(holderID string) (*[32]byte, bool) {
				if holderID == sender.HolderID {
					return senderEncPub, true
				}
				return nil, false
			},
			Adjudicator: &testAdjudicator{covered: true},
			Clock:       func() time.Time { return now },
			Client:      authzSrv.Client(),
		})
		if err != nil {
			t.Fatalf("NewResponder: %v", err)
		}
		srv := httptest.NewServer(r.Handler())
		t.Cleanup(srv.Close)
		return r, srv
	}

	// wrongPayloadHashEnv: valid envelope but token bound to all-zeros hash.
	wrongPayloadHashEnv := func(t *testing.T) []byte {
		t.Helper()
		var env Envelope
		if err := json.Unmarshal(validEnvBytes, &env); err != nil {
			t.Fatalf("unmarshal env: %v", err)
		}
		wrongHash := strings.Repeat("00", 32)
		inTok := Token{
			Operation:     "eligibility-inquiry",
			Scope:         "coverage",
			Subject:       "pci:x",
			Frame:         "provider-tpo",
			CorrelationID: env.Metadata.CorrelationID,
			Holder:        env.Metadata.Sender,
			PayloadHash:   wrongHash,
			Expiry:        now.Add(time.Hour),
		}
		inTok.Signature = ed25519.Sign(authzPriv, tokenSigningPayload(inTok))
		inTokJSON, _ := json.Marshal(inTok)
		env.Metadata.AuthzToken = string(inTokJSON)
		b, err := EncodeEnvelope(env)
		if err != nil {
			t.Fatalf("EncodeEnvelope: %v", err)
		}
		return b
	}

	// garbleTokenEnv: valid envelope but AuthzToken is garbage JSON.
	garbleTokenEnv := func(t *testing.T) []byte {
		t.Helper()
		var env Envelope
		if err := json.Unmarshal(validEnvBytes, &env); err != nil {
			t.Fatalf("unmarshal env: %v", err)
		}
		env.Metadata.AuthzToken = "{not valid json"
		b, err := EncodeEnvelope(env)
		if err != nil {
			t.Fatalf("EncodeEnvelope: %v", err)
		}
		return b
	}

	// wrongKeyEnv: ciphertext sealed to a DIFFERENT enc key than the responder's
	// (so Open fails), but the token is minted over that ciphertext's hash so
	// VerifyBound passes — decryption is the first post-token failure.
	wrongKeyEnv := func(t *testing.T) []byte {
		t.Helper()
		var env Envelope
		if err := json.Unmarshal(validEnvBytes, &env); err != nil {
			t.Fatalf("unmarshal env: %v", err)
		}
		// Seal to a fresh, unrelated enc key — the responder cannot open it.
		otherIdent, err := GenerateIdentity("other-key")
		if err != nil {
			t.Fatalf("GenerateIdentity other-key: %v", err)
		}
		resealedEnv, err := Seal(env.Metadata, []byte(`{"resourceType":"CoverageEligibilityRequest"}`), otherIdent.EncPub)
		if err != nil {
			t.Fatalf("Seal to wrong key: %v", err)
		}
		// Re-mint the token bound to the new ciphertext hash so VerifyBound passes.
		ctHash := sha256.Sum256(resealedEnv.Ciphertext)
		inTok := Token{
			Operation:     "eligibility-inquiry",
			Scope:         "coverage",
			Subject:       "pci:x",
			Frame:         "provider-tpo",
			CorrelationID: env.Metadata.CorrelationID,
			Holder:        env.Metadata.Sender,
			PayloadHash:   hex.EncodeToString(ctHash[:]),
			Expiry:        now.Add(time.Hour),
		}
		inTok.Signature = ed25519.Sign(authzPriv, tokenSigningPayload(inTok))
		inTokJSON, _ := json.Marshal(inTok)
		resealedEnv.Metadata.AuthzToken = string(inTokJSON)
		b, err := EncodeEnvelope(resealedEnv)
		if err != nil {
			t.Fatalf("EncodeEnvelope wrongKeyEnv: %v", err)
		}
		return b
	}

	// notCEREnv: payload that Opens successfully (sealed to the responder's real
	// key) but is NOT a CoverageEligibilityRequest (e.g. {"resourceType":"Patient"}).
	notCEREnv := func(t *testing.T) []byte {
		t.Helper()
		var env Envelope
		if err := json.Unmarshal(validEnvBytes, &env); err != nil {
			t.Fatalf("unmarshal env: %v", err)
		}
		// Seal a Patient resource (wrong type) to the responder's real enc key.
		notCER := []byte(`{"resourceType":"Patient"}`)
		resealedEnv, err := Seal(env.Metadata, notCER, responderIdent.EncPub)
		if err != nil {
			t.Fatalf("Seal notCER: %v", err)
		}
		// Re-mint token bound to new ciphertext so VerifyBound passes.
		ctHash := sha256.Sum256(resealedEnv.Ciphertext)
		inTok := Token{
			Operation:     "eligibility-inquiry",
			Scope:         "coverage",
			Subject:       "pci:x",
			Frame:         "provider-tpo",
			CorrelationID: env.Metadata.CorrelationID,
			Holder:        env.Metadata.Sender,
			PayloadHash:   hex.EncodeToString(ctHash[:]),
			Expiry:        now.Add(time.Hour),
		}
		inTok.Signature = ed25519.Sign(authzPriv, tokenSigningPayload(inTok))
		inTokJSON, _ := json.Marshal(inTok)
		resealedEnv.Metadata.AuthzToken = string(inTokJSON)
		b, err := EncodeEnvelope(resealedEnv)
		if err != nil {
			t.Fatalf("EncodeEnvelope notCEREnv: %v", err)
		}
		return b
	}

	// unknownSenderEnv: valid ciphertext sealed to the responder's key, but
	// Metadata.Sender is a holder the test ResolveEnc map does NOT contain; the
	// token is minted with Holder = that unknown sender so VerifyBound passes.
	unknownSenderEnv := func(t *testing.T) []byte {
		t.Helper()
		var env Envelope
		if err := json.Unmarshal(validEnvBytes, &env); err != nil {
			t.Fatalf("unmarshal env: %v", err)
		}
		env.Metadata.Sender = "unknown-holder-xyz"
		// Use a well-formed CER payload so parsing succeeds; only ResolveEnc fails.
		cerPayload, err := BuildEligibilityRequest("MBR-001", "9999999999", now)
		if err != nil {
			t.Fatalf("BuildEligibilityRequest unknownSender: %v", err)
		}
		// Re-seal to responder's key so decryption succeeds.
		resealedEnv, err := Seal(env.Metadata, cerPayload, responderIdent.EncPub)
		if err != nil {
			t.Fatalf("Seal unknownSender: %v", err)
		}
		ctHash := sha256.Sum256(resealedEnv.Ciphertext)
		inTok := Token{
			Operation:     "eligibility-inquiry",
			Scope:         "coverage",
			Subject:       "pci:x",
			Frame:         "provider-tpo",
			CorrelationID: env.Metadata.CorrelationID,
			Holder:        "unknown-holder-xyz",
			PayloadHash:   hex.EncodeToString(ctHash[:]),
			Expiry:        now.Add(time.Hour),
		}
		inTok.Signature = ed25519.Sign(authzPriv, tokenSigningPayload(inTok))
		inTokJSON, _ := json.Marshal(inTok)
		resealedEnv.Metadata.AuthzToken = string(inTokJSON)
		b, err := EncodeEnvelope(resealedEnv)
		if err != nil {
			t.Fatalf("EncodeEnvelope unknownSenderEnv: %v", err)
		}
		return b
	}

	type row struct {
		name      string
		body      func(t *testing.T) []byte
		hubHeader func(jti string) string
		wantCode  int
		wantError string
	}

	hubHdr := func(jti string) string {
		return makeHubAssertion(t, hubPriv, "hub", responderID, now, 2*time.Minute, jti)
	}
	noHdr := func(string) string { return "" }

	rows := []row{
		{
			name:      "no hub assertion",
			body:      func(t *testing.T) []byte { return validEnvBytes },
			hubHeader: noHdr,
			wantCode:  http.StatusForbidden,
			wantError: "missing or invalid hub assertion",
		},
		{
			name:      "garbage body with valid assertion",
			body:      func(t *testing.T) []byte { return []byte("{not json") },
			hubHeader: hubHdr,
			wantCode:  http.StatusBadRequest,
			wantError: "decode envelope failed",
		},
		{
			name: "missing authority frame",
			body: func(t *testing.T) []byte {
				return mutateThenSign(t, validEnvBytes, authzPriv, now, func(env *Envelope) {
					env.Metadata.AuthorityFrame = ""
				})
			},
			hubHeader: hubHdr,
			wantCode:  http.StatusBadRequest,
			wantError: "missing authority frame",
		},
		{
			name: "missing correlation id",
			body: func(t *testing.T) []byte {
				return mutateThenSign(t, validEnvBytes, authzPriv, now, func(env *Envelope) {
					env.Metadata.CorrelationID = ""
				})
			},
			hubHeader: hubHdr,
			wantCode:  http.StatusBadRequest,
			wantError: "missing correlation id",
		},
		{
			name: "recipient mismatch",
			body: func(t *testing.T) []byte {
				return mutateThenSign(t, validEnvBytes, authzPriv, now, func(env *Envelope) {
					env.Metadata.Recipient = "other-payer"
				})
			},
			hubHeader: hubHdr,
			wantCode:  http.StatusForbidden,
			wantError: "envelope not addressed to this holder",
		},
		{
			name: "unknown transaction type",
			body: func(t *testing.T) []byte {
				return mutateThenSign(t, validEnvBytes, authzPriv, now, func(env *Envelope) {
					env.Metadata.TransactionType = "unsupported-tx"
				})
			},
			hubHeader: hubHdr,
			wantCode:  http.StatusBadRequest,
			wantError: "unknown transaction type",
		},
		{
			name:      "token json garbage",
			body:      garbleTokenEnv,
			hubHeader: hubHdr,
			wantCode:  http.StatusForbidden,
			wantError: "invalid authz token",
		},
		{
			name:      "token bound to different ciphertext",
			body:      wrongPayloadHashEnv,
			hubHeader: hubHdr,
			wantCode:  http.StatusForbidden,
			wantError: "authz verification failed",
		},
		{
			name:      "ordering pin: no assertion AND garbage body -> 403",
			body:      func(t *testing.T) []byte { return []byte("{not json") },
			hubHeader: noHdr,
			wantCode:  http.StatusForbidden,
			wantError: "missing or invalid hub assertion",
		},
		{
			name:      "decryption failed (ciphertext sealed to wrong key)",
			body:      wrongKeyEnv,
			hubHeader: hubHdr,
			wantCode:  http.StatusBadRequest,
			wantError: "decryption failed",
		},
		{
			name:      "parse member failed (non-CER payload)",
			body:      notCEREnv,
			hubHeader: hubHdr,
			wantCode:  http.StatusBadRequest,
			wantError: "parse member failed",
		},
		{
			name:      "requester key not resolvable (unknown sender)",
			body:      unknownSenderEnv,
			hubHeader: hubHdr,
			wantCode:  http.StatusBadGateway,
			wantError: "requester key not resolvable",
		},
	}

	for i, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := makeResponder(t)
			jti := "jti-row-" + hex.EncodeToString([]byte{byte(i)})
			body := tc.body(t)
			hdr := tc.hubHeader(jti)
			resp := postInbound(t, srv, body, hdr)
			respBody := readBody(t, resp)
			if resp.StatusCode != tc.wantCode {
				t.Errorf("status = %d, want %d; body: %s", resp.StatusCode, tc.wantCode, respBody)
			}
			var errResp struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(respBody, &errResp); err != nil {
				t.Fatalf("unmarshal error body %q: %v", respBody, err)
			}
			if errResp.Error != tc.wantError {
				t.Errorf("error = %q, want %q", errResp.Error, tc.wantError)
			}
		})
	}

	// Hub assertion replay: first succeeds (200), second with same jti fails (403).
	t.Run("hub assertion replay", func(t *testing.T) {
		_, srv := makeResponder(t)
		replayHdr := makeHubAssertion(t, hubPriv, "hub", responderID, now, 2*time.Minute, "jti-replay")

		// First: use a fresh valid forward (different inner corr id to avoid corr
		// collision; the responder doesn't track corr ids so it's fine).
		envBytes1, _, _, _, _ :=
			buildValidForward(t, responderID, responderIdent.EncPub, authzPriv, hubPriv, now)
		resp1 := postInbound(t, srv, envBytes1, replayHdr)
		body1 := readBody(t, resp1)
		if resp1.StatusCode != http.StatusOK {
			t.Fatalf("first request: status = %d, want 200; body: %s", resp1.StatusCode, body1)
		}

		// Second: same hub assertion jti → 403.
		envBytes2, _, _, _, _ :=
			buildValidForward(t, responderID, responderIdent.EncPub, authzPriv, hubPriv, now)
		resp2 := postInbound(t, srv, envBytes2, replayHdr)
		body2 := readBody(t, resp2)
		if resp2.StatusCode != http.StatusForbidden {
			t.Errorf("replay: status = %d, want 403; body: %s", resp2.StatusCode, body2)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body2, &errResp); err != nil {
			t.Fatalf("unmarshal replay error: %v", err)
		}
		if errResp.Error != "missing or invalid hub assertion" {
			t.Errorf("replay error = %q, want %q", errResp.Error, "missing or invalid hub assertion")
		}
	})
}

// ---- TestResponder_AuthzDownstream502 ----

// TestResponder_AuthzDownstream502 pins the downstream-failure contract: when the
// Authorization Framework returns a 5xx, the responder responds 502 with
// {"error":"authorize response leg failed"}.
func TestResponder_AuthzDownstream502(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	authzPub, authzPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen authz key: %v", err)
	}
	hubPub, hubPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen hub key: %v", err)
	}

	// A healthy authz server is used only to build a valid forward (so the
	// pipeline proceeds past token-verification to the authorize step). The
	// responder itself is wired to a 500-returning stub so its authorize call
	// fails, triggering the 502 path.
	healthyAuthz := testAuthzServer(t, authzPriv, now)
	defer healthyAuthz.Close()

	failingAuthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer failingAuthz.Close()

	responderID := "payer-responder-502"
	responderIdent, err := GenerateIdentity(responderID)
	if err != nil {
		t.Fatalf("GenerateIdentity responder: %v", err)
	}
	responderIdent.Clock = func() time.Time { return now }

	// Build a valid forward using the healthy authz server (tokens signed correctly).
	envBytes, _, sender, hubAssertion, _ :=
		buildValidForward(t, responderID, responderIdent.EncPub, authzPriv, hubPriv, now)

	senderEncPub := sender.EncPub
	r, err := NewResponder(ResponderConfig{
		Identity:        responderIdent,
		AuthzURL:        failingAuthz.URL, // responder calls failing authz
		AuthzPub:        authzPub,
		HubTransportPub: hubPub,
		ResolveEnc: func(holderID string) (*[32]byte, bool) {
			if holderID == sender.HolderID {
				return senderEncPub, true
			}
			return nil, false
		},
		Adjudicator: &testAdjudicator{covered: true},
		Clock:       func() time.Time { return now },
		Client:      failingAuthz.Client(),
	})
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp := postInbound(t, srv, envBytes, hubAssertion)
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", resp.StatusCode, body)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("unmarshal error body %q: %v", body, err)
	}
	if errResp.Error != "authorize response leg failed" {
		t.Errorf("error = %q, want %q", errResp.Error, "authorize response leg failed")
	}
}

// ---- TestNewResponder_FailsClosed ----

func TestNewResponder_FailsClosed(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	authzPub, _, _ := ed25519.GenerateKey(rand.Reader)
	hubPub, _, _ := ed25519.GenerateKey(rand.Reader)
	validIdent, _ := GenerateIdentity("payer")
	validIdent.Clock = func() time.Time { return now }
	validResolve := func(string) (*[32]byte, bool) { return nil, false }
	validAdj := &testAdjudicator{}

	tests := []struct {
		name    string
		cfg     ResponderConfig
		wantErr string
	}{
		{
			name: "zero Identity (empty HolderID)",
			cfg: ResponderConfig{
				Identity:        Identity{},
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "Identity.HolderID",
		},
		{
			name: "empty AuthzURL",
			cfg: ResponderConfig{
				Identity:        validIdent,
				AuthzURL:        "",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "AuthzURL",
		},
		{
			name: "nil AuthzPub",
			cfg: ResponderConfig{
				Identity:        validIdent,
				AuthzURL:        "http://authz",
				AuthzPub:        nil,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "AuthzPub",
		},
		{
			name: "nil HubTransportPub",
			cfg: ResponderConfig{
				Identity:        validIdent,
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: nil,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "HubTransportPub",
		},
		{
			name: "nil ResolveEnc",
			cfg: ResponderConfig{
				Identity:        validIdent,
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      nil,
				Adjudicator:     validAdj,
			},
			wantErr: "ResolveEnc",
		},
		{
			name: "nil Adjudicator",
			cfg: ResponderConfig{
				Identity:        validIdent,
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     nil,
			},
			wantErr: "Adjudicator",
		},
		{
			name: "nil SignPriv",
			cfg: ResponderConfig{
				Identity: Identity{
					HolderID: validIdent.HolderID,
					SignPub:  validIdent.SignPub,
					SignPriv: nil,
					EncPub:   validIdent.EncPub,
					EncPriv:  validIdent.EncPriv,
				},
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "Identity.SignPriv",
		},
		{
			name: "nil EncPriv",
			cfg: ResponderConfig{
				Identity: Identity{
					HolderID: validIdent.HolderID,
					SignPub:  validIdent.SignPub,
					SignPriv: validIdent.SignPriv,
					EncPub:   validIdent.EncPub,
					EncPriv:  nil,
				},
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "Identity.EncPriv",
		},
		{
			name: "nil EncPub",
			cfg: ResponderConfig{
				Identity: Identity{
					HolderID: validIdent.HolderID,
					SignPub:  validIdent.SignPub,
					SignPriv: validIdent.SignPriv,
					EncPub:   nil,
					EncPriv:  validIdent.EncPriv,
				},
				AuthzURL:        "http://authz",
				AuthzPub:        authzPub,
				HubTransportPub: hubPub,
				ResolveEnc:      validResolve,
				Adjudicator:     validAdj,
			},
			wantErr: "Identity.EncPub",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewResponder(tc.cfg)
			if err == nil {
				t.Fatalf("NewResponder(%s): expected error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---- PA-chain test harness ----

// paTestHarness holds shared key material and servers for PA-chain tests.
type paTestHarness struct {
	now          time.Time
	authzPub     ed25519.PublicKey
	authzPriv    ed25519.PrivateKey
	hubPub       ed25519.PublicKey
	hubPriv      ed25519.PrivateKey
	responderID  string
	responderEnc *[32]byte // *[32]byte alias for EncPub
	senderID     string
	senderEncPub *[32]byte
	senderEncPrv *[32]byte
	authzSrv     *httptest.Server
}

// newPAHarness sets up keys, authz server, and identities for a PA-chain test.
func newPAHarness(t *testing.T) (*paTestHarness, Identity, Identity) {
	t.Helper()
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	authzPub, authzPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen authz key: %v", err)
	}
	hubPub, hubPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen hub key: %v", err)
	}

	responderIdent, err := GenerateIdentity("test-payer")
	if err != nil {
		t.Fatalf("GenerateIdentity responder: %v", err)
	}
	responderIdent.Clock = func() time.Time { return now }

	senderIdent, err := GenerateIdentity("test-provider")
	if err != nil {
		t.Fatalf("GenerateIdentity sender: %v", err)
	}
	senderIdent.Clock = func() time.Time { return now }

	authzSrv := testAuthzServer(t, authzPriv, now)
	t.Cleanup(authzSrv.Close)

	h := &paTestHarness{
		now:          now,
		authzPub:     authzPub,
		authzPriv:    authzPriv,
		hubPub:       hubPub,
		hubPriv:      hubPriv,
		responderID:  responderIdent.HolderID,
		responderEnc: responderIdent.EncPub,
		senderID:     senderIdent.HolderID,
		senderEncPub: senderIdent.EncPub,
		senderEncPrv: senderIdent.EncPriv,
		authzSrv:     authzSrv,
	}
	return h, responderIdent, senderIdent
}

// makeResponderSrv builds a Responder+httptest.Server using the harness.
func (h *paTestHarness) makeResponderSrv(t *testing.T, responderIdent Identity, adj Adjudicator) (*Responder, *httptest.Server) {
	t.Helper()
	r, err := NewResponder(ResponderConfig{
		Identity:        responderIdent,
		AuthzURL:        h.authzSrv.URL,
		AuthzPub:        h.authzPub,
		HubTransportPub: h.hubPub,
		ResolveEnc: func(holderID string) (*[32]byte, bool) {
			if holderID == h.senderID {
				return h.senderEncPub, true
			}
			return nil, false
		},
		Adjudicator: adj,
		Clock:       func() time.Time { return h.now },
		Client:      h.authzSrv.Client(),
	})
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return r, srv
}

// buildForwardEnv builds a valid inbound envelope for the given txType and payload,
// with the authz token pinned to txOp and the hub assertion header.
func (h *paTestHarness) buildForwardEnv(t *testing.T, txType, txOp, corrID string, payload []byte) (envBytes []byte, hubHdr string) {
	t.Helper()
	meta := Metadata{
		Sender:          h.senderID,
		Recipient:       h.responderID,
		TransactionType: txType,
		AuthorityFrame:  "provider-tpo",
		Timestamp:       h.now.UTC().Format(time.RFC3339),
		CorrelationID:   corrID,
	}
	env, err := Seal(meta, payload, h.responderEnc)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	ctHash := sha256.Sum256(env.Ciphertext)
	subject := ResolvePCI("MBR-001", "1975-04-02", "Johansson")
	inTok := Token{
		Operation:     txOp,
		Scope:         "coverage",
		Subject:       subject,
		Frame:         "provider-tpo",
		CorrelationID: corrID,
		Holder:        h.senderID,
		PayloadHash:   hex.EncodeToString(ctHash[:]),
		Expiry:        h.now.Add(time.Hour),
	}
	inTok.Signature = ed25519.Sign(h.authzPriv, tokenSigningPayload(inTok))
	inTokJSON, _ := json.Marshal(inTok)
	env.Metadata.AuthzToken = string(inTokJSON)
	envBytes, err = EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	hubHdr = makeHubAssertion(t, h.hubPriv, "hub", h.responderID, h.now, 2*time.Minute, "jti-"+corrID)
	return envBytes, hubHdr
}

// openResponse decrypts the response envelope using the sender's enc keys.
func (h *paTestHarness) openResponse(t *testing.T, body []byte) []byte {
	t.Helper()
	respEnv, err := DecodeEnvelope(body)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	plaintext, err := Open(respEnv, h.senderEncPub, h.senderEncPrv)
	if err != nil {
		t.Fatalf("Open response: %v", err)
	}
	return plaintext
}

// assertError asserts that resp is an error response with the given status and message.
func assertError(t *testing.T, resp *http.Response, body []byte, wantStatus int, wantMsg string) {
	t.Helper()
	if resp.StatusCode != wantStatus {
		t.Errorf("status = %d, want %d; body: %s", resp.StatusCode, wantStatus, body)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("unmarshal error body %q: %v", body, err)
	}
	if errResp.Error != wantMsg {
		t.Errorf("error = %q, want %q", errResp.Error, wantMsg)
	}
}

// ---- TestResponder_CRD ----
// ---- TestResponder_DTR ----

// TestResponder_DTR proves the dtr-questionnaire-fetch dispatch: happy path + rejections.
func TestResponder_DTR(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &paTestAdjudicator{now: h.now}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)

	t.Run("happy path → $questionnaire-package round-trips", func(t *testing.T) {
		fetchPayload, err := BuildQuestionnaireFetch(SupportedQuestionnaireCanonical)
		if err != nil {
			t.Fatalf("BuildQuestionnaireFetch: %v", err)
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-happy-1", fetchPayload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		// §6.2: the responder now emits a $questionnaire-package collection Bundle;
		// the bare Questionnaire is extracted on the far side.
		questionnaireJSON, err := ExtractQuestionnaireFromPackage(plaintext)
		if err != nil {
			t.Fatalf("ExtractQuestionnaireFromPackage: %v; body: %s", err, plaintext)
		}
		url, err := ParseQuestionnaireURL(questionnaireJSON)
		if err != nil {
			t.Fatalf("ParseQuestionnaireURL: %v", err)
		}
		if url != SupportedQuestionnaireCanonical {
			t.Errorf("canonical = %q, want %q", url, SupportedQuestionnaireCanonical)
		}
	})

	t.Run("unknown canonical → 400 unknown questionnaire canonical", func(t *testing.T) {
		fetchPayload, _ := BuildQuestionnaireFetch("http://example.com/unknown-questionnaire")
		envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-unknown-1", fetchPayload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "unknown questionnaire canonical")
	})

	t.Run("garbage payload → 400 parse questionnaire fetch failed", func(t *testing.T) {
		envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-garbage-1", []byte("not json"))
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "parse questionnaire fetch failed")
	})
}

// ---- FR-16/FR-27 attestation conformance fence (parity with the substrate
// gateway's gateway/engine/attestfence_test.go — the two fences are twins on
// purpose, so their rejection rows are too) ----

// fenceWrapQRItemBundle wraps a single QuestionnaireResponse.item (produced by a
// real builder, or a plain system-sourced item literal) into the minimal
// conformant PAS Bundle shape fenceAttestedItems walks. No extension URL is
// hand-typed here — only generic FHIR envelope keys.
func fenceWrapQRItemBundle(t *testing.T, itemJSON []byte) []byte {
	t.Helper()
	var item map[string]any
	if err := json.Unmarshal(itemJSON, &item); err != nil {
		t.Fatalf("fenceWrapQRItemBundle: unmarshal item: %v", err)
	}
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry": []any{map[string]any{"resource": map[string]any{
			"resourceType": "QuestionnaireResponse",
			"status":       "completed",
			"item":         []any{item},
		}}},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("fenceWrapQRItemBundle: marshal bundle: %v", err)
	}
	return raw
}

// fenceWrapTwoQRItemBundle wraps TWO QuestionnaireResponse.item values into a
// Bundle carrying TWO separate QuestionnaireResponse entries (first, second) —
// the hand-built-bundle shape the FR-16/FR-27 fence must walk in full, not
// only its first entry.
func fenceWrapTwoQRItemBundle(t *testing.T, firstItemJSON, secondItemJSON []byte) []byte {
	t.Helper()
	qrEntry := func(itemJSON []byte) map[string]any {
		var item map[string]any
		if err := json.Unmarshal(itemJSON, &item); err != nil {
			t.Fatalf("fenceWrapTwoQRItemBundle: unmarshal item: %v", err)
		}
		return map[string]any{"resource": map[string]any{
			"resourceType": "QuestionnaireResponse",
			"status":       "completed",
			"item":         []any{item},
		}}
	}
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry":        []any{qrEntry(firstItemJSON), qrEntry(secondItemJSON)},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("fenceWrapTwoQRItemBundle: marshal bundle: %v", err)
	}
	return raw
}

// TestFenceAttestedItems_SecondQREntryFenced closes the multi-QR bypass: a
// hand-built bundle can carry a SECOND QuestionnaireResponse bundle entry, and
// the fence must walk it exactly as it walks the first. Parity with the
// substrate gateway's identical row (gateway/engine/attestfence_test.go) — the
// two fences are twins on purpose, so their rejection rows are too.
func TestFenceAttestedItems_SecondQREntryFenced(t *testing.T) {
	att := Attestation{NPI: "1999999999", Text: "I attest these are my clinical findings.", When: "2026-06-04"}

	cleanFirst, err := BuildManualAttestedItem("functional-status-oswestry", "42", att)
	if err != nil {
		t.Fatalf("BuildManualAttestedItem (first, clean): %v", err)
	}

	t.Run("second-qr-defective-rejects", func(t *testing.T) {
		defectiveSecond, err := BuildManualAttestedItem("functional-status-oswestry", "43", att)
		if err != nil {
			t.Fatalf("BuildManualAttestedItem (second, defective): %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(defectiveSecond, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		delete(m, "extension")
		defectiveSecond, err = json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reason, ok := fenceAttestedItems(fenceWrapTwoQRItemBundle(t, cleanFirst, defectiveSecond))
		if ok {
			t.Fatalf("second QR entry carries an unattested clinician item: want ok=false, got accept")
		}
		if !strings.Contains(reason, "FR-16") {
			t.Fatalf("reject reason %q does not name FR-16", reason)
		}
	})

	t.Run("two-clean-qr-entries-pass", func(t *testing.T) {
		cleanSecond, err := BuildManualAttestedItem("functional-status-oswestry", "43", att)
		if err != nil {
			t.Fatalf("BuildManualAttestedItem (second, clean): %v", err)
		}
		reason, ok := fenceAttestedItems(fenceWrapTwoQRItemBundle(t, cleanFirst, cleanSecond))
		if !ok {
			t.Fatalf("two clean QR entries: want ok=true, got reject reason=%q", reason)
		}
	})
}

// fenceDropSubExtension deletes the sub-extension whose url == sub from the
// item-level extension whose url == parent — the "field absent" mutation, twin
// of the "field present but blank" one an empty Attestation field produces.
func fenceDropSubExtension(t *testing.T, itemJSON []byte, parent, sub string) []byte {
	t.Helper()
	var item map[string]any
	if err := json.Unmarshal(itemJSON, &item); err != nil {
		t.Fatalf("fenceDropSubExtension: unmarshal: %v", err)
	}
	dropped := false
	extAny, _ := item["extension"].([]any)
	for _, e := range extAny {
		em, ok := e.(map[string]any)
		if !ok || em["url"] != parent {
			continue
		}
		subAny, _ := em["extension"].([]any)
		kept := make([]any, 0, len(subAny))
		for _, s := range subAny {
			sm, ok := s.(map[string]any)
			if ok && sm["url"] == sub {
				dropped = true
				continue
			}
			kept = append(kept, s)
		}
		em["extension"] = kept
	}
	if !dropped {
		t.Fatalf("fenceDropSubExtension: sub-extension %q not found under %q", sub, parent)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("fenceDropSubExtension: marshal: %v", err)
	}
	return raw
}

// TestFenceAttestedItems_ClinicianContent proves this module's fence enforces
// FR-16's CONTENT, not merely the presence of an element with the right url: an
// attestation whose NPI, text or date is blank or absent attests nothing and is
// rejected, naming FR-16 and the offending field. One row per mandated field in
// both mutation forms, the wholly-empty attestation that motivated the guard,
// and a positive control proving a well-formed attestation still passes.
func TestFenceAttestedItems_ClinicianContent(t *testing.T) {
	const (
		goodNPI  = "1999999999"
		goodText = "I attest these are my clinical findings."
		goodWhen = "2026-06-04"
	)
	good := Attestation{NPI: goodNPI, Text: goodText, When: goodWhen}

	t.Run("complete-attestation-passes", func(t *testing.T) {
		item, err := BuildManualAttestedItem("functional-status-oswestry", "42", good)
		if err != nil {
			t.Fatalf("BuildManualAttestedItem: %v", err)
		}
		if reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, item)); !ok {
			t.Fatalf("complete attestation: want ok=true, got reject reason=%q", reason)
		}
	})

	t.Run("no-attestation-extension-rejects", func(t *testing.T) {
		item, err := BuildManualAttestedItem("functional-status-oswestry", "42", good)
		if err != nil {
			t.Fatalf("BuildManualAttestedItem: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(item, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		delete(m, "extension")
		stripped, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, stripped))
		if ok {
			t.Fatalf("attestation extension stripped: want ok=false, got accept")
		}
		if !strings.Contains(reason, "FR-16") {
			t.Fatalf("reject reason %q does not name FR-16", reason)
		}
	})

	for _, tc := range []struct {
		field string
		att   Attestation
	}{
		{"npi", Attestation{NPI: "", Text: goodText, When: goodWhen}},
		{"text", Attestation{NPI: goodNPI, Text: "", When: goodWhen}},
		{"date", Attestation{NPI: goodNPI, Text: goodText, When: ""}},
	} {
		t.Run("blank-"+tc.field+"-rejects", func(t *testing.T) {
			item, err := BuildManualAttestedItem("functional-status-oswestry", "42", tc.att)
			if err != nil {
				t.Fatalf("BuildManualAttestedItem: %v", err)
			}
			reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, item))
			if ok {
				t.Fatalf("attestation with blank %q: want ok=false, got accept", tc.field)
			}
			if !strings.Contains(reason, "FR-16") || !strings.Contains(reason, tc.field) {
				t.Fatalf("reject reason %q does not name both FR-16 and %q", reason, tc.field)
			}
		})

		t.Run("absent-"+tc.field+"-rejects", func(t *testing.T) {
			item, err := BuildManualAttestedItem("functional-status-oswestry", "42", good)
			if err != nil {
				t.Fatalf("BuildManualAttestedItem: %v", err)
			}
			mutated := fenceDropSubExtension(t, item, ClinicianAttestationExt, tc.field)
			reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, mutated))
			if ok {
				t.Fatalf("attestation with %q deleted: want ok=false, got accept", tc.field)
			}
			if !strings.Contains(reason, "FR-16") || !strings.Contains(reason, tc.field) {
				t.Fatalf("reject reason %q does not name both FR-16 and %q", reason, tc.field)
			}
		})
	}

	t.Run("all-fields-blank-rejects", func(t *testing.T) {
		item, err := BuildManualAttestedItem("functional-status-oswestry", "42", Attestation{})
		if err != nil {
			t.Fatalf("BuildManualAttestedItem: %v", err)
		}
		reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, item))
		if ok {
			t.Fatalf("wholly empty attestation: want ok=false, got accept")
		}
		if !strings.Contains(reason, "FR-16") {
			t.Fatalf("reject reason %q does not name FR-16", reason)
		}
	})
}

// fenceBlankSignatureField blanks one string field of the FR-27 valueSignature —
// "when" / "data" directly, "who.reference" through the nested object, and
// "type" by emptying the coding array.
func fenceBlankSignatureField(t *testing.T, itemJSON []byte, field string) []byte {
	t.Helper()
	var item map[string]any
	if err := json.Unmarshal(itemJSON, &item); err != nil {
		t.Fatalf("fenceBlankSignatureField: unmarshal: %v", err)
	}
	blanked := false
	extAny, _ := item["extension"].([]any)
	for _, e := range extAny {
		em, ok := e.(map[string]any)
		if !ok || em["url"] != QRSignatureExt {
			continue
		}
		value, ok := em["valueSignature"].(map[string]any)
		if !ok {
			continue
		}
		switch field {
		case "type":
			value["type"] = []any{}
		case "who.reference":
			who, _ := value["who"].(map[string]any)
			if who == nil {
				t.Fatalf("fenceBlankSignatureField: valueSignature has no who")
			}
			who["reference"] = ""
		default:
			value[field] = ""
		}
		blanked = true
	}
	if !blanked {
		t.Fatalf("fenceBlankSignatureField: no patient signature extension found")
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("fenceBlankSignatureField: marshal: %v", err)
	}
	return raw
}

// TestFenceAttestedItems_PatientContent is the FR-27 twin: a
// questionnaireresponse-signature whose typed assertion, timestamp, signer or
// identity token is blank is not an attestation, and is rejected. Positive
// control included.
func TestFenceAttestedItems_PatientContent(t *testing.T) {
	build := func(t *testing.T) []byte {
		t.Helper()
		item, err := BuildPatientAttestedItem("functional-status-oswestry", "42", "Patient/MBR-UC07", "2026-06-04")
		if err != nil {
			t.Fatalf("BuildPatientAttestedItem: %v", err)
		}
		return item
	}

	t.Run("complete-signature-passes", func(t *testing.T) {
		if reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, build(t))); !ok {
			t.Fatalf("complete patient attestation: want ok=true, got reject reason=%q", reason)
		}
	})

	// who.reference's wantSubstr is "who", not "who.reference": since Task D (register §15(b))
	// the fence accepts FHIR R4's two legal forms for Signature.who (reference OR identifier),
	// so blanking ONLY who.reference — leaving who with neither a usable reference nor an
	// identifier — is refused under the combined "who" reason. See TestFenceWhoUsable for the
	// fence's actual new decision boundary (both legal forms, the identifier-empty-value
	// refusal, and the identifier-only acceptance).
	fields := []struct{ field, wantSubstr string }{
		{"type", "type"},
		{"when", "when"},
		{"who.reference", "who"},
		{"data", "data"},
	}
	for _, tc := range fields {
		t.Run("blank-"+tc.field+"-rejects", func(t *testing.T) {
			mutated := fenceBlankSignatureField(t, build(t), tc.field)
			reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, mutated))
			if ok {
				t.Fatalf("patient attestation with blank %q: want ok=false, got accept", tc.field)
			}
			if !strings.Contains(reason, "FR-27") || !strings.Contains(reason, tc.wantSubstr) {
				t.Fatalf("reject reason %q does not name both FR-27 and %q", reason, tc.wantSubstr)
			}
		})
	}
}

// TestFenceWhoUsable is the sdk twin of gateway/engine's TestFenceWhoUsable (register §15(b) /
// Task D): the FR-27 fence's decision boundary for Signature.who now accepts FHIR R4's two
// legal forms — reference OR identifier — rather than reference-only. who absent, or with
// neither a usable reference nor a usable identifier, is still refused; an identifier with an
// empty "value" is refused (Task D's ruling: "value" is required, "system" is not); and a who
// carrying ONLY an identifier (no reference at all) — the shape a conformant partner PHG such
// as DHIN's may legally send — is accepted.
func TestFenceWhoUsable(t *testing.T) {
	setWho := func(t *testing.T, who map[string]any) []byte {
		t.Helper()
		item, err := BuildPatientAttestedItem("functional-status-oswestry", "42", "Patient/MBR-UC07", "2026-06-04")
		if err != nil {
			t.Fatalf("BuildPatientAttestedItem: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(item, &parsed); err != nil {
			t.Fatalf("unmarshal built item: %v", err)
		}
		extAny, _ := parsed["extension"].([]any)
		found := false
		for _, e := range extAny {
			em, ok := e.(map[string]any)
			if !ok || em["url"] != QRSignatureExt {
				continue
			}
			value, ok := em["valueSignature"].(map[string]any)
			if !ok {
				continue
			}
			if who == nil {
				delete(value, "who")
			} else {
				value["who"] = who
			}
			found = true
		}
		if !found {
			t.Fatalf("setWho: no patient signature extension found")
		}
		raw, err := json.Marshal(parsed)
		if err != nil {
			t.Fatalf("marshal mutated item: %v", err)
		}
		return raw
	}

	rejects := map[string]map[string]any{
		"who-absent":                           nil,
		"who-empty-object":                     {},
		"who-neither-reference-nor-identifier": {"type": "Practitioner"},
		"who-identifier-empty-value":           {"identifier": map[string]any{"system": "urn:dhin:signer", "value": ""}},
	}
	for name, who := range rejects {
		t.Run(name+"-rejects", func(t *testing.T) {
			reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, setWho(t, who)))
			if ok {
				t.Fatalf("who=%v: want ok=false, got accept", who)
			}
			if !strings.Contains(reason, "FR-27") || !strings.Contains(reason, "who") {
				t.Fatalf("reject reason %q does not name both FR-27 and \"who\"", reason)
			}
		})
	}

	t.Run("who-identifier-only-accepts", func(t *testing.T) {
		who := map[string]any{"identifier": map[string]any{"system": "urn:dhin:signer", "value": "dhin-signer-88f21c"}}
		if reason, ok := fenceAttestedItems(fenceWrapQRItemBundle(t, setWho(t, who))); !ok {
			t.Fatalf("who carrying only an identifier (no reference): want ok=true, got reject reason=%q", reason)
		}
	})
}
