package shnsdk

import (
	"encoding/json"

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
