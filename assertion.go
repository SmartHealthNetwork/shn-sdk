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

// assertion is a holder's signed identity claim toward an audience (transport
// identity, FR-3). PORTED standalone from internal/holderauth.Assertion: the json
// tags (holderId/audience/issuedAt/expiry/jti/sig), the signing payload, and the
// verifier-enforced bounds are reproduced here so the SDK is wire-identical
// WITHOUT importing internal/. The cross-module parity test
// (test/sdkparity/assertion_parity_test.go) proves a substrate verifier accepts
// an SDK-issued assertion.
type assertion struct {
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

// MaxAssertionTTL is the verifier-enforced cap on an assertion's lifetime
// (Expiry-IssuedAt), matching internal/holderauth.MaxAssertionTTL. Holders
// self-sign, so a longer ttl would be rejected by the substrate; Assertion clamps
// to this so an SDK-issued assertion always verifies.
const MaxAssertionTTL = time.Hour

// assertionSigningPayload is the exact bytes signed: the struct JSON-marshalled
// with Sig zeroed. Byte-identical to internal/holderauth.signingPayload.
func assertionSigningPayload(a assertion) []byte {
	c := a
	c.Sig = nil
	b, _ := json.Marshal(c)
	return b
}

// Assertion issues a signed holder assertion for audience and returns the
// X-Holder-Assertion header value: base64.StdEncoding(json(assertion)). The ttl
// is clamped to MaxAssertionTTL so the substrate verifier (which caps lifetime)
// always accepts it. A fresh random jti is stamped before signing.
//
// Ported from internal/holderauth.Issue + the sampleparticipant header encoding
// (base64.StdEncoding.EncodeToString(json(assertion))).
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
	a := assertion{
		HolderID: id.HolderID,
		Audience: audience,
		IssuedAt: now,
		Expiry:   now.Add(ttl),
	}
	var jb [16]byte
	if _, err := rand.Read(jb[:]); err != nil {
		// A crypto entropy failure is unrecoverable; a fixed (zero) jti would silently
		// collide across assertions, so fail loud rather than emit one.
		return "", fmt.Errorf("shnsdk: crypto/rand failed: %w", err)
	}
	a.JTI = base64.RawURLEncoding.EncodeToString(jb[:])
	a.Sig = ed25519.Sign(id.SignPriv, assertionSigningPayload(a))

	b, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("shnsdk: marshal assertion: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
