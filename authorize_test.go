package shnsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signTestToken signs t with priv using the SDK's signing payload. This is a TEST
// helper only — the SDK never mints tokens in production; it stands in for the
// substrate framework so VerifyBound can be exercised hermetically.
func signTestToken(t Token, priv ed25519.PrivateKey) Token {
	t.Signature = ed25519.Sign(priv, tokenSigningPayload(t))
	return t
}

// testHash is a well-formed 64-hex payload hash carried by baseToken so the SDK's
// now-strict VerifyBound payload check is satisfied in the dimension-isolation tests.
const testHash = "abababababababababababababababababababababababababababababababab" + "ab"

func baseToken(now time.Time) Token {
	return Token{
		Operation:     "eligibility-response",
		Scope:         "coverage",
		Subject:       "pci:abc123",
		Frame:         "payer-coverage",
		CorrelationID: "corr-1",
		Holder:        "payer-1",
		PayloadHash:   testHash,
		Expiry:        now.Add(time.Hour),
	}
}

func TestVerifyBoundAcceptsValid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0).UTC()
	tok := signTestToken(baseToken(now), priv)
	if err := VerifyBound(tok, pub, now, "payer-coverage", "eligibility-response", "corr-1", "payer-1", "pci:abc123", testHash); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestVerifyBoundRejects(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0).UTC()
	good := signTestToken(baseToken(now), priv)

	tests := []struct {
		name                                       string
		tok                                        Token
		when                                       time.Time
		frame, op, corr, holder, subject, payloadH string
	}{
		{"wrong frame", good, now, "provider-tpo", "eligibility-response", "corr-1", "payer-1", "pci:abc123", testHash},
		{"wrong op", good, now, "payer-coverage", "eligibility-inquiry", "corr-1", "payer-1", "pci:abc123", testHash},
		{"wrong corr", good, now, "payer-coverage", "eligibility-response", "corr-X", "payer-1", "pci:abc123", testHash},
		{"wrong holder", good, now, "payer-coverage", "eligibility-response", "corr-1", "payer-X", "pci:abc123", testHash},
		{"wrong subject", good, now, "payer-coverage", "eligibility-response", "corr-1", "payer-1", "pci:other", testHash},
		{"wrong payloadHash", good, now, "payer-coverage", "eligibility-response", "corr-1", "payer-1", "pci:abc123", "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"},
		{"empty payloadHash", good, now, "payer-coverage", "eligibility-response", "corr-1", "payer-1", "pci:abc123", ""},
		{"expired", good, now.Add(2 * time.Hour), "payer-coverage", "eligibility-response", "corr-1", "payer-1", "pci:abc123", testHash},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyBound(tc.tok, pub, tc.when, tc.frame, tc.op, tc.corr, tc.holder, tc.subject, tc.payloadH); err == nil {
				t.Errorf("%s: expected reject", tc.name)
			}
		})
	}
}

func TestVerifyBoundRejectsTamperedSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0).UTC()
	tok := signTestToken(baseToken(now), priv)
	tok.Scope = "widened-scope" // mutate a covered field; signature no longer matches
	if err := VerifyBound(tok, pub, now, "payer-coverage", "eligibility-response", "corr-1", "payer-1", "pci:abc123", testHash); err == nil {
		t.Error("expected reject for tampered (re-bound) token")
	}

	// Directly corrupt the signature bytes.
	tok2 := signTestToken(baseToken(now), priv)
	tok2.Signature[0] ^= 0xFF
	if err := VerifyBound(tok2, pub, now, "", "", "", "", "", testHash); err == nil {
		t.Error("expected reject for corrupted signature")
	}
}

func TestVerifyBoundRejectsBadKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0).UTC()
	tok := signTestToken(baseToken(now), priv)
	if err := VerifyBound(tok, ed25519.PublicKey{1, 2, 3}, now, "", "", "", "", "", testHash); err == nil {
		t.Error("expected reject for wrong-length verifying key (fail closed, no panic)")
	}
}

func authorizeReq() AuthorizeRequest {
	return AuthorizeRequest{
		Frame:         "payer-coverage",
		Operation:     "eligibility-response",
		SubjectPCI:    "pci:abc123",
		CorrelationID: "corr-1",
	}
}

// TestAuthorizeSuccess drives Identity.Authorize against an httptest stub that
// asserts the request shape (POST /authorize, JSON content-type, a non-empty
// X-Holder-Assertion header, and the decoded body) and returns a minted token.
func TestAuthorizeSuccess(t *testing.T) {
	id := newTestIdentity(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	want := baseToken(now)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/authorize" {
			t.Errorf("path = %q, want /authorize", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if a := r.Header.Get("X-Holder-Assertion"); a == "" {
			t.Error("X-Holder-Assertion header is empty")
		}
		var got AuthorizeRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if got != authorizeReq() {
			t.Errorf("body = %+v, want %+v", got, authorizeReq())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authorizeResp{Token: want})
	}))
	defer srv.Close()

	got, err := id.Authorize(context.Background(), srv.Client(), srv.URL, authorizeReq())
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if got.CorrelationID != want.CorrelationID || got.Subject != want.Subject ||
		got.Frame != want.Frame || got.Operation != want.Operation || got.Holder != want.Holder {
		t.Errorf("token = %+v, want %+v", got, want)
	}
}

// TestAuthorizeNon2xxSurfaced confirms a non-2xx response is surfaced as an error
// that names the status code (not silently swallowed into a zero Token).
func TestAuthorizeNon2xxSurfaced(t *testing.T) {
	id := newTestIdentity(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("not authorized for this operation"))
	}))
	defer srv.Close()

	_, err := id.Authorize(context.Background(), srv.Client(), srv.URL, authorizeReq())
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error %q does not mention status 403", err)
	}
}

// TestAuthorizeMalformedResponse exercises the read+parse path (io.LimitReader →
// json.Unmarshal): a malformed JSON body must surface as a decode error rather
// than a zero Token. A literal 8 MiB body is wasteful, so a small malformed body
// is used to confirm the parse step is wired in.
func TestAuthorizeMalformedResponse(t *testing.T) {
	id := newTestIdentity(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{this is not valid json"))
	}))
	defer srv.Close()

	_, err := id.Authorize(context.Background(), srv.Client(), srv.URL, authorizeReq())
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

// TestAuthorizeCanceledContext confirms a canceled context aborts the request and
// surfaces an error rather than blocking or returning a token.
func TestAuthorizeCanceledContext(t *testing.T) {
	id := newTestIdentity(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authorizeResp{Token: baseToken(time.Now())})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call so c.Do fails with a context error

	_, err := id.Authorize(ctx, srv.Client(), srv.URL, authorizeReq())
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}
