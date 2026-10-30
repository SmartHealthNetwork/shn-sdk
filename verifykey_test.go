package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// badKeys are verifying keys that are not Ed25519 public keys: none, short and
// long. ed25519.Verify panics on each.
var badKeys = map[string]ed25519.PublicKey{
	"nil":   nil,
	"empty": {},
	"short": make(ed25519.PublicKey, ed25519.PublicKeySize-1),
	"long":  make(ed25519.PublicKey, ed25519.PublicKeySize+1),
}

// Every verifier that takes a raw key refuses one that is not an Ed25519
// public key with an error, never a panic: a registered holder with no
// signing key, or a transport key that did not decode, is refused like a
// signature that does not verify.
func TestVerifiers_AKeyOfTheWrongLengthIsAnErrorNotAPanic(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	assertion := IssueAssertion("acme", "hub", priv, now, time.Hour)
	hubHeader := makeHubAssertion(t, priv, "hub", "acme", now, time.Hour, "jti-1")
	tok := Token{Operation: "op", Frame: "provider-tpo", CorrelationID: "c", Holder: "acme", Expiry: now.Add(time.Hour), Signature: []byte{1}}
	for name, key := range badKeys {
		t.Run(name, func(t *testing.T) {
			if err := VerifyAssertion(assertion, "hub", key, now); err == nil {
				t.Error("VerifyAssertion accepted a key of the wrong length")
			}
			if _, err := parseAndVerifyHubAssertion(hubHeader, "acme", key, now); err == nil {
				t.Error("the X-Hub-Assertion check accepted a key of the wrong length")
			}
			if err := VerifyBound(tok, key, now, "provider-tpo", "op", "c", "acme", "", ""); err == nil {
				t.Error("VerifyBound accepted a key of the wrong length")
			}
		})
	}
}
