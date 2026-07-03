package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// Tokens is the result of a successful PKCE code exchange or refresh.
type Tokens struct {
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry"` // ID-token expiry (now + expires_in)
}

// LoopbackPorts is the fixed set of 127.0.0.1 ports `shn login` binds, in order.
// These MUST be registered as the Cognito app client's callback URLs, because
// Cognito requires an EXACT redirect_uri match.
var LoopbackPorts = []int{8400, 8401, 8402, 8403, 8404}

// callbackPath is the fixed loopback redirect path.
const callbackPath = "/callback"

// tokenResponse is the OIDC token-endpoint reply shape shared by the authorization-code
// exchange and refresh grant.
type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// pkceResult carries the outcome of the loopback callback to Wait.
type pkceResult struct {
	code string
	err  error
}

// PKCEFlow is an in-flight Authorization-Code+PKCE loopback flow: a bound 127.0.0.1
// listener serving the callback, waiting for the browser to complete the hosted-UI
// sign-in and redirect back with a code.
type PKCEFlow struct {
	ln            net.Listener
	srv           *http.Server
	resCh         chan pkceResult
	authzURL      string
	tokenEndpoint string
	clientID      string
	redirectURI   string
	verifier      string
	hc            *http.Client
	now           func() time.Time
	closeOnce     sync.Once
	done          chan struct{}
}

// StartPKCE begins an Authorization-Code+PKCE loopback flow: it generates a
// high-entropy code_verifier + S256 challenge and state, binds the first free port
// from ports, and starts serving the /callback redirect on it. A nil hc defaults to
// http.DefaultClient (used both for the callback wait path and the eventual code
// exchange in Wait).
//
// Cognito does NOT implement RFC 8252 §7.3 port-agnostic loopback matching — it
// requires redirect_uri to EXACTLY match a registered callback URL (port and all).
// So we bind a registered port (ports, mirrored in the Cognito app client's
// callback_urls) rather than an ephemeral :0, or Cognito returns redirect_mismatch.
func StartPKCE(hc *http.Client, cfg CLIConfig, oidc OIDC, ports []int, now func() time.Time) (*PKCEFlow, error) {
	if hc == nil {
		hc = http.DefaultClient
	}

	verifier, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("generate code_verifier: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state, err := randomURLSafe(24)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	var ln net.Listener
	var boundPort int
	for _, p := range ports {
		l, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if lerr == nil {
			ln, boundPort = l, p
			break
		}
	}
	if ln == nil {
		return nil, fmt.Errorf("no free loopback port in %v (close other login sessions and retry)", ports)
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", boundPort, callbackPath)

	resCh := make(chan pkceResult, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			select {
			case resCh <- pkceResult{err: fmt.Errorf("oauth state mismatch")}:
			default:
			}
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			select {
			case resCh <- pkceResult{err: fmt.Errorf("authorize error: %s", e)}:
			default:
			}
			return
		}
		code := q.Get("code")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body>Login complete. You can close this tab.</body></html>")
		select {
		case resCh <- pkceResult{code: code}:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()

	authzURL := oidc.AuthorizationEndpoint + "?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(cfg.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	return &PKCEFlow{
		ln:            ln,
		srv:           srv,
		resCh:         resCh,
		authzURL:      authzURL,
		tokenEndpoint: oidc.TokenEndpoint,
		clientID:      cfg.ClientID,
		redirectURI:   redirectURI,
		verifier:      verifier,
		hc:            hc,
		now:           now,
		done:          make(chan struct{}),
	}, nil
}

// AuthorizeURL returns the hosted-UI authorize URL the user's browser should visit
// to complete sign-in.
func (f *PKCEFlow) AuthorizeURL() string {
	return f.authzURL
}

// Wait blocks for the loopback callback (or ctx cancellation) and, on success,
// exchanges the authorization code for tokens.
func (f *PKCEFlow) Wait(ctx context.Context) (Tokens, error) {
	var code string
	select {
	case res := <-f.resCh:
		if res.err != nil {
			return Tokens{}, res.err
		}
		if res.code == "" {
			return Tokens{}, fmt.Errorf("no authorization code returned")
		}
		code = res.code
	case <-ctx.Done():
		return Tokens{}, ctx.Err()
	case <-f.done:
		return Tokens{}, fmt.Errorf("accounts: pkce flow closed")
	}

	tr, err := postToken(ctx, f.hc, f.tokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {f.clientID},
		"code":          {code},
		"redirect_uri":  {f.redirectURI},
		"code_verifier": {f.verifier},
	})
	if err != nil {
		return Tokens{}, err
	}
	if tr.IDToken == "" {
		return Tokens{}, fmt.Errorf("token response missing id_token")
	}
	return Tokens{
		IDToken:      tr.IDToken,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		Expiry:       f.now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}, nil
}

// Close shuts down the loopback server and releases its listener. It is idempotent.
func (f *PKCEFlow) Close() {
	f.closeOnce.Do(func() {
		_ = f.srv.Close()
		close(f.done)
	})
}

// Refresh exchanges a refresh token for new tokens. Cognito may omit refresh_token
// from the response when refresh-token rotation is off; in that case the caller's
// refreshToken is kept rather than dropped. A nil hc defaults to http.DefaultClient.
func Refresh(ctx context.Context, hc *http.Client, tokenEndpoint, clientID, refreshToken string, now func() time.Time) (Tokens, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	tr, err := postToken(ctx, hc, tokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	})
	if err != nil {
		return Tokens{}, err
	}
	if tr.IDToken == "" {
		return Tokens{}, fmt.Errorf("token response missing id_token")
	}
	rt := tr.RefreshToken
	if rt == "" {
		rt = refreshToken
	}
	return Tokens{
		IDToken:      tr.IDToken,
		AccessToken:  tr.AccessToken,
		RefreshToken: rt,
		Expiry:       now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}, nil
}

// postToken POSTs a token-endpoint form request and decodes the response.
func postToken(ctx context.Context, hc *http.Client, tokenEndpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
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

// EmailFromIDToken best-effort decodes the "email" claim from a JWT payload for a
// friendly print. It does NOT verify the signature (the Accounts service is the
// verifier); a decode failure returns "" and never fails the caller.
func EmailFromIDToken(idToken string) string {
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
