// Package shnsdk — transport.go: HTTP transport helpers (JSON POST, bounded body
// decode, status-error typing, a timeout client) shared by the gateway engine and
// substrate services. Promoted from internal/wire. Envelope codecs live in
// wire.go (EncodeEnvelope/DecodeEnvelope); envelope crypto in envelope.go (Seal/Open).
package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StatusError is returned by PostJSON when the server responds with a non-2xx
// status, so callers can distinguish a DENIAL (e.g. a 403 authorization/consent
// refusal) from a transport failure (a Do error, which is a plain wrapped error).
// Code is the HTTP status; Body is the (truncated) response body.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("shnsdk: non-2xx response %d: %s", e.Code, e.Body)
}

// DecodeJSONBody reads at most MaxRequestBytes from r.Body and unmarshals the
// first JSON value into dst. On overflow it returns tooLarge=true and a non-nil
// error; callers should map that to 413. Returns (tooLarge bool, err error).
func DecodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) (tooLarge bool, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	if decErr := json.NewDecoder(r.Body).Decode(dst); decErr != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(decErr, &maxBytesErr) {
			return true, decErr
		}
		return false, decErr
	}
	return false, nil
}

// NewClient returns an *http.Client with a 30-second request timeout. Use this
// instead of http.DefaultClient when constructing wired services so that a
// misbehaving downstream cannot hold a goroutine forever.
func NewClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// PostJSON marshals body to JSON, POSTs it to url using client (falling back to
// http.DefaultClient), applies the given headers plus Content-Type:
// application/json. If out is non-nil and the response is 2xx, the response body
// is unmarshalled into out. A non-2xx status causes an error carrying the status
// code and response body text.
func PostJSON(ctx context.Context, client *http.Client, url string, body any, out any, headers map[string]string) error {
	if client == nil {
		client = http.DefaultClient
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("shnsdk: marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("shnsdk: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("shnsdk: do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return fmt.Errorf("shnsdk: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{Code: resp.StatusCode, Body: string(respBody)}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("shnsdk: unmarshal response body: %w", err)
		}
	}

	return nil
}
