package shnsdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Holder is one row of the public registrar /holders feed — wire-identical to
// the substrate's registration DTO (id/role/encPub/signPub/baseURL, std base64).
// For the decoded, in-memory runtime peer-cache entry (keys as usable values, no JSON tags) see RegistryEntry.
type Holder struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	EncPub  string `json:"encPub"`
	SignPub string `json:"signPub"`
	BaseURL string `json:"baseURL"`
}

// EncKey decodes the holder's X25519 public key.
func (h Holder) EncKey() (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(h.EncPub)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("holders: malformed encPub")
	}
	var k [32]byte
	copy(k[:], raw)
	return &k, nil
}

// FetchHolders fetches the COMPLETE public holder directory from the registrar
// /holders feed (founding manifest holders ∪ dynamic registrations).
func FetchHolders(ctx context.Context, c *http.Client, registrarURL string) ([]Holder, error) {
	url := strings.TrimRight(registrarURL, "/") + "/holders"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("holders: status %d", resp.StatusCode)
	}
	var hs []Holder
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxResponseBytes)).Decode(&hs); err != nil {
		return nil, err
	}
	return hs, nil
}

// NewFeedEncResolver returns a resolve-by-holder-id func backed by the live
// /holders feed. It re-fetches on every call — deliberately simple for B1 (a
// sandbox responder answers at human cadence); add caching when volume warrants
// (additive).
func NewFeedEncResolver(c *http.Client, registrarURL string) func(holderID string) (*[32]byte, bool) {
	return func(holderID string) (*[32]byte, bool) {
		hs, err := FetchHolders(context.Background(), c, registrarURL)
		if err != nil {
			return nil, false
		}
		for _, h := range hs {
			if h.ID == holderID {
				k, kerr := h.EncKey()
				if kerr != nil {
					return nil, false
				}
				return k, true
			}
		}
		return nil, false
	}
}
