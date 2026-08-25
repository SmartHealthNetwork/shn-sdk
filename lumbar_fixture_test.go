package shnsdk

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"testing"
)

// demoLumbarQuestionnaireJSON is the demo lumbar-MRI PA questionnaire, captured
// byte-for-byte from the last DemoLumbarQuestionnaire() output before that export was
// retired from this package's published API (register §8, 2026-08-24 breaking window —
// a published API is immutable once fetched, and the export advertised a CPT the live
// reference payer network does not handle). FillQuestionnaire is fenced to fill exactly
// this questionnaire's shape (its url must equal SupportedQuestionnaireCanonical), so
// this package's own tests keep an unexported copy — test-only, never shipped on the
// built module's public API. Byte-identical to the copies internal/dtr,
// gateway/fhirseed, and tools/sampleparticipant carry (four independent module-local
// homes for the one fact, not a hierarchy — TestDemoLumbarQuestionnaireFixture_MatchesDriftGuardHash
// below pins this copy against lumbarQuestionnaireDriftGuardSHA256, the same constant
// the other three homes' own tests pin against; test/fixtureparity additionally proves
// internal/dtr and gateway/fhirseed byte-equal to each other directly, since both are
// reachable from a single root-module test).
//
//go:embed testdata/lumbar-mri-questionnaire.json
var demoLumbarQuestionnaireJSON []byte

// lumbarQuestionnaireDriftGuardSHA256 is the sha256 of the demo lumbar-MRI questionnaire
// fixture, shared (as an identical literal) by every one of its four module-local homes'
// own tests: this package, internal/dtr, gateway/fhirseed, and tools/sampleparticipant.
// A hand-edit to any ONE copy that the other three don't also receive fails THAT copy's
// own test immediately — no cross-module reachability is needed for the guard to fire,
// which matters because this package cannot import internal/dtr or gateway/fhirseed (the
// sanctioned dependency direction runs the other way) and tools/sampleparticipant's copy
// is deliberately unexported. Recompute with
// `shasum -a 256 sdk/testdata/lumbar-mri-questionnaire.json` after any deliberate,
// synchronized edit to all four copies.
const lumbarQuestionnaireDriftGuardSHA256 = "7c5a062da8beacc2d21f9e6361705e4b78b52859825e5521a7aca13b74218773"

// TestDemoLumbarQuestionnaireFixture_MatchesDriftGuardHash pins this package's own copy
// of the demo lumbar-MRI questionnaire against the shared drift-guard hash (see
// lumbarQuestionnaireDriftGuardSHA256's doc comment) — the guard that catches exactly the
// failure mode ("a stand-in copy silently drifts from its twin") that produced three live
// defects on this branch.
func TestDemoLumbarQuestionnaireFixture_MatchesDriftGuardHash(t *testing.T) {
	sum := sha256.Sum256(demoLumbarQuestionnaireJSON)
	if got := hex.EncodeToString(sum[:]); got != lumbarQuestionnaireDriftGuardSHA256 {
		t.Fatalf("sdk's own demo lumbar questionnaire fixture drifted from the shared hash: got sha256 %s, want %s (internal/dtr, gateway/fhirseed, and tools/sampleparticipant all carry byte-identical copies pinned to the same hash — if this fixture changed on purpose, recompute and update the constant in all four places together)", got, lumbarQuestionnaireDriftGuardSHA256)
	}
}

// demoLumbarQuestionnaire returns the FHIR Questionnaire JSON for the demo lumbar-MRI PA
// questionnaire — this package's own test-only fixture, unexported so it never ships on
// the built module's API surface. Each call returns a fresh copy so callers may mutate
// the slice without affecting future calls (matches the retired export's semantics).
func demoLumbarQuestionnaire() []byte {
	cp := make([]byte, len(demoLumbarQuestionnaireJSON))
	copy(cp, demoLumbarQuestionnaireJSON)
	return cp
}

// demoLumbarOrder returns the order the demo lumbar-MRI questionnaire above is written
// for. Test-only (unexported): the retired DemoLumbarOrder export advertised this same
// CPT as a PUBLIC convenience for building a live PriorAuthRequest, which was the
// footgun (no payer on the reference network handles CPT 72148) — this package's own
// tests still need SOME order that pairs with demoLumbarQuestionnaire's fixed leaves,
// which is a different, in-process-only concern.
func demoLumbarOrder() (cpt, display, icd10 string) {
	return "72148", "MRI lumbar spine w/o contrast", "M51.16"
}

// demoSupplementalReport returns the operative DiagnosticReport + provenance facts this
// package's own resume/amend tests attach to a pended demo lumbar PA. Test-only
// (unexported) for the same reason as demoLumbarOrder above.
func demoSupplementalReport() SupplementalReport {
	return SupplementalReport{
		ReportID:        "dr-uc04-operative",
		CPT:             "72148",
		Display:         "MRI lumbar spine w/o contrast",
		ProvenanceAgent: "Organization/provider",
	}
}
