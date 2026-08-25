package shnsdk

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

var testNow = time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

// -- Builder round-trip tests --

// TestBuildClaimResponse_ApprovedRoundTrip: BuildClaimResponse → SDK's own
// ParseClaimResponse reads approved + preAuthRef + validUntil.
func TestBuildClaimResponse_ApprovedRoundTrip(t *testing.T) {
	const (
		preAuthRef = "PA-abc123"
		validUntil = "2026-09-02"
		patientRef = "Patient/MBR-01"
		corrID     = "corr-cr-1"
	)
	cr, err := BuildClaimResponse(preAuthRef, validUntil, patientRef, corrID, testNow)
	if err != nil {
		t.Fatalf("BuildClaimResponse: %v", err)
	}
	res, err := ParseClaimResponse(cr)
	if err != nil {
		t.Fatalf("ParseClaimResponse: %v", err)
	}
	if res.Outcome != "approved" {
		t.Errorf("Outcome = %q, want approved", res.Outcome)
	}
	if res.PreAuthRef != preAuthRef {
		t.Errorf("PreAuthRef = %q, want %q", res.PreAuthRef, preAuthRef)
	}
	if res.ValidUntil != validUntil {
		t.Errorf("ValidUntil = %q, want %q", res.ValidUntil, validUntil)
	}
}

// TestBuildPendedResponse_RoundTrip: BuildPendedResponse → ParsePendedResponse
// reads pended=true + the NeededItems.
func TestBuildPendedResponse_RoundTrip(t *testing.T) {
	needed := []string{"operative-diagnostic-report"}
	resp, err := BuildPendedResponse("Patient/MBR-UC04", "corr-pend-1", needed, testNow)
	if err != nil {
		t.Fatalf("BuildPendedResponse: %v", err)
	}
	pended, items, err := ParsePendedResponse(resp)
	if err != nil {
		t.Fatalf("ParsePendedResponse: %v", err)
	}
	if !pended {
		t.Fatal("pended = false, want true")
	}
	if len(items) != 1 || items[0].Code != "operative-diagnostic-report" {
		t.Errorf("NeededItems = %v, want [{Code:operative-diagnostic-report}]", items)
	}
	if got := pendedBundleTimestamp(t, resp); got != testNow.UTC().Format(time.RFC3339) {
		t.Errorf("Bundle.timestamp = %q, want %q (PAS SD min=1 since PAS 2.0.1)", got, testNow.UTC().Format(time.RFC3339))
	}
}

// TestBuildDeniedResponse_RoundTrip: BuildDeniedResponse → SDK's own
// ParseClaimResponse reads Outcome "denied" + ReasonCode "A3" + rationale.
func TestBuildDeniedResponse_RoundTrip(t *testing.T) {
	const rationale = "Conservative therapy of at least 6 weeks is not documented."
	cr, err := BuildDeniedResponse("Patient/MBR-UC08", "corr-deny-1", rationale, testNow)
	if err != nil {
		t.Fatalf("BuildDeniedResponse: %v", err)
	}
	res, err := ParseClaimResponse(cr)
	if err != nil {
		t.Fatalf("ParseClaimResponse: %v", err)
	}
	if res.Outcome != "denied" {
		t.Errorf("Outcome = %q, want denied", res.Outcome)
	}
	if res.Denial == nil {
		t.Fatal("Denial is nil")
	}
	if res.Denial.ReasonCode != "A3" {
		t.Errorf("ReasonCode = %q, want A3", res.Denial.ReasonCode)
	}
	if res.Denial.Rationale != rationale {
		t.Errorf("Rationale = %q, want %q", res.Denial.Rationale, rationale)
	}
}

// -- PAS responder builders at line --

// TestBuildClaimResponseAtLine_RegressionFenceAndRejection: the legacy
// BuildClaimResponse is byte-identical to AtLine("2.0"); an unrecognised line
// errors (fail-closed).
func TestBuildClaimResponseAtLine_RegressionFenceAndRejection(t *testing.T) {
	const (
		preAuthRef = "PA-abc123"
		validUntil = "2026-09-02"
		patientRef = "Patient/MBR-01"
		corrID     = "corr-cr-atline"
	)
	legacy, err := BuildClaimResponse(preAuthRef, validUntil, patientRef, corrID, testNow)
	if err != nil {
		t.Fatalf("BuildClaimResponse: %v", err)
	}
	atLine, err := BuildClaimResponseAtLine("2.0", preAuthRef, validUntil, patientRef, corrID, testNow)
	if err != nil {
		t.Fatalf("BuildClaimResponseAtLine(2.0): %v", err)
	}
	if !bytes.Equal(legacy, atLine) {
		t.Fatalf("BuildClaimResponse != AtLine(\"2.0\"):\n legacy: %s\n atLine: %s", legacy, atLine)
	}
	if _, err := BuildClaimResponseAtLine("9.9", preAuthRef, validUntil, patientRef, corrID, testNow); err == nil {
		t.Fatal("BuildClaimResponseAtLine(\"9.9\") = nil error, want an error")
	}
}

// TestBuildDeniedResponseAtLine_RegressionFenceAndRejection mirrors the
// ClaimResponse case for BuildDeniedResponse.
func TestBuildDeniedResponseAtLine_RegressionFenceAndRejection(t *testing.T) {
	const rationale = "Conservative therapy of at least 6 weeks is not documented."
	legacy, err := BuildDeniedResponse("Patient/MBR-UC08", "corr-deny-atline", rationale, testNow)
	if err != nil {
		t.Fatalf("BuildDeniedResponse: %v", err)
	}
	atLine, err := BuildDeniedResponseAtLine("2.0", "Patient/MBR-UC08", "corr-deny-atline", rationale, testNow)
	if err != nil {
		t.Fatalf("BuildDeniedResponseAtLine(2.0): %v", err)
	}
	if !bytes.Equal(legacy, atLine) {
		t.Fatalf("BuildDeniedResponse != AtLine(\"2.0\"):\n legacy: %s\n atLine: %s", legacy, atLine)
	}
	if _, err := BuildDeniedResponseAtLine("9.9", "Patient/MBR-UC08", "corr-deny-atline", rationale, testNow); err == nil {
		t.Fatal("BuildDeniedResponseAtLine(\"9.9\") = nil error, want an error")
	}
}

// TestBuildPendedResponseAtLine_RegressionFenceAndRejection mirrors the
// ClaimResponse case for BuildPendedResponse.
func TestBuildPendedResponseAtLine_RegressionFenceAndRejection(t *testing.T) {
	needed := []string{"operative-diagnostic-report"}
	legacy, err := BuildPendedResponse("Patient/MBR-UC04", "corr-pend-atline", needed, testNow)
	if err != nil {
		t.Fatalf("BuildPendedResponse: %v", err)
	}
	atLine, err := BuildPendedResponseAtLine("2.0", "Patient/MBR-UC04", "corr-pend-atline", needed, testNow)
	if err != nil {
		t.Fatalf("BuildPendedResponseAtLine(2.0): %v", err)
	}
	if !bytes.Equal(legacy, atLine) {
		t.Fatalf("BuildPendedResponse != AtLine(\"2.0\"):\n legacy: %s\n atLine: %s", legacy, atLine)
	}
	if _, err := BuildPendedResponseAtLine("9.9", "Patient/MBR-UC04", "corr-pend-atline", needed, testNow); err == nil {
		t.Fatal("BuildPendedResponseAtLine(\"9.9\") = nil error, want an error")
	}
	// Bundle.timestamp: the PAS StructureDefinition sets min=1 on
	// Bundle.timestamp at every line (2.0.1/2.1.0/2.2.1) — assert it at each.
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		got, err := BuildPendedResponseAtLine(line, "Patient/MBR-UC04", "corr-pend-atline-ts", needed, testNow)
		if err != nil {
			t.Fatalf("BuildPendedResponseAtLine(%s): %v", line, err)
		}
		if ts := pendedBundleTimestamp(t, got); ts != testNow.UTC().Format(time.RFC3339) {
			t.Errorf("line %s: Bundle.timestamp = %q, want %q", line, ts, testNow.UTC().Format(time.RFC3339))
		}
	}
}

// pendedBundleTimestamp extracts Bundle.timestamp ("" if absent) from a built
// pended response.
func pendedBundleTimestamp(t *testing.T, pendedJSON []byte) string {
	t.Helper()
	var probe struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(pendedJSON, &probe); err != nil {
		t.Fatalf("unmarshal pended bundle: %v", err)
	}
	return probe.Timestamp
}

// pendedBundleIdentifier extracts Bundle.identifier (nil if absent) from a built
// pended response.
func pendedBundleIdentifier(t *testing.T, pendedJSON []byte) *struct {
	System string `json:"system"`
	Value  string `json:"value"`
} {
	t.Helper()
	var probe struct {
		Identifier *struct {
			System string `json:"system"`
			Value  string `json:"value"`
		} `json:"identifier"`
	}
	if err := json.Unmarshal(pendedJSON, &probe); err != nil {
		t.Fatalf("unmarshal pended bundle: %v", err)
	}
	return probe.Identifier
}

// TestBuildPendedResponseAtLine_BundleIdentifierByLine: PAS 2.2 makes response
// Bundle.identifier mandatory (verified against the PAS 2.2.1 package
// differential for profile-pas-response-bundle.json; absent at 2.0.1/2.1.0 —
// PAS package differential). 2.0/2.1 carry NO Bundle.identifier (regression fence);
// 2.2 carries it, set to the PAS bundle identifier system + the correlation id.
func TestBuildPendedResponseAtLine_BundleIdentifierByLine(t *testing.T) {
	needed := []string{"operative-diagnostic-report"}
	const corrID = "corr-pend-ident"
	for _, tc := range []struct {
		line     string
		wantsIdn bool
	}{{"2.0", false}, {"2.1", false}, {"2.2", true}} {
		got, err := BuildPendedResponseAtLine(tc.line, "Patient/MBR-UC04", corrID, needed, testNow)
		if err != nil {
			t.Fatalf("BuildPendedResponseAtLine(%s): %v", tc.line, err)
		}
		idn := pendedBundleIdentifier(t, got)
		if tc.wantsIdn && idn == nil {
			t.Errorf("line %s: Bundle.identifier absent, want present", tc.line)
			continue
		}
		if !tc.wantsIdn && idn != nil {
			t.Errorf("line %s: Bundle.identifier = %+v, want absent", tc.line, idn)
			continue
		}
		if tc.wantsIdn {
			if idn.System != pasBundleIdentifierSystem || idn.Value != corrID {
				t.Errorf("line %s: Bundle.identifier = %+v, want system %q value %q", tc.line, idn, pasBundleIdentifierSystem, corrID)
			}
		}
	}
}

// claimResponseRequestOf extracts ClaimResponse.request (nil if absent).
func claimResponseRequestOf(t *testing.T, crJSON []byte) *struct {
	Reference  string `json:"reference"`
	Identifier *struct {
		System string `json:"system"`
		Value  string `json:"value"`
	} `json:"identifier"`
} {
	t.Helper()
	var probe struct {
		Request *struct {
			Reference  string `json:"reference"`
			Identifier *struct {
				System string `json:"system"`
				Value  string `json:"value"`
			} `json:"identifier"`
		} `json:"request"`
	}
	if err := json.Unmarshal(crJSON, &probe); err != nil {
		t.Fatalf("unmarshal ClaimResponse: %v", err)
	}
	return probe.Request
}

// TestClaimResponseRequestByLine (Finding A): PAS 2.1+ makes ClaimResponse.request
// mandatory (profile-claimresponse-base.json differential min=1 at 2.1.0 AND 2.2.1;
// min=0 at 2.0.1). All THREE response builders (approved / denied / the pended
// bundle's inner ClaimResponse) carry it at 2.1/2.2 and NOT at 2.0 (byte-freeze),
// and it references the request Claim by the correlation business identifier the
// Claim itself carries (never a fabricated literal reference).
func TestClaimResponseRequestByLine(t *testing.T) {
	const corrID = "corr-cr-request"
	for _, tc := range []struct {
		line string
		want bool
	}{{"2.0", false}, {"2.1", true}, {"2.2", true}} {
		approved, err := BuildClaimResponseAtLine(tc.line, "PA-abc123", "2026-09-02", "Patient/MBR-01", corrID, testNow)
		if err != nil {
			t.Fatalf("BuildClaimResponseAtLine(%s): %v", tc.line, err)
		}
		denied, err := BuildDeniedResponseAtLine(tc.line, "Patient/MBR-UC08", corrID, "not documented", testNow)
		if err != nil {
			t.Fatalf("BuildDeniedResponseAtLine(%s): %v", tc.line, err)
		}
		pended, err := BuildPendedResponseAtLine(tc.line, "Patient/MBR-UC04", corrID, []string{"operative-diagnostic-report"}, testNow)
		if err != nil {
			t.Fatalf("BuildPendedResponseAtLine(%s): %v", tc.line, err)
		}
		var pendedBundle struct {
			Entry []struct {
				Resource json.RawMessage `json:"resource"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(pended, &pendedBundle); err != nil {
			t.Fatalf("unmarshal pended bundle: %v", err)
		}
		if len(pendedBundle.Entry) == 0 {
			t.Fatalf("line %s: pended bundle has no entries", tc.line)
		}

		for name, crJSON := range map[string][]byte{
			"approved": approved,
			"denied":   denied,
			"pended":   pendedBundle.Entry[0].Resource,
		} {
			req := claimResponseRequestOf(t, crJSON)
			if !tc.want {
				if req != nil {
					t.Errorf("line %s %s: ClaimResponse.request = %+v, want absent", tc.line, name, req)
				}
				continue
			}
			if req == nil {
				t.Errorf("line %s %s: ClaimResponse.request absent, want the request Claim reference", tc.line, name)
				continue
			}
			if req.Identifier == nil || req.Identifier.System != pasCorrelationSystem || req.Identifier.Value != corrID {
				t.Errorf("line %s %s: ClaimResponse.request.identifier = %+v, want system %q value %q",
					tc.line, name, req.Identifier, pasCorrelationSystem, corrID)
			}
		}
	}
}

// TestBuildPendedResponseAtLine_OutcomeByLine (Finding G): PAS 2.2.1 narrows
// ClaimResponse.outcome's required binding from base R4 remittance-outcome (which
// carries "queued") to its own ValueSet-ClaimResponseOutcome = {complete, error,
// partial} — "queued" is NOT a conformant outcome at 2.2. The IG's own pended
// example (ClaimResponse-PractitionerRequestorPendingResponseExample) uses
// "complete" at 2.1.0 AND 2.2.1; the pend stays expressed by the Task entry (the
// SHN wire convention) and the A4 reviewAction, never by the outcome code.
func TestBuildPendedResponseAtLine_OutcomeByLine(t *testing.T) {
	for _, tc := range []struct {
		line, want string
	}{{"2.0", "queued"}, {"2.1", "queued"}, {"2.2", "complete"}} {
		got, err := BuildPendedResponseAtLine(tc.line, "Patient/MBR-UC04", "corr-pend-outcome", []string{"operative-diagnostic-report"}, testNow)
		if err != nil {
			t.Fatalf("BuildPendedResponseAtLine(%s): %v", tc.line, err)
		}
		var bundle struct {
			Entry []struct {
				Resource struct {
					ResourceType string `json:"resourceType"`
					Outcome      string `json:"outcome"`
				} `json:"resource"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(got, &bundle); err != nil {
			t.Fatalf("unmarshal pended bundle: %v", err)
		}
		found := false
		for _, e := range bundle.Entry {
			if e.Resource.ResourceType != "ClaimResponse" {
				continue
			}
			found = true
			if e.Resource.Outcome != tc.want {
				t.Errorf("line %s: pended ClaimResponse.outcome = %q, want %q", tc.line, e.Resource.Outcome, tc.want)
			}
		}
		if !found {
			t.Errorf("line %s: pended bundle carries no ClaimResponse", tc.line)
		}
	}
}
