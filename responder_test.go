package shnsdk

import (
	"bytes"
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
				var a assertion
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
// use sandbox defaults so existing eligibility tests are unaffected.
type testAdjudicator struct {
	covered bool
	reason  string
}

func (a *testAdjudicator) Eligibility(_ string) (bool, string) {
	return a.covered, a.reason
}

func (a *testAdjudicator) OrderSelect(cpt string) (bool, string) {
	if cpt == "72148" {
		return true, QuestionnaireCanonicalLumbarMRI
	}
	return false, ""
}

func (a *testAdjudicator) Questionnaire(canonical string) ([]byte, bool) {
	if canonical == QuestionnaireCanonicalLumbarMRI {
		return SandboxLumbarQuestionnaire(), true
	}
	return nil, false
}

func (a *testAdjudicator) PriorAuth(qrJSON []byte, hasDR bool) (PASDecision, error) {
	return SandboxAdjudicate(qrJSON, hasDR, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), nil)
}

// sandboxTestAdjudicator is the full sandbox adjudicator used in PA-chain tests.
// It delegates to the same sandbox helpers the sample participant uses.
type sandboxTestAdjudicator struct {
	now time.Time
}

func (a *sandboxTestAdjudicator) Eligibility(_ string) (bool, string) { return true, "" }

func (a *sandboxTestAdjudicator) OrderSelect(cpt string) (bool, string) {
	if cpt == "72148" {
		return true, QuestionnaireCanonicalLumbarMRI
	}
	return false, ""
}

func (a *sandboxTestAdjudicator) Questionnaire(canonical string) ([]byte, bool) {
	if canonical == QuestionnaireCanonicalLumbarMRI {
		return SandboxLumbarQuestionnaire(), true
	}
	return nil, false
}

func (a *sandboxTestAdjudicator) PriorAuth(qrJSON []byte, hasDR bool) (PASDecision, error) {
	return SandboxAdjudicate(qrJSON, hasDR, a.now, nil)
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

// TestResponder_CRD proves the crd-order-select dispatch: happy path + rejection rows.
func TestResponder_CRD(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)

	// Build a valid order-select payload.
	patientRef := "Patient/MBR-COVERED"
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	covJSON, err := BuildCoverage(patientRef, "Coverage/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildCoverage: %v", err)
	}
	osPayload, err := BuildOrderSelectRequest(srJSON, covJSON, patientRef)
	if err != nil {
		t.Fatalf("BuildOrderSelectRequest: %v", err)
	}

	t.Run("happy path → crd-cards + PA required + canonical", func(t *testing.T) {
		corrID := "crd-happy-1"
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", corrID, osPayload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		paRequired, canonical, err := ParseCards(plaintext)
		if err != nil {
			t.Fatalf("ParseCards: %v", err)
		}
		if !paRequired {
			t.Error("paRequired = false, want true")
		}
		if canonical != QuestionnaireCanonicalLumbarMRI {
			t.Errorf("canonical = %q, want %q", canonical, QuestionnaireCanonicalLumbarMRI)
		}
	})

	t.Run("garbage payload → 400 parse order-select failed", func(t *testing.T) {
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-garbage-1", []byte("not json"))
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "parse order-select failed")
	})

	t.Run("SR subject ≠ coverage beneficiary → 400 inconsistent patient", func(t *testing.T) {
		// Build an OS request where SR is for patient A, Coverage for patient B.
		srA, _ := BuildServiceRequest("72148", "MRI", "M51.16", "Patient/A")
		covB, _ := BuildCoverage("Patient/B", "Coverage/B")
		payload, _ := BuildOrderSelectRequest(srA, covB, "Patient/A")
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-inconsist-1", payload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "inconsistent patient in order-select")
	})

	t.Run("context.patientId mismatch → 400 inconsistent patient", func(t *testing.T) {
		// SR and Coverage agree on patient A, but context.patientId is B.
		srA, _ := BuildServiceRequest("72148", "MRI", "M51.16", "Patient/A")
		covA, _ := BuildCoverage("Patient/A", "Coverage/A")
		payload, _ := BuildOrderSelectRequest(srA, covA, "Patient/B") // mismatch context
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-ctx-mismatch-1", payload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "inconsistent patient in order-select")
	})

	t.Run("CPT missing → 400 parse CPT failed", func(t *testing.T) {
		// Build a SR with no code.coding (just resourceType + subject).
		srNoCPT := []byte(`{"resourceType":"ServiceRequest","status":"draft","intent":"order","subject":{"reference":"Patient/MBR-COVERED"},"code":{"coding":[]}}`)
		covOK, _ := BuildCoverage(patientRef, "Coverage/MBR-COVERED")
		payload, _ := BuildOrderSelectRequest(srNoCPT, covOK, patientRef)
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-nocpt-1", payload)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "parse CPT failed")
	})
}

// ---- TestResponder_DTR ----

// TestResponder_DTR proves the dtr-questionnaire-fetch dispatch: happy path + rejections.
func TestResponder_DTR(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)

	t.Run("happy path → questionnaire round-trips", func(t *testing.T) {
		fetchPayload, err := BuildQuestionnaireFetch(QuestionnaireCanonicalLumbarMRI)
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
		url, err := ParseQuestionnaireURL(plaintext)
		if err != nil {
			t.Fatalf("ParseQuestionnaireURL: %v", err)
		}
		if url != QuestionnaireCanonicalLumbarMRI {
			t.Errorf("canonical = %q, want %q", url, QuestionnaireCanonicalLumbarMRI)
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

// ---- PAS test helpers ----

// approvedQRForResponder builds an approved-path QR for responder PAS tests.
func approvedQRForResponder(t *testing.T, patientRef string, now time.Time) []byte {
	t.Helper()
	q := SandboxLumbarQuestionnaire()
	qr, err := FillQuestionnaire(q, SandboxUC03Context(), QRContext{
		PatientRef:  patientRef,
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-1",
		Authored:    now,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire approved: %v", err)
	}
	return qr
}

// priorSurgeryQRForResponder builds a prior-surgery QR (pended path).
func priorSurgeryQRForResponder(t *testing.T, patientRef string, now time.Time) []byte {
	t.Helper()
	q := SandboxLumbarQuestionnaire()
	cc := SandboxUC03Context()
	cc.PriorSurgery = true
	cc.PriorSurgeryRef = "Procedure/proc-laminectomy"
	qr, err := FillQuestionnaire(q, cc, QRContext{
		PatientRef:  patientRef,
		CoverageRef: "Coverage/MBR-UC04",
		OrderRef:    "ServiceRequest/sr-1",
		Authored:    now,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire prior-surgery: %v", err)
	}
	return qr
}

// deniedQRForResponder builds a denied-path QR (4 weeks conservative therapy).
func deniedQRForResponder(t *testing.T, patientRef string, now time.Time) []byte {
	t.Helper()
	q := SandboxLumbarQuestionnaire()
	cc := SandboxUC08Context()
	qr, err := FillQuestionnaire(q, cc, QRContext{
		PatientRef:  patientRef,
		CoverageRef: "Coverage/MBR-UC08",
		OrderRef:    "ServiceRequest/sr-1",
		Authored:    now,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire denied: %v", err)
	}
	return qr
}

// buildValidPASBundle creates a valid PAS submit bundle for the given QR and SR.
func buildValidPASBundle(t *testing.T, qrJSON, srJSON []byte, patientRef, corrID string, now time.Time) []byte {
	t.Helper()
	bundle, err := BuildClaimBundle(qrJSON, srJSON, patientRef, "Coverage/MBR-COVERED", corrID, now)
	if err != nil {
		t.Fatalf("BuildClaimBundle: %v", err)
	}
	return bundle
}

// ---- TestResponder_PASSubmit ----

// TestResponder_PASSubmit proves the pas-claim dispatch: all three branches + rejections.
func TestResponder_PASSubmit(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)

	const patientRef = "Patient/MBR-COVERED"
	srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)

	t.Run("approved (UC-03 QR + hasDR bundle) → ClaimResponse approved", func(t *testing.T) {
		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		// Build a bundle with a DiagnosticReport so hasDR=true.
		drJSON, err := BuildDiagnosticReport("dr-test-1", patientRef, "72148", "MRI lumbar spine")
		if err != nil {
			t.Fatalf("BuildDiagnosticReport: %v", err)
		}
		// Add DR to a bundle manually.
		corrID := "pas-approved-1"
		// BuildClaimBundle doesn't support DR directly, build via update helper conceptually.
		// Use BuildClaimBundle then add DR via custom approach.
		bundle, err := BuildClaimBundle(qrJSON, srJSON, patientRef, "Coverage/MBR-COVERED", corrID, h.now)
		if err != nil {
			t.Fatalf("BuildClaimBundle: %v", err)
		}
		// Inject DR into the bundle JSON.
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(bundle, &raw)
		var entries []json.RawMessage
		_ = json.Unmarshal(raw["entry"], &entries)
		drEntry, _ := json.Marshal(map[string]interface{}{
			"resource": json.RawMessage(drJSON),
		})
		entries = append(entries, json.RawMessage(drEntry))
		entriesJSON, _ := json.Marshal(entries)
		raw["entry"] = entriesJSON
		bundle, _ = json.Marshal(raw)

		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", corrID, bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		cr, err := ParseClaimResponse(plaintext)
		if err != nil {
			t.Fatalf("ParseClaimResponse: %v", err)
		}
		if cr.Outcome != "approved" {
			t.Errorf("outcome = %q, want approved", cr.Outcome)
		}
		if cr.PreAuthRef == "" {
			t.Error("PreAuthRef is empty, want non-empty")
		}
	})

	t.Run("pended (prior-surgery QR, no DR) → pended bundle with NeededItems", func(t *testing.T) {
		patientPend := "Patient/MBR-UC04"
		srPend, _ := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientPend)
		qrPend := priorSurgeryQRForResponder(t, patientPend, h.now)
		corrID := "pas-pended-1"
		bundle := buildValidPASBundle(t, qrPend, srPend, patientPend, corrID, h.now)

		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", corrID, bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		pended, items, err := ParsePendedResponse(plaintext)
		if err != nil {
			t.Fatalf("ParsePendedResponse: %v", err)
		}
		if !pended {
			t.Error("pended = false, want true")
		}
		if len(items) == 0 {
			t.Error("NeededItems is empty, want at least one")
		}
	})

	t.Run("denied (< 6 weeks) → denial parses with rationale", func(t *testing.T) {
		patientDeny := "Patient/MBR-UC08"
		srDeny, _ := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientDeny)
		qrDeny := deniedQRForResponder(t, patientDeny, h.now)
		corrID := "pas-denied-1"
		// Override coverage ref in bundle to match patient.
		bundle2, _ := BuildClaimBundle(qrDeny, srDeny, patientDeny, "Coverage/MBR-UC08", corrID, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", corrID, bundle2)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		cr, err := ParseClaimResponse(plaintext)
		if err != nil {
			t.Fatalf("ParseClaimResponse: %v", err)
		}
		if cr.Outcome != "denied" {
			t.Errorf("outcome = %q, want denied", cr.Outcome)
		}
		if cr.Denial == nil || cr.Denial.Rationale == "" {
			t.Error("Denial rationale is empty")
		}
	})

	t.Run("garbage payload → 400 parse bundle failed", func(t *testing.T) {
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-garbage-1", []byte("not json"))
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusBadRequest, "parse bundle failed")
	})

	t.Run("QR subject missing → 403 PAS bundle QuestionnaireResponse missing subject", func(t *testing.T) {
		qrNoSubj := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-ns","status":"completed"}`)
		srOK, _ := BuildServiceRequest("72148", "MRI", "M51.16", patientRef)
		bundle, _ := BuildClaimBundle(qrNoSubj, srOK, patientRef, "Coverage/MBR-COVERED", "pas-qrsubj-1", h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-qrsubj-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusForbidden, "PAS bundle QuestionnaireResponse missing subject")
	})

	t.Run("SR/QR patient ≠ Claim patient → 403 inconsistent patient in PAS bundle", func(t *testing.T) {
		qrB := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-b","status":"completed","subject":{"reference":"Patient/B"}}`)
		srB, _ := BuildServiceRequest("72148", "MRI", "M51.16", "Patient/B")
		// Bundle with Claim for Patient/A but QR/SR for Patient/B.
		bundle, _ := BuildClaimBundle(qrB, srB, "Patient/A", "Coverage/A", "pas-inconsist-1", h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-inconsist-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusForbidden, "inconsistent patient in PAS bundle")
	})

	t.Run("adjudicator error → 422 with error text", func(t *testing.T) {
		// Use a custom adjudicator that always errors.
		errAdj := &errorAdjudicator{paErr: "payer system unavailable"}
		_, errSrv := h.makeResponderSrv(t, responderIdent, errAdj)

		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		bundle := buildValidPASBundle(t, qrJSON, srJSON, patientRef, "pas-err-1", h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-err-1", bundle)
		resp := postInbound(t, errSrv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusUnprocessableEntity, "payer system unavailable")
	})
}

// errorAdjudicator is an adjudicator that returns an error from PriorAuth.
type errorAdjudicator struct {
	paErr string
}

func (a *errorAdjudicator) Eligibility(_ string) (bool, string)   { return true, "" }
func (a *errorAdjudicator) OrderSelect(_ string) (bool, string)   { return false, "" }
func (a *errorAdjudicator) Questionnaire(_ string) ([]byte, bool) { return nil, false }
func (a *errorAdjudicator) PriorAuth(_ []byte, _ bool) (PASDecision, error) {
	return PASDecision{}, &paErrString{a.paErr}
}

type paErrString struct{ s string }

func (e *paErrString) Error() string { return e.s }

// ---- TestResponder_PASUpdate ----

// TestResponder_PASUpdate proves the pas-claim-update dispatch: happy path + rejections.
func TestResponder_PASUpdate(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}

	// Build the base pended submit to exercise the ledger.
	patientRef := "Patient/MBR-UC04"
	srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)
	qrPend := priorSurgeryQRForResponder(t, patientRef, h.now)
	origCorr := "pas-update-orig-1"
	submitBundle, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr, h.now)

	// Submit function: submits a PAS bundle and asserts pended response.
	submitAndAssertPended := func(t *testing.T, srv *httptest.Server, corrID string, bundle []byte) {
		t.Helper()
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", corrID, bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit: status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		pended, _, err := ParsePendedResponse(plaintext)
		if err != nil {
			t.Fatalf("ParsePendedResponse: %v", err)
		}
		if !pended {
			t.Fatal("submit did not pend, want pended")
		}
	}

	t.Run("pend→update(with DR+Provenance)→approved", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)

		// First: submit → pended.
		submitAndAssertPended(t, srv, origCorr, submitBundle)

		// Build the update bundle.
		drJSON, err := BuildDiagnosticReport("dr-uc04-op", patientRef, "72148", "MRI lumbar spine w/o contrast")
		if err != nil {
			t.Fatalf("BuildDiagnosticReport: %v", err)
		}
		provJSON, err := BuildProvenance("DiagnosticReport/dr-uc04-op", "Organization/provider", h.now)
		if err != nil {
			t.Fatalf("BuildProvenance: %v", err)
		}

		// Build an approved QR (6 weeks + prior surgery + has DR → approve).
		qrApproved := approvedQRForResponder(t, patientRef, h.now)

		updateCorr := "pas-update-1"
		updateBundle, err := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provJSON,
			patientRef, "Coverage/MBR-UC04", updateCorr, origCorr, h.now)
		if err != nil {
			t.Fatalf("BuildClaimUpdateBundle: %v", err)
		}

		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr, updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update: status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		cr, err := ParseClaimResponse(plaintext)
		if err != nil {
			t.Fatalf("ParseClaimResponse: %v", err)
		}
		if cr.Outcome != "approved" {
			t.Errorf("update outcome = %q, want approved", cr.Outcome)
		}
	})

	t.Run("replayed identical update → 409", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		origCorr2 := "pas-update-orig-2"
		sb, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr2, h.now)
		submitAndAssertPended(t, srv, origCorr2, sb)

		// First update → approve.
		drJSON, _ := BuildDiagnosticReport("dr-replay", patientRef, "72148", "MRI lumbar spine")
		provJSON, _ := BuildProvenance("DiagnosticReport/dr-replay", "Organization/provider", h.now)
		qrApproved := approvedQRForResponder(t, patientRef, h.now)
		updateCorr := "pas-update-replay-1"
		updateBundle, _ := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provJSON,
			patientRef, "Coverage/MBR-UC04", updateCorr, origCorr2, h.now)

		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr, updateBundle)
		resp1 := postInbound(t, srv, envBytes, hubHdr)
		body1 := readBody(t, resp1)
		if resp1.StatusCode != http.StatusOK {
			t.Fatalf("first update: status = %d; body: %s", resp1.StatusCode, body1)
		}

		// Second update: same origCorr → ledger has been finalized → 409.
		updateCorr2 := "pas-update-replay-2"
		updateBundle2, _ := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provJSON,
			patientRef, "Coverage/MBR-UC04", updateCorr2, origCorr2, h.now)
		envBytes2, hubHdr2 := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr2, updateBundle2)
		resp2 := postInbound(t, srv, envBytes2, hubHdr2)
		body2 := readBody(t, resp2)
		assertError(t, resp2, body2, http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
	})

	t.Run("missing Claim.related → 403", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		drJSON, _ := BuildDiagnosticReport("dr-norelated", patientRef, "72148", "MRI")
		provJSON, _ := BuildProvenance("DiagnosticReport/dr-norelated", "Organization/provider", h.now)
		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		// Build update bundle with empty origCorr → Claim.related is empty.
		updateBundle, err := BuildClaimUpdateBundle(qrJSON, srJSON, drJSON, provJSON,
			patientRef, "Coverage/MBR-UC04", "pas-norelated-1", "", h.now)
		if err != nil {
			t.Fatalf("BuildClaimUpdateBundle: %v", err)
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "pas-norelated-1", updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusForbidden, "ClaimUpdate missing original-claim reference (Claim.related)")
	})

	t.Run("no prior pend → 409", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		drJSON, _ := BuildDiagnosticReport("dr-nopend", patientRef, "72148", "MRI")
		provJSON, _ := BuildProvenance("DiagnosticReport/dr-nopend", "Organization/provider", h.now)
		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		// origCorr that was never submitted.
		updateBundle, _ := BuildClaimUpdateBundle(qrJSON, srJSON, drJSON, provJSON,
			patientRef, "Coverage/MBR-UC04", "pas-nopend-upd-1", "nonexistent-corr", h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "pas-nopend-upd-1", updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
	})

	t.Run("missing Provenance → 403", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		origCorr3 := "pas-update-orig-3"
		sb, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr3, h.now)
		submitAndAssertPended(t, srv, origCorr3, sb)

		// Build an update bundle WITHOUT Provenance entry.
		drJSON, _ := BuildDiagnosticReport("dr-noprov", patientRef, "72148", "MRI")
		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		// Manually build the bundle without provenance.
		updateBundle, _ := BuildClaimUpdateBundle(qrJSON, srJSON, drJSON, nil,
			patientRef, "Coverage/MBR-UC04", "pas-noprov-1", origCorr3, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "pas-noprov-1", updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusForbidden, "ClaimUpdate missing Provenance")
	})

	t.Run("Provenance without agent → 403", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		origCorr4 := "pas-update-orig-4"
		sb, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr4, h.now)
		submitAndAssertPended(t, srv, origCorr4, sb)

		// Build a Provenance without agent.
		provNoAgent := []byte(`{"resourceType":"Provenance","target":[{"reference":"DiagnosticReport/dr-noagent"}],"recorded":"2026-06-12T10:00:00Z","agent":[]}`)
		drJSON, _ := BuildDiagnosticReport("dr-noagent", patientRef, "72148", "MRI")
		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		updateBundle, _ := BuildClaimUpdateBundle(qrJSON, srJSON, drJSON, provNoAgent,
			patientRef, "Coverage/MBR-UC04", "pas-noagent-1", origCorr4, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "pas-noagent-1", updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusForbidden, "ClaimUpdate Provenance missing agent")
	})

	t.Run("Provenance wrong target → 403", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		origCorr5 := "pas-update-orig-5"
		sb, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr5, h.now)
		submitAndAssertPended(t, srv, origCorr5, sb)

		drJSON, _ := BuildDiagnosticReport("dr-wrongtarget", patientRef, "72148", "MRI")
		// Provenance targets DIFFERENT resource.
		provWrong, _ := BuildProvenance("DiagnosticReport/OTHER-ID", "Organization/provider", h.now)
		qrJSON := approvedQRForResponder(t, patientRef, h.now)
		updateBundle, _ := BuildClaimUpdateBundle(qrJSON, srJSON, drJSON, provWrong,
			patientRef, "Coverage/MBR-UC04", "pas-wrongtarget-1", origCorr5, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "pas-wrongtarget-1", updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusForbidden, "ClaimUpdate Provenance does not target the supplemental data")
	})

	t.Run("still-insufficient amendment → 422 + claim released (next complete update succeeds)", func(t *testing.T) {
		_, srv := h.makeResponderSrv(t, responderIdent, adj)
		origCorr6 := "pas-update-orig-6"
		sb, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr6, h.now)
		submitAndAssertPended(t, srv, origCorr6, sb)

		// First update: still insufficient (no DR, prior-surgery QR → still pended).
		// Use a handcrafted QR with an id for the Provenance target (not the
		// priorSurgeryQRForResponder fixture, which lacks an id field).
		qrWithID := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-insuff-1","status":"completed","subject":{"reference":"Patient/MBR-UC04"},"item":[{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},{"linkId":"prior-surgery","answer":[{"valueBoolean":true}]}]}`)
		provQR, _ := BuildProvenance("QuestionnaireResponse/qr-insuff-1", "Organization/provider", h.now)

		updateCorr7 := "pas-update-insuff-1"
		updateBundle, err := BuildClaimUpdateBundle(qrWithID, srJSON, nil, provQR,
			patientRef, "Coverage/MBR-UC04", updateCorr7, origCorr6, h.now)
		if err != nil {
			t.Fatalf("BuildClaimUpdateBundle insuff: %v", err)
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr7, updateBundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		assertError(t, resp, body, http.StatusUnprocessableEntity, "amendment still insufficient")

		// Claim was released → a subsequent complete update (with DR) succeeds.
		drJSON, _ := BuildDiagnosticReport("dr-complete", patientRef, "72148", "MRI")
		provDR, _ := BuildProvenance("DiagnosticReport/dr-complete", "Organization/provider", h.now)
		qrApproved := approvedQRForResponder(t, patientRef, h.now)
		updateCorr8 := "pas-update-complete-1"
		completBundle, _ := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provDR,
			patientRef, "Coverage/MBR-UC04", updateCorr8, origCorr6, h.now)
		envBytes2, hubHdr2 := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr8, completBundle)
		resp2 := postInbound(t, srv, envBytes2, hubHdr2)
		body2 := readBody(t, resp2)
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("complete update: status = %d, want 200; body: %s", resp2.StatusCode, body2)
		}
	})
}

// ---- TestResponder_PASUpdate_AuthzFailureReleasesClaim ----

// TestResponder_PASUpdate_AuthzFailureReleasesClaim pins the fix for the
// stranded-approved-claim regression (PR review Finding 1): when seal+authorize
// fail AFTER handlePASUpdate returns a successful crJSON, the claim must be
// released (rollback) so the provider can retry. Before the fix, finalize ran
// inside the handler before the pipeline; a 502 from authz left the claim
// finalized/removed, so a retry would hit begin() → 409.
//
// Strategy: use a toggleable authz stub — a single httptest.Server whose handler
// is swapped between a working implementation and a 500-returning stub. Both the
// submit (pend) and the first update (failing authz) share the same Responder so
// they share the same pendedLedger instance.
func TestResponder_PASUpdate_AuthzFailureReleasesClaim(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}

	// toggleable authz: starts working, can be set to fail.
	authzFail := false
	toggleAuthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authzFail {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Delegate to the harness's working authz server.
		if r.Method != http.MethodPost || r.URL.Path != "/authorize" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		holderID := "unknown"
		if hdrVal := r.Header.Get("X-Holder-Assertion"); hdrVal != "" {
			if raw, err2 := base64.StdEncoding.DecodeString(hdrVal); err2 == nil {
				var a assertion
				if err2 := json.Unmarshal(raw, &a); err2 == nil {
					holderID = a.HolderID
				}
			}
		}
		var req AuthorizeRequest
		body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
		if err2 := json.Unmarshal(body, &req); err2 != nil {
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
			Expiry:        h.now.Add(time.Hour),
		}
		tok.Signature = ed25519.Sign(h.authzPriv, tokenSigningPayload(tok))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authorizeResp{Token: tok})
	}))
	t.Cleanup(toggleAuthz.Close)

	// Build one Responder (and thus one pendedLedger) wired to the toggleable stub.
	r, err := NewResponder(ResponderConfig{
		Identity:        responderIdent,
		AuthzURL:        toggleAuthz.URL,
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
		Client:      toggleAuthz.Client(),
	})
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	// 1. Submit → pend (authz working).
	patientRef := "Patient/MBR-UC04"
	srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)
	qrPend := priorSurgeryQRForResponder(t, patientRef, h.now)
	origCorr := "authz-fail-update-orig-1"
	submitBundle, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr, h.now)

	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", origCorr, submitBundle)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	plaintext := h.openResponse(t, body)
	pended, _, err := ParsePendedResponse(plaintext)
	if err != nil || !pended {
		t.Fatalf("submit did not pend: pended=%v err=%v", pended, err)
	}

	// 2. Build the complete update bundle (approved path).
	drJSON, _ := BuildDiagnosticReport("dr-authzfail", patientRef, "72148", "MRI lumbar spine w/o contrast")
	provJSON, _ := BuildProvenance("DiagnosticReport/dr-authzfail", "Organization/provider", h.now)
	qrApproved := approvedQRForResponder(t, patientRef, h.now)
	updateCorr1 := "authz-fail-update-1"
	updateBundle, err := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provJSON,
		patientRef, "Coverage/MBR-UC04", updateCorr1, origCorr, h.now)
	if err != nil {
		t.Fatalf("BuildClaimUpdateBundle: %v", err)
	}

	// 3. First update attempt: authz is FAILING → expect 502.
	authzFail = true
	envBytes2, hubHdr2 := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr1, updateBundle)
	resp2 := postInbound(t, srv, envBytes2, hubHdr2)
	body2 := readBody(t, resp2)
	assertError(t, resp2, body2, http.StatusBadGateway, "authorize response leg failed")

	// 4. Second update attempt: authz is WORKING → expect 200 approved.
	// This proves the claim was RELEASED on the 502 (rollback ran), not stranded.
	// Before the fix, the claim was finalized inside the handler before authz ran,
	// so the retry would hit begin() → 409.
	authzFail = false
	updateCorr2 := "authz-fail-update-2"
	updateBundle2, err := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provJSON,
		patientRef, "Coverage/MBR-UC04", updateCorr2, origCorr, h.now)
	if err != nil {
		t.Fatalf("BuildClaimUpdateBundle retry: %v", err)
	}
	envBytes3, hubHdr3 := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr2, updateBundle2)
	resp3 := postInbound(t, srv, envBytes3, hubHdr3)
	body3 := readBody(t, resp3)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("retry update after authz-fail: status = %d, want 200; body: %s", resp3.StatusCode, body3)
	}
	plaintext3 := h.openResponse(t, body3)
	cr, err := ParseClaimResponse(plaintext3)
	if err != nil {
		t.Fatalf("ParseClaimResponse retry: %v", err)
	}
	if cr.Outcome != "approved" {
		t.Errorf("retry update outcome = %q, want approved", cr.Outcome)
	}
}

// ---- TestResponder_PASSubmit_AuthzFailureLeavesNoPend ----

// TestResponder_PASSubmit_AuthzFailureLeavesNoPend pins the fix for the
// submit-pend orphan (PR review Finding 1, submit side): when a pended-class
// submit's authz call fails (502), the ledger record must NOT have been made —
// commit runs only on success. A subsequent ClaimUpdate referencing that correlation
// must 409 (no pend exists), proving no orphan was created.
func TestResponder_PASSubmit_AuthzFailureLeavesNoPend(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}

	// A permanently failing authz for the submit attempt.
	failingAuthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	t.Cleanup(failingAuthz.Close)

	// Responder wired to the failing authz stub. Uses failingAuthz.Client() so the
	// transport is from the same server (matches testAuthzServer pattern).
	rFail, err := NewResponder(ResponderConfig{
		Identity:        responderIdent,
		AuthzURL:        failingAuthz.URL,
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
		Client:      failingAuthz.Client(),
	})
	if err != nil {
		t.Fatalf("NewResponder (fail): %v", err)
	}
	srvFail := httptest.NewServer(rFail.Handler())
	t.Cleanup(srvFail.Close)

	// 1. Send a pended-class submit to the failing responder → expect 502.
	patientRef := "Patient/MBR-UC04"
	srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)
	qrPend := priorSurgeryQRForResponder(t, patientRef, h.now)
	origCorr := "no-pend-orphan-orig-1"
	submitBundle, _ := BuildClaimBundle(qrPend, srJSON, patientRef, "Coverage/MBR-UC04", origCorr, h.now)

	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", origCorr, submitBundle)
	resp := postInbound(t, srvFail, envBytes, hubHdr)
	body := readBody(t, resp)
	assertError(t, resp, body, http.StatusBadGateway, "authorize response leg failed")

	// 2. Now send a ClaimUpdate referencing origCorr to a WORKING responder that
	//    shares the SAME pendedLedger as rFail. Since NewResponder always allocates a
	//    fresh ledger, we verify via the failing responder itself: a ClaimUpdate on
	//    the same rFail instance (which would use the same ledger) returns 409 because
	//    no pend was ever recorded — proving commit was not called on the 502 path.
	//    Use the working authz harness server for the update token (inbound token only).
	drJSON, _ := BuildDiagnosticReport("dr-nopend", patientRef, "72148", "MRI")
	provJSON, _ := BuildProvenance("DiagnosticReport/dr-nopend", "Organization/provider", h.now)
	qrApproved := approvedQRForResponder(t, patientRef, h.now)
	updateCorr := "no-pend-orphan-update-1"
	updateBundle, err := BuildClaimUpdateBundle(qrApproved, srJSON, drJSON, provJSON,
		patientRef, "Coverage/MBR-UC04", updateCorr, origCorr, h.now)
	if err != nil {
		t.Fatalf("BuildClaimUpdateBundle: %v", err)
	}

	// The update envelope is sent to the failing responder (same ledger). The inbound
	// token is signed by h.authzPriv (as built by buildForwardEnv), so it passes
	// VerifyBound. The claim-update handler will hit begin() → false → 409 because no
	// record was committed during the failed submit.
	envBytes2, hubHdr2 := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", updateCorr, updateBundle)
	resp2 := postInbound(t, srvFail, envBytes2, hubHdr2)
	body2 := readBody(t, resp2)
	assertError(t, resp2, body2, http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
}

// ---- T3 review follow-ups ----

// TestParseClaimBundle_DuplicateClaim: two Claim entries → parse error.
func TestParseClaimBundle_DuplicateClaim(t *testing.T) {
	// Build a valid bundle then inject a second Claim entry.
	qr := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-dup","status":"completed","subject":{"reference":"Patient/P"}}`)
	sr := []byte(`{"resourceType":"ServiceRequest","id":"sr-dup","status":"active","subject":{"reference":"Patient/P"}}`)
	bundle, _ := BuildClaimBundle(qr, sr, "Patient/P", "Coverage/P", "corr-dup", time.Now())
	// Inject a second Claim entry.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(bundle, &raw)
	var entries []json.RawMessage
	_ = json.Unmarshal(raw["entry"], &entries)
	claimEntry := entries[0] // first entry should be the Claim
	entries = append(entries, claimEntry)
	entriesJSON, _ := json.Marshal(entries)
	raw["entry"] = entriesJSON
	bundle, _ = json.Marshal(raw)

	_, err := ParseClaimBundle(bundle)
	if err == nil {
		t.Fatal("expected error on duplicate Claim, got nil")
	}
}

// TestParseClaimBundle_MissingPatientReference: missing patient.reference → parse error,
// surfaced via the responder as 400 "parse bundle failed".
func TestParseClaimBundle_MissingPatientReference(t *testing.T) {
	// Build a bundle where the Claim has no patient.reference.
	claimNoPatient := []byte(`{"resourceType":"Claim","id":"c1","status":"active","type":{"coding":[{"system":"x","code":"y"}]},"use":"preauthorization","patient":{"reference":""},"created":"2026-06-12T10:00:00Z","insurer":{"reference":"Organization/payer"},"provider":{"reference":"Practitioner/1"},"priority":{"coding":[{"code":"normal"}]},"insurance":[{"sequence":1,"focal":true,"coverage":{"reference":"Coverage/P"}}]}`)
	qr := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-np","status":"completed","subject":{"reference":"Patient/P"}}`)
	sr := []byte(`{"resourceType":"ServiceRequest","id":"sr-np","status":"active","subject":{"reference":"Patient/P"}}`)
	bundle := []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":` + string(claimNoPatient) + `},{"resource":` + string(qr) + `},{"resource":` + string(sr) + `}]}`)

	_, err := ParseClaimBundle(bundle)
	if err == nil {
		t.Fatal("expected error on missing patient.reference, got nil")
	}
}

// TestParseClaimBundle_MissingPatientReference_ViaResponder proves the responder
// surfaces missing patient.reference as 400 "parse bundle failed".
func TestParseClaimBundle_MissingPatientReference_ViaResponder(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &sandboxTestAdjudicator{now: h.now}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)

	claimNoPatient := []byte(`{"resourceType":"Claim","id":"c1","status":"active","type":{"coding":[{"system":"x","code":"y"}]},"use":"preauthorization","patient":{"reference":""},"created":"2026-06-12T10:00:00Z","insurer":{"reference":"Organization/payer"},"provider":{"reference":"Practitioner/1"},"priority":{"coding":[{"code":"normal"}]},"insurance":[{"sequence":1,"focal":true,"coverage":{"reference":"Coverage/P"}}]}`)
	qr := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-np2","status":"completed","subject":{"reference":"Patient/P"}}`)
	sr := []byte(`{"resourceType":"ServiceRequest","id":"sr-np2","status":"active","subject":{"reference":"Patient/P"}}`)
	bundle := []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":` + string(claimNoPatient) + `},{"resource":` + string(qr) + `},{"resource":` + string(sr) + `}]}`)

	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-nopatref-1", bundle)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	assertError(t, resp, body, http.StatusBadRequest, "parse bundle failed")
}

// TestSandboxAdjudicate_HighDisabilityUnattested: high-disability=true without
// clinician attestation → PASPended.
func TestSandboxAdjudicate_HighDisabilityUnattested(t *testing.T) {
	// Build a QR with high-disability=true but no clinician-attestation extension.
	qr, _ := json.Marshal(map[string]interface{}{
		"resourceType": "QuestionnaireResponse",
		"status":       "completed",
		"item": []map[string]interface{}{
			{
				"linkId": "conservative-therapy-weeks",
				"answer": []map[string]interface{}{{"valueInteger": 6}},
			},
			{
				"linkId": "high-disability",
				"answer": []map[string]interface{}{{"valueBoolean": true}},
			},
		},
	})
	dec, err := SandboxAdjudicate(qr, false, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatalf("SandboxAdjudicate: %v", err)
	}
	if dec.Outcome != PASPended {
		t.Errorf("Outcome = %v, want PASPended (high-disability unattested)", dec.Outcome)
	}
}

// TestSandboxAdjudicate_PatientReportedRequired_Unattested: patient-reported-required=true
// without patient signature → PASPended.
func TestSandboxAdjudicate_PatientReportedRequired_Unattested(t *testing.T) {
	qr, _ := json.Marshal(map[string]interface{}{
		"resourceType": "QuestionnaireResponse",
		"status":       "completed",
		"item": []map[string]interface{}{
			{
				"linkId": "conservative-therapy-weeks",
				"answer": []map[string]interface{}{{"valueInteger": 6}},
			},
			{
				"linkId": "patient-reported-required",
				"answer": []map[string]interface{}{{"valueBoolean": true}},
			},
		},
	})
	dec, err := SandboxAdjudicate(qr, false, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatalf("SandboxAdjudicate: %v", err)
	}
	if dec.Outcome != PASPended {
		t.Errorf("Outcome = %v, want PASPended (patient-reported unattested)", dec.Outcome)
	}
}
