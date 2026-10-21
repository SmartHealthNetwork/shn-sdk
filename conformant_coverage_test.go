package shnsdk

import "testing"

// A conformant PAS Bundle with no Coverage at all, and one whose Coverage
// names no beneficiary, are refused with texts that tell them apart, so a
// sender is not sent looking for a beneficiary on a Coverage it never sent.
func TestConformantClaimSubjectTellsNoCoverageFromNoBeneficiary(t *testing.T) {
	base := conformantClaimSubmit{
		claimPatient: "Patient/M1", srSubject: "Patient/M1", haveOrder: true,
		covBeneficiary: "Patient/M1", haveCoverage: true,
	}
	noCoverage := base
	noCoverage.haveCoverage, noCoverage.covBeneficiary = false, ""
	noBeneficiary := base
	noBeneficiary.covBeneficiary = ""
	for _, c := range []struct {
		name   string
		cs     conformantClaimSubmit
		status int
		msg    string
	}{
		{"no Coverage", noCoverage, 400, "PAS bundle carries no Coverage"},
		{"a Coverage without a beneficiary", noBeneficiary, 400, "PAS bundle missing Coverage.beneficiary"},
	} {
		if status, msg := bindConformantClaimSubject(c.cs); status != c.status || msg != c.msg {
			t.Errorf("%s: %d %q, want %d %q", c.name, status, msg, c.status, c.msg)
		}
	}
}
