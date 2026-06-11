package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func newTestIdentity(t *testing.T) Identity {
	t.Helper()
	id, err := GenerateIdentity("holder-test")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id
}

// decodeAssertion base64-decodes the header value back to the wire struct.
func decodeAssertion(t *testing.T, hdr string) assertion {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(hdr)
	if err != nil {
		t.Fatalf("base64 decode header: %v", err)
	}
	var a assertion
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("unmarshal assertion: %v", err)
	}
	return a
}

func TestAssertionRoundTripAndSignature(t *testing.T) {
	id := newTestIdentity(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	hdr, err := id.Assertion("hub", now, 30*time.Minute)
	if err != nil {
		t.Fatalf("Assertion: %v", err)
	}
	a := decodeAssertion(t, hdr)

	if a.HolderID != "holder-test" || a.Audience != "hub" {
		t.Errorf("fields: holderID=%q audience=%q", a.HolderID, a.Audience)
	}
	if !a.IssuedAt.Equal(now) || !a.Expiry.Equal(now.Add(30*time.Minute)) {
		t.Errorf("times: issuedAt=%v expiry=%v", a.IssuedAt, a.Expiry)
	}
	if a.JTI == "" {
		t.Error("jti must be stamped")
	}
	if !ed25519.Verify(id.SignPub, assertionSigningPayload(a), a.Sig) {
		t.Error("signature does not verify against signing payload")
	}
}

func TestAssertionClampsTTL(t *testing.T) {
	id := newTestIdentity(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	hdr, err := id.Assertion("hub", now, 24*time.Hour)
	if err != nil {
		t.Fatalf("Assertion: %v", err)
	}
	a := decodeAssertion(t, hdr)
	if got := a.Expiry.Sub(a.IssuedAt); got != MaxAssertionTTL {
		t.Errorf("ttl not clamped: lifetime=%v want=%v", got, MaxAssertionTTL)
	}
}

func TestAssertionUniqueJTI(t *testing.T) {
	id := newTestIdentity(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	a1 := decodeAssertion(t, mustAssert(t, id, now))
	a2 := decodeAssertion(t, mustAssert(t, id, now))
	if a1.JTI == a2.JTI {
		t.Error("jti must be unique per assertion")
	}
}

func mustAssert(t *testing.T, id Identity, now time.Time) string {
	t.Helper()
	h, err := id.Assertion("hub", now, time.Hour)
	if err != nil {
		t.Fatalf("Assertion: %v", err)
	}
	return h
}

func TestAssertionNoSigningKey(t *testing.T) {
	id := Identity{HolderID: "x"}
	if _, err := id.Assertion("hub", time.Now(), time.Hour); err == nil {
		t.Error("expected error for identity without signing key")
	}
}
