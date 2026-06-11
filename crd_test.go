package shnsdk

import (
	"encoding/json"
	"testing"
)

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

// TestParseCards covers both branches + the zero-card error path.
func TestParseCards(t *testing.T) {
	paReq := []byte(`{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"shnPaRequired":true,"questionnaireCanonical":"http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri"}}]}`)
	pa, canon, err := ParseCards(paReq)
	if err != nil {
		t.Fatalf("ParseCards(pa-required): %v", err)
	}
	if !pa || canon != "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri" {
		t.Errorf("pa-required parse = (%v,%q), want (true, the canonical)", pa, canon)
	}

	noPA := []byte(`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"shnPaRequired":false}}]}`)
	pa, canon, err = ParseCards(noPA)
	if err != nil {
		t.Fatalf("ParseCards(no-pa): %v", err)
	}
	if pa || canon != "" {
		t.Errorf("no-pa parse = (%v,%q), want (false, \"\")", pa, canon)
	}

	if _, _, err := ParseCards([]byte(`{"cards":[]}`)); err == nil {
		t.Error("ParseCards should reject a zero-card response")
	}
}
