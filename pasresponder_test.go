package shnsdk

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

var testNow = time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

// -- SandboxAdjudicate tests --

// seededReader returns a deterministic io.Reader for test isolation.
func seededReader() *bytes.Reader {
	return bytes.NewReader([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
}

// qrJSON returns a minimal QR with the given conservative-therapy-weeks value.
func approvedQRJSON(weeks int) []byte {
	raw, _ := json.Marshal(map[string]interface{}{
		"resourceType": "QuestionnaireResponse",
		"status":       "completed",
		"item": []map[string]interface{}{
			{
				"linkId": "conservative-therapy-weeks",
				"answer": []map[string]interface{}{{"valueInteger": weeks}},
			},
		},
	})
	return raw
}

// priorSurgeryQRJSON returns a QR with prior-surgery=true and 6 weeks.
func priorSurgeryQRJSON() []byte {
	raw, _ := json.Marshal(map[string]interface{}{
		"resourceType": "QuestionnaireResponse",
		"status":       "completed",
		"item": []map[string]interface{}{
			{
				"linkId": "conservative-therapy-weeks",
				"answer": []map[string]interface{}{{"valueInteger": 6}},
			},
			{
				"linkId": "prior-surgery",
				"answer": []map[string]interface{}{{"valueBoolean": true}},
			},
		},
	})
	return raw
}

// deniedQRJSON returns a QR with < 6 weeks (denied path).
func deniedQRJSON() []byte {
	return approvedQRJSON(4)
}

// TestSandboxAdjudicate_UC03Approved: the autofilled QR (6 weeks,
// hasDR=true) → PASApproved with non-empty PreAuthRef/ValidUntil.
func TestSandboxAdjudicate_UC03Approved(t *testing.T) {
	qr, err := FillQuestionnaire(SandboxLumbarQuestionnaire(), SandboxUC03Context(), QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    testNow,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}

	dec, err := SandboxAdjudicate(qr, true, testNow, seededReader())
	if err != nil {
		t.Fatalf("SandboxAdjudicate: %v", err)
	}
	if dec.Outcome != PASApproved {
		t.Errorf("Outcome = %v, want PASApproved", dec.Outcome)
	}
	if dec.PreAuthRef == "" {
		t.Error("PreAuthRef is empty, want non-empty")
	}
	if dec.ValidUntil == "" {
		t.Error("ValidUntil is empty, want non-empty")
	}
}

// TestSandboxAdjudicate_PriorSurgeryPends: prior-surgery context + hasDR=false → PASPended.
func TestSandboxAdjudicate_PriorSurgeryPends(t *testing.T) {
	qr := priorSurgeryQRJSON()

	dec, err := SandboxAdjudicate(qr, false, testNow, seededReader())
	if err != nil {
		t.Fatalf("SandboxAdjudicate: %v", err)
	}
	if dec.Outcome != PASPended {
		t.Errorf("Outcome = %v, want PASPended", dec.Outcome)
	}
	if len(dec.NeededItems) == 0 {
		t.Error("NeededItems is empty, want at least one item")
	}
	if dec.NeededItems[0] != "operative-diagnostic-report" {
		t.Errorf("NeededItems[0] = %q, want operative-diagnostic-report", dec.NeededItems[0])
	}
}

// TestSandboxAdjudicate_ShortTherapyDenied: < 6 weeks → PASDenied.
func TestSandboxAdjudicate_ShortTherapyDenied(t *testing.T) {
	dec, err := SandboxAdjudicate(deniedQRJSON(), false, testNow, seededReader())
	if err != nil {
		t.Fatalf("SandboxAdjudicate: %v", err)
	}
	if dec.Outcome != PASDenied {
		t.Errorf("Outcome = %v, want PASDenied", dec.Outcome)
	}
}

// TestSandboxAdjudicate_SeededDeterministic: same seeded reader → same PreAuthRef.
func TestSandboxAdjudicate_SeededDeterministic(t *testing.T) {
	qr := approvedQRJSON(6)
	fixedBytes := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}

	dec1, err := SandboxAdjudicate(qr, false, testNow, bytes.NewReader(fixedBytes))
	if err != nil {
		t.Fatalf("SandboxAdjudicate run1: %v", err)
	}
	dec2, err := SandboxAdjudicate(qr, false, testNow, bytes.NewReader(fixedBytes))
	if err != nil {
		t.Fatalf("SandboxAdjudicate run2: %v", err)
	}
	if dec1.PreAuthRef != dec2.PreAuthRef {
		t.Errorf("seeded adjudication not deterministic: %q vs %q", dec1.PreAuthRef, dec2.PreAuthRef)
	}
	if dec1.ValidUntil != dec2.ValidUntil {
		t.Errorf("seeded ValidUntil not deterministic: %q vs %q", dec1.ValidUntil, dec2.ValidUntil)
	}
}

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

// TestSandboxAdjudicate_AcceptsDecimalWeeks: the operated $populate engine emits the weeks as
// valueDecimal (HAPI maps a CQL numeric to valueDecimal). The parser must read it identically
// to valueInteger — without this, native weeks defaults to 0 and a 6-week approval wrongly denies.
func TestSandboxAdjudicate_AcceptsDecimalWeeks(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","item":[{"linkId":"conservative-therapy-weeks","answer":[{"valueDecimal":6}]}]}`)
	dec, err := SandboxAdjudicate(qr, true, testNow, nil)
	if err != nil {
		t.Fatalf("SandboxAdjudicate: %v", err)
	}
	if dec.Outcome != PASApproved {
		t.Fatalf("decimal weeks=6 → %v, want PASApproved", dec.Outcome)
	}
	// And a sub-threshold decimal denies.
	qr4 := []byte(`{"resourceType":"QuestionnaireResponse","item":[{"linkId":"conservative-therapy-weeks","answer":[{"valueDecimal":4}]}]}`)
	dec4, err := SandboxAdjudicate(qr4, true, testNow, nil)
	if err != nil {
		t.Fatalf("SandboxAdjudicate(4): %v", err)
	}
	if dec4.Outcome != PASDenied {
		t.Fatalf("decimal weeks=4 → %v, want PASDenied", dec4.Outcome)
	}
}
