package shnsdk

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// BuildEligibilityRequest constructs a FHIR R4 CoverageEligibilityRequest for the
// member, ordering NPI, and date of service (now). Returns FHIR JSON. PORTED
// standalone from internal/fhirmap.BuildEligibilityRequest with the SAME field
// shapes so the wire resource is identical — proven by the cross-module parity
// test (test/sdkparity/fhir_parity_test.go), which feeds an SDK-built request to
// the substrate parser.
func BuildEligibilityRequest(memberID, npi string, now time.Time) ([]byte, error) {
	created := now.UTC().Format(time.RFC3339)
	patientRef := "Patient/" + memberID
	providerRef := "Practitioner/" + npi
	cer := fhir.CoverageEligibilityRequest{
		Status:   fhir.FinancialResourceStatusCodesActive,
		Purpose:  []fhir.EligibilityRequestPurpose{fhir.EligibilityRequestPurposeBenefits},
		Patient:  fhir.Reference{Reference: strPtr(patientRef)},
		Created:  created,
		Insurer:  fhir.Reference{Reference: strPtr("Organization/payer")},
		Provider: &fhir.Reference{Reference: strPtr(providerRef)},
	}
	return json.Marshal(cer)
}

// ParseEligibilityResponse returns whether coverage is in force and, if not, the
// reason carried in the disposition of a CoverageEligibilityResponse. PORTED
// standalone from internal/fhirmap.ParseEligibilityResponse; the parity test
// proves it agrees with the substrate parser on a substrate-built response (both
// covered and not-covered branches).
func ParseEligibilityResponse(b []byte) (covered bool, reason string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err = json.Unmarshal(b, &probe); err != nil {
		return false, "", err
	}
	if probe.ResourceType != "CoverageEligibilityResponse" {
		return false, "", fmt.Errorf("shnsdk: expected CoverageEligibilityResponse, got %q", probe.ResourceType)
	}
	var resp fhir.CoverageEligibilityResponse
	if err = json.Unmarshal(b, &resp); err != nil {
		return false, "", err
	}
	if len(resp.Insurance) > 0 && resp.Insurance[0].Inforce != nil {
		covered = *resp.Insurance[0].Inforce
	}
	if !covered && resp.Disposition != nil {
		reason = *resp.Disposition
	}
	return covered, reason, nil
}

const (
	// profileUSCoreDiagnosticReportNote — US Core 6.1.0 DiagnosticReport Note profile.
	// Mirrors internal/fhirmap.profileUSCoreDiagnosticReportNote.
	profileUSCoreDiagnosticReportNote = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-diagnosticreport-note"
	// systemV2DiagnosticService — HL7 v2-0074 code system for the required category.
	systemV2DiagnosticService = "http://terminology.hl7.org/CodeSystem/v2-0074"
	// effectiveDateUC04 — fixed effective date for UC-04 supplemental DiagnosticReports
	// (deterministic across runs; no clock). Mirrors internal/fhirmap.effectiveDateUC04.
	effectiveDateUC04 = "2026-05-15"
)

// BuildDiagnosticReport builds a US Core DiagnosticReport (Note profile) for the UC-04
// supplemental operative report (FR-32). Reimplements internal/fhirmap.BuildDiagnosticReport
// standalone; test/sdkparity asserts byte-identity. systemCPT is the package const from
// order.go. Fixed effectiveDateTime ⇒ deterministic (no clock).
func BuildDiagnosticReport(id, patientRef, cptCode, display string) ([]byte, error) {
	effectiveDate := effectiveDateUC04
	dr := fhir.DiagnosticReport{
		Id:     strPtr(id),
		Meta:   &fhir.Meta{Profile: []string{profileUSCoreDiagnosticReportNote}},
		Status: fhir.DiagnosticReportStatusFinal,
		Category: []fhir.CodeableConcept{
			{
				Coding: []fhir.Coding{{
					System:  strPtr(systemV2DiagnosticService),
					Code:    strPtr("RAD"),
					Display: strPtr("Radiology"),
				}},
			},
		},
		Code: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System:  strPtr(systemCPT),
				Code:    strPtr(cptCode),
				Display: strPtr(display),
			}},
		},
		Subject:           &fhir.Reference{Reference: strPtr(patientRef)},
		EffectiveDateTime: &effectiveDate,
	}
	// fhir.DiagnosticReport.MarshalJSON injects resourceType automatically.
	return json.Marshal(dr)
}

func strPtr(s string) *string { return &s }
