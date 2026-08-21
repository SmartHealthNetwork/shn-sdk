package shnsdk

import "testing"

// FuzzValidatePatientAnswer checks the patient-answer validator on the untrusted
// patient-input boundary: ValidatePatientAnswer must never panic on arbitrary
// (linkID, answer) strings. An error is the expected outcome for out-of-range,
// non-integer, or unknown-item input; the bar here is no-panic. The Oswestry
// disability index (functional-status-oswestry) is the one item with a known rule.
// Run in seed-corpus mode under `make check`; deep-fuzzed by the nightly matrix.
func FuzzValidatePatientAnswer(f *testing.F) {
	f.Add("functional-status-oswestry", "42")
	f.Add("functional-status-oswestry", "-1")
	f.Add("functional-status-oswestry", "")
	f.Add("unknown-link", "x")
	f.Fuzz(func(t *testing.T, linkID, answer string) {
		_ = ValidatePatientAnswer(linkID, answer) // assert: never panics on arbitrary patient input
	})
}
