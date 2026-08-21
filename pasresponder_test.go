package shnsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

// -- nested QuestionnaireResponse items --

// pasNestedTestQRs returns three QRs carrying IDENTICAL clinical content in
// three shapes: flat, nested under a group item (QuestionnaireResponse.item.item),
// and nested under another answer (QuestionnaireResponse.item.answer.item). Both
// nested axes contentReference back to QuestionnaireResponse.item, so all three
// describe the same clinical facts and must adjudicate the same way.
func pasNestedTestQRs(weeks int, priorSurgery bool) (flat, nestedGroup, nestedAnswer []byte) {
	inner := fmt.Sprintf(
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":%d}]},`+
			`{"linkId":"prior-surgery","answer":[{"valueBoolean":%t}]},`+
			`{"linkId":"high-disability","answer":[{"valueBoolean":false}]}`,
		weeks, priorSurgery)

	flat = []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` + inner + `]}`)
	nestedGroup = []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"grp-1","item":[` + inner + `]}]}`)
	nestedAnswer = []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"parent-q","answer":[{"valueBoolean":true,"item":[` + inner + `]}]}]}`)
	return flat, nestedGroup, nestedAnswer
}

// TestSandboxAdjudicate_NestedQRMatchesFlat is P1 and P2. A nested QR carrying
// the same clinical content as a flat one must adjudicate IDENTICALLY.
//
// Before the walker recursed, nested items were discarded at json.Unmarshal, so
// every input took its zero value — weeks 0, all flags false, no attestation —
// and the responder returned a decision the clinical content never supported. It
// did not error. That is strictly worse than losing an element: it silently
// changes an outcome.
//
// The expectation is the FLAT result, never a hardcoded outcome: pinning
// literals would let both sides drift together and still pass.
func TestSandboxAdjudicate_NestedQRMatchesFlat(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		weeks         int
		priorSurgery  bool
		hasDiagReport bool
	}{
		{"approve-shaped", 6, false, false},
		{"deny-shaped", 2, false, false},
		{"pend-shaped", 6, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flat, nestedGroup, nestedAnswer := pasNestedTestQRs(c.weeks, c.priorSurgery)

			want, err := SandboxAdjudicate(flat, c.hasDiagReport, now, bytes.NewReader([]byte("SEEDNG")))
			if err != nil {
				t.Fatalf("flat: %v", err)
			}
			for _, n := range []struct {
				axis string
				qr   []byte
			}{{"item.item", nestedGroup}, {"item.answer.item", nestedAnswer}} {
				got, err := SandboxAdjudicate(n.qr, c.hasDiagReport, now, bytes.NewReader([]byte("SEEDNG")))
				if err != nil {
					t.Fatalf("%s: %v", n.axis, err)
				}
				if got.Outcome != want.Outcome {
					t.Errorf("%s: outcome = %v, want %v (the flat result for identical clinical content)",
						n.axis, got.Outcome, want.Outcome)
				}
				if !reflect.DeepEqual(got.NeededItems, want.NeededItems) {
					t.Errorf("%s: neededItems = %v, want %v", n.axis, got.NeededItems, want.NeededItems)
				}
				if got.PreAuthRef != want.PreAuthRef || got.ValidUntil != want.ValidUntil {
					t.Errorf("%s: preAuthRef/validUntil = %q/%q, want %q/%q",
						n.axis, got.PreAuthRef, got.ValidUntil, want.PreAuthRef, want.ValidUntil)
				}
			}
		})
	}
}

// TestSandboxAdjudicate_NestedAttestation is P3: the clinician-attestation path
// reads item.extension, not item.answer, so it needs its own nested case — a
// walker could reach nested answers and still miss nested item extensions.
func TestSandboxAdjudicate_NestedAttestation(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	const attestedItem = `{"linkId":"functional-status-oswestry",` +
		`"extension":[{"url":"http://smarthealth.network/fhir/StructureDefinition/clinician-attestation",` +
		`"extension":[{"url":"npi","valueString":"1234567893"},{"url":"text","valueString":"reviewed"},{"url":"date","valueDate":"2026-08-18"}]}],` +
		`"answer":[{"valueString":"severe"}]}`
	inner := `{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` + attestedItem

	flat := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` + inner + `]}`)
	nested := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"grp-1","item":[` + inner + `]}]}`)

	want, err := SandboxAdjudicate(flat, false, now, bytes.NewReader([]byte("SEEDAT")))
	if err != nil {
		t.Fatalf("flat: %v", err)
	}
	if want.Outcome != PASApproved {
		t.Fatalf("precondition: flat attested QR should approve, got %v — the fixture is wrong, not the walker", want.Outcome)
	}
	got, err := SandboxAdjudicate(nested, false, now, bytes.NewReader([]byte("SEEDAT")))
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	if got.Outcome != want.Outcome {
		t.Fatalf("nested attested QR outcome = %v, want %v", got.Outcome, want.Outcome)
	}
}

// TestSandboxAdjudicate_RepeatingGroupIsAmbiguous is P6. FHIR's que-2 makes
// linkIds unique within a QUESTIONNAIRE, not within a QuestionnaireResponse: a
// group with repeats=true yields one QR item per occurrence, all sharing a
// linkId. Those duplicates were unreachable while nested items were dropped at
// unmarshal — flattening makes them reachable for the first time.
//
// The switch that reads adjudication inputs is last-write-wins, so without this
// guard a QR recording 2 weeks of conservative therapy in one occurrence and 8 in
// another would adjudicate on whichever was visited last. Refusing is the only
// safe answer for a prior authorization.
func TestSandboxAdjudicate_RepeatingGroupIsAmbiguous(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[
		{"linkId":"therapy-episode","item":[{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":2}]}]},
		{"linkId":"therapy-episode","item":[{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":8}]}]}
	]}`)

	_, err := SandboxAdjudicate(qr, false, now, bytes.NewReader([]byte("SEEDDU")))
	if err == nil {
		t.Fatal("adjudicated a QR with two conservative-therapy-weeks items instead of refusing")
	}
	if !strings.Contains(err.Error(), "conservative-therapy-weeks") {
		t.Errorf("error does not name the ambiguous linkId: %v", err)
	}
}

// TestSandboxAdjudicate_RepeatingGroupElsewhereIsFine is P6's restraint half: a
// repeating group the rules do not read is legal FHIR, none of this responder's
// business, and must NOT become an error.
func TestSandboxAdjudicate_RepeatingGroupElsewhereIsFine(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[
		{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},
		{"linkId":"unrelated-repeat","answer":[{"valueString":"a"}]},
		{"linkId":"unrelated-repeat","answer":[{"valueString":"b"}]}
	]}`)

	got, err := SandboxAdjudicate(qr, false, now, bytes.NewReader([]byte("SEEDDU")))
	if err != nil {
		t.Fatalf("a repeating group the rules do not read must not be an error: %v", err)
	}
	if got.Outcome != PASApproved {
		t.Fatalf("outcome = %v, want PASApproved", got.Outcome)
	}
}

// -- Sole-adjudicator behaviour rows (ported from the retired substrate-side twin) --
//
// internal/pas.Adjudicate was a hand-maintained twin of SandboxAdjudicate with
// no production caller; the live prior-auth path is gateway/engine/adjudicator.go
// → SandboxAdjudicate. When the twin was deleted its behaviour rows moved here
// rather than disappearing, so every rule the sandbox adjudicator enforces
// (R1 operative report, R2 clinician attestation FR-16, R3 patient attestation
// FR-27, the lumbar-MRI baseline) keeps a test that fails when the rule does.

// TestSandboxAdjudicate_MissingWeeksItemDenies: a QR with no
// conservative-therapy-weeks item reads weeks as 0 → denied, never approved by
// a zero-value accident in the other direction.
func TestSandboxAdjudicate_MissingWeeksItemDenies(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"prior-imaging","answer":[{"valueBoolean":true}]}]}`)
	dec, err := SandboxAdjudicate(qr, false, testNow, nil)
	if err != nil || dec.Outcome != PASDenied {
		t.Fatalf("missing weeks item: outcome=%v err=%v, want PASDenied", dec.Outcome, err)
	}
}

// TestSandboxAdjudicate_OperativeReportClearsPriorSurgeryPend (R1 clears):
// prior-surgery=true WITH hasDiagnosticReport=true → approved.
func TestSandboxAdjudicate_OperativeReportClearsPriorSurgeryPend(t *testing.T) {
	dec, err := SandboxAdjudicate(priorSurgeryQRJSON(), true, testNow, nil)
	if err != nil || dec.Outcome != PASApproved || dec.PreAuthRef == "" {
		t.Fatalf("prior surgery + operative report: outcome=%v ref=%q err=%v, want PASApproved", dec.Outcome, dec.PreAuthRef, err)
	}
}

// TestSandboxAdjudicate_LumbarBaselineApproves: no pend trigger, weeks>=6, an
// unrelated answered item → approved (the lumbar-MRI auto-approval baseline
// must not regress as rules are added).
func TestSandboxAdjudicate_LumbarBaselineApproves(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"prior-imaging","answer":[{"valueBoolean":true}]}]}`)
	dec, err := SandboxAdjudicate(qr, false, testNow, nil)
	if err != nil || dec.Outcome != PASApproved {
		t.Fatalf("lumbar-MRI baseline must approve; outcome=%v err=%v", dec.Outcome, err)
	}
}

// TestSandboxAdjudicate_AttestationWithoutAnswerPends (FR-16): the clinician
// attestation extension on an ANSWERLESS functional-status item must still
// pend — metadata alone approves nothing; the attestation has to cover a value.
func TestSandboxAdjudicate_AttestationWithoutAnswerPends(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` +
		`{"linkId":"functional-status-oswestry","extension":[{"url":"` + ClinicianAttestationExt + `"}]}]}`)
	dec, err := SandboxAdjudicate(qr, false, testNow, nil)
	if err != nil || dec.Outcome != PASPended {
		t.Fatalf("attestation without answer must pend; outcome=%v err=%v", dec.Outcome, err)
	}
}

// attestedOswestryItem returns a functional-status-oswestry item with a
// non-empty answer and a clinician attestation whose NPI/text/date
// sub-extensions are individually omittable — FR-16 requires all three.
func attestedOswestryItem(withNPI, withText, withDate bool) string {
	var sub []string
	if withNPI {
		sub = append(sub, `{"url":"npi","valueString":"1999999999"}`)
	}
	if withText {
		sub = append(sub, `{"url":"text","valueString":"I attest these are my findings."}`)
	}
	if withDate {
		sub = append(sub, `{"url":"date","valueDate":"2026-06-05"}`)
	}
	return `{"linkId":"functional-status-oswestry","answer":[{"valueString":"42"}],"extension":[{"url":"` +
		ClinicianAttestationExt + `","extension":[` + strings.Join(sub, ",") + `]}]}`
}

// TestSandboxAdjudicate_WellFormedAttestationApproves (FR-16): a non-empty
// answer with NPI+text+date approves a high-disability claim.
func TestSandboxAdjudicate_WellFormedAttestationApproves(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` +
		attestedOswestryItem(true, true, true) + `]}`)
	dec, err := SandboxAdjudicate(qr, false, testNow, nil)
	if err != nil || dec.Outcome != PASApproved {
		t.Fatalf("attested answer must approve; outcome=%v err=%v", dec.Outcome, err)
	}
}

// TestSandboxAdjudicate_MalformedAttestationPends (FR-16): an attestation
// missing ANY of NPI/text/date is malformed and must not approve.
func TestSandboxAdjudicate_MalformedAttestationPends(t *testing.T) {
	for _, tc := range []struct {
		name            string
		npi, text, date bool
	}{
		{"no npi", false, true, true},
		{"no text", true, false, true},
		{"no date", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
				`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
				`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` +
				attestedOswestryItem(tc.npi, tc.text, tc.date) + `]}`)
			dec, err := SandboxAdjudicate(qr, false, testNow, nil)
			if err != nil || dec.Outcome != PASPended {
				t.Fatalf("%s: malformed attestation must pend; outcome=%v err=%v", tc.name, dec.Outcome, err)
			}
		})
	}
}

// TestSandboxAdjudicate_PatientReportedPendsThenApproves (R3, FR-27):
// patient-reported-required=true with weeks>=6 and NO patient signature →
// pended with a needed item; the same QR with the patient Author's Signature on
// functional-status-oswestry (the real PHG-built item) → approved.
func TestSandboxAdjudicate_PatientReportedPendsThenApproves(t *testing.T) {
	qrPended := []byte(`{"resourceType":"QuestionnaireResponse","item":[
		{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},
		{"linkId":"patient-reported-required","answer":[{"valueBoolean":true}]},
		{"linkId":"functional-status-oswestry","answer":[{"valueString":"42"}]}]}`)
	dec, err := SandboxAdjudicate(qrPended, false, testNow, nil)
	if err != nil {
		t.Fatalf("adjudicate pended: %v", err)
	}
	if dec.Outcome != PASPended || len(dec.NeededItems) == 0 {
		t.Fatalf("got %v needed=%v; want PASPended with a needed item", dec.Outcome, dec.NeededItems)
	}
	item, err := BuildPatientAttestedItem("functional-status-oswestry", "42", "Patient/MBR-PATIENT-REPORTED", "2026-06-04")
	if err != nil {
		t.Fatalf("BuildPatientAttestedItem: %v", err)
	}
	qrApproved := []byte(`{"resourceType":"QuestionnaireResponse","item":[
		{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},
		{"linkId":"patient-reported-required","answer":[{"valueBoolean":true}]},` + string(item) + `]}`)
	dec2, err := SandboxAdjudicate(qrApproved, false, testNow, nil)
	if err != nil {
		t.Fatalf("adjudicate approved: %v", err)
	}
	if dec2.Outcome != PASApproved || dec2.PreAuthRef == "" {
		t.Fatalf("got %v preAuth=%q; want PASApproved with an auth number", dec2.Outcome, dec2.PreAuthRef)
	}
}

// TestSandboxAdjudicate_ClinicianAttestationDoesNotSatisfyPatientPend is the
// forged-source independence guard (FR-27): where the policy requires a
// PATIENT signature, a CLINICIAN attestation (the clinician-amend item, via
// BuildManualAttestedItem) must still pend. The two attestation kinds are
// checked independently; a provider presenting a clinician item in place of the
// patient's cannot clear the patient-reported pend. This is the sole
// enforcement point — the PHG builds the patient attestation request-scoped and
// persists nothing (OWD-8), so the only forgery vector is a provider-built fake
// ClaimUpdate, caught here.
func TestSandboxAdjudicate_ClinicianAttestationDoesNotSatisfyPatientPend(t *testing.T) {
	clinicianItem, err := BuildManualAttestedItem("functional-status-oswestry", "42",
		Attestation{NPI: "1999999999", Text: "I attest these are my clinical findings.", When: "2026-06-04"})
	if err != nil {
		t.Fatalf("BuildManualAttestedItem: %v", err)
	}
	qrForged := []byte(`{"resourceType":"QuestionnaireResponse","item":[
		{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},
		{"linkId":"patient-reported-required","answer":[{"valueBoolean":true}]},` +
		string(clinicianItem) + `]}`)
	dec, err := SandboxAdjudicate(qrForged, false, testNow, nil)
	if err != nil {
		t.Fatalf("forged-source: %v", err)
	}
	if dec.Outcome != PASPended || len(dec.NeededItems) == 0 {
		t.Fatalf("forged-source: got %v needed=%v; want PASPended — a clinician attestation must NOT satisfy a patient-reported pend", dec.Outcome, dec.NeededItems)
	}
}

// -- Ambiguity refusals, swept over every read linkId at every depth --

// adjudicationShapes returns the same clinical content in four QR shapes:
// flat, nested under a group item (item.item), nested under another item's
// answer (item.answer.item), and three levels deep alternating the two axes.
// Every axis contentReferences back to QuestionnaireResponse.item, so all four
// describe the same facts and the adjudicator must treat them identically.
//
// "deep-alternating" is the only row that discriminates a ONE-LEVEL flatten: a
// walk descending a single level per axis satisfies the other three and still
// loses everything below depth 1. Its path is group → answer → group → content,
// so it also proves the walk switches axes mid-descent.
func adjudicationShapes(flatItemsJSON string) map[string][]byte {
	return map[string][]byte{
		"flat": []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` + flatItemsJSON + `]}`),
		"nested-group": []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"shape-grp","item":[` + flatItemsJSON + `]}]}`),
		"nested-answer": []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"shape-parent","answer":[{"valueBoolean":true,"item":[` + flatItemsJSON + `]}]}]}`),
		"deep-alternating": []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"shape-deep-grp","item":[` +
			`{"linkId":"shape-deep-q","answer":[{"valueBoolean":true,"item":[` +
			`{"linkId":"shape-deep-grp2","item":[` + flatItemsJSON + `]}]}]}` +
			`]}]}`),
	}
}

// adjudicationShapeNames is the sweep every ambiguity row runs. One list, so a
// shape cannot be added to adjudicationShapes and silently never swept.
var adjudicationShapeNames = []string{"flat", "nested-group", "nested-answer", "deep-alternating"}

// ambiguityRows is one row per read linkId, driven through BOTH ambiguity
// shapes by the two sweeps below:
//
//	items   — two ITEMS share the linkId (a repeating group); the consuming
//	          switch would otherwise be last-write-wins.
//	answers — ONE item carries two ANSWERS (a repeating item, cardinality 0..*);
//	          the rules would otherwise read Answer[0] and discard the rest.
//
// Both are "a decision derived from data the payload does not actually assert",
// and both must be refused, naming the linkId. The values deliberately
// conflict so a guard that compared values instead of counting would still be
// exercised — but counting is the rule: two answers on an item the rules
// model as single-valued is ambiguous regardless of whether they agree.
//
// Hand-listed, and checked below against the adjudicator's own read set, so a
// sixth read linkId added to the rules without a row here reds the sweep
// instead of going untested.
var ambiguityRows = []struct{ linkID, a, b string }{
	{"conservative-therapy-weeks", `{"valueInteger":2}`, `{"valueInteger":8}`},
	{"prior-surgery", `{"valueBoolean":true}`, `{"valueBoolean":false}`},
	{"high-disability", `{"valueBoolean":true}`, `{"valueBoolean":false}`},
	{"patient-reported-required", `{"valueBoolean":true}`, `{"valueBoolean":false}`},
	{"functional-status-oswestry", `{"valueString":"mild"}`, `{"valueString":"severe"}`},
}

// TestSandboxAdjudicate_AmbiguityRowsCoverTheReadSet pins the hand-listed rows
// to the adjudicator's read set in both directions.
func TestSandboxAdjudicate_AmbiguityRowsCoverTheReadSet(t *testing.T) {
	listed := map[string]bool{}
	for _, r := range ambiguityRows {
		listed[r.linkID] = true
	}
	if !reflect.DeepEqual(listed, sandboxAdjudicationReadLinkIDs) {
		t.Fatalf("ambiguityRows %v != adjudication read set %v — every read linkId needs an ambiguity row", listed, sandboxAdjudicationReadLinkIDs)
	}
}

// TestSandboxAdjudicate_DuplicateItemRefusedOnEveryReadLinkID: two items
// sharing a read linkId → refused, naming the linkId, at every depth.
func TestSandboxAdjudicate_DuplicateItemRefusedOnEveryReadLinkID(t *testing.T) {
	for _, r := range ambiguityRows {
		items := `{"linkId":"` + r.linkID + `","answer":[` + r.a + `]},{"linkId":"` + r.linkID + `","answer":[` + r.b + `]}`
		for _, shape := range adjudicationShapeNames {
			t.Run(r.linkID+"/"+shape, func(t *testing.T) {
				dec, err := SandboxAdjudicate(adjudicationShapes(items)[shape], false, testNow, seededReader())
				assertAmbiguityRefusal(t, dec, err, r.linkID, "repeating group")
			})
		}
	}
}

// TestSandboxAdjudicate_MultiAnswerRefusedOnEveryReadLinkID: ONE item carrying
// two answers on a read linkId → refused, naming the linkId, at every depth.
// Before this guard the rules read Answer[0] and silently decided a prior
// authorization on one of two contradicting clinical facts with err == nil.
func TestSandboxAdjudicate_MultiAnswerRefusedOnEveryReadLinkID(t *testing.T) {
	for _, r := range ambiguityRows {
		items := `{"linkId":"` + r.linkID + `","answer":[` + r.a + `,` + r.b + `]}`
		for _, shape := range adjudicationShapeNames {
			t.Run(r.linkID+"/"+shape, func(t *testing.T) {
				dec, err := SandboxAdjudicate(adjudicationShapes(items)[shape], false, testNow, seededReader())
				assertAmbiguityRefusal(t, dec, err, r.linkID, "2 answers")
			})
		}
	}
}

// TestSandboxAdjudicate_MultiAnswerRefusedEvenWhenAnswersAgree: the rule is
// cardinality, not disagreement. Two identical answers on a single-valued
// adjudication input are still a repeating item the rules do not model; picking
// one "because they agree" is a judgment the adjudicator must not make.
func TestSandboxAdjudicate_MultiAnswerRefusedEvenWhenAnswersAgree(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6},{"valueInteger":6}]}]}`)
	dec, err := SandboxAdjudicate(qr, false, testNow, seededReader())
	assertAmbiguityRefusal(t, dec, err, "conservative-therapy-weeks", "2 answers")
}

// TestSandboxAdjudicate_MultiAnswerElsewhereIsFine is the restraint half: a
// multi-answer item the rules do NOT read is legal FHIR, none of this
// adjudicator's business, and must not become an error.
func TestSandboxAdjudicate_MultiAnswerElsewhereIsFine(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"prior-imaging-modalities","answer":[{"valueString":"xray"},{"valueString":"ct"}]}]}`)
	dec, err := SandboxAdjudicate(qr, false, testNow, seededReader())
	if err != nil {
		t.Fatalf("a multi-answer item the rules do not read must not be an error: %v", err)
	}
	if dec.Outcome != PASApproved {
		t.Fatalf("outcome = %v, want PASApproved", dec.Outcome)
	}
}

// assertAmbiguityRefusal asserts the (decision, error) pair is a refusal that
// names linkID and the ambiguity kind, with the package prefix exactly once.
func assertAmbiguityRefusal(t *testing.T, dec PASDecision, err error, linkID, kind string) {
	t.Helper()
	if err == nil {
		t.Fatalf("adjudicated an ambiguous QR (outcome %v) instead of refusing", dec.Outcome)
	}
	if !strings.Contains(err.Error(), linkID) {
		t.Errorf("refusal does not name %q: %v", linkID, err)
	}
	if !strings.Contains(err.Error(), kind) {
		t.Errorf("refusal does not say %q: %v", kind, err)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("refusal does not say it is ambiguous: %v", err)
	}
	// The package prefix appears ONCE: SandboxAdjudicate wraps the inner error,
	// so a prefixed inner error would read "shnsdk: SandboxAdjudicate: shnsdk: …".
	if strings.Count(err.Error(), "shnsdk:") != 1 {
		t.Errorf("refusal repeats its package prefix: %v", err)
	}
	// Not a real decision: the refusal's outcome is PASDenied only as a
	// never-read placeholder, and callers must check err first.
	if dec.Outcome != PASDenied {
		t.Errorf("refusal outcome = %v, want the PASDenied placeholder", dec.Outcome)
	}
}
