package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// TestRegistration_PoP verifies Registration builds a body whose pop is a valid
// ed25519 signature, by the registered signPub, over the canonical newline-joined
// statement (id\nrole\nencPub\nsignPub\nbaseURL). This is a pure byte-level check;
// the behavioral parity gate against the real registrar lives in
// test/sdkparity/registration_parity_test.go.
func TestRegistration_PoP(t *testing.T) {
	id, err := GenerateIdentity("ext-provider")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	const role, baseURL = "provider", "https://ext-provider.example.com"

	req := id.Registration(role, baseURL)

	if req.ID != "ext-provider" || req.Role != role || req.BaseURL != baseURL {
		t.Errorf("field mismatch: id=%q role=%q baseURL=%q", req.ID, req.Role, req.BaseURL)
	}
	if got := base64.StdEncoding.EncodeToString(id.EncPub[:]); req.EncPub != got {
		t.Errorf("encPub = %q, want %q", req.EncPub, got)
	}
	if got := base64.StdEncoding.EncodeToString(id.SignPub); req.SignPub != got {
		t.Errorf("signPub = %q, want %q", req.SignPub, got)
	}

	pop, err := base64.StdEncoding.DecodeString(req.Pop)
	if err != nil {
		t.Fatalf("pop is not std-base64: %v", err)
	}
	want := registrationSigningPayload(req.ID, req.Role, req.EncPub, req.SignPub, req.BaseURL)
	if !ed25519.Verify(id.SignPub, want, pop) {
		t.Error("pop does not verify against signPub over the canonical statement")
	}

	// Negative: a mutated statement must NOT verify (the pop binds every field).
	bad := registrationSigningPayload(req.ID, "payer", req.EncPub, req.SignPub, req.BaseURL)
	if ed25519.Verify(id.SignPub, bad, pop) {
		t.Error("pop verified against a mutated (role-swapped) statement; binding is broken")
	}
}

// TestRegistration_DistinctFieldsDoNotCollide guards the newline-joined
// canonicalization: two registrations differing only in a field boundary must
// produce distinct signing payloads (no canonicalization ambiguity).
func TestRegistration_DistinctFieldsDoNotCollide(t *testing.T) {
	a := registrationSigningPayload("ab", "provider", "e", "s", "u")
	b := registrationSigningPayload("a", "bprovider", "e", "s", "u")
	if string(a) == string(b) {
		t.Error("distinct id/role pairs produced an identical signing payload")
	}
}

// TestRegistrationAdvertisesMessageFrames verifies Registration self-declares
// the library's supported message-frame versions (messageFrames), and that the
// capability token does NOT enter the frozen 5-field PoP signing payload.
func TestRegistrationAdvertisesMessageFrames(t *testing.T) {
	id, err := GenerateIdentity("frames-holder")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	req := id.Registration("provider", "https://gw.example.org")
	if len(req.MessageFrames) != 1 || req.MessageFrames[0] != MessageFrameV1 {
		t.Fatalf("Registration did not self-declare frames: %v", req.MessageFrames)
	}
	// The PoP payload is FROZEN at 5 fields — capability must not enter it.
	want := registrationSigningPayload(req.ID, "provider", req.EncPub, req.SignPub, req.BaseURL)
	if !ed25519.Verify(id.SignPub, want, mustB64(t, req.Pop)) {
		t.Fatal("pop no longer verifies over the 5-field payload — capability leaked into the signing payload")
	}
}

// mustB64 decodes std-base64, failing the test on error.
func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("mustB64: %v", err)
	}
	return b
}
