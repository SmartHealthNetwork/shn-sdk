package shnsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Token is a per-leg, scope-bound authorization minted by the substrate's
// Authorization Framework (no standing access, AI-11). PORTED standalone from
// internal/authz.Token with the SAME json tags so the SDK can verify a
// substrate-signed token. The SDK only VERIFIES tokens — it NEVER mints them
// (minting is a substrate-private capability). Parity is proven by
// test/sdkparity/token_parity_test.go.
type Token struct {
	Operation     string `json:"operation"`
	Scope         string `json:"scope"`
	Subject       string `json:"subject"` // PCI, never member ID (AI-5)
	Frame         string `json:"frame"`
	CorrelationID string `json:"correlationId"`
	Holder        string `json:"holder"`
	ConsentRef    string `json:"consentRef,omitempty"`
	// PayloadHash is sha256hex(envelope ciphertext) — the per-leg binding of THIS
	// authorization to THIS payload (AI-2). NOT omitempty (field order + presence is
	// load-bearing for signing-payload parity with internal/authz.Token). Empty only
	// for non-envelope tokens (patient-access-read).
	PayloadHash string    `json:"payloadHash"`
	Expiry      time.Time `json:"expiry"`
	Signature   []byte    `json:"signature"`
}

// AuthorizeRequest is the JSON body for POST {authzURL}/authorize, matching the
// substrate's authorize request shape (internal/authzsvc).
type AuthorizeRequest struct {
	Frame         string `json:"frame"`
	Operation     string `json:"operation"`
	SubjectPCI    string `json:"subjectPCI"`
	CorrelationID string `json:"correlationId"`
	// PayloadHash is sha256hex(envelope ciphertext): the participant seals the
	// payload FIRST, then authorizes against that ciphertext so the minted token
	// binds THIS payload (AI-2). Empty for non-envelope ops (patient-access-read).
	PayloadHash string `json:"payloadHash,omitempty"`
}

// authorizeResp is the success body returned by POST {authzURL}/authorize:
// {"token": <Token>} (sampleparticipant authorizeResp).
type authorizeResp struct {
	Token Token `json:"token"`
}

// tokenSigningPayload is the exact bytes the framework signs: the token JSON
// marshalled with Signature zeroed. Byte-identical to internal/authz.signingPayload.
func tokenSigningPayload(t Token) []byte {
	c := t
	c.Signature = nil
	b, _ := json.Marshal(c)
	return b
}

// Authorize obtains a per-operation token from the substrate's Authorization
// Framework. It issues a holder assertion for audience "authz", POSTs req to
// {authzURL}/authorize with the X-Holder-Assertion header, and returns the minted
// Token. Ported from sampleparticipant.authorize.
//
// The assertion audience is FIXED to "authz" and its lifetime to MaxAssertionTTL
// — Authorize does not take these as parameters (unlike Identity.Assertion, which
// does). This is the holder assertion the Authorization Framework expects.
func (id Identity) Authorize(ctx context.Context, c *http.Client, authzURL string, req AuthorizeRequest) (Token, error) {
	if c == nil {
		c = http.DefaultClient
	}
	hdr, err := id.Assertion("authz", id.now(), MaxAssertionTTL)
	if err != nil {
		return Token{}, fmt.Errorf("shnsdk: build authz assertion: %w", err)
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return Token{}, fmt.Errorf("shnsdk: marshal authorize request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, authzURL+"/authorize", bytes.NewReader(reqJSON))
	if err != nil {
		return Token{}, fmt.Errorf("shnsdk: build authorize request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Holder-Assertion", hdr)

	resp, err := c.Do(httpReq)
	if err != nil {
		return Token{}, fmt.Errorf("shnsdk: POST /authorize: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return Token{}, fmt.Errorf("shnsdk: read /authorize response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, fmt.Errorf("shnsdk: /authorize returned %d: %s", resp.StatusCode, body)
	}
	var out authorizeResp
	if err := json.Unmarshal(body, &out); err != nil {
		return Token{}, fmt.Errorf("shnsdk: unmarshal /authorize response: %w", err)
	}
	return out.Token, nil
}

// verifyToken checks the signature and expiry against the framework's verifying
// key. Tampering with any field or expiry invalidates the token. Ported from
// internal/authz.Verify (incl. the fail-closed nil/wrong-length key guard:
// ed25519.Verify PANICS on a wrong-length key, so reject it here rather than panic).
func verifyToken(t Token, pub ed25519.PublicKey, now time.Time) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("shnsdk: no valid verifying key configured")
	}
	if len(t.Signature) == 0 || !ed25519.Verify(pub, tokenSigningPayload(t), t.Signature) {
		return errors.New("shnsdk: invalid token signature")
	}
	if now.After(t.Expiry) {
		return errors.New("shnsdk: token expired")
	}
	return nil
}

// VerifyBound checks signature+expiry AND binds the token to exactly this
// operation/frame/correlation/holder/subject. Pass empty string to skip a
// particular check — EXCEPT wantPayloadHash, which is STRICT: every envelope leg
// binds a payload (AI-2), so an empty want or empty token hash is rejected. This
// stops a validly-signed token from being lifted into a DIFFERENT envelope,
// operation, correlation, holder, patient, or PAYLOAD and replayed (H1/AI-2).
// Ported from internal/authz.VerifyBound; the SDK verifies what the substrate
// signs (test/sdkparity/token_parity_test.go).
func VerifyBound(t Token, authzPub ed25519.PublicKey, now time.Time, wantFrame, wantOp, wantCorr, wantHolder, wantSubject, wantPayloadHash string) error {
	if err := verifyToken(t, authzPub, now); err != nil {
		return err
	}
	if wantFrame != "" && t.Frame != wantFrame {
		return errors.New("shnsdk: token frame mismatch")
	}
	if wantOp != "" && t.Operation != wantOp {
		return errors.New("shnsdk: token operation mismatch")
	}
	if wantCorr != "" && t.CorrelationID != wantCorr {
		return errors.New("shnsdk: token correlation mismatch")
	}
	if wantHolder != "" && t.Holder != wantHolder {
		return errors.New("shnsdk: holder mismatch")
	}
	if wantSubject != "" && t.Subject != wantSubject {
		return errors.New("shnsdk: subject mismatch")
	}
	// PayloadHash is STRICT — every envelope leg binds a payload, so an empty want
	// or an empty token hash is a misuse/unprotected call, not a skip (AI-2). Mirrors
	// internal/authz.VerifyBound.
	if wantPayloadHash == "" || t.PayloadHash == "" || t.PayloadHash != wantPayloadHash {
		return errors.New("shnsdk: payload hash mismatch")
	}
	return nil
}
