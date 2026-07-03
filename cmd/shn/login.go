package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	acct "github.com/SmartHealthNetwork/shn-sdk/accounts"
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

// cachedCreds is the on-disk credential cache for the Accounts API bearer. Token is
// the Cognito ID token; the authenticated Accounts commands read it via loadToken.
type cachedCreds struct {
	Accounts string    `json:"accounts"` // accounts base URL these creds are scoped to
	Token    string    `json:"token"`    // Cognito ID token (sent as Bearer)
	Expiry   time.Time `json:"expiry"`   // ID token expiry (RFC3339)
}

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
	cfg, err := acct.FetchCLIConfig(ctx, nil, accountsURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}
	issuer := strings.TrimRight(cfg.Issuer, "/")

	// Resolve the authorize/token endpoints from the issuer's OIDC discovery doc (they
	// point at the Cognito hosted UI domain; the issuer host itself does not serve
	// /oauth2/*). Mirrors the developer portal's browser PKCE.
	oidc, err := acct.FetchOIDC(ctx, nil, issuer)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}

	flow, err := acct.StartPKCE(nil, cfg, oidc, acct.LoopbackPorts, time.Now)
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}
	defer flow.Close()

	// Build and open the authorize URL (the hosted-UI endpoint from OIDC discovery).
	authzURL := flow.AuthorizeURL()
	if err := openBrowser(authzURL); err != nil {
		fmt.Fprintf(stderr, "shn login: could not open a browser; visit this URL to continue:\n  %s\n", authzURL)
	} else {
		fmt.Fprintf(stderr, "shn login: opening browser to complete sign-in (if it does not open, visit):\n  %s\n", authzURL)
	}

	// Wait for the callback and exchange the code for tokens (bounded so we never hang
	// forever).
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tok, err := flow.Wait(waitCtx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(stderr, "shn login: timed out waiting for the authorization callback")
			return 1
		}
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}

	if err := writeCreds(*cache, cachedCreds{Accounts: accountsURL, Token: tok.IDToken, Expiry: tok.Expiry}); err != nil {
		fmt.Fprintf(stderr, "shn login: write cache: %v\n", err)
		return 1
	}

	if email := acct.EmailFromIDToken(tok.IDToken); email != "" {
		fmt.Fprintf(stdout, "Logged in as %s\n", email)
	} else {
		fmt.Fprintln(stdout, "Logged in")
	}
	return 0
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

// homeDir returns the user's home directory, or "." if it cannot be determined.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}
