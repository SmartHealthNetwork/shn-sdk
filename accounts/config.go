package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// CLIConfig is the unauthenticated discovery payload from {accounts}/cli-config.
type CLIConfig struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

// OIDC is the subset of the issuer's OIDC discovery document the login flow
// needs. The issuer (from /cli-config) is the Cognito USER-POOL issuer
// (cognito-idp.<region>.amazonaws.com/<poolID>), whose host is the Cognito API — it does
// NOT serve /oauth2/* (hitting it returns a BadRequest "did not understand the
// operation"). The OIDC discovery doc lives at the issuer but its authorize/token
// endpoints point at the HOSTED UI domain. So resolve the endpoints via discovery —
// never construct issuer+"/oauth2/*".
type OIDC struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// FetchCLIConfig GETs {accountsURL}/cli-config and decodes the discovery payload.
// A nil hc defaults to http.DefaultClient.
func FetchCLIConfig(ctx context.Context, hc *http.Client, accountsURL string) (CLIConfig, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountsURL+"/cli-config", nil)
	if err != nil {
		return CLIConfig{}, fmt.Errorf("build cli-config request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return CLIConfig{}, fmt.Errorf("GET /cli-config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CLIConfig{}, fmt.Errorf("GET /cli-config: HTTP %d", resp.StatusCode)
	}
	var cfg CLIConfig
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&cfg); err != nil {
		return CLIConfig{}, fmt.Errorf("decode cli-config: %w", err)
	}
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return CLIConfig{}, fmt.Errorf("cli-config missing issuer or client_id")
	}
	return cfg, nil
}

// FetchOIDC GETs {issuer}/.well-known/openid-configuration and returns the authorize +
// token endpoints. Cognito's user-pool issuer serves this doc; the endpoints it returns
// point at the hosted-UI domain (constructing issuer+"/oauth2/*" instead returns a
// BadRequest from the cognito-idp API host). A nil hc defaults to http.DefaultClient.
func FetchOIDC(ctx context.Context, hc *http.Client, issuer string) (OIDC, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return OIDC{}, fmt.Errorf("build openid-configuration request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return OIDC{}, fmt.Errorf("GET openid-configuration: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OIDC{}, fmt.Errorf("GET openid-configuration: HTTP %d", resp.StatusCode)
	}
	var oc OIDC
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&oc); err != nil {
		return OIDC{}, fmt.Errorf("decode openid-configuration: %w", err)
	}
	if oc.AuthorizationEndpoint == "" || oc.TokenEndpoint == "" {
		return OIDC{}, fmt.Errorf("openid-configuration missing authorization_endpoint or token_endpoint")
	}
	return oc, nil
}
