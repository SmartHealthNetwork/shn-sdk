// Package shnsdk is the public SHN participant SDK. It REIMPLEMENTS the
// participant protocol standalone (stdlib + golang.org/x/crypto only) and must
// never import the platform's internal packages — that is the public/private
// IP boundary. Correctness is proven by the cross-module parity tests in the
// platform's own repository.
package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// memberID|birthDate|familyName)). Kept in lockstep with the network's own PCI derivation.
const pciVersion = "v1-demo-sha256"

// ResolvePCI derives the network patient identifier (PCI position) from member
// demographics. Derivation (pciVersion v1-demo-sha256): sha256 over the lowercased
// "memberID|birthDate|familyName" key, first 16 bytes hex-encoded, with a "pci:"
// prefix. This MUST match the network's own PCI derivation byte-for-byte — the
// platform's SDK parity test proves it.
//
// CAVEAT: this deterministic hash is the DEMO approach only. The goal-state PCI is
// TRUST-ISSUED (AI-5) — minted by the Trust, NOT externally re-derivable. External
// participants MUST NOT depend on re-deriving the PCI from demographics; treat it
// as an opaque, Trust-assigned identifier. pciVersion marks this scheme so a future
// Trust-issued scheme is a clean, detectable swap.
//
// A blank member id (empty, only whitespace, or "Patient/" with nothing but
// whitespace after it) names no one, so no identifier is
// derived from it: ResolvePCI returns "", which is no PCI, where earlier releases
// derived one from it as from any other member id. "" names no patient: a
// system-of-record connector refuses such a member as a failed read, never as a
// member it does not hold and never as a held member with identifier "". A caller
// that must not proceed without an identifier uses ResolvePCIStrict, which refuses
// a blank member id with an error.
func ResolvePCI(memberID, birthDate, familyName string) string {
	pci, _ := ResolvePCIStrict(memberID, birthDate, familyName)
	return pci
}

// ErrBlankMember is the refusal of a blank member (empty, only whitespace, or a bare
// "Patient/"): ResolvePCIStrict returns it, and every reader's refusal of a request
// whose member reads as blank wraps it (errors.Is). A blank member names no one, so
// no patient identifier is derived from it.
var ErrBlankMember = errors.New("shnsdk: a blank member id names no patient")

// ResolvePCIStrict is ResolvePCI, refusing a blank member id (empty, only
// whitespace, or "Patient/" with nothing but whitespace after it) with ErrBlankMember and returning "". Any other member id derives
// exactly the identifier ResolvePCI derives.
func ResolvePCIStrict(memberID, birthDate, familyName string) (string, error) {
	if memberNamesNoOne(memberID) {
		return "", ErrBlankMember
	}
	_ = pciVersion // documents the scheme version; see the doc comment + caveat above
	key := strings.ToLower(memberID + "|" + birthDate + "|" + familyName)
	sum := sha256.Sum256([]byte(key))
	return "pci:" + hex.EncodeToString(sum[:16]), nil
}
