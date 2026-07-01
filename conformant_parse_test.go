package shnsdk

import (
	"testing"
	"time"
)

// demoConformantOrderSelect builds the conformant CRD order-select request the Originator
// (RunPriorAuth) sends for the demo persona (MBR-COVERED, CPT 72148), so the builder↔parser
// contract is exercised against the EXACT bytes the Responder receives.
func demoConformantOrderSelect(t *testing.T) []byte {
	t.Helper()
	patientRef := "Patient/MBR-COVERED"
	coverageRef := "Coverage/MBR-COVERED"
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	covJSON, err := BuildCoverageWithPayer(patientRef, coverageRef, CMSPayerIdentity)
	if err != nil {
		t.Fatalf("BuildCoverageWithPayer: %v", err)
	}
	req, err := BuildConformantOrderSelectRequest(srJSON, covJSON, patientRef)
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest: %v", err)
	}
	return req
}

// demoConformantClaim builds the conformant PAS $submit Claim Bundle the Originator sends for
// the demo persona, with an answered QR (sandbox lumbar questionnaire).
func demoConformantClaim(t *testing.T) []byte {
	t.Helper()
	patientRef := "Patient/MBR-COVERED"
	coverageRef := "Coverage/MBR-COVERED"
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	qrJSON, err := FillQuestionnaire(SandboxLumbarQuestionnaire(), ClinicalContext{
		ConservativeTherapyWeeks: 8,
	}, QRContext{
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	bundle, err := BuildConformantClaimBundle(ConformantClaimInputs{
		QR:          qrJSON,
		SR:          srJSON,
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		Corr:        "corr-claim-demo-1",
		Created:     time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
		Payer:       CMSPayerIdentity,
	})
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	return bundle
}

// TestParseConformantOrderSelectSR proves the builder↔parser contract: the conformant CRD
// request the Originator builds yields the ServiceRequest the Responder reads (CPT 72148).
func TestParseConformantOrderSelectSR(t *testing.T) {
	req := demoConformantOrderSelect(t)

	srJSON, ok := parseConformantOrderSelectSR(req)
	if !ok {
		t.Fatal("parseConformantOrderSelectSR: ok=false, want true")
	}
	if !isConformantResourceType(srJSON, "ServiceRequest") {
		t.Fatalf("extracted resource is not a ServiceRequest: %s", srJSON)
	}
	cpt, err := ParseServiceRequestCPT(srJSON)
	if err != nil {
		t.Fatalf("ParseServiceRequestCPT: %v", err)
	}
	if cpt != "72148" {
		t.Errorf("cpt = %q, want 72148", cpt)
	}
	subj, err := ParseServiceRequestSubject(srJSON)
	if err != nil {
		t.Fatalf("ParseServiceRequestSubject: %v", err)
	}
	if subj != "Patient/MBR-COVERED" {
		t.Errorf("subject = %q, want Patient/MBR-COVERED", subj)
	}
}

// TestParseConformantOrderSelectSR_NoSR proves the no-ServiceRequest and malformed paths.
func TestParseConformantOrderSelectSR_NoSR(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"not json", []byte("not json")},
		{"empty draftOrders", []byte(`{"context":{"draftOrders":{"resourceType":"Bundle","type":"collection","entry":[]}}}`)},
		{"no SR entry", []byte(`{"context":{"draftOrders":{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Patient","id":"x"}}]}}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sr, ok := parseConformantOrderSelectSR(tc.body); ok {
				t.Errorf("ok=true, want false (sr=%s)", sr)
			}
		})
	}
}

// TestParseConformantClaimSubmit proves the builder↔parser contract for the PAS $submit bundle:
// the bound member is the demo persona, the QR is extracted, and (no DR in the approve bundle).
func TestParseConformantClaimSubmit(t *testing.T) {
	bundle := demoConformantClaim(t)

	cs, ok := parseConformantClaimSubmit(bundle)
	if !ok {
		t.Fatal("parseConformantClaimSubmit: ok=false, want true")
	}
	if cs.claimPatient != "Patient/MBR-COVERED" {
		t.Errorf("claimPatient = %q, want Patient/MBR-COVERED", cs.claimPatient)
	}
	if conformantBundleMember(cs) != "MBR-COVERED" {
		t.Errorf("member = %q, want MBR-COVERED", conformantBundleMember(cs))
	}
	if cs.qrJSON == nil {
		t.Error("qrJSON = nil, want the QuestionnaireResponse")
	} else if !isConformantResourceType(cs.qrJSON, "QuestionnaireResponse") {
		t.Errorf("qrJSON is not a QuestionnaireResponse: %s", cs.qrJSON)
	}
	if cs.hasDR {
		t.Error("hasDR = true, want false (approve bundle carries no DiagnosticReport)")
	}
}

// TestParseConformantClaimSubmit_Rejects proves the malformed / non-Bundle / no-Claim.patient
// rejection rows (→ 400 at the caller).
func TestParseConformantClaimSubmit_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"not json", []byte("not json")},
		{"not a Bundle", []byte(`{"resourceType":"Claim","patient":{"reference":"Patient/x"}}`)},
		{"no Claim entry", []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Patient","id":"x"}}]}`)},
		{"Claim without patient", []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim"}}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if cs, ok := parseConformantClaimSubmit(tc.body); ok {
				t.Errorf("ok=true, want false (cs=%+v)", cs)
			}
		})
	}
}

// TestParseConformantClaimSubmit_HasDR proves the DiagnosticReport-present branch sets hasDR.
func TestParseConformantClaimSubmit_HasDR(t *testing.T) {
	body := []byte(`{"resourceType":"Bundle","entry":[
		{"resource":{"resourceType":"Claim","patient":{"reference":"Patient/MBR-COVERED"}}},
		{"resource":{"resourceType":"DiagnosticReport","id":"dr1","subject":{"reference":"Patient/MBR-COVERED"}}}
	]}`)
	cs, ok := parseConformantClaimSubmit(body)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if !cs.hasDR {
		t.Error("hasDR = false, want true")
	}
}
