// Package shnsdk is the public SHN participant SDK. It REIMPLEMENTS the
// participant protocol standalone (stdlib + golang.org/x/crypto only) and must
// never import the substrate's internal/ packages — that is the public/private
// IP boundary. Correctness is proven by the cross-module parity tests in the
// substrate module under test/sdkparity/.
package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// Identity is a participant's substrate identity: an ed25519 signing key pair
// (for holder authentication / signatures) and an X25519 key pair (for envelope
// encryption, matching the substrate's nacl/box scheme).
type Identity struct {
	HolderID string
	SignPub  ed25519.PublicKey
	SignPriv ed25519.PrivateKey
	EncPub   *[32]byte
	EncPriv  *[32]byte

	// Clock returns the current time. Nil defaults to time.Now. The substrate
	// verifier enforces a ~5m clock-skew bound on holder assertions and token
	// expiry, so a participant talking to a fixed-clock substrate (tests) MUST
	// inject a matching clock or every assertion is rejected as future/expired.
	// Real participants leave this nil (NTP-synced wall clock).
	Clock func() time.Time
}

// now returns the current time via the injected Clock or time.Now.
func (id Identity) now() time.Time {
	if id.Clock != nil {
		return id.Clock()
	}
	return time.Now()
}

// GenerateIdentity creates fresh signing and encryption key pairs for holderID.
// Keys are always generated, never literal/hard-coded (AI-5). The X25519 pair is
// produced via nacl/box.GenerateKey to match the substrate's envelope scheme.
func GenerateIdentity(holderID string) (Identity, error) {
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	encPub, encPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		HolderID: holderID,
		SignPub:  signPub,
		SignPriv: signPriv,
		EncPub:   encPub,
		EncPriv:  encPriv,
	}, nil
}

// pciVersion versions the PCI derivation scheme so consumers can detect a change.
// v1-demo-sha256 = "pci:" + hex(first 16 bytes of sha256(lowercase
// memberID|birthDate|familyName)). Kept in lockstep with internal/identity.
const pciVersion = "v1-demo-sha256"

// ResolvePCI derives the substrate patient identifier (PCI position) from member
// demographics. Derivation (pciVersion v1-demo-sha256): sha256 over the lowercased
// "memberID|birthDate|familyName" key, first 16 bytes hex-encoded, with a "pci:"
// prefix. This MUST match internal/identity.ResolvePCI byte-for-byte — the
// test/sdkparity parity test proves it.
//
// CAVEAT: this deterministic hash is the DEMO approach only. The goal-state PCI is
// TRUST-ISSUED (AI-5) — minted by the Trust, NOT externally re-derivable. External
// participants MUST NOT depend on re-deriving the PCI from demographics; treat it
// as an opaque, Trust-assigned identifier. pciVersion marks this scheme so a future
// Trust-issued scheme is a clean, detectable swap.
func ResolvePCI(memberID, birthDate, familyName string) string {
	_ = pciVersion // documents the scheme version; see the doc comment + caveat above
	key := strings.ToLower(memberID + "|" + birthDate + "|" + familyName)
	sum := sha256.Sum256([]byte(key))
	return "pci:" + hex.EncodeToString(sum[:16])
}
