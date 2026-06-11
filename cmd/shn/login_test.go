package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
