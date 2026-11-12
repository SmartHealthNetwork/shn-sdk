package shnsdk

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// ErrLowOrderKey is the refusal of an X25519 public key of small order: the all-zero
// key, or another point of small order, in any encoding. Such a key cannot
// authenticate its holder: anything sealed to it derives a predictable shared secret,
// so anyone can open it. Seal refuses one, or a nil key, with it (errors.Is); from
// shn-gateway v1.9.0, the network's deploy, the registrar refuses to register one.
var ErrLowOrderKey = errors.New("shnsdk: X25519 public key of small order (all zero, or a point of small order): " +
	"anything sealed to it can be opened by anyone, so it cannot authenticate its holder")

// lowOrderScalar is a fixed, valid X25519 scalar. Clamping makes every scalar a
// multiple of the cofactor, so its product with a point of small order is the
// identity, which curve25519.X25519 refuses as an all-zero output.
var lowOrderScalar = [32]byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

// lowOrderKey reports whether pub is an X25519 public key of small order, or nil.
func lowOrderKey(pub *[32]byte) bool {
	if pub == nil {
		return true
	}
	_, err := curve25519.X25519(lowOrderScalar[:], pub[:])
	return err != nil
}

// KeyFingerprint is a public key's display fingerprint: the first 8 hex characters of
// its SHA-256, the shape the registrar's directory and the Accounts service show as
// encPubFp. It identifies a key without printing it.
func KeyFingerprint(raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf("%x", h[:4])
}

// EncKeyVerdict is the verdict of Bundle.EncKeyCheck.
type EncKeyVerdict string

// The verdicts of Bundle.EncKeyCheck.
const (
	// EncKeyOK: the manifest's encPub is the public key of the bundle's private key.
	EncKeyOK EncKeyVerdict = "ok"
	// EncKeyManifestMissing: manifest.json carries no encPub, or an empty one.
	EncKeyManifestMissing EncKeyVerdict = "manifest-missing"
	// EncKeyManifestMalformed: manifest.json's encPub is not 32 bytes of std-base64.
	EncKeyManifestMalformed EncKeyVerdict = "manifest-malformed"
	// EncKeyManifestLowOrder: manifest.json's encPub is a key of small order (ErrLowOrderKey).
	EncKeyManifestLowOrder EncKeyVerdict = "manifest-low-order"
	// EncKeyManifestDiffers: manifest.json's encPub is another key than the private key's.
	EncKeyManifestDiffers EncKeyVerdict = "manifest-differs-from-private-key"
)

// EncKeyReport is what Bundle.EncKeyCheck finds of a bundle's encryption key: a verdict
// and the public fingerprints (KeyFingerprint) of the key the private key derives and
// of the manifest's, never a key byte.
type EncKeyReport struct {
	// Verdict is one of the EncKey* constants.
	Verdict EncKeyVerdict
	// Derived is the fingerprint of the public key the bundle's private key derives,
	// which Identity.EncPub holds.
	Derived string
	// Manifest is the fingerprint of manifest.json's encPub, or "" when it is missing
	// or malformed.
	Manifest string
}

// EncKeyCheck compares the encPub manifest.json states with the public key the
// bundle's private key derives (Identity.EncPub). The verdict names how they compare,
// and the fingerprints show both without printing either key.
func (b Bundle) EncKeyCheck() EncKeyReport {
	c := EncKeyReport{}
	if b.Identity.EncPub != nil {
		c.Derived = KeyFingerprint(b.Identity.EncPub[:])
	}
	if b.Manifest.EncPub == "" {
		c.Verdict = EncKeyManifestMissing
		return c
	}
	raw, err := base64.StdEncoding.DecodeString(b.Manifest.EncPub)
	if err != nil || len(raw) != 32 {
		c.Verdict = EncKeyManifestMalformed
		return c
	}
	c.Manifest = KeyFingerprint(raw)
	var manifest [32]byte
	copy(manifest[:], raw)
	switch {
	case lowOrderKey(&manifest):
		c.Verdict = EncKeyManifestLowOrder
	case b.Identity.EncPub == nil || manifest != *b.Identity.EncPub:
		c.Verdict = EncKeyManifestDiffers
	default:
		c.Verdict = EncKeyOK
	}
	return c
}
