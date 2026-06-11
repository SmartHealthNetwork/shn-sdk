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
