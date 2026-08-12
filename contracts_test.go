package shnsdk

import (
	"regexp"
	"testing"
)

// TestSupportedContractVersions pins the token set this build self-DECLARES by
// default (capability ≠ declaration — see TestNativeContractVersions for the
// full native/buildable set): the builders' pinned lines (CRD/DTR/PAS 2.0.1 →
// @2.0, PDex 2.1.0 → @2.1).
func TestSupportedContractVersions(t *testing.T) {
	got := SupportedContractVersions()
	want := []string{"pa.crd@2.0", "pa.dtr@2.0", "pa.pas@2.0", "pa.pdex@2.1"}
	if len(got) != len(want) {
		t.Fatalf("SupportedContractVersions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SupportedContractVersions()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// contractVersionTokenRe mirrors the registrar's admission grammar
// (internal/registrar/service.go's contractVersionRe): "<contract>@<line>",
// contract = dot-separated [a-z0-9] segments, line = dot-separated digit
// segments. The sdk module cannot import the internal registrar package, so
// this is a same-shape local copy for self-checking the tokens this library
// declares/builds natively.
var contractVersionTokenRe = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)*@[0-9]+(\.[0-9]+)*$`)

// TestNativeContractVersions pins the FULL set of contract-version tokens this
// library can BUILD natively (capability), independent of which subset a
// given deployment self-declares via SupportedContractVersions (declaration).
// Order is stable: contract, then line (crd*, dtr*, pas*, pdex*).
func TestNativeContractVersions(t *testing.T) {
	got := NativeContractVersions()
	want := []string{
		"pa.crd@2.0", "pa.crd@2.1", "pa.crd@2.2",
		"pa.dtr@2.0", "pa.dtr@2.1", "pa.dtr@2.2",
		"pa.pas@2.0", "pa.pas@2.1", "pa.pas@2.2",
		"pa.pdex@2.1",
	}
	if len(got) != len(want) {
		t.Fatalf("NativeContractVersions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NativeContractVersions()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	for _, tok := range got {
		if !contractVersionTokenRe.MatchString(tok) {
			t.Errorf("NativeContractVersions() token %q does not match the token grammar", tok)
		}
	}

	native := make(map[string]bool, len(got))
	for _, tok := range got {
		native[tok] = true
	}
	for _, tok := range SupportedContractVersions() {
		if !native[tok] {
			t.Errorf("SupportedContractVersions() token %q is not a member of NativeContractVersions() — declared set must be a subset of the native set", tok)
		}
	}
}
