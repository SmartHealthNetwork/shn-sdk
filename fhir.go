package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

const (
	// systemLOINC is the LOINC code system URI.
	// Mirrors internal/fhirmap.systemLOINC (consent.go).
	systemLOINC = "http://loinc.org"

	// systemUSCoreDocClass is the US Core DocumentReference category code system.
	// Mirrors internal/fhirmap.systemUSCoreDocClass (documentreference.go).
	systemUSCoreDocClass = "http://hl7.org/fhir/us/core/CodeSystem/us-core-documentreference-category"

	// docRefDateUC05 is the fixed instant for the operative DocumentReference
	// disclosed by the external facility (matches the operative DiagnosticReport
	// effectiveDateTime date — 2026-05-15). FHIR DocumentReference.date is an instant
	// type, so a full ISO 8601 timestamp is required. recordClinicalDate truncates to
	// YYYY-MM-DD for InRange comparison (FR-24). Fixed value keeps the golden
	// byte-deterministic (FR-35/FR-39). Mirrors internal/fhirmap.docRefDateUC05.
	docRefDateUC05 = "2026-05-15T00:00:00Z"

	// systemCARC is the X12 Claim Adjustment Reason Codes system (PDex PPA denial
	// reason binding). CARC 50 = "non-covered… not deemed a 'medical necessity'."
	// Mirrors internal/fhirmap.systemCARC (eob.go).
	systemCARC = "https://x12.org/codes/claim-adjustment-reason-codes"

	// systemPDexAdjudication is the PDex Adjudication Discriminator code system.
	// Mirrors internal/fhirmap.systemPDexAdjudication (eob.go).
	systemPDexAdjudication = "http://hl7.org/fhir/us/davinci-pdex/CodeSystem/PDexAdjudicationDiscriminator"

	// systemPAProcedureCPT reuses the package CPT system const (order.go).
	// Mirrors internal/fhirmap.systemPAProcedureCPT (eob.go).
	systemPAProcedureCPT = systemCPT

	// systemAdjudication is the standard FHIR adjudication codesystem; "submitted"
	// is in the PDex "adjudicationamounttype" slice binding (PDexAdjudication VS).
	// Mirrors internal/fhirmap.systemAdjudication (eob.go).
	systemAdjudication = "http://terminology.hl7.org/CodeSystem/adjudication"

	// EOBAppealNote is the appeal-rights text carried on the EOB.processNote so the
	// patient surface (PHG) renders the appeal window FROM the FHIR resource (FR-28),
	// not from a bespoke UI string. The 30-day window matches the provider-facing
	// PAS ClaimResponse processNote. Exported so a test can assert the patient view
	// is data-driven (it equals THIS text, not a generic constant).
	// Mirrors internal/fhirmap.EOBAppealNote (eob.go).
	EOBAppealNote = "Appeal window: 30 days from the date of this determination. " +
		"A peer-to-peer review with the medical director may be requested before filing a formal appeal."
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

// BuildEligibilityResponse constructs a FHIR R4 CoverageEligibilityResponse for the
// payer side of a coverage-eligibility check. When covered, insurance is in force;
// when not covered, inforce is false and reason is carried in the disposition.
// PORTED standalone from
// internal/fhirmap.BuildEligibilityResponse with identical field shapes — proven by
// the cross-module parity test (test/sdkparity/fhir_parity_test.go).
//
// The "req-" prefix on the request reference is applied here so callers pass only the
// bare correlationID. created is supplied by the caller so the resource is
// byte-deterministic for a given clock.
func BuildEligibilityResponse(correlationID, patientRef string, covered bool, reason string, created time.Time) ([]byte, error) {
	member := strings.TrimPrefix(patientRef, "Patient/")
	resp := fhir.CoverageEligibilityResponse{
		Status:  fhir.FinancialResourceStatusCodesActive,
		Purpose: []fhir.EligibilityResponsePurpose{fhir.EligibilityResponsePurposeBenefits},
		Patient: fhir.Reference{Reference: strPtr(patientRef)},
		Created: created.UTC().Format(time.RFC3339),
		Request: fhir.Reference{Reference: strPtr("CoverageEligibilityRequest/req-" + correlationID)},
		Outcome: fhir.ClaimProcessingCodesComplete,
		Insurer: fhir.Reference{Reference: strPtr("Organization/payer")},
		Insurance: []fhir.CoverageEligibilityResponseInsurance{{
			Coverage: fhir.Reference{Reference: strPtr("Coverage/" + member)},
			Inforce:  boolPtr(covered),
		}},
	}
	if !covered {
		resp.Disposition = strPtr(reason)
	}
	return json.Marshal(resp)
}

// ParseEligibilityRequestMember extracts the member ID from a
// CoverageEligibilityRequest's patient.reference, stripping the leading "Patient/"
// prefix. It errors if the resource is not a CoverageEligibilityRequest or the
// patient reference is missing. PORTED standalone from
// internal/fhirmap.ParseEligibilityRequestMember; the parity test proves it agrees
// with the substrate on both SDK-built and substrate-built requests
// (test/sdkparity/fhir_parity_test.go).
func ParseEligibilityRequestMember(data []byte) (memberID string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err = json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	if probe.ResourceType != "CoverageEligibilityRequest" {
		return "", fmt.Errorf("shnsdk: expected CoverageEligibilityRequest, got %q", probe.ResourceType)
	}
	var req fhir.CoverageEligibilityRequest
	if err = json.Unmarshal(data, &req); err != nil {
		return "", err
	}
	if req.Patient.Reference == nil {
		return "", fmt.Errorf("shnsdk: CoverageEligibilityRequest missing patient.reference")
	}
	ref := *req.Patient.Reference
	return strings.TrimPrefix(ref, "Patient/"), nil
}

const (
	// profileUSCoreDiagnosticReportNote — US Core 6.1.0 DiagnosticReport Note profile.
	// Mirrors internal/fhirmap.profileUSCoreDiagnosticReportNote.
	profileUSCoreDiagnosticReportNote = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-diagnosticreport-note"
	// systemV2DiagnosticService — HL7 v2-0074 code system for the required category.
	systemV2DiagnosticService = "http://terminology.hl7.org/CodeSystem/v2-0074"
	// effectiveDateUC04 — fixed effective date for supplemental DiagnosticReports
	// (deterministic across runs; no clock). Mirrors internal/fhirmap.effectiveDateUC04.
	effectiveDateUC04 = "2026-05-15"
)

// BuildDiagnosticReport builds a US Core DiagnosticReport (Note profile) for the
// supplemental operative report attached to a pended prior-auth amendment (FR-32).
// Reimplements internal/fhirmap.BuildDiagnosticReport standalone; test/sdkparity
// asserts byte-identity. systemCPT is the package const from order.go.
// Fixed effectiveDateTime ⇒ deterministic (no clock).
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

// BuildDocumentReference builds a base-R4 DocumentReference for the operative
// report disclosed by an external facility during a federated-query prior-auth flow.
// type = LOINC 28570-0 (Procedure note); category = clinical-note;
// content.attachment.url references the companion DiagnosticReport.
// drRef is "DiagnosticReport/<id>".
//
// NOTE: no meta.profile is set — the US Core DocumentReference Type value set
// requires LOINC codes, which the IG-enabled HAPI does not load, causing
// validation failures. Base-R4 conformance is the pinned posture here; US Core
// profile pinning for DocumentReference is a tracked fast-follow.
//
// Promoted verbatim from internal/fhirmap.BuildDocumentReference; parity
// proven by test/sdkparity/fhirmap_builders_parity_test.go (byte-equal). FR-36
// boundary guard: no fhircodes value-set is referenced — terminology-agnostic.
func BuildDocumentReference(id, patientRef, drRef string) ([]byte, error) {
	date := docRefDateUC05
	dr := fhir.DocumentReference{
		Id:     strPtr(id),
		Status: fhir.DocumentReferenceStatusCurrent,
		Type: &fhir.CodeableConcept{
			Coding: []fhir.Coding{{System: strPtr(systemLOINC), Code: strPtr("28570-0"), Display: strPtr("Procedure note")}},
		},
		Category: []fhir.CodeableConcept{
			{Coding: []fhir.Coding{{System: strPtr(systemUSCoreDocClass), Code: strPtr("clinical-note"), Display: strPtr("Clinical Note")}}},
		},
		Subject: &fhir.Reference{Reference: strPtr(patientRef)},
		// Date is the creation/indexing instant of the document — used for FR-24
		// range filtering (recordClinicalDate reads this field). Fixed to match the
		// operative DiagnosticReport effectiveDateTime (2026-05-15, FR-35/FR-39).
		Date: &date,
		Content: []fhir.DocumentReferenceContent{
			{Attachment: fhir.Attachment{
				ContentType: strPtr("application/fhir+json"),
				Url:         strPtr(drRef),
				Title:       strPtr("Operative report — lumbar microdiscectomy"),
			}},
		},
	}
	// fhir.DocumentReference.MarshalJSON injects "resourceType":"DocumentReference"
	// automatically (confirmed in samply v0.3.2).
	return json.Marshal(dr)
}

// PADecision selects the adjudication shape of a PDex PA decision EOB.
// Promoted verbatim from internal/fhirmap.PADecision.
type PADecision int

const (
	PADecisionApproved PADecision = iota
	PADecisionDenied
)

// eobJSON and its supporting types are package-local structs for the PDex PA
// decision ExplanationOfBenefit wire shape. Promoted verbatim from
// internal/fhirmap/eob.go.
type eobJSON struct {
	ResourceType string             `json:"resourceType"`
	Id           string             `json:"id"`
	Status       string             `json:"status"`
	Type         eobCodeableConcept `json:"type"`
	Use          string             `json:"use"`
	Patient      eobReference       `json:"patient"`
	PreAuthRef   []string           `json:"preAuthRef,omitempty"`
	Created      string             `json:"created"`
	Insurer      eobReference       `json:"insurer"`
	Provider     eobReference       `json:"provider"`
	Outcome      string             `json:"outcome"`
	Insurance    []eobInsurance     `json:"insurance"`
	Item         []eobItem          `json:"item"`
	ProcessNote  []eobProcessNote   `json:"processNote,omitempty"`
}

type eobProcessNote struct {
	Number int    `json:"number"`
	Type   string `json:"type"`
	Text   string `json:"text"`
}

type eobInsurance struct {
	Focal    bool         `json:"focal"`
	Coverage eobReference `json:"coverage"`
}

type eobItem struct {
	Sequence         int                `json:"sequence"`
	ProductOrService eobCodeableConcept `json:"productOrService"`
	Adjudication     []eobAdjudication  `json:"adjudication"`
}

type eobAdjudication struct {
	Category eobCodeableConcept  `json:"category"`
	Reason   *eobCodeableConcept `json:"reason,omitempty"`
	Amount   *eobMoney           `json:"amount,omitempty"`
}

type eobMoney struct {
	Value    json.Number `json:"value"`
	Currency string      `json:"currency"`
}

type eobCodeableConcept struct {
	Coding []eobCoding `json:"coding,omitempty"`
	Text   string      `json:"text,omitempty"`
}
type eobCoding struct {
	System  string `json:"system,omitempty"`
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`
}
type eobReference struct {
	Reference string `json:"reference,omitempty"`
}

// BuildPADecisionEOB builds the Da Vinci PDex Prior Authorization (PPA)
// ExplanationOfBenefit for a PA decision (FR-28): use=preauthorization,
// outcome=complete. The item adjudication shape branches on decision:
//   - PADecisionDenied: the denialreason adjudication slice carrying CARC 50 (not
//     medically necessary) + the appeal-window/peer-to-peer processNote (authNumber
//     ignored — a denial has no authorization).
//   - PADecisionApproved: a "submitted" adjudication (standard adjudication
//     CodeSystem, in the PDexAdjudication binding) with no Reason/no denialreason,
//     and the authorization number on EOB.preAuthRef so the patient surface (PHG)
//     renders the approved auth from the FHIR resource (no appeal processNote).
//
// Validated base-R4 + curated terminology; the PDex profile pin is the tracked
// fast-follow (no meta.profile set; base-R4 conformance is the pinned posture).
//
// Promoted verbatim from internal/fhirmap.BuildPADecisionEOB; parity
// proven by test/sdkparity/fhirmap_builders_parity_test.go (byte-equal, both
// branches). FR-36 boundary guard: no fhircodes value-set referenced —
// terminology-agnostic (codes come in as params / package-local system* URI consts).
// PADecisionEOBParams groups the inputs to BuildPADecisionEOB — a struct rather than
// seven positional args (five of them same-typed strings, easy to transpose) so the
// public call site is readable and the param set can grow without a breaking change.
type PADecisionEOBParams struct {
	ID          string
	PatientRef  string
	CoverageRef string
	CPTCode     string
	Decision    PADecision
	AuthNumber  string
	Created     time.Time
}

// BuildPatientAccessCapabilityStatement returns the CMS-0057/PDex Patient Access
// CapabilityStatement the payer serves at GET /metadata (FR-37): a kind=instance
// server statement declaring the ExplanationOfBenefit read+search surface (PDex PA
// EOB profile), gated by the per-operation patient-access token (AI-11). It mirrors
// the real payer routes (GET /ExplanationOfBenefit?patient= and /ExplanationOfBenefit/{id})
// and validates as a base FHIR CapabilityStatement (0 errors).
//
// Promoted verbatim from internal/fhirmap.BuildPatientAccessCapabilityStatement.
// fhircodes-clean — no value-set references; dependency-light.
func BuildPatientAccessCapabilityStatement(created time.Time) ([]byte, error) {
	doc := "Patient Access to prior-authorization decisions. Each request carries a per-operation patient-access authority token bound to the patient (AI-11) — no blanket access."
	sec := "Per-operation patient-access bearer token (substrate Authorization Framework); subject-bound, signature-covered."
	cs := fhir.CapabilityStatement{
		Status:      fhir.PublicationStatusActive,
		Date:        created.UTC().Format(time.RFC3339),
		Kind:        fhir.CapabilityStatementKindInstance,
		FhirVersion: fhir.FHIRVersion4_0_1,
		Format:      []string{"json"},
		Software:    &fhir.CapabilityStatementSoftware{Name: "SHN Payer Patient Access API"},
		Implementation: &fhir.CapabilityStatementImplementation{
			Description: "SHN CMS-0057 Patient Access API (Da Vinci PDex Prior Authorization ExplanationOfBenefit).",
		},
		Rest: []fhir.CapabilityStatementRest{{
			Mode:          fhir.RestfulCapabilityModeServer,
			Documentation: &doc,
			Security:      &fhir.CapabilityStatementRestSecurity{Description: &sec},
			Resource: []fhir.CapabilityStatementRestResource{{
				Type:             fhir.ResourceTypeExplanationOfBenefit,
				SupportedProfile: []string{"http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/pdex-priorauthorization"},
				Interaction: []fhir.CapabilityStatementRestResourceInteraction{
					{Code: fhir.TypeRestfulInteractionRead},
					{Code: fhir.TypeRestfulInteractionSearchType},
				},
				SearchParam: []fhir.CapabilityStatementRestResourceSearchParam{
					{Name: "_id", Type: fhir.SearchParamTypeToken},
					{Name: "patient", Type: fhir.SearchParamTypeReference},
				},
			}},
		}},
	}
	return json.Marshal(cs) // fhir.CapabilityStatement.MarshalJSON injects resourceType
}

func BuildPADecisionEOB(p PADecisionEOBParams) ([]byte, error) {
	id, patientRef, coverageRef, cptCode, decision, authNumber, created :=
		p.ID, p.PatientRef, p.CoverageRef, p.CPTCode, p.Decision, p.AuthNumber, p.Created
	eob := eobJSON{
		ResourceType: "ExplanationOfBenefit",
		Id:           id,
		Status:       "active",
		Type:         eobCodeableConcept{Coding: []eobCoding{{System: "http://terminology.hl7.org/CodeSystem/claim-type", Code: "professional"}}},
		Use:          "preauthorization",
		Patient:      eobReference{Reference: patientRef},
		Created:      created.UTC().Format(time.RFC3339),
		Insurer:      eobReference{Reference: "Organization/payer"},
		Provider:     eobReference{Reference: "Practitioner/reviewer-uc08"},
		Outcome:      "complete",
		Insurance:    []eobInsurance{{Focal: true, Coverage: eobReference{Reference: coverageRef}}},
		Item: []eobItem{{
			Sequence:         1,
			ProductOrService: eobCodeableConcept{Coding: []eobCoding{{System: systemPAProcedureCPT, Code: cptCode, Display: "MRI lumbar spine w/o contrast"}}},
		}},
	}
	switch decision {
	case PADecisionApproved:
		// Approval: the PDex "adjudicationamounttype" adjudication slice — category
		// "submitted" (standard adjudication codesystem, in the PDexAdjudication
		// binding) with the slice-required amount. No denialreason / no CARC; this is a
		// conformant approved PA decision (validated against pdex-priorauthorization).
		// The amount is 0 USD — a pre-authorization carries no adjudicated dollar
		// figure; the slice mandates the element's presence, so 0 is the deliberate
		// "not applicable to a pre-auth" placeholder, not a real payment amount.
		eob.Item[0].Adjudication = []eobAdjudication{{
			Category: eobCodeableConcept{Coding: []eobCoding{{System: systemAdjudication, Code: "submitted", Display: "Submitted Amount"}}},
			Amount:   &eobMoney{Value: "0", Currency: "USD"},
		}}
		// Carry the authorization number ON the resource as EOB.preAuthRef (the FHIR
		// field for a pre-authorization reference number — a plain string, no
		// terminology), so the patient surface renders the approved auth from the EOB
		// (FR-28). No processNote on an approval (no appeal/peer-to-peer applies).
		if authNumber != "" {
			eob.PreAuthRef = []string{authNumber}
		}
	default: // PADecisionDenied
		eob.Item[0].Adjudication = []eobAdjudication{{
			Category: eobCodeableConcept{Coding: []eobCoding{{System: systemPDexAdjudication, Code: "denialreason"}}},
			Reason:   &eobCodeableConcept{Coding: []eobCoding{{System: systemCARC, Code: "50", Display: "These are non-covered services because this is not deemed a 'medical necessity' by the payer"}}},
		}}
		// FR-28: the appeal window + peer-to-peer instruction travel ON the FHIR
		// resource so the patient surface renders them from the EOB, not a UI string.
		eob.ProcessNote = []eobProcessNote{{Number: 1, Type: "print", Text: EOBAppealNote}}
	}
	return json.Marshal(eob)
}
