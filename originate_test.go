package shnsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// fakeSubstrate is a minimal stand-in for the Authorization Framework + Hub +
// payer, just enough to exercise RunEligibility hermetically (no internal/). It
// mints tokens with a test signing key and answers the payer leg in-process.
// The cross-module conformance test drives the SDK against the REAL substrate;
// this only proves the SDK's own originate wiring end-to-end without a network.
type fakeSubstrate struct {
	signPriv ed25519.PrivateKey
	payerEnc *[32]byte
	payerPub *[32]byte
	payerID  string
	covered  bool
	now      time.Time
	// requesterEnc is the originating SDK identity's enc key, so the fake payer
	// can seal the response back to it.
	requesterEnc *[32]byte
	// frameStatus, if nonzero, seals a v1 HTTP frame carrying frameStatus/frameBody
	// as the response payload instead of the normal CoverageEligibilityResponse —
	// drives the framed-app-error behavioral test (RunEligibility unframing).
	frameStatus int
	frameBody   []byte
}

// mint signs a Token the way the substrate authz framework does (Signature over
// the JSON with Signature zeroed).
func (f *fakeSubstrate) mint(tok Token) Token {
	tok.Signature = ed25519.Sign(f.signPriv, tokenSigningPayload(tok))
	return tok
}

func (f *fakeSubstrate) authorizeHandler(w http.ResponseWriter, r *http.Request) {
	var req AuthorizeRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// holder is stamped by the framework from the assertion; for the test, derive
	// it from frame: request leg → "ext-provider", response leg → payer.
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

// routeHandler mimics the Hub+payer: it opens the request, mints a response token,
// builds a CoverageEligibilityResponse, seals it back to the requester.
func (f *fakeSubstrate) routeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes))
		env, err := DecodeEnvelope(body)
		if err != nil {
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		// Open the request payload with the payer key (proves the seal target).
		if _, err := Open(env, f.payerPub, f.payerEnc); err != nil {
			http.Error(w, "open", http.StatusBadRequest)
			return
		}
		corr := env.Metadata.CorrelationID
		var inTok Token
		_ = json.Unmarshal([]byte(env.Metadata.AuthzToken), &inTok)

		var crr string
		if f.covered {
			crr = `{"resourceType":"CoverageEligibilityResponse","status":"active","purpose":["benefits"],` +
				`"outcome":"complete","patient":{"reference":"Patient/X"},` +
				`"insurance":[{"coverage":{"reference":"Coverage/X"},"inforce":true}]}`
		} else {
			crr = `{"resourceType":"CoverageEligibilityResponse","status":"active","purpose":["benefits"],` +
				`"outcome":"complete","disposition":"member not enrolled","patient":{"reference":"Patient/X"},` +
				`"insurance":[{"coverage":{"reference":"Coverage/X"},"inforce":false}]}`
		}
		// AI-2 (seal-then-bind): seal the response FIRST, then mint the response token
		// bound to sha256hex(ciphertext) and stamp it into the cleartext metadata.
		reqEnc := f.requesterEnc
		respMeta := Metadata{
			Sender:          f.payerID,
			Recipient:       env.Metadata.Sender,
			TransactionType: "coverage-eligibility",
			AuthorityFrame:  "payer-coverage",
			Timestamp:       f.now.UTC().Format(time.RFC3339),
			CorrelationID:   corr,
		}
		payload := []byte(crr)
		if f.frameStatus != 0 {
			framed, ferr := EncodeHTTPFrame(f.frameStatus, "application/fhir+json", f.frameBody)
			if ferr != nil {
				http.Error(w, "frame", http.StatusInternalServerError)
				return
			}
			payload = framed
		}
		respEnv, err := Seal(respMeta, payload, reqEnc)
		if err != nil {
			http.Error(w, "seal", http.StatusInternalServerError)
			return
		}
		respHash := sha256.Sum256(respEnv.Ciphertext)
		respTok := f.mint(Token{
			Operation:     "eligibility-response",
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

func TestRunEligibility_Hermetic(t *testing.T) {
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen sign key: %v", err)
	}
	payerPub, payerPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen payer enc: %v", err)
	}
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	id, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	id.Clock = func() time.Time { return now }

	for _, tc := range []struct {
		name    string
		covered bool
		member  string
	}{
		{"covered", true, "MBR-COVERED"},
		{"not-covered", false, "MBR-NOTCOVERED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSubstrate{
				signPriv: signPriv,
				payerEnc: payerPriv, payerPub: payerPub,
				payerID: "payer", covered: tc.covered, now: now,
				requesterEnc: id.EncPub,
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/authorize", f.authorizeHandler)
			authzSrv := httptest.NewServer(mux)
			defer authzSrv.Close()

			hubMux := http.NewServeMux()
			hubMux.HandleFunc("/route", f.routeHandler())
			hubSrv := httptest.NewServer(hubMux)
			defer hubSrv.Close()

			covered, reason, err := id.RunEligibility(
				context.Background(), authzSrv.Client(),
				Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
				Payer{ID: "payer", EncPub: payerPub, AuthzPub: signPub},
				"9999999999", tc.member, "1975-04-02", "Johansson",
			)
			if err != nil {
				t.Fatalf("RunEligibility: %v", err)
			}
			if covered != tc.covered {
				t.Errorf("covered = %v, want %v", covered, tc.covered)
			}
			if !tc.covered && reason == "" {
				t.Error("not-covered branch: expected a non-empty reason")
			}
		})
	}
}

// TestRunEligibility_RejectsTamperedResponseToken proves the SDK's response-leg
// VerifyBound rejects a response signed by the WRONG key (a substitution attack).
func TestRunEligibility_RejectsTamperedResponseToken(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	id, _ := GenerateIdentity("ext-provider")
	id.Clock = func() time.Time { return now }

	f := &fakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", covered: true, now: now, requesterEnc: id.EncPub}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorizeHandler)
	authzSrv := httptest.NewServer(mux)
	defer authzSrv.Close()
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", f.routeHandler())
	hubSrv := httptest.NewServer(hubMux)
	defer hubSrv.Close()

	// Verify with the WRONG authz pub → response token signature check fails.
	_, _, err := id.RunEligibility(
		context.Background(), authzSrv.Client(),
		Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
		Payer{ID: "payer", EncPub: payerPub, AuthzPub: wrongPub},
		"9999999999", "MBR-COVERED", "1975-04-02", "Johansson",
	)
	if err == nil {
		t.Fatal("RunEligibility should reject a response token verified against the wrong authz key")
	}
}

// TestRunEligibility_FramedAppError proves the originator side of frame
// negotiation: a frame-capable payer answering a framed non-2xx surfaces an
// *AppAnswerError (errors.As-able) carrying the verbatim payload, rather than a
// ParseEligibilityResponse failure on the opaque frame bytes.
func TestRunEligibility_FramedAppError(t *testing.T) {
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen sign key: %v", err)
	}
	payerPub, payerPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen payer enc: %v", err)
	}
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	id, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	id.Clock = func() time.Time { return now }

	oo := []byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"error","diagnostics":"member not found"}]}`)
	f := &fakeSubstrate{
		signPriv: signPriv,
		payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: now,
		requesterEnc: id.EncPub,
		frameStatus:  422,
		frameBody:    oo,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorizeHandler)
	authzSrv := httptest.NewServer(mux)
	defer authzSrv.Close()

	hubMux := http.NewServeMux()
	hubMux.HandleFunc("/route", f.routeHandler())
	hubSrv := httptest.NewServer(hubMux)
	defer hubSrv.Close()

	_, _, err = id.RunEligibility(
		context.Background(), authzSrv.Client(),
		Endpoints{HubURL: hubSrv.URL, AuthzURL: authzSrv.URL},
		Payer{ID: "payer", EncPub: payerPub, AuthzPub: signPub, MessageFrames: []string{"v1"}},
		"9999999999", "MBR-COVERED", "1975-04-02", "Johansson",
	)
	var ae *AppAnswerError
	if !errors.As(err, &ae) {
		t.Fatalf("RunEligibility error = %v, want errors.As *AppAnswerError", err)
	}
	if ae.Status != 422 || !bytes.Equal(ae.Body, oo) {
		t.Fatalf("AppAnswerError = %+v, want status 422 body %s", ae, oo)
	}
}
