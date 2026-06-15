package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Assertion is a holder's signed identity claim toward an audience (transport
// identity, FR-3, distinct from per-operation authority — AI-11). Single-sourced
// in the SDK: the json tags
// (holderId/audience/issuedAt/expiry/jti/sig), the signing payload, and the
// verifier-enforced bounds live in the SDK so it is wire-identical WITHOUT
// importing internal/. The cross-module parity tests
// (test/sdkparity/assertion_parity_test.go,
// test/sdkparity/holderauth_verify_parity_test.go) prove a substrate verifier
// accepts an SDK-issued assertion and that VerifyAssertion matches
// internal/holderauth.Verify.
type Assertion struct {
	HolderID string    `json:"holderId"`
	Audience string    `json:"audience"`
	IssuedAt time.Time `json:"issuedAt"`
	Expiry   time.Time `json:"expiry"`
	// JTI is a unique per-assertion id (SMART private_key_jwt style); consuming
	// verifiers enforce one-time-use on it, so it is stamped BEFORE signing (covered
	// by the signature). A captured assertion cannot be replayed with a swapped jti.
	JTI string `json:"jti"`
	Sig []byte `json:"sig"`
}

// MaxAssertionTTL and MaxClockSkew are verifier-enforced bounds on a holder
// assertion. Holders self-sign their assertions, so the verifier must not trust
// an arbitrary lifetime or issuance time: it caps the assertion lifetime
// (Expiry-IssuedAt) at MaxAssertionTTL and rejects assertions issued more than
// MaxClockSkew in the future. PRODUCTION PREREQUISITE: holders and verifiers
// share a disciplined time source (NTP); the skew window only absorbs small
// drift, not an unsynchronized clock. Mirrors internal/holderauth.
const (
	MaxAssertionTTL = time.Hour
	MaxClockSkew    = 5 * time.Minute
)

// assertionSigningPayload is the exact bytes signed: the struct JSON-marshalled
// with Sig zeroed. Byte-identical to internal/holderauth.signingPayload.
func assertionSigningPayload(a Assertion) []byte {
	c := a
	c.Sig = nil
	b, _ := json.Marshal(c)
	return b
}

// IssueAssertion creates a signed assertion valid for ttl from issuedAt. Ported
// verbatim from internal/holderauth.Issue: a fresh random jti is stamped BEFORE
// signing so it is covered by the signature (a replay guard at the verifier
// cannot be defeated by swapping in a fresh jti). Returns the raw Assertion
// struct; most holders should use Identity.Assertion, which wraps this and
// returns the ready-to-send base64 X-Holder-Assertion header value.
func IssueAssertion(holderID, audience string, priv ed25519.PrivateKey, issuedAt time.Time, ttl time.Duration) Assertion {
	a := Assertion{
		HolderID: holderID,
		Audience: audience,
		IssuedAt: issuedAt,
		Expiry:   issuedAt.Add(ttl),
	}
	var jb [16]byte
	if _, err := rand.Read(jb[:]); err != nil {
		// A crypto entropy failure is unrecoverable; failing loud is safer than
		// emitting a zero (fixed) jti that would silently collide across assertions.
		panic("shnsdk: crypto/rand failed: " + err.Error())
	}
	a.JTI = base64.RawURLEncoding.EncodeToString(jb[:])
	a.Sig = ed25519.Sign(priv, assertionSigningPayload(a))
	return a
}

// VerifyAssertion checks the signature, audience, and expiry against the holder's
// public key. Ported verbatim from internal/holderauth.Verify (error strings
// carry the shnsdk: public-surface prefix).
func VerifyAssertion(a Assertion, expectedAudience string, pub ed25519.PublicKey, now time.Time) error {
	if len(a.Sig) == 0 || !ed25519.Verify(pub, assertionSigningPayload(a), a.Sig) {
		return errors.New("shnsdk: invalid signature")
	}
	if a.Audience != expectedAudience {
		return errors.New("shnsdk: wrong audience")
	}
	if now.After(a.Expiry) {
		return errors.New("shnsdk: assertion expired")
	}
	// Verifier-enforced bounds (F2): holders self-sign, so cap the lifetime and
	// reject future-dated issuance beyond the allowed skew.
	if a.IssuedAt.After(now.Add(MaxClockSkew)) {
		return errors.New("shnsdk: assertion issued in the future")
	}
	if !a.Expiry.After(a.IssuedAt) {
		return errors.New("shnsdk: expiry not after issuance")
	}
	if a.Expiry.Sub(a.IssuedAt) > MaxAssertionTTL {
		return errors.New("shnsdk: assertion lifetime exceeds maximum")
	}
	// A jti is required so consuming verifiers can enforce one-time-use (SMART
	// private_key_jwt). It is covered by the signature, so absence is a real defect.
	if a.JTI == "" {
		return errors.New("shnsdk: missing jti")
	}
	return nil
}

// Assertion issues a signed holder assertion for audience and returns the
// X-Holder-Assertion header value: base64.StdEncoding(json(assertion)). The ttl
// is clamped to MaxAssertionTTL so the substrate verifier (which caps lifetime)
// always accepts it. Issues via the shared IssueAssertion path (a fresh random
// jti is stamped before signing). Returns the ready-to-send base64
// X-Holder-Assertion header; for the raw Assertion struct (to embed elsewhere)
// use IssueAssertion.
//
// The substrate verifier ALSO enforces a future-issuance clock-skew bound (~5m)
// and Expiry > IssuedAt, so passing a far-future now mints an assertion the
// substrate will reject — pass a now close to real wall-clock time.
func (id Identity) Assertion(audience string, now time.Time, ttl time.Duration) (string, error) {
	if len(id.SignPriv) != ed25519.PrivateKeySize {
		return "", errors.New("shnsdk: identity has no valid signing key")
	}
	if ttl > MaxAssertionTTL {
		ttl = MaxAssertionTTL
	}
	a := IssueAssertion(id.HolderID, audience, id.SignPriv, now, ttl)
	b, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("shnsdk: marshal assertion: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
