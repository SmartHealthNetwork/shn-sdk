package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// manualStub serves a Cognito-shaped token endpoint and records the exchange form.
func manualStub(t *testing.T, status int, reply map[string]any) (*httptest.Server, *url.Values) {
	t.Helper()
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hostedui/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		got = r.PostForm
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func manualFlow(t *testing.T, srv *httptest.Server) *ManualFlow {
	t.Helper()
	cfg := CLIConfig{Issuer: srv.URL, ClientID: "cli-1", Scopes: []string{"openid", "email"}}
	oidc := OIDC{AuthorizationEndpoint: srv.URL + "/hostedui/authorize", TokenEndpoint: srv.URL + "/hostedui/token"}
	f, err := StartManualPKCE(nil, cfg, oidc, "https://accounts.example.org/", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// stateOf extracts the flow's state from its authorize URL (the only place it is
// exposed — exactly what the /cli/code page reflects back to the user).
func stateOf(t *testing.T, f *ManualFlow) string {
	t.Helper()
	u, err := url.Parse(f.AuthorizeURL())
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// TestManualFlow_Exchange: happy path. The authorize URL carries the accounts-page
// redirect + S256 challenge; Exchange parses "<state>.<code>" (whitespace-trimmed)
// and POSTs code+verifier with the SAME redirect_uri.
func TestManualFlow_Exchange(t *testing.T) {
	srv, got := manualStub(t, http.StatusOK, map[string]any{
		"id_token": "hdr.e30.sig", "access_token": "at", "expires_in": 3600, "token_type": "Bearer",
	})
	f := manualFlow(t, srv)

	u, _ := url.Parse(f.AuthorizeURL())
	q := u.Query()
	if got := q.Get("redirect_uri"); got != "https://accounts.example.org/cli/code" {
		t.Errorf("redirect_uri = %q, want the accounts /cli/code page (trailing slash trimmed)", got)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
		t.Errorf("authorize URL missing PKCE/code params: %s", f.AuthorizeURL())
	}

	tok, err := f.Exchange(context.Background(), "  "+stateOf(t, f)+".test-code\n")
	if err != nil {
		t.Fatal(err)
	}
	if tok.IDToken != "hdr.e30.sig" {
		t.Errorf("IDToken = %q", tok.IDToken)
	}
	if got.Get("grant_type") != "authorization_code" || got.Get("code") != "test-code" || got.Get("client_id") != "cli-1" {
		t.Errorf("token form = %v", *got)
	}
	if got.Get("redirect_uri") != "https://accounts.example.org/cli/code" {
		t.Errorf("token redirect_uri = %q", got.Get("redirect_uri"))
	}
	sum := sha256.Sum256([]byte(got.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Error("S256(code_verifier) != code_challenge from the authorize URL")
	}
}

// TestManualFlow_ExchangeRejections: valid paste − one mutation → reject, and the
// token endpoint is never hit (rejection-row discipline).
func TestManualFlow_ExchangeRejections(t *testing.T) {
	srv, got := manualStub(t, http.StatusOK, map[string]any{"id_token": "x", "expires_in": 3600})
	f := manualFlow(t, srv)
	state := stateOf(t, f)
	for name, paste := range map[string]string{
		"empty":       "",
		"whitespace":  "   \n",
		"no dot":      state + "code",
		"empty code":  state + ".",
		"empty state": ".test-code",
		"wrong state": "not-the-state.test-code",
		"junk":        "click here to verify",
	} {
		if _, err := f.Exchange(context.Background(), paste); err == nil {
			t.Errorf("%s: paste %q should be rejected", name, paste)
		}
	}
	if len(*got) != 0 {
		t.Error("a rejected paste must not reach the token endpoint")
	}
}

// TestManualFlow_ExpiredCode: Cognito's invalid_grant (expired/replayed code) maps
// to ErrCodeExpired so the CLI can say "re-run shn login".
func TestManualFlow_ExpiredCode(t *testing.T) {
	srv, _ := manualStub(t, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
	f := manualFlow(t, srv)
	_, err := f.Exchange(context.Background(), stateOf(t, f)+".stale-code")
	if !errors.Is(err, ErrCodeExpired) {
		t.Errorf("err = %v, want ErrCodeExpired", err)
	}
	if !strings.Contains(err.Error(), "shn login") {
		t.Errorf("expired-code error should tell the user to re-run shn login: %v", err)
	}
}

// TestManualFlow_ContextCancelled: a dead context fails Exchange at the token
// POST (surfacing the context error) instead of hanging — the CLI's paste
// deadline relies on this.
func TestManualFlow_ContextCancelled(t *testing.T) {
	srv, _ := manualStub(t, http.StatusOK, map[string]any{"id_token": "x", "expires_in": 3600})
	f := manualFlow(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.Exchange(ctx, stateOf(t, f)+".c")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestManualFlow_MissingIDToken: a 200 reply without id_token is an error.
func TestManualFlow_MissingIDToken(t *testing.T) {
	srv, _ := manualStub(t, http.StatusOK, map[string]any{"access_token": "at", "expires_in": 3600})
	f := manualFlow(t, srv)
	if _, err := f.Exchange(context.Background(), stateOf(t, f)+".c"); err == nil {
		t.Error("missing id_token should fail")
	}
}
