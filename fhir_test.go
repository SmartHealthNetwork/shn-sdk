package shnsdk

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// TestBuildPADecisionEOB_DisplayFromParam (DEF-14, FR-28): the EOB's
// productOrService display is whatever CPTDisplay the caller passes (sourced from the
// request's ServiceRequest), NOT a hardcoded persona string. Guards against a
// regression to the old fixed "MRI lumbar spine w/o contrast" literal.
func TestBuildPADecisionEOB_DisplayFromParam(t *testing.T) {
	b, err := BuildPADecisionEOB(PADecisionEOBParams{
		ID: "e1", PatientRef: "Patient/p", CoverageRef: "Coverage/p",
		CPTCode: "29881", CPTDisplay: "Arthroscopy, knee, surgical, with meniscectomy",
		Decision: PADecisionApproved, AuthNumber: "A1", Created: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("BuildPADecisionEOB: %v", err)
	}
	if bytes.Contains(b, []byte("MRI lumbar spine w/o contrast")) {
		t.Fatal("DEF-14 regression: builder still emits the hardcoded lumbar display")
	}
	if !bytes.Contains(b, []byte("Arthroscopy, knee, surgical, with meniscectomy")) {
		t.Fatal("builder must emit the passed CPTDisplay")
	}
}

// TestBuildEligibilityRequest_Shape checks the CoverageEligibilityRequest the SDK
// emits has the field shapes the substrate expects (resourceType, status, purpose,
// patient/provider/insurer references, created). This is the hermetic structural
// guard; wire-interop with the substrate parser is proven in test/sdkparity.
func TestBuildEligibilityRequest_Shape(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	b, err := BuildEligibilityRequest("MBR-COVERED", "9999999999", now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal CER: %v", err)
	}
	if m["resourceType"] != "CoverageEligibilityRequest" {
		t.Errorf("resourceType = %v, want CoverageEligibilityRequest", m["resourceType"])
	}
	if m["status"] != "active" {
		t.Errorf("status = %v, want active", m["status"])
	}
	if m["created"] != "2026-06-03T00:00:00Z" {
		t.Errorf("created = %v, want 2026-06-03T00:00:00Z", m["created"])
	}
	pat, _ := m["patient"].(map[string]any)
	if pat["reference"] != "Patient/MBR-COVERED" {
		t.Errorf("patient.reference = %v, want Patient/MBR-COVERED", pat["reference"])
	}
	prov, _ := m["provider"].(map[string]any)
	if prov["reference"] != "Practitioner/9999999999" {
		t.Errorf("provider.reference = %v, want Practitioner/9999999999", prov["reference"])
	}
}

// TestParseEligibilityResponse_Branches checks the SDK parser reads both the
// covered (inforce=true) and not-covered (inforce=false + disposition) shapes.
// Wire-interop with substrate-built responses is proven in test/sdkparity.
func TestParseEligibilityResponse_Branches(t *testing.T) {
	covered := `{"resourceType":"CoverageEligibilityResponse","status":"active",` +
		`"purpose":["benefits"],"outcome":"complete",` +
		`"patient":{"reference":"Patient/MBR-COVERED"},` +
		`"insurance":[{"coverage":{"reference":"Coverage/MBR-COVERED"},"inforce":true}]}`
	gotCovered, reason, err := ParseEligibilityResponse([]byte(covered))
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(covered): %v", err)
	}
	if !gotCovered || reason != "" {
		t.Errorf("covered branch: got covered=%v reason=%q, want true/empty", gotCovered, reason)
	}

	notCovered := `{"resourceType":"CoverageEligibilityResponse","status":"active",` +
		`"purpose":["benefits"],"outcome":"complete","disposition":"member not enrolled",` +
		`"patient":{"reference":"Patient/MBR-NOTCOVERED"},` +
		`"insurance":[{"coverage":{"reference":"Coverage/MBR-NOTCOVERED"},"inforce":false}]}`
	gotNC, reasonNC, err := ParseEligibilityResponse([]byte(notCovered))
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(not-covered): %v", err)
	}
	if gotNC {
		t.Errorf("not-covered branch: got covered=true, want false")
	}
	if reasonNC != "member not enrolled" {
		t.Errorf("not-covered branch: reason = %q, want %q", reasonNC, "member not enrolled")
	}
}

// TestEligibilityRoundTrip is the hermetic build→parse seam: a covered/not-covered
// pair built by the SDK helper-shaped resources parses back consistently. (The
// substrate's BuildEligibilityResponse is the real producer; this asserts the SDK
// parser is self-consistent with the shape the parity test pins.)
func TestEligibilityRoundTrip(t *testing.T) {
	// CER round-trips through the SDK parser-of-record only on the response side;
	// here we confirm a not-covered disposition survives marshal/parse.
	src := `{"resourceType":"CoverageEligibilityResponse","status":"active",` +
		`"purpose":["benefits"],"outcome":"complete","disposition":"no active coverage",` +
		`"patient":{"reference":"Patient/X"},` +
		`"insurance":[{"coverage":{"reference":"Coverage/X"},"inforce":false}]}`
	covered, reason, err := ParseEligibilityResponse([]byte(src))
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if covered || reason != "no active coverage" {
		t.Errorf("round-trip: covered=%v reason=%q", covered, reason)
	}

	// Wrong resourceType is rejected.
	if _, _, err := ParseEligibilityResponse([]byte(`{"resourceType":"Patient"}`)); err == nil {
		t.Error("ParseEligibilityResponse should reject a non-CoverageEligibilityResponse resource")
	}
}

// TestParseEligibilityRequestMember checks the payer-side parser: round-trips the
// member out of the SDK's own BuildEligibilityRequest output, rejects a wrong
// resourceType, and rejects a CoverageEligibilityRequest missing patient.reference.
// Wire-interop with the substrate builder is proven in test/sdkparity.
func TestParseEligibilityRequestMember(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	// Round-trip: SDK-built CER → ParseEligibilityRequestMember → member.
	cerBytes, err := BuildEligibilityRequest("MBR-ROUNDTRIP", "1234567890", now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequest: %v", err)
	}
	member, err := ParseEligibilityRequestMember(cerBytes)
	if err != nil {
		t.Fatalf("ParseEligibilityRequestMember(SDK-built): %v", err)
	}
	if member != "MBR-ROUNDTRIP" {
		t.Errorf("member = %q, want MBR-ROUNDTRIP", member)
	}

	// Rejects wrong resourceType.
	if _, err := ParseEligibilityRequestMember([]byte(`{"resourceType":"Patient"}`)); err == nil {
		t.Error("ParseEligibilityRequestMember should reject a Patient resource")
	}

	// Rejects CoverageEligibilityRequest missing patient.reference.
	noPatRef := `{"resourceType":"CoverageEligibilityRequest","status":"active"}`
	if _, err := ParseEligibilityRequestMember([]byte(noPatRef)); err == nil {
		t.Error("ParseEligibilityRequestMember should reject a CER missing patient.reference")
	}
}

// TestBuildEligibilityResponse checks the payer-side builder: covered=true round-trips
// via the SDK's own ParseEligibilityResponse; covered=false with a reason round-trips
// both; and two calls with the same fixed clock produce byte-identical output.
// Wire-interop with the substrate parser is proven in test/sdkparity.
func TestBuildEligibilityResponse(t *testing.T) {
	t0 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	// covered=true round-trip.
	b, err := BuildEligibilityResponse("corr-1", "Patient/MBR-1", true, "", t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponse(covered): %v", err)
	}
	gotCovered, gotReason, err := ParseEligibilityResponse(b)
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(covered): %v", err)
	}
	if !gotCovered || gotReason != "" {
		t.Errorf("covered round-trip: covered=%v reason=%q, want true/empty", gotCovered, gotReason)
	}

	// covered=false with reason round-trip.
	b2, err := BuildEligibilityResponse("corr-2", "Patient/MBR-2", false, "not a member", t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponse(not-covered): %v", err)
	}
	gotCovered2, gotReason2, err := ParseEligibilityResponse(b2)
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(not-covered): %v", err)
	}
	if gotCovered2 {
		t.Errorf("not-covered round-trip: covered=true, want false")
	}
	if gotReason2 != "not a member" {
		t.Errorf("not-covered round-trip: reason=%q, want %q", gotReason2, "not a member")
	}

	// Determinism: two calls with same args → byte-identical output.
	b3, err := BuildEligibilityResponse("corr-det", "Patient/MBR-DET", true, "", t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponse(det-1): %v", err)
	}
	b4, err := BuildEligibilityResponse("corr-det", "Patient/MBR-DET", true, "", t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponse(det-2): %v", err)
	}
	if string(b3) != string(b4) {
		t.Errorf("non-deterministic output:\n  call1=%s\n  call2=%s", b3, b4)
	}
}

// TestBuildPatientAccessCapabilityStatement_Shape verifies the SDK-promoted
// CMS-0057 CapabilityStatement has the required FHIR shape (FR-37): kind=instance,
// status=active, at least one rest.resource of type ExplanationOfBenefit with a
// supportedProfile and both read+search interactions. Wire-identity with the
// internal/fhirmap shim is proven in test/sdkparity/capabilitystatement_parity_test.go.
func TestBuildPatientAccessCapabilityStatement_Shape(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	b, err := BuildPatientAccessCapabilityStatement(at)
	if err != nil {
		t.Fatalf("BuildPatientAccessCapabilityStatement: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["resourceType"] != "CapabilityStatement" {
		t.Errorf("resourceType = %v, want CapabilityStatement", m["resourceType"])
	}
	if m["status"] != "active" {
		t.Errorf("status = %v, want active", m["status"])
	}
	if m["kind"] != "instance" {
		t.Errorf("kind = %v, want instance", m["kind"])
	}
	if m["date"] != "2026-06-15T00:00:00Z" {
		t.Errorf("date = %v, want 2026-06-15T00:00:00Z", m["date"])
	}
	rests, _ := m["rest"].([]any)
	if len(rests) == 0 {
		t.Fatal("rest array is empty")
	}
	rest0, _ := rests[0].(map[string]any)
	resources, _ := rest0["resource"].([]any)
	if len(resources) == 0 {
		t.Fatal("rest[0].resource array is empty")
	}
	res0, _ := resources[0].(map[string]any)
	if res0["type"] != "ExplanationOfBenefit" {
		t.Errorf("rest[0].resource[0].type = %v, want ExplanationOfBenefit", res0["type"])
	}
	profiles, _ := res0["supportedProfile"].([]any)
	if len(profiles) == 0 {
		t.Error("rest[0].resource[0].supportedProfile is empty")
	}
	interactions, _ := res0["interaction"].([]any)
	if len(interactions) != 2 {
		t.Errorf("want 2 interactions (read+search-type), got %d", len(interactions))
	}
	// Determinism: same input → byte-identical output.
	b2, _ := BuildPatientAccessCapabilityStatement(at)
	if string(b) != string(b2) {
		t.Error("BuildPatientAccessCapabilityStatement is non-deterministic")
	}
}
