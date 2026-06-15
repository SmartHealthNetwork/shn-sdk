package shnsdk_test

import (
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestStandardCodesVerified pins each STANDARD terminology code to the exact value verified
// against a terminology service, with its returned display. It is the FR-36 guard ("terminology
// codes ... validated against a terminology service — never hallucinated"): the structural
// $validate gate (make validate) certifies resource SHAPE, not code MEANING — the IG-HAPI
// returns only a *warning* for an unresolvable SNOMED/LOINC code, which passes — so a
// hallucinated code can slip through $validate (it did: two invented SNOMED codes were caught
// in PR #13 review, not by the gate). This hermetic test runs in `make check` and fails loudly
// if any code constant changes, forcing whoever changes it to RE-VERIFY the new code against a
// terminology service and update the expected display here.
//
// The urn:shn:clinical-context codes (conservative-therapy-weeks, neuro-deficit) are
// intentionally LOCAL — no canonical FHIR coding exists for those facts — and are deliberately
// NOT pinned here (there is nothing to verify against).
func TestStandardCodesVerified(t *testing.T) {
	cases := []struct{ name, got, want, system, display, provenance string }{
		{"ConditionCodeLumbar", shnsdk.ConditionCodeLumbar, "M51.16", shnsdk.SystemICD10CM,
			"Intervertebral disc disorders with radiculopathy, lumbar region", "tx.fhir.org $lookup 2026-06-13"},
		{"ODICode", shnsdk.ODICode, "97909-6", shnsdk.SystemLOINC,
			"Oswestry disability index score ODI", "tx.fhir.org $lookup 2026-06-13"},
		{"ProcLaminectomySNOMED", shnsdk.ProcLaminectomySNOMED, "387731002", shnsdk.SystemSNOMED,
			"Laminectomy", "tx.fhir.org $lookup 2026-06-13"},
		{"ProcMicrodiscectomySNOMED", shnsdk.ProcMicrodiscectomySNOMED, "178625001", shnsdk.SystemSNOMED,
			"Primary lumbar microdiscectomy", "tx.fhir.org $lookup 2026-06-13"},
		// ImagingCPT (72148) is AMA-licensed — not expandable on the open tx server — but is
		// battle-tested in existing $validate-gated goldens (testdata/golden/servicerequest-72148.json).
		{"ImagingCPT", shnsdk.ImagingCPT, "72148", shnsdk.SystemCPT,
			"MRI lumbar spine without contrast", "existing $validate goldens (servicerequest-72148.json)"},
		{"ReportImagingStudyLOINC", shnsdk.ReportImagingStudyLOINC, "18748-4", shnsdk.SystemLOINC,
			"Diagnostic imaging study", "tx.fhir.org $lookup 2026-06-13"},
		{"ReportOperativeNoteLOINC", shnsdk.ReportOperativeNoteLOINC, "11504-8", shnsdk.SystemLOINC,
			"Surgical operation note", "tx.fhir.org $lookup 2026-06-13"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s (%s) = %q, want %q [verified meaning: %q; %s].\n"+
					"  A code constant changed: RE-VERIFY the new code against a terminology service "+
					"(e.g. tx.fhir.org $lookup) and update this test's expected value + display.",
					c.name, c.system, c.got, c.want, c.display, c.provenance)
			}
		})
	}
}

// TestReportValueSet asserts that ReportValueSet contains exactly the two expected LOINC codes.
func TestReportValueSet(t *testing.T) {
	want := map[string]bool{shnsdk.ReportImagingStudyLOINC: true, shnsdk.ReportOperativeNoteLOINC: true}
	if len(shnsdk.ReportValueSet) != len(want) {
		t.Fatalf("ReportValueSet = %v, want the two report LOINCs", shnsdk.ReportValueSet)
	}
	for _, c := range shnsdk.ReportValueSet {
		if !want[c] {
			t.Errorf("unexpected code %q in ReportValueSet", c)
		}
	}
}

// TestProcedureValueSet asserts that ProcedureValueSet contains exactly the two expected SNOMED codes.
func TestProcedureValueSet(t *testing.T) {
	want := map[string]bool{shnsdk.ProcLaminectomySNOMED: true, shnsdk.ProcMicrodiscectomySNOMED: true}
	if len(shnsdk.ProcedureValueSet) != len(want) {
		t.Fatalf("ProcedureValueSet = %v, want the two spine SNOMED procedures", shnsdk.ProcedureValueSet)
	}
	for _, c := range shnsdk.ProcedureValueSet {
		if !want[c] {
			t.Errorf("unexpected code %q in ProcedureValueSet", c)
		}
	}
}
