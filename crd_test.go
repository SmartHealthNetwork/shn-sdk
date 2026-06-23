package shnsdk

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"testing"
)

// readConformantGolden reads a conformant golden from the platform testdata (../testdata). These
// goldens live in the monorepo and are ABSENT in the standalone published SDK module, so a test that
// byte-matches them SKIPS there — the builder↔golden contract is a monorepo gate, not a published-
// module one (the published module still compiles + runs its construction/validation tests). In the
// monorepo the golden is present and the byte-match runs.
func readConformantGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../testdata/golden/conformant/" + name)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("conformant golden %q lives in the monorepo (../testdata); skipped in the standalone SDK module", name)
	}
	if err != nil {
		t.Fatalf("read conformant golden %q: %v", name, err)
	}
	return b
}

// jsonEqual canonicalizes both byte slices (Unmarshal → reflect.DeepEqual on the
// resulting maps, which is order-insensitive for JSON objects) and reports whether
// they are semantically equal. On mismatch the caller prints both sides.
func jsonEqual(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("jsonEqual: unmarshal got: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("jsonEqual: unmarshal want: %v", err)
	}
	return reflect.DeepEqual(g, w)
}

// TestBuildConformantOrderSelectRequest_MatchesGolden: the SDK builder reproduces the
// demo-persona conformant CRD request
// (testdata/golden/conformant/crd-order-select-request.json) byte-for-byte (canonical
// JSON). This is the byte-match oracle for the conformant CRD originator.
func TestBuildConformantOrderSelectRequest_MatchesGolden(t *testing.T) {
	want := readConformantGolden(t, "crd-order-select-request.json")
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	covJSON, err := BuildCoverageWithPayer("Patient/MBR-COVERED", "Coverage/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildCoverageWithPayer: %v", err)
	}
	got, err := BuildConformantOrderSelectRequest(srJSON, covJSON, "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest: %v", err)
	}
	if !jsonEqual(t, got, want) {
		t.Fatalf("conformant CRD request drift:\n got: %s\nwant: %s", got, want)
	}
}

// TestParseOrderSelectRequest_RoundTrip verifies that ParseOrderSelectRequest
// recovers the patientID, draft-order SR, and Coverage from BuildOrderSelectRequest
// output.
func TestParseOrderSelectRequest_RoundTrip(t *testing.T) {
	sr := []byte(`{"resourceType":"ServiceRequest","id":"sr-1"}`)
	cov := []byte(`{"resourceType":"Coverage","id":"cov-1"}`)
	const patientRef = "Patient/MBR-COVERED"

	b, err := BuildOrderSelectRequest(sr, cov, patientRef)
	if err != nil {
		t.Fatalf("BuildOrderSelectRequest: %v", err)
	}
	req, err := ParseOrderSelectRequest(b)
	if err != nil {
		t.Fatalf("ParseOrderSelectRequest: %v", err)
	}
	if req.Hook != "order-select" {
		t.Errorf("hook = %q, want order-select", req.Hook)
	}
	if req.Context.PatientID != patientRef {
		t.Errorf("patientId = %q, want %q", req.Context.PatientID, patientRef)
	}
	if len(req.Context.DraftOrders) != 1 {
		t.Fatalf("draftOrders len = %d, want 1", len(req.Context.DraftOrders))
	}
	if string(req.Context.DraftOrders[0]) != string(sr) {
		t.Errorf("draftOrders[0] = %s, want %s", req.Context.DraftOrders[0], sr)
	}
	if string(req.Prefetch.Coverage) != string(cov) {
		t.Errorf("prefetch.coverage = %s, want %s", req.Prefetch.Coverage, cov)
	}
}

// TestParseOrderSelectRequest_Rejects verifies the two invalid inputs are rejected.
func TestParseOrderSelectRequest_Rejects(t *testing.T) {
	// Empty draft orders.
	if _, err := ParseOrderSelectRequest([]byte(`{"hook":"order-select","context":{"patientId":"p","draftOrders":[]},"prefetch":{"coverage":{}}}`)); err == nil {
		t.Error("ParseOrderSelectRequest should reject empty draftOrders")
	}
	// Garbage JSON.
	if _, err := ParseOrderSelectRequest([]byte(`not json`)); err == nil {
		t.Error("ParseOrderSelectRequest should reject garbage JSON")
	}
	// Wrong hook.
	if _, err := ParseOrderSelectRequest([]byte(`{"hook":"appointment-book","context":{"patientId":"p","draftOrders":[{}]},"prefetch":{"coverage":{}}}`)); err == nil {
		t.Error("ParseOrderSelectRequest should reject wrong hook")
	}
}

// TestBuildCards covers the PA-required and no-PA branches, and verifies that
// ParseCards round-trips each branch correctly.
func TestBuildCards(t *testing.T) {
	const canon = QuestionnaireCanonicalLumbarMRI

	// PA-required branch.
	b, err := BuildCards(CardCoverage{Covered: "covered", PANeeded: "auth-needed", Questionnaires: []string{canon}})
	if err != nil {
		t.Fatalf("BuildCards(pa-required): %v", err)
	}
	cov, err := ParseCards(b)
	if err != nil {
		t.Fatalf("ParseCards(pa-required): %v", err)
	}
	if !cov.PARequired() || !cov.NeedsDTR() || cov.Questionnaires[0] != canon {
		t.Errorf("pa-required round-trip = %+v, want PA-required carrying %q", cov, canon)
	}

	// No-PA branch.
	b, err = BuildCards(CardCoverage{Covered: "covered", PANeeded: "no-auth"})
	if err != nil {
		t.Fatalf("BuildCards(no-pa): %v", err)
	}
	cov, err = ParseCards(b)
	if err != nil {
		t.Fatalf("ParseCards(no-pa): %v", err)
	}
	if cov.PARequired() || cov.NeedsDTR() {
		t.Errorf("no-pa round-trip = %+v, want not PA-required, no questionnaire", cov)
	}
}

// TestCardCoverageRoundTrip verifies BuildCards→ParseCards preserves the widened
// CardCoverage fields and that the PA-required/NeedsDTR predicates read them.
func TestCardCoverageRoundTrip(t *testing.T) {
	in := CardCoverage{Covered: "covered", PANeeded: "auth-needed",
		Questionnaires: []string{"http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri"}}
	cardsJSON, err := BuildCards(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCards(cardsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got.Covered != "covered" || !got.PARequired() || !got.NeedsDTR() {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
}

// TestCardCoverageNotCovered verifies the not-covered/no-auth projection round-trips
// and is not PA-required.
func TestCardCoverageNotCovered(t *testing.T) {
	cardsJSON, err := BuildCards(CardCoverage{Covered: "not-covered", PANeeded: "no-auth"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCards(cardsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got.Covered != "not-covered" || got.PARequired() {
		t.Fatalf("got %+v", got)
	}
}

// TestStripCanonicalVersion verifies the trailing |version is stripped and a bare
// canonical is left unchanged.
func TestStripCanonicalVersion(t *testing.T) {
	if StripCanonicalVersion("http://x/Q|1.0.0") != "http://x/Q" {
		t.Fatal("strip |version")
	}
	if StripCanonicalVersion("http://x/Q") != "http://x/Q" {
		t.Fatal("bare unchanged")
	}
}

// TestBuildOrderSelectRequest_Shape verifies the CRD order-select request carries the
// fixed hook, the patientId + the ServiceRequest as the sole draft order, and the
// Coverage in prefetch — embedded VERBATIM (FR-14 minimum-necessary).
func TestBuildOrderSelectRequest_Shape(t *testing.T) {
	sr := []byte(`{"resourceType":"ServiceRequest","id":"sr-1"}`)
	cov := []byte(`{"resourceType":"Coverage","id":"cov-1"}`)
	const patientRef = "Patient/MBR-COVERED"

	b, err := BuildOrderSelectRequest(sr, cov, patientRef)
	if err != nil {
		t.Fatalf("BuildOrderSelectRequest: %v", err)
	}

	var got struct {
		Hook    string `json:"hook"`
		Context struct {
			PatientID   string            `json:"patientId"`
			DraftOrders []json.RawMessage `json:"draftOrders"`
		} `json:"context"`
		Prefetch struct {
			Coverage json.RawMessage `json:"coverage"`
		} `json:"prefetch"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Hook != "order-select" {
		t.Errorf("hook = %q, want order-select", got.Hook)
	}
	if got.Context.PatientID != patientRef {
		t.Errorf("patientId = %q, want %q", got.Context.PatientID, patientRef)
	}
	if len(got.Context.DraftOrders) != 1 || string(got.Context.DraftOrders[0]) != string(sr) {
		t.Errorf("draftOrders = %v, want exactly the SR verbatim", got.Context.DraftOrders)
	}
	if string(got.Prefetch.Coverage) != string(cov) {
		t.Errorf("prefetch.coverage = %s, want %s", got.Prefetch.Coverage, cov)
	}
}

// TestParseServiceRequestCPT verifies that ParseServiceRequestCPT extracts the CPT
// code from BuildServiceRequest output and rejects wrong resourceType.
func TestParseServiceRequestCPT(t *testing.T) {
	const cpt = "72148"
	sr, err := BuildServiceRequest(cpt, "MRI lumbar spine w/o contrast", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	got, err := ParseServiceRequestCPT(sr)
	if err != nil {
		t.Fatalf("ParseServiceRequestCPT: %v", err)
	}
	if got != cpt {
		t.Errorf("CPT = %q, want %q", got, cpt)
	}
	// Wrong resourceType.
	if _, err := ParseServiceRequestCPT([]byte(`{"resourceType":"Coverage"}`)); err == nil {
		t.Error("ParseServiceRequestCPT should reject wrong resourceType")
	}
}

// TestParseServiceRequestSubject verifies that ParseServiceRequestSubject extracts
// the subject reference from BuildServiceRequest output and rejects wrong resourceType.
func TestParseServiceRequestSubject(t *testing.T) {
	const patientRef = "Patient/MBR-COVERED"
	sr, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	got, err := ParseServiceRequestSubject(sr)
	if err != nil {
		t.Fatalf("ParseServiceRequestSubject: %v", err)
	}
	if got != patientRef {
		t.Errorf("subject = %q, want %q", got, patientRef)
	}
	// Wrong resourceType.
	if _, err := ParseServiceRequestSubject([]byte(`{"resourceType":"Coverage","subject":{"reference":"Patient/X"}}`)); err == nil {
		t.Error("ParseServiceRequestSubject should reject wrong resourceType")
	}
}

// TestParseCoverageBeneficiary verifies that ParseCoverageBeneficiary extracts the
// beneficiary reference from BuildCoverage output and rejects wrong resourceType.
func TestParseCoverageBeneficiary(t *testing.T) {
	const patientRef = "Patient/MBR-COVERED"
	cov, err := BuildCoverage(patientRef, "Coverage/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildCoverage: %v", err)
	}
	got, err := ParseCoverageBeneficiary(cov)
	if err != nil {
		t.Fatalf("ParseCoverageBeneficiary: %v", err)
	}
	if got != patientRef {
		t.Errorf("beneficiary = %q, want %q", got, patientRef)
	}
	// Wrong resourceType.
	if _, err := ParseCoverageBeneficiary([]byte(`{"resourceType":"ServiceRequest","beneficiary":{"reference":"Patient/X"}}`)); err == nil {
		t.Error("ParseCoverageBeneficiary should reject wrong resourceType")
	}
}

// TestParseCards covers both branches + the zero-card error path.
func TestParseCards(t *testing.T) {
	paReq := []byte(`{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed","questionnaires":["http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri"]}}]}`)
	cov, err := ParseCards(paReq)
	if err != nil {
		t.Fatalf("ParseCards(pa-required): %v", err)
	}
	if !cov.PARequired() || !cov.NeedsDTR() || cov.Questionnaires[0] != "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri" {
		t.Errorf("pa-required parse = %+v, want PA-required carrying the canonical", cov)
	}

	noPA := []byte(`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`)
	cov, err = ParseCards(noPA)
	if err != nil {
		t.Fatalf("ParseCards(no-pa): %v", err)
	}
	if cov.PARequired() || cov.NeedsDTR() {
		t.Errorf("no-pa parse = %+v, want not PA-required, no questionnaire", cov)
	}

	if _, err := ParseCards([]byte(`{"cards":[]}`)); err == nil {
		t.Error("ParseCards should reject a zero-card response")
	}
}
