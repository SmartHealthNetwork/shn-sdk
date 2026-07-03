package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// Client is a thin net/http client for the Accounts service API. Every call sets
// Authorization: Bearer <token> + Content-Type: application/json (when the request
// carries a body), reads bodies under a bound, and surfaces a non-2xx (with the
// server's body) as an error.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient builds a client scoped to a (trailing-slash-trimmed) base URL.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
	}
}

// WithHTTP overrides the *http.Client used for calls and returns the receiver for
// chaining. A nil hc (or never calling WithHTTP) defaults to http.DefaultClient.
func (c *Client) WithHTTP(hc *http.Client) *Client {
	c.hc = hc
	return c
}

// httpClient returns the configured *http.Client, defaulting to
// http.DefaultClient.
func (c *Client) httpClient() *http.Client {
	if c.hc == nil {
		return http.DefaultClient
	}
	return c.hc
}

// do issues an authenticated JSON request and returns the response body (bounded).
// A transport error or non-2xx status becomes an error carrying the server's body.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

// Create is step one of two-step registration: POST /clients assigns a server id
// for the pending client and returns it.
func (c *Client) Create(ctx context.Context, name, role, encPub, signPub, baseURL string) (string, error) {
	body := map[string]string{
		"name":    name,
		"role":    role,
		"encPub":  encPub,
		"signPub": signPub,
		"baseURL": baseURL,
	}
	respBody, err := c.do(ctx, http.MethodPost, "/clients", body)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode create response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("create response missing id")
	}
	return out.ID, nil
}

// SubmitPoP is step two of two-step registration: POST /clients/{id}/pop forwards
// the proof-of-possession (built over the server-assigned id) so the Accounts
// service can register the holder with the registrar.
func (c *Client) SubmitPoP(ctx context.Context, id string, reg shnsdk.RegistrationRequest) error {
	body := map[string]string{
		"pop":     reg.Pop,
		"encPub":  reg.EncPub,
		"signPub": reg.SignPub,
		"baseURL": reg.BaseURL,
		"role":    reg.Role,
	}
	_, err := c.do(ctx, http.MethodPost, "/clients/"+id+"/pop", body)
	return err
}

// ClientRow is one row of GET /clients (the developer's registered clients).
type ClientRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	SignPubFp string `json:"signPubFp"`
	EncPubFp  string `json:"encPubFp"`
}

// List returns the developer's clients (GET /clients).
func (c *Client) List(ctx context.Context) ([]ClientRow, error) {
	respBody, err := c.do(ctx, http.MethodGet, "/clients", nil)
	if err != nil {
		return nil, err
	}
	var rows []ClientRow
	if err := json.Unmarshal(respBody, &rows); err != nil {
		return nil, fmt.Errorf("decode clients response: %w", err)
	}
	return rows, nil
}

// Revoke revokes a client (POST /clients/{id}/revoke).
func (c *Client) Revoke(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/clients/"+id+"/revoke", nil)
	return err
}
