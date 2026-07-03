package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// idTokenWithEmail builds a JWT-ish string whose middle segment base64url-decodes
// to {"email": email}. No signature is verified; EmailFromIDToken only base64-decodes
// the payload for a friendly print.
func idTokenWithEmail(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return "hdr." + payload + ".sig"
}

// freePort allocates an ephemeral 127.0.0.1 port and releases it immediately, so
// tests never bind the real registered loopback ports (8400-8404).
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newOIDCStub starts an httptest server exposing /authorize and /token (token may be
// nil when a test never expects a code exchange) and returns the OIDC endpoints.
func newOIDCStub(t *testing.T, authorize, token http.HandlerFunc) (*httptest.Server, OIDC) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", authorize)
	if token != nil {
		mux.HandleFunc("/token", token)
	}
	stub := httptest.NewServer(mux)
	t.Cleanup(stub.Close)
	return stub, OIDC{
		AuthorizationEndpoint: stub.URL + "/authorize",
		TokenEndpoint:         stub.URL + "/token",
	}
}

// TestStartPKCE_HappyPath drives StartPKCE with hc == nil (pinning the nil-default
// through the code exchange) end to end: the authorize stub captures code_challenge
// and 302s to the flow's own loopback callback; the token stub asserts the PKCE
// verifier hashes to the challenge and returns id/access/refresh tokens.
func TestStartPKCE_HappyPath(t *testing.T) {
	var challenge string
	var gotForm struct {
		grantType, code, verifier, redirectURI, clientID string
	}
	authorize := func(w http.ResponseWriter, r *http.Request) {
		challenge = r.URL.Query().Get("code_challenge")
		if m := r.URL.Query().Get("code_challenge_method"); m != "S256" {
			t.Errorf("code_challenge_method = %q, want S256", m)
		}
		redir := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, redir+"?code=test-code&state="+state, http.StatusFound)
	}
	token := func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm.grantType = r.PostFormValue("grant_type")
		gotForm.code = r.PostFormValue("code")
		gotForm.verifier = r.PostFormValue("code_verifier")
		gotForm.redirectURI = r.PostFormValue("redirect_uri")
		gotForm.clientID = r.PostFormValue("client_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at",
			"refresh_token": "rt-1",
			"expires_in":    3600,
		})
	}
	_, oidc := newOIDCStub(t, authorize, token)
	cfg := CLIConfig{ClientID: "cli-1", Scopes: []string{"openid", "email"}}

	fixedNow := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	flow, err := StartPKCE(nil, cfg, oidc, []int{freePort(t)}, now)
	if err != nil {
		t.Fatalf("StartPKCE: %v", err)
	}
	defer flow.Close()

	// Default http.Client follows the 302 to the flow's own loopback listener,
	// delivering the code.
	go func() { _, _ = http.Get(flow.AuthorizeURL()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := flow.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if tok.IDToken == "" {
		t.Error("IDToken is empty")
	}
	if tok.AccessToken != "at" {
		t.Errorf("AccessToken = %q, want at", tok.AccessToken)
	}
	if tok.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken = %q, want rt-1", tok.RefreshToken)
	}
	if want := fixedNow.Add(time.Hour); !tok.Expiry.Equal(want) {
		t.Errorf("Expiry = %v, want %v", tok.Expiry, want)
	}

	if gotForm.grantType != "authorization_code" {
		t.Errorf("grant_type = %q, want authorization_code", gotForm.grantType)
	}
	if gotForm.code != "test-code" {
		t.Errorf("code = %q, want test-code", gotForm.code)
	}
	if gotForm.clientID != "cli-1" {
		t.Errorf("client_id = %q, want cli-1", gotForm.clientID)
	}
	if gotForm.redirectURI == "" {
		t.Error("redirect_uri not sent to /token")
	}
	sum := sha256.Sum256([]byte(gotForm.verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if wantChallenge != challenge {
		t.Errorf("S256(verifier) = %q, challenge = %q", wantChallenge, challenge)
	}
}

// TestStartPKCE_StateMismatch: the stub authorize server redirects back with a
// state that doesn't match the flow's generated state. Wait must reject it and the
// callback must have answered 400.
func TestStartPKCE_StateMismatch(t *testing.T) {
	authorize := func(w http.ResponseWriter, r *http.Request) {
		redir := r.URL.Query().Get("redirect_uri")
		http.Redirect(w, r, redir+"?code=test-code&state=WRONG", http.StatusFound)
	}
	_, oidc := newOIDCStub(t, authorize, nil)
	cfg := CLIConfig{ClientID: "cli-1", Scopes: []string{"openid"}}

	flow, err := StartPKCE(nil, cfg, oidc, []int{freePort(t)}, time.Now)
	if err != nil {
		t.Fatalf("StartPKCE: %v", err)
	}
	defer flow.Close()

	respCh := make(chan *http.Response, 1)
	go func() {
		resp, _ := http.Get(flow.AuthorizeURL())
		respCh <- resp
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = flow.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("Wait err = %v, want containing state mismatch", err)
	}

	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("no response from callback")
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("callback status = %d, want 400", resp.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback response")
	}
}

// TestStartPKCE_AuthorizeError: the stub authorize server redirects with
// ?error=access_denied. Wait must error naming it and the callback must answer 400.
func TestStartPKCE_AuthorizeError(t *testing.T) {
	authorize := func(w http.ResponseWriter, r *http.Request) {
		redir := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, redir+"?error=access_denied&state="+state, http.StatusFound)
	}
	_, oidc := newOIDCStub(t, authorize, nil)
	cfg := CLIConfig{ClientID: "cli-1", Scopes: []string{"openid"}}

	flow, err := StartPKCE(nil, cfg, oidc, []int{freePort(t)}, time.Now)
	if err != nil {
		t.Fatalf("StartPKCE: %v", err)
	}
	defer flow.Close()

	respCh := make(chan *http.Response, 1)
	go func() {
		resp, _ := http.Get(flow.AuthorizeURL())
		respCh <- resp
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = flow.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("Wait err = %v, want containing access_denied", err)
	}

	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("no response from callback")
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("callback status = %d, want 400", resp.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback response")
	}
}

// TestStartPKCE_CtxCancel: canceling Wait's ctx before any callback arrives must
// return ctx.Err() promptly, never hang.
func TestStartPKCE_CtxCancel(t *testing.T) {
	authorize := func(w http.ResponseWriter, r *http.Request) {
		// Never respond within the test's lifetime; Wait must not depend on this.
	}
	_, oidc := newOIDCStub(t, authorize, nil)
	cfg := CLIConfig{ClientID: "cli-1", Scopes: []string{"openid"}}

	flow, err := StartPKCE(nil, cfg, oidc, []int{freePort(t)}, time.Now)
	if err != nil {
		t.Fatalf("StartPKCE: %v", err)
	}
	defer flow.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err = flow.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Wait took %v after cancel, want prompt return", elapsed)
	}
}

// TestStartPKCE_PortExhaustion: every port in a small test port list is already
// bound, so StartPKCE must fail, naming the port set.
func TestStartPKCE_PortExhaustion(t *testing.T) {
	p1, p2 := freePort(t), freePort(t)
	l1, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p1))
	if err != nil {
		t.Fatalf("bind p1: %v", err)
	}
	defer l1.Close()
	l2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p2))
	if err != nil {
		t.Fatalf("bind p2: %v", err)
	}
	defer l2.Close()

	ports := []int{p1, p2}
	cfg := CLIConfig{ClientID: "cli-1"}
	oidc := OIDC{AuthorizationEndpoint: "http://127.0.0.1:1/authorize", TokenEndpoint: "http://127.0.0.1:1/token"}

	_, err = StartPKCE(nil, cfg, oidc, ports, time.Now)
	if err == nil {
		t.Fatal("expected a port-exhaustion error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%v", ports)) {
		t.Errorf("err = %v, want it to name the port set %v", err, ports)
	}
}

// TestRefresh_KeepsTokenWhenOmitted: Cognito refresh-token rotation off means the
// response omits refresh_token; Refresh must keep the caller's refresh token.
func TestRefresh_KeepsTokenWhenOmitted(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":     idTokenWithEmail("dev@x.io"),
			"access_token": "at2",
			"expires_in":   3600,
		})
	})
	stub := httptest.NewServer(mux)
	defer stub.Close()

	fixedNow := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	tok, err := Refresh(context.Background(), nil, stub.URL+"/token", "cli-1", "rt-1", now)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken = %q, want kept rt-1", tok.RefreshToken)
	}
	if want := fixedNow.Add(time.Hour); !tok.Expiry.Equal(want) {
		t.Errorf("Expiry = %v, want %v", tok.Expiry, want)
	}
	if got.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", got.Get("grant_type"))
	}
	if got.Get("refresh_token") != "rt-1" {
		t.Errorf("refresh_token = %q, want rt-1", got.Get("refresh_token"))
	}
	if got.Get("client_id") != "cli-1" {
		t.Errorf("client_id = %q, want cli-1", got.Get("client_id"))
	}
}

// TestRefresh_ReplacesTokenWhenPresent: when the response DOES carry a
// refresh_token, Refresh uses the replacement.
func TestRefresh_ReplacesTokenWhenPresent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idTokenWithEmail("dev@x.io"),
			"access_token":  "at2",
			"refresh_token": "rt-2",
			"expires_in":    3600,
		})
	})
	stub := httptest.NewServer(mux)
	defer stub.Close()

	tok, err := Refresh(context.Background(), nil, stub.URL+"/token", "cli-1", "rt-1", time.Now)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "rt-2" {
		t.Errorf("RefreshToken = %q, want replaced rt-2", tok.RefreshToken)
	}
}

// TestRefresh_NonSuccess: a non-2xx refresh response surfaces the body in the error.
func TestRefresh_NonSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	stub := httptest.NewServer(mux)
	defer stub.Close()

	_, err := Refresh(context.Background(), nil, stub.URL+"/token", "cli-1", "rt-1", time.Now)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v, want containing invalid_grant", err)
	}
}

// TestPKCEFlow_CloseUnblocksWait: a goroutine parked in Wait(ctx) on a flow that
// never receives a callback must be unblocked by Close, not hang forever — the Kit
// daemon reuses PKCEFlow as an async object where Close-while-waiting is a real
// interleaving.
func TestPKCEFlow_CloseUnblocksWait(t *testing.T) {
	authorize := func(w http.ResponseWriter, r *http.Request) {
		// Never respond; Wait must be unblocked by Close, not by a callback.
	}
	_, oidc := newOIDCStub(t, authorize, nil)
	cfg := CLIConfig{ClientID: "cli-1", Scopes: []string{"openid"}}

	flow, err := StartPKCE(nil, cfg, oidc, []int{freePort(t)}, time.Now)
	if err != nil {
		t.Fatalf("StartPKCE: %v", err)
	}

	type waitResult struct {
		err error
	}
	resultCh := make(chan waitResult, 1)
	go func() {
		_, err := flow.Wait(context.Background())
		resultCh <- waitResult{err: err}
	}()

	// Give the Wait goroutine a moment to park on its select before closing.
	time.Sleep(50 * time.Millisecond)
	flow.Close()

	select {
	case res := <-resultCh:
		if res.err == nil || !strings.Contains(res.err.Error(), "closed") {
			t.Fatalf("Wait err = %v, want containing closed", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within 2s of Close (goroutine leak)")
	}
}

// TestEmailFromIDToken: decodes the email claim; a malformed token yields "".
func TestEmailFromIDToken(t *testing.T) {
	if got := EmailFromIDToken(idTokenWithEmail("dev@x.io")); got != "dev@x.io" {
		t.Errorf("EmailFromIDToken = %q, want dev@x.io", got)
	}
	if got := EmailFromIDToken("not-a-jwt"); got != "" {
		t.Errorf("EmailFromIDToken(malformed) = %q, want empty", got)
	}
}
