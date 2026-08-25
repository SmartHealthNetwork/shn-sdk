package shnsdk

// The PAS response builders in this file (BuildClaimResponse, BuildPendedResponse,
// BuildDeniedResponse and their AtLine variants) are PORTED-standalone from
// internal/pas/pas.go — byte-identical in logic to their internal twins, which
// the substrate's golden-corpus generator still uses; the difference is the
// package-level prefix and the use of pasFullURLFor / pasInjectResourceType
// (sdk/pas.go) instead of the internal-private copies.
// Parity tests live in test/sdkparity/pas_parity_test.go.
//
// This package ships NO prior-auth policy. Deciding a PA is the deployer's job: a
// Responder gets its verdicts from the ResponderConfig.Adjudicator the occupant
// supplies (responder.go — the partner/enclave seam AI-9 protects, and the one place
// PASDecision is produced). The reference implementation of that seam is a real payer,
// not a fixture in this module.

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// PASOutcome is a prior-auth adjudication verdict.
type PASOutcome int

const (
	PASApproved PASOutcome = iota
	PASPended
	PASDenied
)

// PASDecision is the partner's prior-auth verdict (returned by
// Adjudicator.PriorAuth — added to the interface in the dispatch change).
type PASDecision struct {
	Outcome     PASOutcome
	NeededItems []string // pended: what exchange-2 must supply
	PreAuthRef  string   // approved: the authorization number
	ValidUntil  string   // approved: expiry
	DenyReason  string   // denied: rationale carried in the ClaimResponse
}

// BuildClaimResponse builds a Da Vinci PAS APPROVED ClaimResponse (FR-22).
// It self-declares profile-claimresponse and carries the A1 reviewAction
// (Certified in Total) on item.adjudication — the conformant approved shape
// that the runtime PAS validator (FR-36) enforces at egress.
// Custom-marshalled (mirroring the denied A3 path) so the CodeableConcept-valued
// reviewAction extension serialises cleanly.
//
// PORTED-standalone: internal/pas.BuildClaimResponse.
//
// BuildClaimResponse speaks PAS line 2.0 — it is BuildClaimResponseAtLine("2.0", …),
// byte-identical (regression-fenced by test/sdkparity). No structural delta was found
// for profile-claimresponse.json across 2.0.1/2.1.0/2.2.1 (PAS package differential); the
// AtLine variant exists for interface symmetry + meta.profile-from-def discipline.
func BuildClaimResponse(preAuthRef, validUntil, patientRef, correlationID string, created time.Time) ([]byte, error) {
	def, _ := PASLineDef("2.0") // always present — pinned by manifest + parity-tested
	return buildClaimResponse(def, preAuthRef, validUntil, patientRef, correlationID, created)
}

// BuildClaimResponseAtLine is BuildClaimResponse parameterized by PAS line
// ("2.0"|"2.1"|"2.2"). Unknown line errors (fail-closed).
func BuildClaimResponseAtLine(line, preAuthRef, validUntil, patientRef, correlationID string, created time.Time) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildClaimResponseAtLine: unknown PAS line %q", line)
	}
	return buildClaimResponse(def, preAuthRef, validUntil, patientRef, correlationID, created)
}

func buildClaimResponse(def PASDef, preAuthRef, validUntil, patientRef, correlationID string, created time.Time) ([]byte, error) {
	cr := pasApprovedCR{
		ResourceType: "ClaimResponse",
		Meta:         &pasClaimResponseMeta{Profile: []string{def.ClaimResponseProfile}},
		Status:       "active",
		Type:         pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: "http://terminology.hl7.org/CodeSystem/claim-type", Code: "professional"}}},
		Use:          "preauthorization",
		Patient:      pasDeniedReference{Reference: patientRef},
		Created:      created.UTC().Format(time.RFC3339),
		Insurer:      pasDeniedReference{Reference: "Organization/payer"},
		Outcome:      "complete",
		Identifier:   []pasDeniedIdentifier{{System: pasCorrelationSystem, Value: correlationID}},
		// PAS 2.1+ (def-driven): ClaimResponse.request — the Reference to the request
		// Claim this response answers. nil (omitted) at 2.0, keeping that line's
		// byte-frozen shape.
		Request: pasClaimRequestFor(def, correlationID),
		Item: []pasDeniedItem{{
			ItemSequence: 1,
			// PAS 2.0.1 declares extension-reviewAction's context as item.adjudication
			// (not .item) — it rides on the adjudication. A1 = "Certified in Total"
			// (approved); X12 306 system (tx.fhir.org returns not-found for the licensed
			// X12 codesystem — curated code, allowlisted offline like the denied A3).
			Adjudication: []pasDeniedAdj{{
				Category: pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: "http://terminology.hl7.org/CodeSystem/adjudication", Code: "submitted"}}},
				Extension: []pasReviewActionExt{{
					URL: pasReviewActionExtURL,
					Extension: []pasReviewActionSubExt{{
						URL: pasReviewActionCodeExtURL,
						ValueCodeableConcept: &pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{
							System: pasSystemX12ReviewAction, Code: "A1", Display: "Certified in Total",
						}}},
					}},
				}},
			}},
		}},
	}
	if preAuthRef != "" {
		cr.PreAuthRef = preAuthRef
		cr.PreAuthPeriod = &pasApprovedPreAuthPeriod{End: validUntil}
	}
	return json.Marshal(cr)
}

// BuildPendedResponse builds the exchange-1 PENDED response (FR-20): a
// collection Bundle holding a ClaimResponse (outcome=queued,
// use=preauthorization) and a Task (status=requested) whose inputs enumerate
// the supplemental items the payer needs. The provider distinguishes this from
// an approved bare ClaimResponse by resourceType (Bundle ⇒ pended). The
// pended/approved business outcome stays in the payload — the payload-blind Hub
// never sees it (AI-2).
//
// PORTED-standalone: internal/pas.BuildPendedResponse.
//
// BuildPendedResponse speaks PAS line 2.0 — it is BuildPendedResponseAtLine("2.0", …),
// byte-identical (regression-fenced by test/sdkparity). Use BuildPendedResponseAtLine
// to target 2.1/2.2 (PAS package differential: PAS 2.2 makes response Bundle.identifier
// mandatory — profile-pas-response-bundle.json, absent at 2.0.1/2.1.0).
func BuildPendedResponse(patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	def, _ := PASLineDef("2.0") // always present — pinned by manifest + parity-tested
	return buildPendedResponse(def, patientRef, correlationID, needed, created)
}

// BuildPendedResponseAtLine is BuildPendedResponse parameterized by PAS line
// ("2.0"|"2.1"|"2.2"). Unknown line errors (fail-closed).
func BuildPendedResponseAtLine(line, patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildPendedResponseAtLine: unknown PAS line %q", line)
	}
	return buildPendedResponse(def, patientRef, correlationID, needed, created)
}

// pasPendedOutcome maps def.PendedResponseOutcome onto the samply enum,
// fail-closed (an unmapped code is a manifest/def bug, never a silent default).
func pasPendedOutcome(def PASDef) (fhir.ClaimProcessingCodes, error) {
	switch def.PendedResponseOutcome {
	case "queued":
		return fhir.ClaimProcessingCodesQueued, nil
	case "complete":
		return fhir.ClaimProcessingCodesComplete, nil
	default:
		return 0, fmt.Errorf("shnsdk: PAS line %q: unsupported pended ClaimResponse.outcome %q", def.Line, def.PendedResponseOutcome)
	}
}

func buildPendedResponse(def PASDef, patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	// PAS 2.2 (def-driven): "queued" leaves the required ClaimResponseOutcome value
	// set at 2.2.1 — the pend is carried by the Task entry, not the outcome code.
	outcome, err := pasPendedOutcome(def)
	if err != nil {
		return nil, err
	}
	cr := fhir.ClaimResponse{
		Meta:   &fhir.Meta{Profile: []string{def.ClaimResponseProfile}},
		Id:     strPtr("claim-response-" + correlationID),
		Status: fhir.FinancialResourceStatusCodesActive,
		Type: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System: strPtr("http://terminology.hl7.org/CodeSystem/claim-type"),
				Code:   strPtr("professional"),
			}},
		},
		Use:     fhir.UsePreauthorization,
		Patient: fhir.Reference{Reference: strPtr(patientRef)},
		Created: created.UTC().Format(time.RFC3339),
		Insurer: fhir.Reference{Reference: strPtr("Organization/payer")},
		Outcome: outcome,
		Identifier: []fhir.Identifier{{
			System: strPtr(pasCorrelationSystem),
			Value:  strPtr(correlationID),
		}},
	}
	// PAS 2.1+ (def-driven): the request Claim this pended response answers. nil at
	// 2.0 (fhir.Reference is a pointer field — omitted, byte-frozen shape).
	if def.ClaimResponseRequestRequired {
		cr.Request = &fhir.Reference{Identifier: &fhir.Identifier{
			System: strPtr(pasCorrelationSystem),
			Value:  strPtr(correlationID),
		}}
	}
	crJSON, err := json.Marshal(cr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal pended ClaimResponse: %w", err)
	}
	crJSON, err = pasInjectResourceType(crJSON, "ClaimResponse")
	if err != nil {
		return nil, err
	}
	taskJSON, err := buildPASTask(patientRef, correlationID, needed, created)
	if err != nil {
		return nil, err
	}
	crURL, err := pasFullURLFor(crJSON)
	if err != nil {
		return nil, err
	}
	taskURL, err := pasFullURLFor(taskJSON)
	if err != nil {
		return nil, err
	}
	bundle := fhir.Bundle{
		Type:      fhir.BundleTypeCollection,
		Timestamp: strPtr(created.UTC().Format(time.RFC3339)),
		Entry: []fhir.BundleEntry{
			{FullUrl: strPtr(crURL), Resource: json.RawMessage(crJSON)},
			{FullUrl: strPtr(taskURL), Resource: json.RawMessage(taskJSON)},
		},
	}
	// PAS 2.2 (def-driven, PAS package differential): response Bundle.identifier is
	// mandatory. No-op at 2.0/2.1 (def.ResponseBundleIdentifierRequired false) —
	// byte-identical to the existing legacy shape.
	if def.ResponseBundleIdentifierRequired {
		bundle.Identifier = &fhir.Identifier{System: strPtr(pasBundleIdentifierSystem), Value: strPtr(correlationID)}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal pended bundle: %w", err)
	}
	return pasInjectResourceType(raw, "Bundle")
}

// pasTaskInputJSON is a minimal FHIR R4 Task.input that emits ONLY the
// value[x] discriminant actually set (valueString here). The samply
// golang-fhir-models TaskInput marshals every value[x] variant to its zero
// value, which the FHIR validator correctly rejects as unrecognised properties
// on a choice type. We bypass the generated struct for this field only.
//
// PORTED-standalone: internal/pas.taskInputJSON.
type pasTaskInputJSON struct {
	Type        pasTaskCodeableConceptJSON `json:"type"`
	ValueString string                     `json:"valueString"`
}

type pasTaskCodeableConceptJSON struct {
	Text string `json:"text"`
}

// pasTaskJSON is a minimal FHIR R4 Task that emits exactly the required fields
// and avoids the samply TaskInput marshalling problem (see pasTaskInputJSON).
//
// PORTED-standalone: internal/pas.taskJSON.
type pasTaskJSON struct {
	ResourceType string               `json:"resourceType"`
	Id           string               `json:"id,omitempty"`
	Status       string               `json:"status"`
	Intent       string               `json:"intent"`
	For          pasTaskReferenceJSON `json:"for"`
	AuthoredOn   string               `json:"authoredOn"`
	Input        []pasTaskInputJSON   `json:"input,omitempty"`
}

type pasTaskReferenceJSON struct {
	Reference string `json:"reference"`
}

// buildPASTask builds the FHIR Task enumerating needed supplemental items
// (FR-20). Uses a custom minimal struct rather than the samply fhir.Task to
// avoid the generated TaskInput marshalling all value[x] zero values.
//
// PORTED-standalone: internal/pas.buildTask.
func buildPASTask(patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	inputs := make([]pasTaskInputJSON, 0, len(needed))
	for _, item := range needed {
		inputs = append(inputs, pasTaskInputJSON{
			Type:        pasTaskCodeableConceptJSON{Text: item},
			ValueString: item,
		})
	}
	task := pasTaskJSON{
		ResourceType: "Task",
		Id:           "task-" + correlationID,
		Status:       "requested",
		Intent:       "order",
		For:          pasTaskReferenceJSON{Reference: patientRef},
		AuthoredOn:   created.UTC().Format(time.RFC3339),
		Input:        inputs,
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal Task: %w", err)
	}
	return raw, nil
}

// Denied ClaimResponse types — ported standalone from internal/pas.

// pasDeniedCR is a minimal FHIR R4 ClaimResponse expressing a Da Vinci PAS
// DENIAL: outcome=complete (the request was processed; denial is a decision,
// not an error), the reviewAction extension on the item carrying reviewActionCode
// A3 (Not Certified), a plain-language disposition (rationale), and a processNote
// carrying the appeal window + peer-to-peer instruction. NO preAuthRef — a denial
// issues no authorization number, so ParseClaimResponse reads it as not-approved.
//
// PORTED-standalone: internal/pas.claimResponseDeniedJSON.
type pasDeniedCR struct {
	ResourceType string                   `json:"resourceType"`
	Meta         *pasClaimResponseMeta    `json:"meta,omitempty"`
	Status       string                   `json:"status"`
	Type         pasDeniedCodeableConcept `json:"type"`
	Use          string                   `json:"use"`
	Patient      pasDeniedReference       `json:"patient"`
	Created      string                   `json:"created"`
	Insurer      pasDeniedReference       `json:"insurer"`
	Outcome      string                   `json:"outcome"`
	Disposition  string                   `json:"disposition"`
	Identifier   []pasDeniedIdentifier    `json:"identifier"`
	// Request is the PAS 2.1+ mandatory ClaimResponse.request (omitted at 2.0 —
	// PASDef.ClaimResponseRequestRequired).
	Request     *pasClaimRequestRef    `json:"request,omitempty"`
	Item        []pasDeniedItem        `json:"item"`
	ProcessNote []pasDeniedProcessNote `json:"processNote"`
}

type pasDeniedItem struct {
	ItemSequence int            `json:"itemSequence"`
	Adjudication []pasDeniedAdj `json:"adjudication"`
}

type pasReviewActionExt struct {
	URL       string                  `json:"url"`
	Extension []pasReviewActionSubExt `json:"extension"`
}

type pasReviewActionSubExt struct {
	URL                  string                    `json:"url"`
	ValueCodeableConcept *pasDeniedCodeableConcept `json:"valueCodeableConcept,omitempty"`
}

type pasDeniedAdj struct {
	Category  pasDeniedCodeableConcept `json:"category"`
	Extension []pasReviewActionExt     `json:"extension,omitempty"`
}

type pasDeniedProcessNote struct {
	Number int    `json:"number"`
	Type   string `json:"type"`
	Text   string `json:"text"`
}

type pasDeniedCodeableConcept struct {
	Coding []pasDeniedCoding `json:"coding,omitempty"`
	Text   string            `json:"text,omitempty"`
}
type pasDeniedCoding struct {
	System  string `json:"system,omitempty"`
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`
}
type pasDeniedReference struct {
	Reference string `json:"reference,omitempty"`
}
type pasDeniedIdentifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// pasClaimRequestRef is ClaimResponse.request: a Reference to the request Claim
// this response answers, carried as the Claim's BUSINESS identifier
// (urn:shn:correlation|<correlationID>) rather than a literal "Claim/<id>". See
// PASDef.ClaimResponseRequestRequired for why the identifier is the honest form
// here (the conformant submit path stamps its own stable Claim.id; the
// correlation identifier is the one datum both paths' Claims genuinely carry).
type pasClaimRequestRef struct {
	Identifier pasDeniedIdentifier `json:"identifier"`
}

// pasClaimRequestFor returns the ClaimResponse.request Reference for def, or nil
// at a line that does not require it (2.0 — keeps the built response byte-identical
// to the pre-multi-version shape).
func pasClaimRequestFor(def PASDef, correlationID string) *pasClaimRequestRef {
	if !def.ClaimResponseRequestRequired {
		return nil
	}
	return &pasClaimRequestRef{Identifier: pasDeniedIdentifier{System: pasCorrelationSystem, Value: correlationID}}
}

type pasClaimResponseMeta struct {
	Profile []string `json:"profile,omitempty"`
}

// pasApprovedCR is the Da Vinci PAS APPROVED ClaimResponse:
// outcome=complete, meta.profile=profile-claimresponse, the reviewAction A1
// (Certified in Total) on item.adjudication, and the preAuthRef/preAuthPeriod
// carrying the authorization number + expiry. Custom-marshalled (like the denied
// twin) so the CodeableConcept-valued reviewAction extension serialises cleanly.
//
// PORTED-standalone: internal/pas.claimResponseApprovedJSON.
type pasApprovedCR struct {
	ResourceType string                   `json:"resourceType"`
	Meta         *pasClaimResponseMeta    `json:"meta,omitempty"`
	Status       string                   `json:"status"`
	Type         pasDeniedCodeableConcept `json:"type"`
	Use          string                   `json:"use"`
	Patient      pasDeniedReference       `json:"patient"`
	Created      string                   `json:"created"`
	Insurer      pasDeniedReference       `json:"insurer"`
	Outcome      string                   `json:"outcome"`
	Identifier   []pasDeniedIdentifier    `json:"identifier"`
	// Request is the PAS 2.1+ mandatory ClaimResponse.request (omitted at 2.0 —
	// PASDef.ClaimResponseRequestRequired).
	Request       *pasClaimRequestRef       `json:"request,omitempty"`
	PreAuthRef    string                    `json:"preAuthRef,omitempty"`
	PreAuthPeriod *pasApprovedPreAuthPeriod `json:"preAuthPeriod,omitempty"`
	Item          []pasDeniedItem           `json:"item"`
}

type pasApprovedPreAuthPeriod struct {
	End string `json:"end,omitempty"`
}

const (
	// pasProfileClaimResponse is the Da Vinci PAS 2.0.1 ClaimResponse profile (see
	// the internal twin profilePASClaimResponse).
	pasProfileClaimResponse = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claimresponse"
	// pasSystemX12ReviewAction is the X12 278 review-action code system used by
	// the Da Vinci PAS reviewAction extension. A1 = "Certified in Total"
	// (approved), A3 = "Not Certified" (denied).
	// PORTED-standalone: internal/pas.systemX12ReviewAction.
	pasSystemX12ReviewAction = "https://codesystem.x12.org/005010/306"
	// pasReviewActionExtURL is the Da Vinci PAS reviewAction extension URL.
	pasReviewActionExtURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	// pasReviewActionCodeExtURL is the canonical url of the PAS reviewAction
	// "code" sub-extension.
	pasReviewActionCodeExtURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"
)

// BuildDeniedResponse builds the Da Vinci PAS denied ClaimResponse (FR-22).
// The rationale is the human-readable disposition; the appeal window (30 days)
// + peer-to-peer instruction ride in a processNote. No preAuthRef is issued.
// Outcome is "complete" — denial is a decision, not an error.
//
// PORTED-standalone: internal/pas.BuildDeniedResponse.
//
// BuildDeniedResponse speaks PAS line 2.0 — it is BuildDeniedResponseAtLine("2.0", …),
// byte-identical (regression-fenced by test/sdkparity). No structural delta was found
// for profile-claimresponse.json across 2.0.1/2.1.0/2.2.1 (PAS package differential); the
// AtLine variant exists for interface symmetry + meta.profile-from-def discipline.
func BuildDeniedResponse(patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	def, _ := PASLineDef("2.0") // always present — pinned by manifest + parity-tested
	return buildDeniedResponse(def, patientRef, correlationID, rationale, created)
}

// BuildDeniedResponseAtLine is BuildDeniedResponse parameterized by PAS line
// ("2.0"|"2.1"|"2.2"). Unknown line errors (fail-closed).
func BuildDeniedResponseAtLine(line, patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildDeniedResponseAtLine: unknown PAS line %q", line)
	}
	return buildDeniedResponse(def, patientRef, correlationID, rationale, created)
}

func buildDeniedResponse(def PASDef, patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	cr := pasDeniedCR{
		ResourceType: "ClaimResponse",
		Meta:         &pasClaimResponseMeta{Profile: []string{def.ClaimResponseProfile}},
		Status:       "active",
		Type:         pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: "http://terminology.hl7.org/CodeSystem/claim-type", Code: "professional"}}},
		Use:          "preauthorization",
		Patient:      pasDeniedReference{Reference: patientRef},
		Created:      created.UTC().Format(time.RFC3339),
		Insurer:      pasDeniedReference{Reference: "Organization/payer"},
		Outcome:      "complete",
		Disposition:  rationale,
		Identifier:   []pasDeniedIdentifier{{System: pasCorrelationSystem, Value: correlationID}},
		// PAS 2.1+ (def-driven): the request Claim this denial answers. nil at 2.0.
		Request: pasClaimRequestFor(def, correlationID),
		Item: []pasDeniedItem{{
			ItemSequence: 1,
			Adjudication: []pasDeniedAdj{{
				Category: pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: "http://terminology.hl7.org/CodeSystem/adjudication", Code: "submitted"}}},
				Extension: []pasReviewActionExt{{
					URL: pasReviewActionExtURL,
					Extension: []pasReviewActionSubExt{{
						URL: pasReviewActionCodeExtURL,
						ValueCodeableConcept: &pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{
							System: pasSystemX12ReviewAction, Code: "A3", Display: "Not Certified",
						}}},
					}},
				}},
			}},
		}},
		ProcessNote: []pasDeniedProcessNote{{
			Number: 1,
			Type:   "print",
			Text:   "Appeal window: 30 days from the date of this determination. A peer-to-peer review with the medical director may be requested before filing a formal appeal.",
		}},
	}
	return json.Marshal(cr)
}
