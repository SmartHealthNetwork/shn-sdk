package shnsdk_test

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

func TestGenerateIdentity(t *testing.T) {
	id, err := shnsdk.GenerateIdentity("holder-x")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if id.HolderID != "holder-x" {
		t.Errorf("HolderID = %q, want holder-x", id.HolderID)
	}

	// ed25519 key pair is valid: a signature verifies under the public key.
	if len(id.SignPub) != ed25519.PublicKeySize {
		t.Errorf("SignPub size = %d, want %d", len(id.SignPub), ed25519.PublicKeySize)
	}
	if len(id.SignPriv) != ed25519.PrivateKeySize {
		t.Errorf("SignPriv size = %d, want %d", len(id.SignPriv), ed25519.PrivateKeySize)
	}
	msg := []byte("parity")
	sig := ed25519.Sign(id.SignPriv, msg)
	if !ed25519.Verify(id.SignPub, msg, sig) {
		t.Error("ed25519 keypair does not verify its own signature")
	}

	// X25519 enc keys are present, 32 bytes, and pub != priv.
	if id.EncPub == nil || id.EncPriv == nil {
		t.Fatal("enc keys are nil")
	}
	if len(id.EncPub) != 32 || len(id.EncPriv) != 32 {
		t.Errorf("enc key sizes = %d/%d, want 32/32", len(id.EncPub), len(id.EncPriv))
	}
	if bytes.Equal(id.EncPub[:], id.EncPriv[:]) {
		t.Error("enc pub equals enc priv")
	}
}

func TestGenerateIdentityDistinct(t *testing.T) {
	a, err := shnsdk.GenerateIdentity("h")
	if err != nil {
		t.Fatal(err)
	}
	b, err := shnsdk.GenerateIdentity("h")
	if err != nil {
		t.Fatal(err)
	}
	if a.EncPub != nil && b.EncPub != nil && bytes.Equal(a.EncPub[:], b.EncPub[:]) {
		t.Error("two fresh identities share an enc public key (AI-5: keys must be generated)")
	}
	if bytes.Equal(a.SignPub, b.SignPub) {
		t.Error("two fresh identities share a signing public key")
	}
}

func TestResolvePCIDeterministicAndPrefixed(t *testing.T) {
	got1 := shnsdk.ResolvePCI("MBR-COVERED", "1975-04-02", "Johansson")
	got2 := shnsdk.ResolvePCI("MBR-COVERED", "1975-04-02", "Johansson")
	if got1 != got2 {
		t.Errorf("ResolvePCI not deterministic: %q != %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "pci:") {
		t.Errorf("ResolvePCI = %q, want pci: prefix", got1)
	}
	if got1 == shnsdk.ResolvePCI("MBR-OTHER", "1975-04-02", "Johansson") {
		t.Error("ResolvePCI collides for different member IDs")
	}
}
