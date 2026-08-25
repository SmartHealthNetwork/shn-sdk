package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
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
	covJSON, err := BuildCoverageWithPayer("Patient/MBR-COVERED", "MBR-COVERED", CMSPayerIdentity)
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
	const canon = SupportedQuestionnaireCanonical

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

// TestBuildCardsAtLine_RegressionFenceAndRejection: the legacy BuildCards is
// byte-identical to AtLine("2.0"); an unrecognised line errors (fail-closed).
// Derived live from packages.simplifier.net/hl7.fhir.us.davinci-crd/{2.0.1,2.1.0,2.2.1}
// (StructureDefinition-ext-coverage-information.json differential): confirmed the
// covered/pa-needed/questionnaire/satisfied-pa-id split sub-extension shape this
// projection carries is min/max-IDENTICAL across all three published CRD STUs — so
// BuildCardsAtLine has no per-line behavioral delta to gate; the parameter exists to
// fail closed on an unrecognized line, matching the AtLine convention already
// established for PAS/DTR.
func TestBuildCardsAtLine_RegressionFenceAndRejection(t *testing.T) {
	cov := CardCoverage{Covered: "covered", PANeeded: "auth-needed", Questionnaires: []string{SupportedQuestionnaireCanonical}}
	legacy, err := BuildCards(cov)
	if err != nil {
		t.Fatalf("BuildCards: %v", err)
	}
	atLine, err := BuildCardsAtLine("2.0", cov)
	if err != nil {
		t.Fatalf("BuildCardsAtLine(2.0): %v", err)
	}
	if !bytes.Equal(legacy, atLine) {
		t.Fatalf("BuildCards != BuildCardsAtLine(\"2.0\"):\n legacy: %s\n atLine: %s", legacy, atLine)
	}
	if _, err := BuildCardsAtLine("9.9", cov); err == nil {
		t.Fatal("BuildCardsAtLine(\"9.9\") = nil error, want an error")
	}
}

// TestBuildCardsAtLine_LineInvariantShape asserts BuildCardsAtLine emits
// byte-identical output at every known CRD line (2.0/2.1/2.2) — the Step-1
// package diff found no behavioral delta for the split coverage-information
// projection SHN builds, so the three lines must stay byte-equal until a future
// diff finds a real one (regression fence against silent drift).
func TestBuildCardsAtLine_LineInvariantShape(t *testing.T) {
	cov := CardCoverage{Covered: "not-covered", PANeeded: "no-auth"}
	b20, err := BuildCardsAtLine("2.0", cov)
	if err != nil {
		t.Fatalf("BuildCardsAtLine(2.0): %v", err)
	}
	for _, line := range []string{"2.1", "2.2"} {
		b, err := BuildCardsAtLine(line, cov)
		if err != nil {
			t.Fatalf("BuildCardsAtLine(%s): %v", line, err)
		}
		if !bytes.Equal(b20, b) {
			t.Fatalf("BuildCardsAtLine(%s) != BuildCardsAtLine(\"2.0\"):\n 2.0: %s\n %s: %s", line, b20, line, b)
		}
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

// TestParseServiceRequestProcedure verifies the code+display sibling recovers BOTH
// the CPT code and its display, that ParseServiceRequestCPT (which now delegates to
// it) is unchanged, and that a wrong resourceType is rejected. The display sources
// the PA-decision EOB's productOrService.display from the ACTUAL service (FR-28).
func TestParseServiceRequestProcedure(t *testing.T) {
	sr, err := BuildServiceRequest("29881", "Arthroscopy, knee, surgical, with meniscectomy", "M23.2", "Patient/p")
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	code, display, err := ParseServiceRequestProcedure(sr)
	if err != nil {
		t.Fatalf("ParseServiceRequestProcedure: %v", err)
	}
	if code != "29881" || display != "Arthroscopy, knee, surgical, with meniscectomy" {
		t.Fatalf("got (%q,%q), want (29881, knee)", code, display)
	}
	// Old API unchanged.
	c, err := ParseServiceRequestCPT(sr)
	if err != nil || c != "29881" {
		t.Fatalf("ParseServiceRequestCPT regressed: %q %v", c, err)
	}
	if _, _, err := ParseServiceRequestProcedure([]byte(`{"resourceType":"Coverage"}`)); err == nil {
		t.Error("ParseServiceRequestProcedure must reject wrong resourceType")
	}
	// A CPT coding with a code but NO display recovers the code with display == "".
	noDisplay := `{"resourceType":"ServiceRequest","status":"draft","intent":"order",` +
		`"code":{"coding":[{"system":"` + systemCPT + `","code":"72148"}]},` +
		`"subject":{"reference":"Patient/p"}}`
	code, display, err = ParseServiceRequestProcedure([]byte(noDisplay))
	if err != nil {
		t.Fatalf("ParseServiceRequestProcedure(no-display): %v", err)
	}
	if code != "72148" || display != "" {
		t.Fatalf("no-display coding: got (%q,%q), want (72148, \"\")", code, display)
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
	cov, err := BuildCoverage(patientRef, "MBR-COVERED")
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

func TestBuildConformantOrderSelectRequest_PatientPrefetch(t *testing.T) {
	sr, err := BuildServiceRequest("72100", "X-ray lumbar spine", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	cov, err := BuildCoverageWithPayer("Patient/MBR-COVERED", "MBR-COVERED", CMSPayerIdentity)
	if err != nil {
		t.Fatalf("BuildCoverageWithPayer: %v", err)
	}
	reqJSON, err := BuildConformantOrderSelectRequest(sr, cov, "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest: %v", err)
	}
	var req struct {
		Prefetch struct {
			Patient json.RawMessage `json:"patient"`
		} `json:"prefetch"`
	}
	if err := json.Unmarshal(reqJSON, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var p struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(req.Prefetch.Patient, &p); err != nil {
		t.Fatalf("prefetch.patient missing/invalid: %v (%s)", err, req.Prefetch.Patient)
	}
	if p.ResourceType != "Patient" || p.ID != "MBR-COVERED" {
		t.Fatalf("prefetch.patient = %+v, want Patient/MBR-COVERED (id bare, no Patient/ prefix)", p)
	}
}

// TestBuildCoverageWithPayerRejectsCoveragePrefixedMember is the conformant builder's half of
// the producer-side rejection row (sibling: TestBuildCoverageRejectsCoveragePrefixedMember in
// order_test.go). Same identifier-semantics rule, same rationale: the urn:shn:coverage value is
// the bare member id, and a "Coverage/"-prefixed argument is a pre-v0.42.0 caller that the
// unchanged signature would otherwise let through silently.
func TestBuildCoverageWithPayerRejectsCoveragePrefixedMember(t *testing.T) {
	if _, err := BuildCoverageWithPayer("Patient/p1", "Coverage/MBR-X", CMSPayerIdentity); err == nil {
		t.Fatal("Coverage/-prefixed member must refuse loudly (signature-compatible semantic break)")
	} else if !strings.Contains(err.Error(), "must be the bare member id") {
		t.Fatalf("refusal must name the contract it enforces, got %v", err)
	}
	if _, err := BuildCoverageWithPayer("Patient/p1", "MBR-X", CMSPayerIdentity); err != nil {
		t.Fatalf("bare member id must build: %v", err)
	}
}

// TestBuildConformantOrderSelectRequest_SelectionResourceTypeAware verifies that
// context.selections references the order by its ACTUAL resourceType (br-payer's
// order-select service matches selections[] against draftOrders type-sensitively via
// IdType.equalsIgnoreBase: ServiceRequest/x does NOT match DeviceRequest/x). A
// ServiceRequest order stays "ServiceRequest/sr1" (regression guard); a DeviceRequest
// order (UC-02 hospital-bed E0250) must yield "DeviceRequest/sr1" so br-payer matches
// it and returns cards. (UC-02)
func TestBuildConformantOrderSelectRequest_SelectionResourceTypeAware(t *testing.T) {
	cov, err := BuildCoverageWithPayer("Patient/MBR-COVERED", "MBR-COVERED", CMSPayerIdentity)
	if err != nil {
		t.Fatalf("BuildCoverageWithPayer: %v", err)
	}
	// decode pulls context.selections + the (single) draftOrders entry resourceType.
	decode := func(t *testing.T, reqJSON []byte) (selections []string, entryType string) {
		t.Helper()
		var req struct {
			Context struct {
				Selections  []string `json:"selections"`
				DraftOrders struct {
					Entry []struct {
						Resource struct {
							ResourceType string `json:"resourceType"`
						} `json:"resource"`
					} `json:"entry"`
				} `json:"draftOrders"`
			} `json:"context"`
		}
		if err := json.Unmarshal(reqJSON, &req); err != nil {
			t.Fatalf("unmarshal request: %v (%s)", err, reqJSON)
		}
		if len(req.Context.DraftOrders.Entry) != 1 {
			t.Fatalf("draftOrders.entry = %d, want 1", len(req.Context.DraftOrders.Entry))
		}
		return req.Context.Selections, req.Context.DraftOrders.Entry[0].Resource.ResourceType
	}

	// ServiceRequest order — selection stays ServiceRequest/<id> (regression guard).
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	srReq, err := BuildConformantOrderSelectRequest(srJSON, cov, "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest(ServiceRequest): %v", err)
	}
	srSel, srEntryType := decode(t, srReq)
	if want := []string{"ServiceRequest/" + conformantCRDOrderID}; !reflect.DeepEqual(srSel, want) {
		t.Errorf("ServiceRequest selections = %v, want %v", srSel, want)
	}
	if srEntryType != "ServiceRequest" {
		t.Errorf("ServiceRequest draftOrders entry resourceType = %q, want ServiceRequest", srEntryType)
	}

	// DeviceRequest order (UC-02 hospital-bed E0250) — selection must be DeviceRequest/<id>.
	drJSON := []byte(`{"resourceType":"DeviceRequest","status":"draft","intent":"order","codeCodeableConcept":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0250"}]},"subject":{"reference":"Patient/x"}}`)
	drReq, err := BuildConformantOrderSelectRequest(drJSON, cov, "Patient/MBR-COVERED")
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest(DeviceRequest): %v", err)
	}
	drSel, drEntryType := decode(t, drReq)
	if want := []string{"DeviceRequest/" + conformantCRDOrderID}; !reflect.DeepEqual(drSel, want) {
		t.Errorf("DeviceRequest selections = %v, want %v", drSel, want)
	}
	if drEntryType != "DeviceRequest" {
		t.Errorf("DeviceRequest draftOrders entry resourceType = %q, want DeviceRequest", drEntryType)
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
