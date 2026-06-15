package shnsdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

// Ported from internal/holderauth's unit + fuzz suite: IssueAssertion and
// VerifyAssertion are single-sourced here, so their behavioral coverage lives
// SDK-side. The shim internal/holderauth.Issue/Verify forward to these.

func auKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

var auIssued = time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

func TestIssueVerify_OK(t *testing.T) {
	pub, priv := auKeys(t)
	a := IssueAssertion("provider", "hub", priv, auIssued, time.Hour)
	now := auIssued.Add(30 * time.Minute)
	if err := VerifyAssertion(a, "hub", pub, now); err != nil {
		t.Fatalf("valid assertion should verify: %v", err)
	}
}

func TestVerify_RejectsTamperedHolder(t *testing.T) {
	pub, priv := auKeys(t)
	a := IssueAssertion("provider", "hub", priv, auIssued, time.Hour)
	a.HolderID = "payer" // tamper
	now := auIssued.Add(30 * time.Minute)
	if err := VerifyAssertion(a, "hub", pub, now); err == nil {
		t.Fatal("tampered holder id must be rejected")
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	pub, priv := auKeys(t)
	a := IssueAssertion("provider", "hub", priv, auIssued, time.Hour)
	now := auIssued.Add(2 * time.Hour) // past expiry
	if err := VerifyAssertion(a, "hub", pub, now); err == nil {
		t.Fatal("expired assertion must be rejected")
	}
}

func TestVerify_RejectsWrongAudience(t *testing.T) {
	pub, priv := auKeys(t)
	a := IssueAssertion("provider", "hub", priv, auIssued, time.Hour)
	now := auIssued.Add(30 * time.Minute)
	if err := VerifyAssertion(a, "audit", pub, now); err == nil {
		t.Fatal("assertion for a different audience must be rejected")
	}
}

// TestVerify_RejectsFutureIssuedAt asserts F2: an assertion whose IssuedAt is
// beyond MaxClockSkew in the future (relative to the verifier's now) is rejected,
// even though it is otherwise validly signed and unexpired.
func TestVerify_RejectsFutureIssuedAt(t *testing.T) {
	pub, priv := auKeys(t)
	// IssuedAt = now+10m (> 5m skew); Expiry well in the future, ttl within max.
	future := auIssued.Add(10 * time.Minute)
	a := IssueAssertion("provider", "hub", priv, future, 30*time.Minute)
	if err := VerifyAssertion(a, "hub", pub, auIssued); err == nil {
		t.Fatal("assertion issued in the future must be rejected")
	}
}

// TestVerify_RejectsOverlongTTL asserts F2: an assertion whose lifetime
// (Expiry-IssuedAt) exceeds MaxAssertionTTL is rejected, capping how long a
// self-signed holder assertion can live.
func TestVerify_RejectsOverlongTTL(t *testing.T) {
	pub, priv := auKeys(t)
	a := IssueAssertion("provider", "hub", priv, auIssued, 2*time.Hour) // > MaxAssertionTTL
	now := auIssued.Add(30 * time.Minute)
	if err := VerifyAssertion(a, "hub", pub, now); err == nil {
		t.Fatal("assertion with lifetime > MaxAssertionTTL must be rejected")
	}
}

// TestVerify_AcceptsMaxTTL asserts the boundary: a ttl of exactly MaxAssertionTTL
// (1h) is NOT > the max and must verify (this is what the live gateways issue).
func TestVerify_AcceptsMaxTTL(t *testing.T) {
	pub, priv := auKeys(t)
	a := IssueAssertion("provider", "hub", priv, auIssued, MaxAssertionTTL) // exactly 1h
	now := auIssued.Add(30 * time.Minute)
	if err := VerifyAssertion(a, "hub", pub, now); err != nil {
		t.Fatalf("assertion with ttl == MaxAssertionTTL must verify: %v", err)
	}
}

// TestVerify_AcceptsWithinSkewIssuedAt asserts a normal 30m-ttl assertion issued
// slightly ahead (within MaxClockSkew) of the verifier still verifies.
func TestVerify_AcceptsWithinSkewIssuedAt(t *testing.T) {
	pub, priv := auKeys(t)
	// IssuedAt 2m ahead of verifier now (< 5m skew), 30m ttl.
	ahead := auIssued.Add(2 * time.Minute)
	a := IssueAssertion("provider", "hub", priv, ahead, 30*time.Minute)
	if err := VerifyAssertion(a, "hub", pub, auIssued); err != nil {
		t.Fatalf("assertion issued within skew must verify: %v", err)
	}
}

// TestVerify_RejectsBackwardsExpiry (T2): an assertion whose Expiry is not after
// its IssuedAt is malformed and must be rejected, even if it happens to fall
// inside the future-issuance and lifetime bounds.
func TestVerify_RejectsBackwardsExpiry(t *testing.T) {
	pub, priv := auKeys(t)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	// IssuedAt slightly in the future, Expiry BEFORE IssuedAt — passes the expiry
	// (not-yet-past) and skew/lifetime checks but is logically backwards.
	a := Assertion{
		HolderID: "provider",
		Audience: "hub",
		IssuedAt: now.Add(2 * time.Minute),
		Expiry:   now.Add(1 * time.Minute),
		JTI:      "fixed-jti", // present so ONLY the backwards-expiry defect is under test
	}
	a.Sig = ed25519.Sign(priv, assertionSigningPayload(a))

	if err := VerifyAssertion(a, "hub", pub, now); err == nil {
		t.Fatal("VerifyAssertion accepted an assertion with Expiry <= IssuedAt")
	}
}

// TestIssue_GeneratesUniqueJTI verifies IssueAssertion stamps a non-empty, unique jti.
func TestIssue_GeneratesUniqueJTI(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	a1 := IssueAssertion("h", "authz", priv, now, time.Hour)
	a2 := IssueAssertion("h", "authz", priv, now, time.Hour)
	if a1.JTI == "" || a2.JTI == "" {
		t.Fatal("IssueAssertion did not stamp a jti")
	}
	if a1.JTI == a2.JTI {
		t.Fatal("IssueAssertion produced duplicate jti")
	}
}

// TestVerify_RejectsMissingJTI verifies an assertion with no jti is rejected.
func TestVerify_RejectsMissingJTI(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	a := IssueAssertion("h", "authz", priv, now, time.Hour)
	a.JTI = ""                                             // strip it
	a.Sig = ed25519.Sign(priv, assertionSigningPayload(a)) // re-sign so ONLY jti-absence fails
	if err := VerifyAssertion(a, "authz", pub, now); err == nil {
		t.Fatal("VerifyAssertion accepted a jti-less assertion")
	}
}

// FuzzAssertionVerify is a Class-A seed-corpus fuzz target over VerifyAssertion:
// no panic on arbitrary bytes, AND if a parsed assertion is ACCEPTED, acceptance
// is exactly as wide as a genuine signature — perturbing ANY signed field
// (HolderID, Audience, IssuedAt, Expiry, JTI) or flipping ANY bit of Sig must
// reject. The seed is signed with a test key for audience "authz" and verified
// against that SAME audience so the accept branch runs. Catches a bound field
// missing from assertionSigningPayload.
func FuzzAssertionVerify(f *testing.F) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	const aud = "authz"
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	seed := IssueAssertion("holder-a", aud, priv, now.Add(-time.Minute), 30*time.Minute)
	if b, err := json.Marshal(seed); err == nil {
		f.Add(b)
	}
	f.Add([]byte("{}"))
	f.Add([]byte("not json"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var a Assertion
		if err := json.Unmarshal(data, &a); err != nil {
			return // malformed — no-panic is the only requirement
		}
		if VerifyAssertion(a, aud, pub, now) != nil {
			return // not accepted
		}
		// ACCEPTED: each signed-field mutation must reject. The Audience and time
		// mutations are kept inside the verifier's non-signature gates (audience
		// still equals `aud` only for the unmutated value; times stay within the
		// TTL/skew window) so the ONLY thing that can reject is the signature.
		mutators := []func(*Assertion){
			func(m *Assertion) { m.HolderID += "X" },
			func(m *Assertion) { m.Audience += "X" }, // also fails the audience gate, still a reject
			func(m *Assertion) { m.IssuedAt = m.IssuedAt.Add(time.Second) },
			func(m *Assertion) { m.Expiry = m.Expiry.Add(time.Second) },
			func(m *Assertion) { m.JTI += "X" },
		}
		for i, mut := range mutators {
			m := a
			mut(&m)
			if VerifyAssertion(m, aud, pub, now) == nil {
				t.Fatalf("mutated signed field %d still verified — field not bound by the signature", i)
			}
		}
		for i := 0; i < len(a.Sig)*8; i++ {
			m := a
			sig := append([]byte(nil), a.Sig...)
			sig[i/8] ^= 1 << (i % 8)
			m.Sig = sig
			if VerifyAssertion(m, aud, pub, now) == nil {
				t.Fatalf("flipped signature bit %d still verified", i)
			}
		}
	})
}
