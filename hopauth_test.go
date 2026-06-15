package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// hubTestKeys generates a fresh hub key pair for testing.
func hubTestKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate hub key: %v", err)
	}
	return pub, priv
}

// makeHubAssertion builds an X-Hub-Assertion header value signed by priv.
func makeHubAssertion(t *testing.T, priv ed25519.PrivateKey, holderID, audience string, issuedAt time.Time, ttl time.Duration, jti string) string {
	t.Helper()
	a := Assertion{
		HolderID: holderID,
		Audience: audience,
		IssuedAt: issuedAt,
		Expiry:   issuedAt.Add(ttl),
		JTI:      jti,
	}
	a.Sig = ed25519.Sign(priv, assertionSigningPayload(a))
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal assertion: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestVerifyHubAssertionHeader(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	hubPub, hubPriv := hubTestKeys(t)
	_, otherPriv := hubTestKeys(t)

	validHeader := makeHubAssertion(t, hubPriv, "hub", "acme", now, 2*time.Minute, "jti-valid-1")

	tests := []struct {
		name     string
		header   string
		holderID string
		wantErr  bool
		wantJTI  string
	}{
		{
			name:     "valid",
			header:   validHeader,
			holderID: "acme",
			wantErr:  false,
			wantJTI:  "jti-valid-1",
		},
		{
			name:     "empty header",
			header:   "",
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "garbage base64",
			header:   "!!!not-base64!!!",
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "garbage json",
			header:   base64.StdEncoding.EncodeToString([]byte("{nope")),
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "issuer not hub",
			header:   makeHubAssertion(t, hubPriv, "payer", "acme", now, 2*time.Minute, "jti-payer"),
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "wrong audience",
			header:   makeHubAssertion(t, hubPriv, "hub", "other", now, 2*time.Minute, "jti-wrong-aud"),
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "expired",
			header:   makeHubAssertion(t, hubPriv, "hub", "acme", now.Add(-10*time.Minute), 2*time.Minute, "jti-expired"),
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "future-dated",
			header:   makeHubAssertion(t, hubPriv, "hub", "acme", now.Add(10*time.Minute), 2*time.Minute, "jti-future"),
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "ttl beyond MaxAssertionTTL",
			header:   makeHubAssertion(t, hubPriv, "hub", "acme", now, 2*time.Hour, "jti-long-ttl"),
			holderID: "acme",
			wantErr:  true,
		},
		{
			name:     "signed by different key",
			header:   makeHubAssertion(t, otherPriv, "hub", "acme", now, 2*time.Minute, "jti-wrong-key"),
			holderID: "acme",
			wantErr:  true,
		},
		{
			// Zero-TTL: Expiry == IssuedAt — accepted by old code, must be rejected
			// to match internal/holderauth.Verify's !a.Expiry.After(a.IssuedAt) guard.
			name:     "zero ttl (expiry == issuedAt)",
			header:   makeHubAssertion(t, hubPriv, "hub", "acme", now, 0, "jti-zero-ttl"),
			holderID: "acme",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jti, err := parseAndVerifyHubAssertion(tc.header, tc.holderID, hubPub, now)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got jti=%q", jti)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if jti != tc.wantJTI {
				t.Errorf("jti = %q, want %q", jti, tc.wantJTI)
			}
		})
	}
}

func TestFetchHubTransportKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	t.Run("200 valid", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]string{"pubkey": base64.StdEncoding.EncodeToString(pub)})
			w.Write(b)
		}))
		defer srv.Close()

		got, err := FetchHubTransportKey(t.Context(), srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(pub) {
			t.Errorf("returned key does not equal input key")
		}
	})

	t.Run("404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		_, err := FetchHubTransportKey(t.Context(), srv.Client(), srv.URL)
		if err == nil {
			t.Error("expected error on 404, got nil")
		}
	})

	t.Run("200 garbage json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{not valid json"))
		}))
		defer srv.Close()

		_, err := FetchHubTransportKey(t.Context(), srv.Client(), srv.URL)
		if err == nil {
			t.Error("expected error on garbage json, got nil")
		}
	})

	t.Run("200 invalid base64 pubkey", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Valid JSON, but the pubkey value is not valid base64.
			b, _ := json.Marshal(map[string]string{"pubkey": "!!!"})
			w.Write(b)
		}))
		defer srv.Close()

		_, err := FetchHubTransportKey(t.Context(), srv.Client(), srv.URL)
		if err == nil {
			t.Error("expected error on invalid base64 pubkey, got nil")
		}
	})

	t.Run("200 short base64 key", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// A valid base64 string but only 10 bytes — too short for ed25519.
			b, _ := json.Marshal(map[string]string{"pubkey": base64.StdEncoding.EncodeToString([]byte("tooshort"))})
			w.Write(b)
		}))
		defer srv.Close()

		_, err := FetchHubTransportKey(t.Context(), srv.Client(), srv.URL)
		if err == nil {
			t.Error("expected error on short key, got nil")
		}
	})
}
