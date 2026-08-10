package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// idTokenWithEmail builds a JWT-ish string whose middle segment base64url-decodes
// to {"email": email}. No signature is verified CLI-side; we only base64-decode the
// payload for a friendly print, so a real signature is unnecessary.
func idTokenWithEmail(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return "hdr." + payload + ".sig"
}

// TestLogin_PKCE_CachesIDToken drives runLogin against a stub Cognito-shaped OAuth
// server: it fetches /cli-config, runs the Authorization-Code+PKCE flow through a
// loopback redirect, and caches the ID token. The stub's /authorize captures the
// code_challenge so /token can assert S256(code_verifier) == code_challenge. The
// injected openBrowser drives the authorize URL itself, following the 302 to the
// loopback callback so the code is delivered.
func TestLogin_PKCE_CachesIDToken(t *testing.T) {
	var challenge string
	var gotForm struct {
		grantType, code, verifier, redirectURI, clientID string
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		// issuer == self so the OIDC discovery doc resolves to this stub.
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":    base,
			"client_id": "cli-1",
			"scopes":    []string{"openid", "email"},
		})
	})
	// OIDC discovery advertises the authorize/token endpoints at /hostedui/* — distinct
	// from /oauth2/* — so a regression to the old issuer+"/oauth2/*" shortcut would hit a
	// 404 here and fail this test (the issuer host does NOT serve /oauth2/*).
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": base + "/hostedui/authorize",
			"token_endpoint":         base + "/hostedui/token",
		})
	})
	mux.HandleFunc("/hostedui/authorize", func(w http.ResponseWriter, r *http.Request) {
		challenge = r.URL.Query().Get("code_challenge")
		if m := r.URL.Query().Get("code_challenge_method"); m != "S256" {
			t.Errorf("code_challenge_method = %q, want S256", m)
		}
		if rt := r.URL.Query().Get("response_type"); rt != "code" {
			t.Errorf("response_type = %q, want code", rt)
		}
		redir := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		http.Redirect(w, r, redir+"?code=test-code&state="+state, http.StatusFound)
	})
	mux.HandleFunc("/hostedui/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm.grantType = r.PostFormValue("grant_type")
		gotForm.code = r.PostFormValue("code")
		gotForm.verifier = r.PostFormValue("code_verifier")
		gotForm.redirectURI = r.PostFormValue("redirect_uri")
		gotForm.clientID = r.PostFormValue("client_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":     idTokenWithEmail("dev@x.io"),
			"access_token": "at",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	})
	stub := httptest.NewServer(mux)
	defer stub.Close()

	cache := filepath.Join(t.TempDir(), "creds")

	detectHeadless = func() bool { return false }
	defer func() { detectHeadless = defaultDetectHeadless }()

	// Inject openBrowser so the test drives the authorize URL itself: the default
	// http.Client follows the 302 to our own loopback listener, delivering the code.
	openBrowser = func(u string) error { go func() { _, _ = http.Get(u) }(); return nil }
	defer func() { openBrowser = defaultOpenBrowser }()

	code := runLogin([]string{"--accounts", stub.URL, "--cache", cache}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("login exit %d", code)
	}

	// PKCE: the token request's code_verifier must S256-hash to the challenge the
	// authorize handler saw.
	if gotForm.grantType != "authorization_code" {
		t.Errorf("grant_type = %q", gotForm.grantType)
	}
	if gotForm.code != "test-code" {
		t.Errorf("code = %q", gotForm.code)
	}
	if gotForm.clientID != "cli-1" {
		t.Errorf("client_id = %q", gotForm.clientID)
	}
	if gotForm.redirectURI == "" {
		t.Error("redirect_uri not sent to /token")
	}
	sum := sha256.Sum256([]byte(gotForm.verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if wantChallenge != challenge {
		t.Errorf("S256(verifier) = %q, challenge = %q", wantChallenge, challenge)
	}

	// Cache: 0600 file carrying the accounts URL, the id_token, and a future expiry.
	fi, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cache perms = %v, want 600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(cache)
	var cached cachedCreds
	if err := json.Unmarshal(b, &cached); err != nil {
		t.Fatalf("unmarshal cache: %v", err)
	}
	if cached.Accounts != stub.URL {
		t.Errorf("cached accounts = %q, want %q", cached.Accounts, stub.URL)
	}
	if cached.Token != idTokenWithEmail("dev@x.io") {
		t.Errorf("cached token = %q (want the id_token, not the access_token)", cached.Token)
	}
	if !cached.Expiry.After(time.Now()) {
		t.Errorf("cached expiry %v is not in the future", cached.Expiry)
	}

	// loadToken returns the cached id_token for the matching accounts URL.
	tok, ok := loadToken(cache, stub.URL)
	if !ok || tok != idTokenWithEmail("dev@x.io") {
		t.Errorf("loadToken = %q, %v", tok, ok)
	}
	// A different accounts URL must not match.
	if _, ok := loadToken(cache, "http://other"); ok {
		t.Error("loadToken matched a different accounts URL")
	}
}

// TestLoadToken_ExpiredRejected: an expired cache entry is not returned.
func TestLoadToken_ExpiredRejected(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "creds")
	c := cachedCreds{Accounts: "http://a", Token: "t", Expiry: time.Now().Add(-time.Minute)}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(cache, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadToken(cache, "http://a"); ok {
		t.Error("expired token should not load")
	}
}

// TestLogin_RequiresAccounts: missing --accounts is a usage error.
func TestLogin_RequiresAccounts(t *testing.T) {
	_, stderr, code := runCLI("login")
	if code == 0 {
		t.Fatal("login without --accounts should fail")
	}
	if !strings.Contains(strings.ToLower(stderr), "accounts") {
		t.Errorf("stderr should mention accounts: %s", stderr)
	}
}

// syncBuffer is a goroutine-safe io.Writer the manual-mode tests poll while
// runLogin runs concurrently (it blocks reading the paste from stdin).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestHeadlessEnv: the auto-detect matrix. SSH always wins; Linux additionally
// detects a missing X/Wayland display; darwin/windows never env-detect.
func TestHeadlessEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		goos string
		vars map[string]string
		want bool
	}{
		{"ssh tty", "darwin", map[string]string{"SSH_TTY": "/dev/pts/0"}, true},
		{"ssh connection", "windows", map[string]string{"SSH_CONNECTION": "1.2.3.4 22"}, true},
		{"linux no display", "linux", map[string]string{}, true},
		{"linux x11", "linux", map[string]string{"DISPLAY": ":0"}, false},
		{"linux wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, false},
		{"darwin desktop", "darwin", map[string]string{}, false},
		{"windows desktop", "windows", map[string]string{}, false},
	}
	for _, c := range cases {
		if got := headlessEnv(c.goos, env(c.vars)); got != c.want {
			t.Errorf("%s: headlessEnv = %v, want %v", c.name, got, c.want)
		}
	}
}

// manualLoginStub serves cli-config + OIDC discovery + a token endpoint that
// records the exchange form. No /authorize handler: manual mode must never hit
// it from the CLI side (the user's browser does).
func manualLoginStub(t *testing.T) (srv *httptest.Server, gotForm *url.Values) {
	t.Helper()
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": base, "client_id": "cli-1", "scopes": []string{"openid", "email"},
		})
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": base + "/hostedui/authorize",
			"token_endpoint":         base + "/hostedui/token",
		})
	})
	mux.HandleFunc("/hostedui/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token": idTokenWithEmail("dev@x.io"), "access_token": "at",
			"expires_in": 3600, "token_type": "Bearer",
		})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, &got
}

// authorizeURLFrom polls stderr for the printed "Open:" line and returns the
// parsed authorize URL (carrying the state the /cli/code page would reflect).
func authorizeURLFrom(t *testing.T, stderr *syncBuffer) *url.URL {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := stderr.String()
		if i := strings.Index(out, "Open:  "); i >= 0 {
			rest := out[i+len("Open:  "):]
			if j := strings.IndexByte(rest, '\n'); j >= 0 {
				u, err := url.Parse(strings.TrimSpace(rest[:j]))
				if err != nil {
					t.Fatalf("parse authorize URL: %v", err)
				}
				return u
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("authorize URL never printed; stderr:\n%s", stderr.String())
	return nil
}

// TestLogin_NoBrowser_ManualPaste: --no-browser prints the authorize URL with the
// accounts /cli/code redirect, accepts the pasted "<state>.<code>", exchanges it
// (PKCE-verified), and caches the ID token — without ever opening a browser or a
// loopback listener.
func TestLogin_NoBrowser_ManualPaste(t *testing.T) {
	srv, gotForm := manualLoginStub(t)
	cache := filepath.Join(t.TempDir(), "creds")

	openBrowser = func(u string) error {
		t.Error("manual mode must not open a browser")
		return nil
	}
	defer func() { openBrowser = defaultOpenBrowser }()

	stdinR, stdinW := io.Pipe()
	loginStdin = stdinR
	defer func() { loginStdin = os.Stdin }()

	stderr := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- runLogin([]string{"--accounts", srv.URL, "--cache", cache, "--no-browser"}, io.Discard, stderr)
	}()

	u := authorizeURLFrom(t, stderr)
	q := u.Query()
	if got, want := q.Get("redirect_uri"), srv.URL+"/cli/code"; got != want {
		t.Errorf("redirect_uri = %q, want %q", got, want)
	}
	if _, err := stdinW.Write([]byte(q.Get("state") + ".test-code\n")); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("login exit %d; stderr:\n%s", code, stderr.String())
	}

	if gotForm.Get("code") != "test-code" {
		t.Errorf("token form code = %q", gotForm.Get("code"))
	}
	sum := sha256.Sum256([]byte(gotForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Error("S256(verifier) != challenge from the printed authorize URL")
	}
	if tok, ok := loadToken(cache, srv.URL); !ok || tok != idTokenWithEmail("dev@x.io") {
		t.Errorf("cached token = %q, %v", tok, ok)
	}
}

// TestLogin_ManualPaste_WrongState: a paste whose state does not match this
// attempt is rejected (exit 1) and the token endpoint is never called.
func TestLogin_ManualPaste_WrongState(t *testing.T) {
	srv, gotForm := manualLoginStub(t)
	cache := filepath.Join(t.TempDir(), "creds")
	stdinR, stdinW := io.Pipe()
	loginStdin = stdinR
	defer func() { loginStdin = os.Stdin }()

	stderr := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- runLogin([]string{"--accounts", srv.URL, "--cache", cache, "--no-browser"}, io.Discard, stderr)
	}()
	authorizeURLFrom(t, stderr)
	_, _ = stdinW.Write([]byte("attacker-state.test-code\n"))
	if code := <-done; code == 0 {
		t.Fatal("wrong-state paste must fail login")
	}
	if len(*gotForm) != 0 {
		t.Error("wrong-state paste must not reach the token endpoint")
	}
	if _, ok := loadToken(cache, srv.URL); ok {
		t.Error("no credentials may be cached on a rejected paste")
	}
}

// TestLogin_BrowserLaunchFails_FallsBackToManual: with detection pinned to
// "desktop", a synchronous openBrowser error abandons the loopback flow and
// restarts in manual mode (spec fallback #3, missing-launcher case).
func TestLogin_BrowserLaunchFails_FallsBackToManual(t *testing.T) {
	srv, _ := manualLoginStub(t)
	cache := filepath.Join(t.TempDir(), "creds")

	detectHeadless = func() bool { return false }
	defer func() { detectHeadless = defaultDetectHeadless }()
	openBrowser = func(u string) error { return fmt.Errorf("exec: \"xdg-open\": not found") }
	defer func() { openBrowser = defaultOpenBrowser }()

	stdinR, stdinW := io.Pipe()
	loginStdin = stdinR
	defer func() { loginStdin = os.Stdin }()

	stderr := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- runLogin([]string{"--accounts", srv.URL, "--cache", cache}, io.Discard, stderr)
	}()
	u := authorizeURLFrom(t, stderr)
	if got, want := u.Query().Get("redirect_uri"), srv.URL+"/cli/code"; got != want {
		t.Errorf("fallback should re-issue the MANUAL redirect, got %q", got)
	}
	_, _ = stdinW.Write([]byte(u.Query().Get("state") + ".test-code\n"))
	if code := <-done; code != 0 {
		t.Fatalf("login exit %d; stderr:\n%s", code, stderr.String())
	}
}

// TestReadLine_ReadError: a reader that hits EOF before any byte (stdin closed
// out from under the paste prompt, e.g. Ctrl-D or a piped-empty stdin) reports
// the read error rather than an empty line.
func TestReadLine_ReadError(t *testing.T) {
	r, w := io.Pipe()
	_ = w.Close() // EOF, nothing ever written
	if _, err := readLine(context.Background(), r); err == nil {
		t.Fatal("expected an error reading from an already-closed pipe")
	}
}

// TestReadLine_ContextDeadline: readLine honors ctx even though the underlying
// read blocks forever (stdin has no portable deadline — see readLine's doc
// comment on the resulting leaked reader goroutine).
func TestReadLine_ContextDeadline(t *testing.T) {
	r, w := io.Pipe() // never written; closed at cleanup to unblock the reader goroutine
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := readLine(ctx, r)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}
