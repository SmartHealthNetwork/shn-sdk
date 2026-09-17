package shnsdk

// The approved and denied PAS response builders in this file
// (BuildClaimResponse, BuildDeniedResponse and their AtLine variants) are
// ported from the network's own PAS package and kept byte-identical to it; the
// pended response builder (BuildPendedClaimResponseAtLine) is the one
// implementation both use.
//
// This package ships NO prior-auth policy. Deciding a PA is the deployer's job: a
// Responder gets its verdicts from the ResponderConfig.Adjudicator the occupant
// supplies (responder.go — the partner/enclave seam AI-9 protects, and the one place
// PASDecision is produced). The reference implementation of that seam is a real payer,
// not a fixture in this module.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
//
// A denial carries only what the partner decided: DenyReason becomes
// ClaimResponse.disposition exactly as given (no disposition when it is
// empty), and ProcessNotes become ClaimResponse.processNote in order (none
// when it is empty). The Responder adds no rationale or note of its own.
//
// A pended decision carries the facts of the Task(s) the pended response
// holds (see BuildPendedTasks): PendedItems, TaskIdentifier, TaskStatus,
// TaskRequester, TaskOwner and PayerURL. PayerURL may be left empty when the
// Responder was built with ResponderConfig.PublicBaseURL, which is then used.
// The Responder answers at PAS 2.0, where TaskRequester and TaskOwner are
// required. A pended decision without these facts is refused with 500 rather
// than answered with invented ones.
type PASDecision struct {
	Outcome PASOutcome
	// NeededItems names what the payer needs as bare strings.
	//
	// Deprecated: a bare string does not say whether an item is an
	// attachment or a questionnaire, so the Responder does not answer from it:
	// a pended decision that sets NeededItems without PendedItems is refused
	// with 500 naming PendedItems. Use PendedItems.
	NeededItems []string
	PreAuthRef  string // approved: the authorization number
	ValidUntil  string // approved: expiry
	DenyReason  string // denied: the partner's own rationale; empty emits no disposition
	// ProcessNotes (denied only) are the partner's own notes, for example an
	// appeal window or a review instruction. The Responder refuses a decision
	// that sets them with an approved or pended outcome.
	ProcessNotes []PASProcessNote

	// PendedItems (pended) are the needs, one per request line.
	PendedItems []PendedItem
	// TaskIdentifier (pended) is the payer's tracking identifier for the
	// request for information.
	TaskIdentifier PASIdentifier
	// TaskStatus (pended) is the Task status, an HRex task status code
	// (usually "requested").
	TaskStatus string
	// TaskRequester and TaskOwner (pended) are the Task requester and owner
	// identifiers.
	TaskRequester PASIdentifier
	TaskOwner     PASIdentifier
	// PayerURL (pended) is the payer's follow-up endpoint.
	PayerURL string
}

// PASProcessNote is one ClaimResponse.processNote the partner supplies.
type PASProcessNote struct {
	// Type is the FHIR note-type code: "display", "print" or "printoper".
	// Empty omits processNote.type.
	Type string
	// Text is the note text, carried exactly. Required.
	Text string
}

// pasNoteTypes is the FHIR R4 NoteType value set (required binding on
// ClaimResponse.processNote.type).
var pasNoteTypes = map[string]bool{"display": true, "print": true, "printoper": true}

// pasDeniedNotes renders notes as processNote elements numbered from 1 (the
// number is required at PAS 2.1 and later), refusing a note without text or
// with a type outside the NoteType codes.
func pasDeniedNotes(notes []PASProcessNote) ([]pasDeniedProcessNote, error) {
	if len(notes) == 0 {
		return nil, nil
	}
	out := make([]pasDeniedProcessNote, 0, len(notes))
	for i, n := range notes {
		if n.Text == "" {
			return nil, fmt.Errorf("shnsdk: process note %d has no text", i+1)
		}
		if n.Type != "" && !pasNoteTypes[n.Type] {
			return nil, fmt.Errorf("shnsdk: process note %d type %q is not display, print or printoper", i+1, n.Type)
		}
		out = append(out, pasDeniedProcessNote{Number: i + 1, Type: n.Type, Text: n.Text})
	}
	return out, nil
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
		if validUntil != "" {
			cr.PreAuthPeriod = &pasApprovedPreAuthPeriod{End: validUntil}
		}
	}
	return json.Marshal(cr)
}

// ErrPendedResponseNeedsTaskFacts is returned by the deprecated
// BuildPendedResponse and BuildPendedResponseAtLine: a pended PAS response
// carries a profiled Task whose identifier, status, requester, owner, payer
// URL and needed items only the payer can supply, and those two functions do
// not take them. Use BuildPendedClaimResponseAtLine.
var ErrPendedResponseNeedsTaskFacts = errors.New("shnsdk: a pended PAS response needs the payer's Task facts; use BuildPendedClaimResponseAtLine")

// BuildPendedResponse returns ErrPendedResponseNeedsTaskFacts.
//
// Deprecated: a pended response's Task needs facts this signature does not
// carry. Use BuildPendedClaimResponseAtLine.
func BuildPendedResponse(patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	return nil, ErrPendedResponseNeedsTaskFacts
}

// BuildPendedResponseAtLine returns ErrPendedResponseNeedsTaskFacts.
//
// Deprecated: a pended response's Task needs facts this signature does not
// carry. Use BuildPendedClaimResponseAtLine.
func BuildPendedResponseAtLine(line, patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	return nil, ErrPendedResponseNeedsTaskFacts
}

// PendedResponseInputs are a payer's facts for a pended PAS response.
type PendedResponseInputs struct {
	// Correlation is the ClaimResponse business identifier value
	// (urn:shn:correlation) and the ClaimResponse id suffix.
	Correlation string
	// Created is ClaimResponse.created and the Bundle timestamp.
	Created time.Time
	// Task are the facts of the Task(s) the response carries (see
	// BuildPendedTasks). Task.ID defaults to "task-" + Correlation. Task.Patient
	// is also the ClaimResponse.patient.
	Task PendedTaskInputs
}

// BuildPendedClaimResponseAtLine builds a pended Da Vinci PAS response at a
// PAS line ("2.0", "2.1", "2.2"): a collection Bundle holding the
// ClaimResponse, whose items each carry the X12 306 review action A4 (Pended)
// for a request line the payer needs information about, followed by the
// Task(s) BuildPendedTasks builds from in.Task. The ClaimResponse outcome is
// the line's pended outcome ("queued" at 2.0 and 2.1, "complete" at 2.2, where
// the review action carries the pend).
func BuildPendedClaimResponseAtLine(line string, in PendedResponseInputs) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildPendedClaimResponseAtLine: unknown PAS line %q", line)
	}
	if in.Correlation == "" {
		return nil, errors.New("shnsdk: BuildPendedClaimResponseAtLine: Correlation is required")
	}
	if in.Task.ID == "" {
		in.Task.ID = "task-" + in.Correlation
	}
	tasks, err := BuildPendedTasks(line, in.Task)
	if err != nil {
		return nil, err
	}
	return buildPendedResponse(def, in, tasks)
}

func buildPendedResponse(def PASDef, in PendedResponseInputs, tasks [][]byte) ([]byte, error) {
	patientRef, correlationID, created := in.Task.Patient, in.Correlation, in.Created
	// PAS 2.2 (def-driven): "queued" leaves the required ClaimResponseOutcome value
	// set at 2.2.1 — the A4 review action carries the pending decision.
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
	// One A4 item per request line the payer needs information about, in line
	// order. A4 carries the decision independently of outcome and the Task.
	var sequences []int
	for _, it := range in.Task.Items {
		if !slices.Contains(sequences, it.Sequence) {
			sequences = append(sequences, it.Sequence)
		}
	}
	slices.Sort(sequences)
	items := make([]pasDeniedItem, 0, len(sequences))
	for _, seq := range sequences {
		items = append(items, pasDeniedItem{
			ItemSequence: seq,
			Adjudication: []pasDeniedAdj{{
				Category: pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: "http://terminology.hl7.org/CodeSystem/adjudication", Code: "submitted"}}},
				Extension: []pasReviewActionExt{{URL: pasReviewActionExtURL, Extension: []pasReviewActionSubExt{{
					URL: pasReviewActionCodeExtURL, ValueCodeableConcept: &pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: pasSystemX12ReviewAction, Code: "A4", Display: "Pended"}}},
				}}}},
			}},
		})
	}
	type pendedBase fhir.ClaimResponse
	pendedCR := struct {
		pendedBase
		Item []pasDeniedItem `json:"item"`
	}{pendedBase: pendedBase(cr), Item: items}
	crJSON, err := json.Marshal(pendedCR)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal pended ClaimResponse: %w", err)
	}
	crJSON, err = pasInjectResourceType(crJSON, "ClaimResponse")
	if err != nil {
		return nil, err
	}
	crURL, err := pasFullURLFor(crJSON)
	if err != nil {
		return nil, err
	}
	entries := []fhir.BundleEntry{{FullUrl: strPtr(crURL), Resource: json.RawMessage(crJSON)}}
	for _, taskJSON := range tasks {
		taskURL, err := pasFullURLFor(taskJSON)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fhir.BundleEntry{FullUrl: strPtr(taskURL), Resource: json.RawMessage(taskJSON)})
	}
	bundle := fhir.Bundle{
		Type:      fhir.BundleTypeCollection,
		Timestamp: strPtr(created.UTC().Format(time.RFC3339)),
		Entry:     entries,
	}
	// PAS 2.2 (def-driven, PAS package differential): response Bundle.identifier is
	// mandatory. No-op at 2.0/2.1 (def.ResponseBundleIdentifierRequired false).
	if def.ResponseBundleIdentifierRequired {
		bundle.Identifier = &fhir.Identifier{System: strPtr(pasBundleIdentifierSystem), Value: strPtr(correlationID)}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal pended bundle: %w", err)
	}
	return pasInjectResourceType(raw, "Bundle")
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

// Denied ClaimResponse types — ported standalone from internal/pas.

// pasDeniedCR is a minimal FHIR R4 ClaimResponse expressing a Da Vinci PAS
// DENIAL: outcome=complete (the request was processed; denial is a decision,
// not an error), the reviewAction extension on the item carrying reviewActionCode
// A3 (Not Certified), the partner's own disposition (absent when it gave none), and
// the partner's own processNotes (absent when it gave none). NO preAuthRef — a denial
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
	Disposition  string                   `json:"disposition,omitempty"`
	Identifier   []pasDeniedIdentifier    `json:"identifier"`
	// Request is the PAS 2.1+ mandatory ClaimResponse.request (omitted at 2.0 —
	// PASDef.ClaimResponseRequestRequired).
	Request     *pasClaimRequestRef    `json:"request,omitempty"`
	Item        []pasDeniedItem        `json:"item"`
	ProcessNote []pasDeniedProcessNote `json:"processNote,omitempty"`
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
	Type   string `json:"type,omitempty"`
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
// The rationale is the human-readable disposition, carried exactly; an empty
// rationale emits no disposition. It carries no processNote — use
// BuildDeniedResponseWithNotesAtLine to add the partner's own notes. No
// preAuthRef is issued. Outcome is "complete" — denial is a decision, not an
// error.
//
// PORTED-standalone: internal/pas.BuildDeniedResponse.
//
// BuildDeniedResponse speaks PAS line 2.0 — it is BuildDeniedResponseAtLine("2.0", …),
// byte-identical (regression-fenced by test/sdkparity). No structural delta was found
// for profile-claimresponse.json across 2.0.1/2.1.0/2.2.1 (PAS package differential); the
// AtLine variant exists for interface symmetry + meta.profile-from-def discipline.
func BuildDeniedResponse(patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	def, _ := PASLineDef("2.0") // always present — pinned by manifest + parity-tested
	return buildDeniedResponse(def, patientRef, correlationID, rationale, nil, created)
}

// BuildDeniedResponseAtLine is BuildDeniedResponse parameterized by PAS line
// ("2.0"|"2.1"|"2.2"). Unknown line errors (fail-closed).
func BuildDeniedResponseAtLine(line, patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	return BuildDeniedResponseWithNotesAtLine(line, patientRef, correlationID, rationale, nil, created)
}

// BuildDeniedResponseWithNotesAtLine is BuildDeniedResponseAtLine carrying the
// partner's own notes as ClaimResponse.processNote, in order and numbered from
// 1. A note without text, or with a type other than "display", "print" or
// "printoper", is refused. Nil notes emit no processNote.
func BuildDeniedResponseWithNotesAtLine(line, patientRef, correlationID, rationale string, notes []PASProcessNote, created time.Time) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildDeniedResponseWithNotesAtLine: unknown PAS line %q", line)
	}
	return buildDeniedResponse(def, patientRef, correlationID, rationale, notes, created)
}

func buildDeniedResponse(def PASDef, patientRef, correlationID, rationale string, notes []PASProcessNote, created time.Time) ([]byte, error) {
	processNotes, err := pasDeniedNotes(notes)
	if err != nil {
		return nil, err
	}
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
		ProcessNote: processNotes,
	}
	return json.Marshal(cr)
}
