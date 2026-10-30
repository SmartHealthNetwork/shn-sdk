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

// Token is a per-leg, scope-bound authorization minted by the network's
// Authorization Framework (no standing access, AI-11). PORTED standalone from
// the Authorization Framework's token with the SAME json tags so the SDK can
// verify a network-signed token. The SDK only VERIFIES tokens — it NEVER mints
// them (minting is the network's private capability). Parity is proven by the
// platform's SDK parity suite.
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
	// load-bearing for signing-payload parity with the framework's token). Empty only
	// for a non-envelope token (a direct read; the network authorizes none today).
	PayloadHash string    `json:"payloadHash"`
	Expiry      time.Time `json:"expiry"`
	// Involvement marks a token minted for another patient a leg involves
	// (Involvement*): it rides only in Metadata.Involved to the Hub. Empty on
	// a leg's own token, and omitempty, so every other token keeps its signed
	// bytes.
	Involvement string `json:"involvement,omitempty"`
	Signature   []byte `json:"signature"`
}

// AuthorizeRequest is the JSON body for POST {authzURL}/authorize, matching the
// Authorization Framework service's authorize request shape.
type AuthorizeRequest struct {
	Frame         string `json:"frame"`
	Operation     string `json:"operation"`
	SubjectPCI    string `json:"subjectPCI"`
	CorrelationID string `json:"correlationId"`
	// PayloadHash is sha256hex(envelope ciphertext): the participant seals the
	// payload FIRST, then authorizes against that ciphertext so the minted token
	// binds THIS payload (AI-2). Empty only for a non-envelope op (a direct read;
	// the network authorizes none today).
	PayloadHash string `json:"payloadHash,omitempty"`
	// Involvement, when set, asks for a token for another patient this leg
	// involves (an Involvement* value), carried in Metadata.Involved; the token
	// carries it, signed. The decision is recorded with it, so a refused one
	// still appears in that patient's record. Empty for the leg's own token.
	Involvement string `json:"involvement,omitempty"`
}

// authorizeResp is the success body returned by POST {authzURL}/authorize:
// {"token": <Token>} (the platform's sample participant reads the same shape).
type authorizeResp struct {
	Token Token `json:"token"`
}

// tokenSigningPayload is the exact bytes the framework signs: the token JSON
// marshalled with Signature zeroed. Byte-identical to the framework's own signing payload.
func tokenSigningPayload(t Token) []byte {
	c := t
	c.Signature = nil
	b, _ := json.Marshal(c)
	return b
}

// Authorize obtains a per-operation token from the network's Authorization
// Framework. It issues a holder assertion for audience "authz", POSTs req to
// {authzURL}/authorize with the X-Holder-Assertion header, and returns the minted
// Token. Ported from the platform's sample participant.
//
// The assertion audience is FIXED to "authz" and its lifetime to MaxAssertionTTL
// — Authorize does not take these as parameters (unlike Identity.Assertion, which
// does). This is the holder assertion the Authorization Framework expects.
//
// From shn-sdk v1.1.0, Authorize returns an error, and no token, when the
// Authorization Framework answers 2xx with no token, a null token, or a token
// with no signature: such an answer grants no authority. Before, it returned
// the zero token (or the unsigned one) with no error, and a leg sealed with it
// was refused by the Hub (authz token verification failed). A Responder given
// such an answer for its answer leg now answers 502 authorize response leg
// failed and sends no answer; before, it sent the answer with that token and
// the Hub refused it (response leg authorization failed). Authorize checks
// only that a signature is present. To refuse a token that is not signed by the
// Framework or not bound to the leg before sealing it in, verify it with
// VerifyBound against the Framework's key, as a Smart Gateway does.
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
	if len(out.Token.Signature) == 0 {
		return Token{}, fmt.Errorf("shnsdk: /authorize answered %d with no signed token", resp.StatusCode)
	}
	return out.Token, nil
}

// verifyToken checks the signature and expiry against the framework's verifying
// key. Tampering with any field or expiry invalidates the token. Ported from
// the framework's own token verifier (incl. the fail-closed nil/wrong-length key guard:
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

// sdkVerifyBindings asserts the token's frame/operation/correlation/holder/subject
// match the exchange it arrived in (empty want = skip that check). Shared by
// VerifyBound and VerifyBoundNoPayload so the binding set cannot drift between the
// envelope and non-envelope verifiers. Mirrors the framework's own binding checks.
func sdkVerifyBindings(t Token, wantFrame, wantOp, wantCorr, wantHolder, wantSubject string) error {
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
	return nil
}

// VerifyBound checks signature+expiry AND binds the token to exactly this
// operation/frame/correlation/holder/subject. Pass empty string to skip a
// particular check — EXCEPT wantPayloadHash, which is STRICT: every envelope leg
// binds a payload (AI-2), so an empty want or empty token hash is rejected. This
// stops a validly-signed token from being lifted into a DIFFERENT envelope,
// operation, correlation, holder, patient, or PAYLOAD and replayed (H1/AI-2).
// Ported from the framework's bound verifier; the SDK verifies what the network
// signs (the platform's SDK parity suite pins it). Unlike the network's own
// verifier, it refuses a token that carries an Involvement: such a token is
// minted for another patient a leg involves and rides only to the network's
// Hub in Metadata.Involved, so it is never a leg's own token.
func VerifyBound(t Token, authzPub ed25519.PublicKey, now time.Time, wantFrame, wantOp, wantCorr, wantHolder, wantSubject, wantPayloadHash string) error {
	if err := verifyToken(t, authzPub, now); err != nil {
		return err
	}
	if err := refuseInvolvedToken(t); err != nil {
		return err
	}
	if err := sdkVerifyBindings(t, wantFrame, wantOp, wantCorr, wantHolder, wantSubject); err != nil {
		return err
	}
	// PayloadHash is STRICT — every envelope leg binds a payload, so an empty want
	// or an empty token hash is a misuse/unprotected call, not a skip (AI-2). Mirrors
	// the framework's bound verifier.
	if wantPayloadHash == "" || t.PayloadHash == "" || t.PayloadHash != wantPayloadHash {
		return errors.New("shnsdk: payload hash mismatch")
	}
	return nil
}

// VerifyBoundNoPayload is the verifier for a non-envelope operation: a direct
// REST bearer read that rides no sealed envelope and therefore carries NO payload
// hash (the OPA policy forbids one on such an op). The network authorizes no such
// operation today.
// It performs every binding check VerifyBound does EXCEPT the payload-hash check,
// and instead asserts the token carries NO hash — so an envelope-bound token (which
// always carries a hash) can never be replayed onto the bearer read path (AI-2).
// Use VerifyBound (strict payload) for every sealed-envelope leg; use this ONLY for
// a direct read. Ported from the framework's no-payload bound verifier.
func VerifyBoundNoPayload(t Token, pub ed25519.PublicKey, now time.Time, wantFrame, wantOp, wantCorr, wantHolder, wantSubject string) error {
	if t.PayloadHash != "" {
		return errors.New("shnsdk: payload hash present on non-envelope token")
	}
	if err := verifyToken(t, pub, now); err != nil {
		return err
	}
	if err := refuseInvolvedToken(t); err != nil {
		return err
	}
	if err := sdkVerifyBindings(t, wantFrame, wantOp, wantCorr, wantHolder, wantSubject); err != nil {
		return err
	}
	return nil
}

// VerifyInvolved is the verifier for an involved patient's token: one minted
// for another patient a leg involves, which rides in Metadata.Involved and
// never as a leg's own token. It checks signature+expiry and binds the token
// to exactly this leg (frame/operation/correlation/holder/subject; pass empty
// string to skip one, as with VerifyBound) and to the leg's payload (STRICT,
// as VerifyBound: an empty want or an empty token hash is rejected). The
// involvement is STRICT too: wantInvolvement must be non-empty and equal the
// token's, which the signature covers, so a leg's own token (no involvement)
// or one minted for another kind of involvement is rejected. It does not judge
// the involvement's vocabulary: which values a leg may carry is the network's
// rule, not the token's.
func VerifyInvolved(t Token, authzPub ed25519.PublicKey, now time.Time, wantFrame, wantOp, wantCorr, wantHolder, wantSubject, wantPayloadHash, wantInvolvement string) error {
	if err := verifyToken(t, authzPub, now); err != nil {
		return err
	}
	if wantInvolvement == "" || t.Involvement != wantInvolvement {
		return errors.New("shnsdk: involvement mismatch")
	}
	if err := sdkVerifyBindings(t, wantFrame, wantOp, wantCorr, wantHolder, wantSubject); err != nil {
		return err
	}
	if wantPayloadHash == "" || t.PayloadHash == "" || t.PayloadHash != wantPayloadHash {
		return errors.New("shnsdk: payload hash mismatch")
	}
	return nil
}

// refuseInvolvedToken refuses a token minted for another patient a leg
// involves (Involvement set): it rides only to the network's Hub, never as the
// token of a leg a participant receives.
func refuseInvolvedToken(t Token) error {
	if t.Involvement != "" {
		return errors.New("shnsdk: token minted for an involved patient, not for a leg")
	}
	return nil
}
