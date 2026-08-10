package main

import (
	"bufio"
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

// loginStdin is the paste source for manual (headless) mode; a package var so
// tests can inject a pipe.
var loginStdin io.Reader = os.Stdin

// detectHeadless reports whether the environment looks browserless; a package
// var so tests can pin the browser path on headless CI runners.
var detectHeadless = defaultDetectHeadless

func defaultDetectHeadless() bool { return headlessEnv(runtime.GOOS, os.Getenv) }

// headlessEnv is the auto-detect matrix: an SSH session on any OS, or Linux
// with no X/Wayland display. Headless Windows (CI containers) never
// auto-detects — use --no-browser there; the matrix is deliberately
// conservative (a wrong "manual" guess still logs in, just with one paste).
func headlessEnv(goos string, getenv func(string) string) bool {
	if getenv("SSH_TTY") != "" || getenv("SSH_CONNECTION") != "" {
		return true
	}
	return goos == "linux" && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == ""
}

// errNoBrowser signals that the browser could not be launched, so the login
// should restart in manual mode. Only a missing/unstartable launcher binary
// errors synchronously (exec.Start); a launcher that starts and then fails is
// not detectable here — the env matrix above is the load-bearing detection.
var errNoBrowser = errors.New("browser launch failed")

// manualPasteTimeout bounds the wait for the pasted code. It deliberately
// outlives Cognito's ~5-minute code TTL: a late paste fails at the token
// endpoint and maps to ErrCodeExpired ("re-run shn login").
const manualPasteTimeout = 10 * time.Minute

// runLogin implements `shn login`: OAuth 2.1 Authorization-Code+PKCE against
// Cognito, via a 127.0.0.1 loopback redirect (browser mode) or the accounts
// /cli/code copy-paste page (manual mode: --no-browser, headless env, or
// browser launch failure).
func runLogin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accounts := fs.String("accounts", "", "Accounts service base URL (required)")
	noBrowser := fs.Bool("no-browser", false, "headless login: print a URL to open on another machine, then paste back the code it shows")
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
	// Resolve authorize/token endpoints from the issuer's OIDC discovery doc (they
	// point at the Cognito hosted-UI domain; the issuer host does not serve /oauth2/*).
	oidc, err := acct.FetchOIDC(ctx, nil, strings.TrimRight(cfg.Issuer, "/"))
	if err != nil {
		fmt.Fprintf(stderr, "shn login: %v\n", err)
		return 1
	}

	manual := *noBrowser || detectHeadless()
	var tok acct.Tokens
	if !manual {
		tok, err = browserLogin(ctx, cfg, oidc, stderr)
		if errors.Is(err, errNoBrowser) {
			fmt.Fprintln(stderr, "shn login: could not launch a browser — switching to manual sign-in.")
			manual = true
		} else if err != nil {
			fmt.Fprintf(stderr, "shn login: %v\n", err)
			return 1
		}
	}
	if manual {
		tok, err = manualLogin(ctx, cfg, oidc, accountsURL, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "shn login: %v\n", err)
			return 1
		}
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

// browserLogin runs the loopback-PKCE branch: open the hosted UI in the local
// browser and wait (bounded) for the 127.0.0.1 callback. A synchronous launch
// failure returns errNoBrowser so runLogin restarts in manual mode.
func browserLogin(ctx context.Context, cfg acct.CLIConfig, oidc acct.OIDC, stderr io.Writer) (acct.Tokens, error) {
	flow, err := acct.StartPKCE(nil, cfg, oidc, acct.LoopbackPorts, time.Now)
	if err != nil {
		return acct.Tokens{}, err
	}
	defer flow.Close()
	if err := openBrowser(flow.AuthorizeURL()); err != nil {
		return acct.Tokens{}, errNoBrowser
	}
	fmt.Fprintf(stderr, "shn login: opening browser to complete sign-in (if it does not open, visit):\n  %s\n", flow.AuthorizeURL())
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tok, err := flow.Wait(waitCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		return acct.Tokens{}, fmt.Errorf("timed out waiting for the authorization callback")
	}
	return tok, err
}

// manualLogin runs the headless branch: print the authorize URL (redirecting to
// the accounts /cli/code page), read the pasted "<state>.<code>" from stdin, and
// exchange it. The paste prompt is bounded by manualPasteTimeout.
func manualLogin(ctx context.Context, cfg acct.CLIConfig, oidc acct.OIDC, accountsURL string, stderr io.Writer) (acct.Tokens, error) {
	flow, err := acct.StartManualPKCE(nil, cfg, oidc, accountsURL, time.Now)
	if err != nil {
		return acct.Tokens{}, err
	}
	fmt.Fprintf(stderr, "shn login: complete sign-in from any machine with a browser.\n\n"+
		"  1. Open:  %s\n"+
		"  2. Sign in. The page will show a login code.\n"+
		"  3. Paste it here.\n\nEnter code: ", flow.AuthorizeURL())
	pasteCtx, cancel := context.WithTimeout(ctx, manualPasteTimeout)
	defer cancel()
	line, err := readLine(pasteCtx, loginStdin)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return acct.Tokens{}, fmt.Errorf("timed out waiting for the code — re-run `shn login`")
		}
		return acct.Tokens{}, err
	}
	// Exchange gets its own fresh bound off the outer ctx, not pasteCtx: pasteCtx's
	// deadline is for waiting on the paste, and by the time readLine returns it may
	// have almost no time left for the token POST, which would surface as a raw
	// "context deadline exceeded" instead of a real error.
	exchangeCtx, exchangeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer exchangeCancel()
	return flow.Exchange(exchangeCtx, line)
}

// readLine reads one newline-terminated line from r, honoring ctx. The reader
// goroutine may outlive a timeout (stdin has no portable deadline); acceptable
// for a CLI that exits right after.
func readLine(ctx context.Context, r io.Reader) (string, error) {
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := bufio.NewReader(r).ReadString('\n')
		if err != nil && line == "" {
			ch <- res{"", err}
			return
		}
		ch <- res{line, nil} // a final unterminated line (EOF) still counts
	}()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
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
