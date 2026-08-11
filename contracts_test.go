package shnsdk

import "testing"

// TestSupportedContractVersions pins the token set this build self-declares:
// the builders' pinned lines (CRD/DTR/PAS 2.0.1 → @2.0, PDex 2.1.0 → @2.1).
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
