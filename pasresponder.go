package shnsdk

// The PAS response builders in this file (BuildClaimResponse, BuildPendedResponse,
// BuildDeniedResponse and their AtLine variants) are PORTED-standalone from
// internal/pas/pas.go — byte-identical in logic to their internal twins, which
// the substrate's golden-corpus generator still uses; the difference is the
// package-level prefix and the use of pasFullURLFor / pasEnsureID /
// pasInjectResourceType (sdk/pas.go) instead of the internal-private copies.
// Parity tests live in test/sdkparity/pas_parity_test.go.
//
// The adjudicator (SandboxAdjudicate and its input parser) is NOT a port: it is
// the only prior-auth adjudicator in the codebase. Its former internal twin had
// no production caller and was deleted; the live path is
// gateway/engine/adjudicator.go → SandboxAdjudicate, and the behaviour rows
// (every rule, every ambiguity refusal, at every nesting depth) live in
// pasresponder_test.go.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// SandboxAdjudicate applies the sandbox adjudication rules to the QR.
// hasDiagnosticReport reports whether the bundle carried an operative
// DiagnosticReport. Returns a PASDecision with the outcome and — on
// Approved — a generated preAuthRef + validUntil. randSource seeds the auth
// number (nil → crypto/rand, matching the nil-safe internal default).
//
// Returns an error when the QR is AMBIGUOUS about an adjudication input, in
// either of FHIR's two repeating encodings: two or more ITEMS carrying the same
// read linkId (a repeating group), or ONE item carrying more than one ANSWER (a
// repeating item). The rules model each input as single-valued and cannot
// resolve either shape without arbitrarily picking one clinical fact over
// another, so they refuse rather than choose. In that case the returned
// PASDecision has Outcome set to PASDenied, but it is not a real decision:
// callers MUST check the error before reading Outcome (or any other field)
// rather than treating it as an actual denial.
//
// SANDBOX adjudication policy — the reference implementation for
// quickstarts/tests/feedsmoke. A real payer implements its own PriorAuth. DEF-4
// stub (AI-9 holds).
func SandboxAdjudicate(qrJSON []byte, hasDiagnosticReport bool, now time.Time, randSource io.Reader) (PASDecision, error) {
	weeks, attested, priorSurgery, highDisability, patientReportedRequired, patientAttested, err := parseSandboxAdjudicationInputs(qrJSON)
	if err != nil {
		return PASDecision{Outcome: PASDenied}, fmt.Errorf("shnsdk: SandboxAdjudicate: %w", err)
	}
	if priorSurgery && !hasDiagnosticReport {
		return PASDecision{Outcome: PASPended, NeededItems: []string{"operative-diagnostic-report"}}, nil
	}
	if highDisability && !attested {
		return PASDecision{Outcome: PASPended, NeededItems: []string{"clinician-attested-functional-status"}}, nil
	}
	// R3: patient-reported functional status requires a patient Author's Signature
	// attestation (FR-27). The FIRST submit (no patient signature, auto-filled item)
	// pends; the ClaimUpdate (with the patient-attested item from the PHG) approves.
	if patientReportedRequired && !patientAttested {
		return PASDecision{Outcome: PASPended, NeededItems: []string{"patient-reported-functional-status"}}, nil
	}
	if weeks >= 6 {
		if randSource == nil {
			randSource = rand.Reader
		}
		buf := make([]byte, 6)
		if _, err = io.ReadFull(randSource, buf); err != nil {
			return PASDecision{Outcome: PASDenied}, fmt.Errorf("shnsdk: SandboxAdjudicate: generate preAuthRef: %w", err)
		}
		return PASDecision{
			Outcome:    PASApproved,
			PreAuthRef: "PA-" + hex.EncodeToString(buf),
			ValidUntil: now.AddDate(0, 0, 90).Format("2006-01-02"),
		}, nil
	}
	return PASDecision{Outcome: PASDenied}, nil
}

// sandboxQRItem is the QuestionnaireResponse item shape the sandbox rules read.
//
// NAMED rather than declared inline because QuestionnaireResponse.item is
// self-recursive and an anonymous struct cannot reference itself. FHIR nests
// items on two axes — item.item and item.answer.item — both contentReferencing
// back to QuestionnaireResponse.item, so both fields below are required: without
// them nested items are discarded at json.Unmarshal and no loop can recover them.
//
// Accepts valueDecimal as well as valueInteger for conservative-therapy-weeks:
// the operated $populate engine emits a CQL numeric as valueDecimal regardless
// of item type.
type sandboxQRItem struct {
	LinkId    string `json:"linkId"`
	Extension []struct {
		Url            string `json:"url"`
		ValueSignature *struct {
			Type []struct {
				System string `json:"system"`
				Code   string `json:"code"`
			} `json:"type"`
		} `json:"valueSignature"`
		Extension []struct {
			Url         string  `json:"url"`
			ValueString *string `json:"valueString"`
			ValueDate   *string `json:"valueDate"`
		} `json:"extension"`
	} `json:"extension"`
	Answer []struct {
		ValueInteger *int            `json:"valueInteger"`
		ValueDecimal *float64        `json:"valueDecimal"`
		ValueBoolean *bool           `json:"valueBoolean"`
		ValueString  *string         `json:"valueString"`
		Item         []sandboxQRItem `json:"item"`
	} `json:"answer"`
	Item []sandboxQRItem `json:"item"`
}

// sandboxQRItemsFlattened returns every item in the subtree, at every depth, on
// both recursion axes. The rules below select items by linkId and apply the same
// rule wherever one is found, so nesting depth carries no meaning for them.
//
// Depth is not the same as UNIQUENESS, and this function does not assert it.
// FHIR's que-2 makes linkIds unique within a QUESTIONNAIRE, not within a
// QuestionnaireResponse: a group with repeats=true produces one QR item per
// occurrence, all sharing the group's linkId. Those duplicate ITEMS were
// unreachable while nested items were discarded at unmarshal; flattening makes
// them reachable for the first time, so the caller must decide what to do about
// them rather than letting a last-write-wins switch pick one. See
// sandboxAmbiguousAdjudicationInput, which refuses both that shape and its
// answer-axis sibling (one item, several answers).
//
// Not depth-capped: a cap would silently drop clinical content below it, which
// is the failure this function exists to prevent.
func sandboxQRItemsFlattened(items []sandboxQRItem) []sandboxQRItem {
	var out []sandboxQRItem
	for _, it := range items {
		out = append(out, it)
		for _, a := range it.Answer {
			out = append(out, sandboxQRItemsFlattened(a.Item)...)
		}
		out = append(out, sandboxQRItemsFlattened(it.Item)...)
	}
	return out
}

// sandboxAdjudicationReadLinkIDs is the set of linkIds the sandbox rules
// consume — the ONE definition both the consuming switch in
// parseSandboxAdjudicationInputs and the ambiguity guard are held to. Only
// these are checked for ambiguity: a repeating group or multi-answer item
// elsewhere in the QR is legal, none of this responder's business, and must not
// turn into an error. pasresponder_test.go pins its ambiguity table to this set
// in both directions, so a sixth input added here without a test row reds the
// sweep.
var sandboxAdjudicationReadLinkIDs = map[string]bool{
	"conservative-therapy-weeks": true,
	"prior-surgery":              true,
	"high-disability":            true,
	"patient-reported-required":  true,
	"functional-status-oswestry": true,
}

// sandboxAmbiguousAdjudicationInput returns a non-nil error when the flattened
// items are ambiguous about any adjudication input, in EITHER of FHIR's
// repeating encodings:
//
//   - ITEM axis: two or more items share a read linkId (a group with
//     repeats=true yields one item per occurrence). The consuming switch is
//     last-write-wins, so without this it would decide on whichever occurrence
//     the walk visited last.
//   - ANSWER axis: one item carries more than one answer
//     (QuestionnaireResponse.item.answer is 0..*; an item with repeats=true
//     records its occurrences this way). The rules read a single value, so
//     without this they would decide on Answer[0] and discard the rest —
//     silently, with err == nil.
//
// Both are the same defect: a prior authorization decided on data the payload
// does not actually assert. Cardinality is the rule, not disagreement — two
// answers that happen to agree are still a repeating item the rules do not
// model, and choosing one "because they agree" is a judgment this function
// exists to refuse. The answer-axis check fires per item as the walk visits
// it; the item-axis check needs the whole walk, so it runs after — a
// document-later multi-answer item is therefore reported ahead of a
// document-earlier duplicate pair. Either refusal is correct; only ONE is
// reported, and it names the linkId so the operator can act on it. No package
// prefix here — SandboxAdjudicate wraps this with "shnsdk: SandboxAdjudicate: ",
// and doubling it reads as a bug to the client.
func sandboxAmbiguousAdjudicationInput(items []sandboxQRItem) error {
	counts := map[string]int{}
	var order []string
	for _, it := range items {
		if !sandboxAdjudicationReadLinkIDs[it.LinkId] {
			continue
		}
		if len(it.Answer) > 1 {
			return fmt.Errorf("item %q carries %d answers: the adjudication input is ambiguous", it.LinkId, len(it.Answer))
		}
		if counts[it.LinkId] == 0 {
			order = append(order, it.LinkId)
		}
		counts[it.LinkId]++
	}
	for _, l := range order {
		if counts[l] > 1 {
			return fmt.Errorf("%d items share linkId %q (a repeating group): the adjudication input is ambiguous", counts[l], l)
		}
	}
	return nil
}

// parseSandboxAdjudicationInputs reads the QR items the sandbox rules need.
// weeks defaults 0; priorSurgery/highDisability/patientReportedRequired default
// false; attested is true ONLY when the functional-status-oswestry item carries
// a non-empty answer AND a WELL-FORMED clinician attestation — the attestation
// extension with non-empty NPI, text, and date sub-extensions (FR-16 requires
// all three). patientAttested is true when the item carries a standard
// questionnaireresponse-signature (Author's Signature, system
// urn:iso-astm:E1762-95:2013, code 1.2.840.10065.1.12.1.1) AND a non-empty
// answer (FR-27). Metadata alone, or a signature missing the correct type,
// does not satisfy either attestation.
//
// Every case below reads it.Answer[0] — safe ONLY because
// sandboxAmbiguousAdjudicationInput has already refused any read item with
// more than one answer. Do not read an answer before that guard runs.
func parseSandboxAdjudicationInputs(qrJSON []byte) (weeks int, attested, priorSurgery, highDisability, patientReportedRequired, patientAttested bool, err error) {
	var qr struct {
		Item []sandboxQRItem `json:"item"`
	}
	if e := json.Unmarshal(qrJSON, &qr); e != nil {
		return 0, false, false, false, false, false, fmt.Errorf("parse QuestionnaireResponse: %w", e)
	}
	items := sandboxQRItemsFlattened(qr.Item)
	// Refuse rather than choose. On a QR recording 2 weeks of conservative
	// therapy in one occurrence and 8 in another — whether as two items or as two
	// answers on one item — the rules below would decide on one of them with no
	// error. Declining to decide is the only safe answer: this is a prior
	// authorization, and an arbitrary choice between two clinical facts is worse
	// than an error.
	if e := sandboxAmbiguousAdjudicationInput(items); e != nil {
		return 0, false, false, false, false, false, e
	}
	for _, it := range items {
		switch it.LinkId {
		case "conservative-therapy-weeks":
			// Accept valueInteger (managed FillQuestionnaire) OR valueDecimal (the operated
			// $populate engine — HAPI emits a CQL numeric as valueDecimal regardless of item
			// type; spike 2026-06-19). Whole-number weeks, so int(decimal) is exact.
			if len(it.Answer) > 0 {
				if it.Answer[0].ValueInteger != nil {
					weeks = *it.Answer[0].ValueInteger
				} else if it.Answer[0].ValueDecimal != nil {
					weeks = int(*it.Answer[0].ValueDecimal)
				}
			}
		case "prior-surgery":
			if len(it.Answer) > 0 && it.Answer[0].ValueBoolean != nil {
				priorSurgery = *it.Answer[0].ValueBoolean
			}
		case "high-disability":
			if len(it.Answer) > 0 && it.Answer[0].ValueBoolean != nil {
				highDisability = *it.Answer[0].ValueBoolean
			}
		case "patient-reported-required":
			if len(it.Answer) > 0 && it.Answer[0].ValueBoolean != nil {
				patientReportedRequired = *it.Answer[0].ValueBoolean
			}
		case "functional-status-oswestry":
			hasAnswer := len(it.Answer) > 0 && it.Answer[0].ValueString != nil && *it.Answer[0].ValueString != ""
			hasValidAttestation := false
			for _, ext := range it.Extension {
				if ext.Url == ClinicianAttestationExt {
					var npi, text, date string
					for _, sub := range ext.Extension {
						switch sub.Url {
						case "npi":
							if sub.ValueString != nil {
								npi = *sub.ValueString
							}
						case "text":
							if sub.ValueString != nil {
								text = *sub.ValueString
							}
						case "date":
							if sub.ValueDate != nil {
								date = *sub.ValueDate
							}
						}
					}
					if npi != "" && text != "" && date != "" {
						hasValidAttestation = true
					}
				}
				if ext.Url == QRSignatureExt && ext.ValueSignature != nil {
					for _, ty := range ext.ValueSignature.Type {
						if ty.System == "urn:iso-astm:E1762-95:2013" && ty.Code == "1.2.840.10065.1.12.1.1" {
							patientAttested = hasAnswer
						}
					}
				}
			}
			attested = hasAnswer && hasValidAttestation
		}
	}
	return weeks, attested, priorSurgery, highDisability, patientReportedRequired, patientAttested, nil
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
