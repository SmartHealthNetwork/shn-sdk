package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ManualCodePath is the Accounts-service page that displays the authorization code
// in the headless (copy-paste) login flow. {accounts}+ManualCodePath must be
// registered as a callback URL on the Cognito cli app client (exact match).
const ManualCodePath = "/cli/code"

// ErrCodeExpired is returned by ManualFlow.Exchange when the token endpoint
// rejects the code as invalid_grant — expired (~5 min TTL) or already used.
var ErrCodeExpired = errors.New("authorization code expired or already used — re-run `shn login`")

// ManualFlow is an in-flight headless Authorization-Code+PKCE flow: no loopback
// listener; the user signs in from any browser and pastes back the
// "<state>.<code>" string shown by the accounts /cli/code page. The PKCE
// verifier never leaves this process, so a pasted code is useless to anyone
// who observes it in transit.
type ManualFlow struct {
	authzURL      string
	tokenEndpoint string
	clientID      string
	redirectURI   string
	verifier      string
	state         string
	hc            *http.Client
	now           func() time.Time
}

// StartManualPKCE begins a headless PKCE flow whose redirect_uri is the accounts
// service's /cli/code page. accountsURL must be the same canonical URL registered
// in the Cognito app client's callback URLs (exact-match), i.e. the `--accounts`
// value. A nil hc defaults to http.DefaultClient.
func StartManualPKCE(hc *http.Client, cfg CLIConfig, oidc OIDC, accountsURL string, now func() time.Time) (*ManualFlow, error) {
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
	redirectURI := strings.TrimRight(accountsURL, "/") + ManualCodePath

	authzURL := oidc.AuthorizationEndpoint + "?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(cfg.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	return &ManualFlow{
		authzURL:      authzURL,
		tokenEndpoint: oidc.TokenEndpoint,
		clientID:      cfg.ClientID,
		redirectURI:   redirectURI,
		verifier:      verifier,
		state:         state,
		hc:            hc,
		now:           now,
	}, nil
}

// AuthorizeURL returns the hosted-UI authorize URL the user should open in any
// browser (on any machine) to complete sign-in.
func (f *ManualFlow) AuthorizeURL() string {
	return f.authzURL
}

// Exchange parses the pasted "<state>.<code>" string from the /cli/code page,
// verifies it belongs to this flow, and exchanges the code for tokens. State
// binds the paste to this login attempt; PKCE binds the code to this process.
func (f *ManualFlow) Exchange(ctx context.Context, pasted string) (Tokens, error) {
	pasted = strings.TrimSpace(pasted)
	state, code, ok := strings.Cut(pasted, ".")
	if !ok || state == "" || code == "" {
		return Tokens{}, fmt.Errorf("malformed code — paste the full string shown on the sign-in page")
	}
	if state != f.state {
		return Tokens{}, fmt.Errorf("code does not belong to this login attempt — re-run `shn login` and use the new URL")
	}
	tr, err := postToken(ctx, f.hc, f.tokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {f.clientID},
		"code":          {code},
		"redirect_uri":  {f.redirectURI},
		"code_verifier": {f.verifier},
	})
	if err != nil {
		// Cognito reports an expired or replayed code as invalid_grant; the paste
		// window (10 min) deliberately outlives the code TTL (~5 min), so map this
		// to a clear "start over" error.
		if strings.Contains(err.Error(), "invalid_grant") {
			return Tokens{}, ErrCodeExpired
		}
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
