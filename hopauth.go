package shnsdk

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// parseAndVerifyHubAssertion verifies an X-Hub-Assertion header value exactly as
// a substrate gateway does (PARTICIPANT_PROTOCOL.md §6.2a, same check order):
// decode → issuer pin (holderId "hub") → signature → audience == ownHolderID →
// bounds (ttl cap, future-dating ≤ MaxClockSkew, expiry). Returns the
// assertion's jti so the caller enforces one-time-use. Fails closed on every
// malformed input.
func parseAndVerifyHubAssertion(header, ownHolderID string, hubPub ed25519.PublicKey, now time.Time) (string, error) {
	if header == "" {
		return "", errors.New("missing X-Hub-Assertion")
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	var a Assertion
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if a.HolderID != "hub" {
		return "", errors.New("issuer is not the hub")
	}
	// assertionSigningPayload zeroes Sig on an internal copy before marshalling, so passing a with Sig set is safe.
	sig := a.Sig
	if len(sig) == 0 || !ed25519.Verify(hubPub, assertionSigningPayload(a), sig) {
		return "", errors.New("signature verification failed")
	}
	if a.Audience != ownHolderID {
		return "", errors.New("audience mismatch")
	}
	// Bounds (verifier-enforced, mirrors holderauth.Verify):
	// 1. TTL cap: assertion lifetime must not exceed MaxAssertionTTL.
	if a.Expiry.Sub(a.IssuedAt) > MaxAssertionTTL {
		return "", errors.New("ttl exceeds maximum")
	}
	// 1a. Zero-TTL: expiry must be strictly after issuedAt (mirrors holderauth.Verify).
	if !a.Expiry.After(a.IssuedAt) {
		return "", errors.New("expiry not after issuance")
	}
	// 2. Future-dating: issuedAt must not be more than MaxClockSkew ahead of now.
	if a.IssuedAt.After(now.Add(MaxClockSkew)) {
		return "", errors.New("issued in the future")
	}
	// 3. Expiry: assertion must not have expired.
	if now.After(a.Expiry) {
		return "", errors.New("expired")
	}
	if a.JTI == "" {
		return "", errors.New("missing jti")
	}
	return a.JTI, nil
}

// FetchHubTransportKey fetches the Hub's X-Hub-Assertion verification key from
// the discovery descriptor's hubTransportKeyURL (hub GET /transport-key,
// {"pubkey": base64}).
func FetchHubTransportKey(ctx context.Context, c *http.Client, url string) (ed25519.PublicKey, error) {
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
		return nil, fmt.Errorf("transport-key: status %d", resp.StatusCode)
	}
	var body struct {
		PubKey string `json:"pubkey"`
	}
	// Response is tiny (<1 KiB), but MaxResponseBytes keeps all readers uniform across the SDK.
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxResponseBytes)).Decode(&body); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(body.PubKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("transport-key: malformed pubkey")
	}
	return ed25519.PublicKey(raw), nil
}
