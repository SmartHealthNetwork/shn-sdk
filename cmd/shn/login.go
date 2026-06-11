package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// openBrowser is a package var so tests can inject a fake that drives the authorize
// URL itself (following the 302 to the loopback callback). Default is best-effort.
var openBrowser = defaultOpenBrowser

// defaultOpenBrowser launches the platform's URL opener. It is best-effort: if it
// fails the user can still copy the URL printed to stderr.
func defaultOpenBrowser(u string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	return exec.Command(name, append(args, u)...).Start()
}

// cliConfig is the unauthenticated discovery payload from {accounts}/cli-config.
type cliConfig struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

// oidcEndpoints is the subset of the issuer's OIDC discovery document the login flow
// needs. The issuer (from /cli-config) is the Cognito USER-POOL issuer
// (cognito-idp.<region>.amazonaws.com/<poolID>), whose host is the Cognito API — it does
// NOT serve /oauth2/* (hitting it returns a BadRequest "did not understand the
// operation"). The OIDC discovery doc lives at the issuer but its authorize/token
// endpoints point at the HOSTED UI domain. So resolve the endpoints via discovery —
// never construct issuer+"/oauth2/*".
type oidcEndpoints struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// tokenResponse is the Cognito /oauth2/token reply. We cache the ID token (its aud
// == the app client id and it carries the email claim the Accounts verifier reads),
// not the access token (which lacks email).
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// cachedCreds is the on-disk credential cache for the Accounts API bearer. Token is
// the Cognito ID token; the authenticated Accounts commands read it via loadToken.
type cachedCreds struct {
	Accounts string    `json:"accounts"` // accounts base URL these creds are scoped to
	Token    string    `json:"token"`    // Cognito ID token (sent as Bearer)
	Expiry   time.Time `json:"expiry"`   // ID token expiry (RFC3339)
}

const callbackPath = "/callback"

// loopbackPorts is the fixed set of 127.0.0.1 ports `shn login` binds, in order.
// These MUST be registered as the Cognito app client's callback URLs, because
// Cognito requires an EXACT redirect_uri match.
var loopbackPorts = []int{8400, 8401, 8402, 8403, 8404}

// runLogin implements `shn login`: OAuth 2.1 loopback-PKCE against Cognito.
// It fetches {accounts}/cli-config, runs Authorization-Code+PKCE with a 127.0.0.1
// loopback redirect, exchanges the code for tokens, and caches the ID token (0600).
func runLogin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accounts := fs.String("accounts", "", "Accounts service base URL (required)")
	defaultCache := filepath.Join(homeDir(), ".shn", "credentials")
	cache := fs.String("cache", defaultCache, "credential cache path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *accounts == "" {
		fmt.Fprintln(stderr, "shn login: --accounts is required")
		return 2
	}
	accountsURL := strings.TrimRight(*accounts, "/")

	ctx := context.Background()
	cfg, err := fetchCLIConfig(ctx, accountsURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}
	issuer := strings.TrimRight(cfg.Issuer, "/")

	// Resolve the authorize/token endpoints from the issuer's OIDC discovery doc (they
	// point at the Cognito hosted UI domain; the issuer host itself does not serve
	// /oauth2/*). Mirrors the developer portal's browser PKCE.
	oidc, err := fetchOIDC(ctx, issuer)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}

	// PKCE: high-entropy verifier, S256 challenge.
	verifier, err := randomURLSafe(32)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: generate code_verifier: %v\n", err)
		return 1
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state, err := randomURLSafe(24)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: generate state: %v\n", err)
		return 1
	}

	// Loopback listener on 127.0.0.1, bound to one of a FIXED set of ports.
	// Cognito does NOT implement RFC 8252 §7.3 port-agnostic loopback matching — it
	// requires redirect_uri to EXACTLY match a registered callback URL (port and all).
	// So we bind a registered port (loopbackPorts, mirrored in the Cognito app client's
	// callback_urls) rather than an ephemeral :0, or Cognito returns redirect_mismatch.
	var ln net.Listener
	var boundPort int
	for _, p := range loopbackPorts {
		l, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if lerr == nil {
			ln, boundPort = l, p
			break
		}
	}
	if ln == nil {
		fmt.Fprintf(stderr, "shn login: no free loopback port in %v (close other `shn login` sessions and retry)\n", loopbackPorts)
		return 1
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", boundPort, callbackPath)

	// Serve the callback: validate state, capture code, signal arrival.
	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			select {
			case resCh <- result{err: fmt.Errorf("oauth state mismatch")}:
			default:
			}
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			select {
			case resCh <- result{err: fmt.Errorf("authorize error: %s", e)}:
			default:
			}
			return
		}
		code := q.Get("code")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body>Login complete. You can close this tab.</body></html>")
		select {
		case resCh <- result{code: code}:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// Build and open the authorize URL (the hosted-UI endpoint from OIDC discovery).
	authzURL := oidc.AuthorizationEndpoint + "?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(cfg.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	if err := openBrowser(authzURL); err != nil {
		fmt.Fprintf(stderr, "shn login: could not open a browser; visit this URL to continue:\n  %s\n", authzURL)
	} else {
		fmt.Fprintf(stderr, "shn login: opening browser to complete sign-in (if it does not open, visit):\n  %s\n", authzURL)
	}

	// Wait for the callback (bounded so we never hang forever).
	var code string
	select {
	case res := <-resCh:
		if res.err != nil {
			fmt.Fprintf(stderr, "shn login: %v\n", res.err)
			return 1
		}
		if res.code == "" {
			fmt.Fprintln(stderr, "shn login: no authorization code returned")
			return 1
		}
		code = res.code
	case <-time.After(2 * time.Minute):
		fmt.Fprintln(stderr, "shn login: timed out waiting for the authorization callback")
		return 1
	}

	// Exchange the code for tokens (the hosted-UI token endpoint from OIDC discovery).
	tok, err := exchangeCode(ctx, oidc.TokenEndpoint, cfg.ClientID, code, redirectURI, verifier)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}
	if tok.IDToken == "" {
		fmt.Fprintln(stderr, "shn login: token response missing id_token")
		return 1
	}

	expiry := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if err := writeCreds(*cache, cachedCreds{Accounts: accountsURL, Token: tok.IDToken, Expiry: expiry}); err != nil {
		fmt.Fprintf(stderr, "shn login: write cache: %v\n", err)
		return 1
	}

	if email := emailFromIDToken(tok.IDToken); email != "" {
		fmt.Fprintf(stdout, "Logged in as %s\n", email)
	} else {
		fmt.Fprintln(stdout, "Logged in")
	}
	return 0
}

// fetchCLIConfig GETs {accounts}/cli-config and decodes the discovery payload.
func fetchCLIConfig(ctx context.Context, accountsURL string) (cliConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountsURL+"/cli-config", nil)
	if err != nil {
		return cliConfig{}, fmt.Errorf("build cli-config request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cliConfig{}, fmt.Errorf("GET /cli-config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cliConfig{}, fmt.Errorf("GET /cli-config: HTTP %d", resp.StatusCode)
	}
	var cfg cliConfig
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&cfg); err != nil {
		return cliConfig{}, fmt.Errorf("decode cli-config: %w", err)
	}
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return cliConfig{}, fmt.Errorf("cli-config missing issuer or client_id")
	}
	return cfg, nil
}

// fetchOIDC GETs {issuer}/.well-known/openid-configuration and returns the authorize +
// token endpoints. Cognito's user-pool issuer serves this doc; the endpoints it returns
// point at the hosted-UI domain (constructing issuer+"/oauth2/*" instead returns a
// BadRequest from the cognito-idp API host).
func fetchOIDC(ctx context.Context, issuer string) (oidcEndpoints, error) {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return oidcEndpoints{}, fmt.Errorf("build openid-configuration request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return oidcEndpoints{}, fmt.Errorf("GET openid-configuration: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcEndpoints{}, fmt.Errorf("GET openid-configuration: HTTP %d", resp.StatusCode)
	}
	var oc oidcEndpoints
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&oc); err != nil {
		return oidcEndpoints{}, fmt.Errorf("decode openid-configuration: %w", err)
	}
	if oc.AuthorizationEndpoint == "" || oc.TokenEndpoint == "" {
		return oidcEndpoints{}, fmt.Errorf("openid-configuration missing authorization_endpoint or token_endpoint")
	}
	return oc, nil
}

// exchangeCode POSTs the authorization code + PKCE verifier to the OIDC token endpoint.
func exchangeCode(ctx context.Context, tokenEndpoint, clientID, code, redirectURI, verifier string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("POST token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("POST token endpoint: HTTP %d: %s", resp.StatusCode, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	return tr, nil
}

// writeCreds writes the credential cache as JSON with 0600 perms (dir 0700).
func writeCreds(path string, c cachedCreds) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create cache dir: %w", err)
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal creds: %w", err)
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// loadToken reads the credential cache and returns the cached bearer token if it is
// scoped to accountsURL and not expired. The authenticated Accounts commands use it
// to authenticate API calls.
func loadToken(path, accountsURL string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var c cachedCreds
	if err := json.Unmarshal(b, &c); err != nil {
		return "", false
	}
	if c.Accounts != strings.TrimRight(accountsURL, "/") {
		return "", false
	}
	if c.Token == "" || !c.Expiry.After(time.Now()) {
		return "", false
	}
	return c.Token, true
}

// emailFromIDToken best-effort decodes the "email" claim from a JWT payload for a
// friendly print. It does NOT verify the signature (the Accounts service is the
// verifier); a decode failure returns "" and never fails login.
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ""
	}
	return claims.Email
}

// randomURLSafe returns n random bytes encoded as base64url (no padding).
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// homeDir returns the user's home directory, or "." if it cannot be determined.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}
