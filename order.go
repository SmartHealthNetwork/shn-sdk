package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"

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
	// systemHCPCS is the HCPCS Level II procedure code system. PINNED EXACTLY to the
	// payer wire value: http:// (not https://) is load-bearing for the exact-match
	// allowlist below. FR-36: this is the only HCPCS system the EOB validates against.
	systemHCPCS = "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"

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

	// systemSHNCoverage is the SHN member-number identifier system. It is now named in
	// TWO places on the same bundle — the Coverage entry's own MB identifier and the
	// conformant PAS Claim's insurance[0].coverage LOGICAL reference (the
	// logical-reference shape) — and the two MUST agree for that logical reference to
	// resolve inside the bundle, so the string is single-sourced here rather than repeated.
	systemSHNCoverage = "urn:shn:coverage"
)

// BuildServiceRequestCoded builds a DRAFT order (CDS Hooks order-select context) with an
// EXPLICIT procedure code system (CPT or HCPCS): a FHIR R4 ServiceRequest with status
// "draft", intent "order", the given procedure code + display, the ICD-10-CM diagnosis
// as reasonCode, and the patient subject.
//
// Pulled in from gateway/engine/order_build.go —
// deferral D-PCB-1 ("build-side product-coding gap": shnsdk.BuildServiceRequest was
// CPT-system-locked, so the published SDK could not build an HCPCS order — the mirrored
// families E0250/L8000/G0151/J3490 are ALL HCPCS). That gateway-local stub's own comment
// named the closing condition exactly: "When a real partner consumer needs to build
// HCPCS orders via the SDK, lift this into sdk/order.go (additive)" — this round's
// descriptor-driven persona order is that real consumer. test/sdkparity/
// order_coded_parity_test.go now asserts THIS function is byte-identical to
// gateway/engine's own BuildServiceRequestCoded (the deferral's own promised guard),
// closing D-PCB-1 rather than leaving the gateway-local copy as the only implementation.
func BuildServiceRequestCoded(system, code, display, dxCode, patientRef string) ([]byte, error) {
	sr := fhir.ServiceRequest{
		Meta:   &fhir.Meta{Profile: []string{profileUSCoreServiceRequest}},
		Status: fhir.RequestStatusDraft,
		Intent: fhir.RequestIntentOrder,
		Code: &fhir.CodeableConcept{
			Coding: []fhir.Coding{
				{
					System:  strPtr(system),
					Code:    strPtr(code),
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

// BuildServiceRequest builds a DRAFT order (CDS Hooks order-select context): a FHIR
// R4 ServiceRequest with status "draft", intent "order", the given CPT procedure
// code + display, the ICD-10-CM diagnosis as reasonCode, and the patient subject.
// Reimplements internal/fhirmap.BuildServiceRequest standalone (no internal/ import);
// test/sdkparity asserts byte-identity with the substrate for the same inputs. Thin
// CPT-only wrapper over BuildServiceRequestCoded (unchanged signature/behavior — every
// existing caller of THIS function is unaffected by the D-PCB-1 pull-in above).
func BuildServiceRequest(cptCode, display, dxCode, patientRef string) ([]byte, error) {
	return BuildServiceRequestCoded(systemCPT, cptCode, display, dxCode, patientRef)
}

// BuildCoverage builds a valid R4 Coverage conforming to us-core-coverage (US Core
// 6.1.0): status "active", the given beneficiary (Patient) reference, a single payor
// referencing the payer Organization, a "self" subscriber relationship (min=1), and
// a member-number identifier (v2-0203 "MB" type, system urn:shn:coverage) carrying
// memberID — the member's BARE member number, which is what an MB identifier means
// (satisfies the us-core-15 invariant). memberID is NOT a "Coverage/<id>" reference:
// nothing derives a resource reference from this identifier (a reader that needs one
// uses Coverage.id), and a prefixed value is refused rather than stamped, because the
// signature cannot express the change (both spellings are a string). meta.profile pins
// the US Core profile. Reimplements internal/fhirmap.BuildCoverage standalone;
// test/sdkparity asserts byte-identity.
func BuildCoverage(patientRef, memberID string) ([]byte, error) {
	if err := requireBareMemberID(memberID); err != nil {
		return nil, err
	}
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
			System: strPtr(systemSHNCoverage),
			Value:  strPtr(memberID),
		}},
	}
	// fhir.Coverage's MarshalJSON injects resourceType itself.
	return json.Marshal(cov)
}

// requireBareMemberID refuses a "Coverage/"-prefixed member id for the Coverage builders.
// urn:shn:coverage carries the bare member number; before v0.42.0 these builders were
// (mis)called with a reference-shaped "Coverage/<member>" value, and because the parameter
// is a plain string that mistake is invisible to the compiler. Refusing loudly is the only
// way a caller on the old convention finds out. The check is the PREFIX only — a member id
// that happens to contain "/" elsewhere is the payer's business, not ours.
func requireBareMemberID(memberID string) error {
	if strings.HasPrefix(memberID, "Coverage/") {
		return fmt.Errorf("shnsdk: memberID must be the bare member id, not a Coverage/ reference (see the v0.42.0 identifier-semantics release note)")
	}
	return nil
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

// ParseServiceRequestProductCoding extracts the {system, code, display} of the
// order's procedure coding from a ServiceRequest JSON — the first code.coding[]
// whose system is one of the two known procedure systems (AMA-CPT or HCPCS Level II).
// It returns that coding's system so the PA-decision EOB's productOrService coding
// is the ACTUAL ordered system (a HCPCS order yields a HCPCS-system EOB, not a
// CPT-locked one). display is "" when the coding carries none. It errors if the
// resourceType is not ServiceRequest or no {CPT,HCPCS} coding is present — an
// unrecognized system is an honest no-coding (FR-36 allowlist), never a wrong EOB.
// This is the EOB de-personalization source (FR-28); ParseServiceRequestProcedure
// (CPT-only {code,display}) and ParseServiceRequestCPT (CPT-only code) remain the
// distinct CPT-specific tools.
func ParseServiceRequestProductCoding(data []byte) (system, code, display string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", "", "", err
	}
	if probe.ResourceType != "ServiceRequest" {
		return "", "", "", fmt.Errorf("shnsdk: expected ServiceRequest, got %q", probe.ResourceType)
	}
	var sr fhir.ServiceRequest
	if err := json.Unmarshal(data, &sr); err != nil {
		return "", "", "", err
	}
	if sr.Code == nil || len(sr.Code.Coding) == 0 {
		return "", "", "", fmt.Errorf("shnsdk: ServiceRequest missing code.coding")
	}
	for _, c := range sr.Code.Coding {
		if c.System == nil || c.Code == nil {
			continue
		}
		if *c.System == systemCPT || *c.System == systemHCPCS {
			d := ""
			if c.Display != nil {
				d = *c.Display
			}
			return *c.System, *c.Code, d, nil
		}
	}
	return "", "", "", fmt.Errorf("shnsdk: ServiceRequest has no {CPT,HCPCS} procedure coding")
}

// ParseOrderProductCoding extracts {system, code, display} of the procedure/product
// coding from a ServiceRequest (code.coding) OR a DeviceRequest (codeCodeableConcept.coding),
// matching the first AMA-CPT or HCPCS Level II coding. Real provider orders are not all
// ServiceRequest (DME = DeviceRequest); br-payer keys adjudication on the HCPCS code regardless
// of order resource type. Errors on an unsupported resourceType or no {CPT,HCPCS} coding.
func ParseOrderProductCoding(data []byte) (system, code, display string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", "", "", err
	}
	switch probe.ResourceType {
	case "ServiceRequest":
		return ParseServiceRequestProductCoding(data)
	case "DeviceRequest":
		var dr struct {
			Code struct {
				Coding []struct {
					System  *string `json:"system"`
					Code    *string `json:"code"`
					Display *string `json:"display"`
				} `json:"coding"`
			} `json:"codeCodeableConcept"`
		}
		if err := json.Unmarshal(data, &dr); err != nil {
			return "", "", "", err
		}
		for _, c := range dr.Code.Coding {
			if c.System == nil || c.Code == nil {
				continue
			}
			if *c.System == systemCPT || *c.System == systemHCPCS {
				d := ""
				if c.Display != nil {
					d = *c.Display
				}
				return *c.System, *c.Code, d, nil
			}
		}
		return "", "", "", fmt.Errorf("shnsdk: DeviceRequest has no {CPT,HCPCS} product coding")
	default:
		return "", "", "", fmt.Errorf("shnsdk: ParseOrderProductCoding expected ServiceRequest|DeviceRequest, got %q", probe.ResourceType)
	}
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
