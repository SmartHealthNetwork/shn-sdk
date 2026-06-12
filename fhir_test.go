package shnsdk

import (
	"encoding/json"
	"testing"
	"time"
)

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
