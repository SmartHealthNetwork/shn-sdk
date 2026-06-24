package shnsdk

import (
	"encoding/json"
	"fmt"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// CRD-leg terminology systems + US Core profiles. Ported byte-for-byte from
// internal/fhirmap so the SDK-built ServiceRequest/Coverage are wire-identical to
// the substrate's (test/sdkparity/order_parity_test.go). RunPriorAuth's CRD leg
// builds these itself — the order (procedure/diagnosis) is a dev-VISIBLE input
// (PriorAuthRequest), never conjured inside the orchestrator.
const (
	systemCPT   = "http://www.ama-assn.org/go/cpt"
	systemICD10 = "http://hl7.org/fhir/sid/icd-10-cm"

	// US Core 6.1.0 profiles pinned via meta.profile (the substrate's resources
	// genuinely conform; a plain non-IG validator strips meta.profile). These match
	// internal/fhirmap's profile constants byte-for-byte.
	profileUSCoreServiceRequest = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-servicerequest"
	profileUSCoreCoverage       = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-coverage"

	// systemSubscriberRelationship carries Coverage.relationship (us-core-coverage
	// requires min=1). systemV2Identifier carries the v2-0203 "MB" (Member Number)
	// identifier type us-core-15 requires.
	systemSubscriberRelationship = "http://terminology.hl7.org/CodeSystem/subscriber-relationship"
	systemV2Identifier           = "http://terminology.hl7.org/CodeSystem/v2-0203"
)

// BuildServiceRequest builds a DRAFT order (CDS Hooks order-select context): a FHIR
// R4 ServiceRequest with status "draft", intent "order", the given CPT procedure
// code + display, the ICD-10-CM diagnosis as reasonCode, and the patient subject.
// Reimplements internal/fhirmap.BuildServiceRequest standalone (no internal/ import);
// test/sdkparity asserts byte-identity with the substrate for the same inputs.
func BuildServiceRequest(cptCode, display, dxCode, patientRef string) ([]byte, error) {
	sr := fhir.ServiceRequest{
		Meta:   &fhir.Meta{Profile: []string{profileUSCoreServiceRequest}},
		Status: fhir.RequestStatusDraft,
		Intent: fhir.RequestIntentOrder,
		Code: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{
					System:  strPtr(systemCPT),
					Code:    strPtr(cptCode),
					Display: strPtr(display),
				},
			},
		},
		ReasonCode: []fhir.CodeableConcept{
			{
				Coding: []fhir.Coding{
					{
						System: strPtr(systemICD10),
						Code:   strPtr(dxCode),
					},
				},
			},
		},
		Subject: fhir.Reference{
			Reference: strPtr(patientRef),
		},
	}
	return json.Marshal(sr)
}

// BuildCoverage builds a valid R4 Coverage conforming to us-core-coverage (US Core
// 6.1.0): status "active", the given beneficiary (Patient) reference, a single payor
// referencing the payer Organization, a "self" subscriber relationship (min=1), and
// a member-number identifier (v2-0203 "MB" type) carrying coverageRef (satisfies the
// us-core-15 invariant). meta.profile pins the US Core profile. Reimplements
// internal/fhirmap.BuildCoverage standalone; test/sdkparity asserts byte-identity.
func BuildCoverage(patientRef, coverageRef string) ([]byte, error) {
	cov := fhir.Coverage{
		Meta:        &fhir.Meta{Profile: []string{profileUSCoreCoverage}},
		Status:      fhir.FinancialResourceStatusCodesActive,
		Beneficiary: fhir.Reference{Reference: strPtr(patientRef)},
		Payor:       []fhir.Reference{{Reference: strPtr("Organization/payer")}},
		Relationship: &fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System: strPtr(systemSubscriberRelationship),
				Code:   strPtr("self"),
			}},
		},
		Identifier: []fhir.Identifier{{
			Type: &fhir.CodeableConcept{
				Coding: []fhir.Coding{{
					System: strPtr(systemV2Identifier),
					Code:   strPtr("MB"),
				}},
			},
			System: strPtr("urn:shn:coverage"),
			Value:  strPtr(coverageRef),
		}},
	}
	// fhir.Coverage's MarshalJSON injects resourceType itself.
	return json.Marshal(cov)
}

// ParseServiceRequestProcedure extracts the CPT code AND its display from a
// ServiceRequest JSON (the first code.coding[] with the CPT system
// http://www.ama-assn.org/go/cpt). display is "" when that coding carries no display
// (display is optional in FHIR). It errors if the resourceType is not ServiceRequest
// or the CPT coding is absent. The display lets a responder source the PA-decision
// EOB's productOrService.display from the ACTUAL service (FR-28) rather than a
// hardcoded value. ParseServiceRequestCPT delegates to it. Ported standalone;
// behavior parity proven by test/sdkparity/crd_parity_test.go.
func ParseServiceRequestProcedure(data []byte) (code, display string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", "", err
	}
	if probe.ResourceType != "ServiceRequest" {
		return "", "", fmt.Errorf("shnsdk: expected ServiceRequest, got %q", probe.ResourceType)
	}

	var sr fhir.ServiceRequest
	if err := json.Unmarshal(data, &sr); err != nil {
		return "", "", err
	}
	if sr.Code == nil || len(sr.Code.Coding) == 0 {
		return "", "", fmt.Errorf("shnsdk: ServiceRequest missing code.coding")
	}
	for _, c := range sr.Code.Coding {
		if c.System != nil && *c.System == systemCPT {
			if c.Code != nil {
				d := ""
				if c.Display != nil {
					d = *c.Display
				}
				return *c.Code, d, nil
			}
		}
	}
	return "", "", fmt.Errorf("shnsdk: ServiceRequest has no CPT coding (system %q)", systemCPT)
}

// ParseServiceRequestCPT extracts the CPT code from a ServiceRequest JSON
// (code.coding[0] with system http://www.ama-assn.org/go/cpt).
// It errors if the resourceType is not ServiceRequest or the CPT coding is absent.
// Delegates to ParseServiceRequestProcedure (which also recovers the display).
func ParseServiceRequestCPT(data []byte) (string, error) {
	code, _, err := ParseServiceRequestProcedure(data)
	return code, err
}

// ParseServiceRequestSubject extracts subject.reference from a ServiceRequest JSON
// (e.g. "Patient/MBR-COVERED"). It errors if the resourceType is not ServiceRequest
// or the subject reference is absent. Used to bind the token subject to the
// order-select patient (H2). PORTED standalone from
// internal/fhirmap.ParseServiceRequestSubject; behavior parity proven by
// test/sdkparity/crd_parity_test.go.
func ParseServiceRequestSubject(data []byte) (string, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Subject      struct {
			Reference string `json:"reference"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	if probe.ResourceType != "ServiceRequest" {
		return "", fmt.Errorf("shnsdk: expected ServiceRequest, got %q", probe.ResourceType)
	}
	if probe.Subject.Reference == "" {
		return "", fmt.Errorf("shnsdk: ServiceRequest missing subject.reference")
	}
	return probe.Subject.Reference, nil
}

// ParseCoverageBeneficiary extracts beneficiary.reference from a Coverage JSON
// (e.g. "Patient/MBR-COVERED"). It errors if the resourceType is not Coverage or
// the beneficiary reference is absent. Used to bind the token subject to the
// order-select patient (H2). PORTED standalone from
// internal/fhirmap.ParseCoverageBeneficiary; behavior parity proven by
// test/sdkparity/crd_parity_test.go.
func ParseCoverageBeneficiary(data []byte) (string, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Beneficiary  struct {
			Reference string `json:"reference"`
		} `json:"beneficiary"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	if probe.ResourceType != "Coverage" {
		return "", fmt.Errorf("shnsdk: expected Coverage, got %q", probe.ResourceType)
	}
	if probe.Beneficiary.Reference == "" {
		return "", fmt.Errorf("shnsdk: Coverage missing beneficiary.reference")
	}
	return probe.Beneficiary.Reference, nil
}
