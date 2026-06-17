package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
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
func decodeAssertion(t *testing.T, hdr string) Assertion {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(hdr)
	if err != nil {
		t.Fatalf("base64 decode header: %v", err)
	}
	var a Assertion
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

func TestIssueAssertionForBody_SignedAndVerifies(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"pci":"P1","purpose":"TREAT"}`)
	a := IssueAssertionForBody("authz", "consent", priv, now, time.Hour, body)

	if a.BodyHash == "" {
		t.Fatal("BodyHash not stamped")
	}
	sum := sha256.Sum256(body)
	if a.BodyHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("BodyHash = %q, want sha256 hex of body", a.BodyHash)
	}
	if err := VerifyAssertion(a, "consent", pub, now); err != nil {
		t.Fatalf("verify body-bound assertion: %v", err)
	}
	a.BodyHash = "deadbeef"
	if err := VerifyAssertion(a, "consent", pub, now); err == nil {
		t.Fatal("expected sig failure after mutating BodyHash")
	}
}

func TestIssueAssertion_EmptyBodyHashIsByteIdentical(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_700_000_000, 0)
	a := IssueAssertion("h", "aud", priv, now, time.Hour)
	if a.BodyHash != "" {
		t.Fatalf("unbound assertion stamped a BodyHash %q", a.BodyHash)
	}
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), `"bh"`) {
		t.Fatalf("empty BodyHash leaked into JSON: %s", b)
	}
}
