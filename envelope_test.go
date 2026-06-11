package shnsdk

import (
	"bytes"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestSealOpenRoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	meta := Metadata{Sender: "a", Recipient: "b", TransactionType: "coverage-eligibility", CorrelationID: "c1"}
	payload := []byte(`{"resourceType":"CoverageEligibilityRequest"}`)

	env, err := Seal(meta, payload, pub)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if env.Metadata != meta {
		t.Error("metadata not preserved through Seal")
	}
	got, err := Open(env, pub, priv)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %q want %q", got, payload)
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	pub, _, _ := box.GenerateKey(rand.Reader)
	_, wrongPriv, _ := box.GenerateKey(rand.Reader)
	env, err := Seal(Metadata{Sender: "a"}, []byte("secret"), pub)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(env, pub, wrongPriv); err == nil {
		t.Error("expected Open to fail with wrong private key")
	}
}

func TestEnvelopeWireRoundTrip(t *testing.T) {
	pub, priv, _ := box.GenerateKey(rand.Reader)
	env, err := Seal(Metadata{Sender: "a", Recipient: "b", AuthzToken: "{}"}, []byte("hello"), pub)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	dec, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	got, err := Open(dec, pub, priv)
	if err != nil {
		t.Fatalf("Open after wire round-trip: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("payload mismatch after wire round-trip: %q", got)
	}
}
