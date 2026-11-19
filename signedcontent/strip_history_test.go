// The gateway relay's test of the signed-content reading (its verify_test.go's
// TestStripHistoryReadsATrailingVersion), carried with the reading.

package signedcontent

import "testing"

// stripHistory drops only a trailing one-segment version: a base whose path
// contains /_history/ is part of the reference.
func TestStripHistoryReadsATrailingVersion(t *testing.T) {
	for in, want := range map[string]string{
		"Patient/p":                                    "Patient/p",
		"Patient/p/_history/2":                         "Patient/p",
		"https://h/_history/fhir/Patient/p":            "https://h/_history/fhir/Patient/p",
		"https://h/_history/fhir/Patient/p/_history/7": "https://h/_history/fhir/Patient/p",
		"Patient/p/_history/2/x":                       "Patient/p/_history/2/x",
	} {
		if got := stripHistory(in); got != want {
			t.Errorf("stripHistory(%q) = %q, want %q", in, got, want)
		}
	}
}
