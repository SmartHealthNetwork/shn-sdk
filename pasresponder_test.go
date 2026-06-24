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
