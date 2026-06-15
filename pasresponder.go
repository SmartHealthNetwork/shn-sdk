package shnsdk

// PORTED-standalone from internal/pas/pas.go. Parity tests live in
// test/sdkparity/pas_parity_test.go (TestPASResponderParity). Every exported
// function in this file is byte-identical in logic to its internal twin; the
// difference is the package-level prefix and the use of pasFullURLFor /
// pasEnsureID / pasInjectResourceType (already in sdk/pas.go) instead of
// the internal-private copies.

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

// ClaimBundle is the parsed content of a PAS preauthorization or update Bundle.
// It mirrors internal/pas.ClaimBundle field-for-field so the parity test can
// compare both sides without reflection. ParseClaimBundle produces it in a
// single pass (all mandatory fields in one scan, no duplicate parses of the
// same bytes — trust-critical parsing is where drift bugs live).
//
// ClaimPatient and SRSubject are always present (ParseClaimBundle errors
// otherwise). QRSubject may be "" — a QR subject is optional in R4 — so the
// caller decides; this PA workflow's gateway REQUIRES it (H2) and rejects a
// subjectless QR.
//
// HasDiagnosticReport / DiagnosticReportJSON are set when the bundle includes
// a DiagnosticReport (prior-auth update bundles carrying supplemental evidence).
// ProvenanceJSON is set when the bundle includes a Provenance (FR-32).
// RelatedClaim is Claim.related[0].claim.identifier.value (non-empty for update
// bundles; empty for initial submit bundles).
//
// PORTED-standalone: internal/pas.ClaimBundle (:161–187).
type ClaimBundle struct {
	QRJSON               []byte
	SRJSON               []byte
	ClaimPatient         string
	SRSubject            string
	QRSubject            string
	QRID                 string // QuestionnaireResponse.id
	HasDiagnosticReport  bool
	DiagnosticReportJSON []byte
	DiagnosticReportID   string
	// DiagnosticReportSubject is the supplemental report's subject.reference
	// (empty if no DiagnosticReport). The payer binds it to the same patient
	// as the Claim (FR-32 §5).
	DiagnosticReportSubject string
	ProvenanceJSON          []byte
	// ProvenanceTargets / ProvenanceAgents are the supplemental Provenance's
	// target[].reference and agent[].who.reference.
	ProvenanceTargets []string
	ProvenanceAgents  []string
	// ProvenancePolicies captures Provenance.policy[] (URIs).
	ProvenancePolicies []string
	RelatedClaim       string // Claim.related[0].claim.identifier.value
}

// ParseClaimBundle parses a PAS preauthorization or update Bundle in one pass.
// It indexes entries by resourceType rather than position, supporting both the
// 3-entry submit bundle (Claim+QR+SR) and the 4–5 entry update bundle
// (Claim+QR+SR+DiagnosticReport?+Provenance?) in arbitrary order.
//
// Errors on: wrong top-level resourceType, fewer than 3 entries, duplicate
// resource types, unexpected/missing resourceType in any entry, missing
// Claim.patient.reference, missing ServiceRequest.subject.reference, or missing
// any of Claim/QR/SR.
//
// PORTED-standalone: internal/pas.ParseClaimBundle (:198–372).
func ParseClaimBundle(data []byte) (ClaimBundle, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ClaimBundle{}, fmt.Errorf("shnsdk: parse bundle: %w", err)
	}
	if probe.ResourceType != "Bundle" {
		return ClaimBundle{}, fmt.Errorf("shnsdk: expected resourceType Bundle, got %q", probe.ResourceType)
	}
	if len(probe.Entry) < 3 {
		return ClaimBundle{}, fmt.Errorf("shnsdk: bundle requires at least 3 entries (Claim+QR+SR), got %d", len(probe.Entry))
	}

	var (
		seenClaim bool
		seenQR    bool
		seenSR    bool
		seenDR    bool
		seenProv  bool
		b         ClaimBundle
	)

	for i, e := range probe.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			return ClaimBundle{}, fmt.Errorf("shnsdk: parse entry[%d]: %w", i, err)
		}

		switch rt.ResourceType {
		case "Claim":
			if seenClaim {
				return ClaimBundle{}, fmt.Errorf("shnsdk: duplicate Claim in bundle")
			}
			seenClaim = true
			var claim struct {
				Patient struct {
					Reference string `json:"reference"`
				} `json:"patient"`
				Related []struct {
					Claim struct {
						Identifier struct {
							Value string `json:"value"`
						} `json:"identifier"`
					} `json:"claim"`
				} `json:"related"`
			}
			if err := json.Unmarshal(e.Resource, &claim); err != nil {
				return ClaimBundle{}, fmt.Errorf("shnsdk: parse Claim entry: %w", err)
			}
			b.ClaimPatient = claim.Patient.Reference
			if len(claim.Related) > 0 {
				b.RelatedClaim = claim.Related[0].Claim.Identifier.Value
			}

		case "QuestionnaireResponse":
			if seenQR {
				return ClaimBundle{}, fmt.Errorf("shnsdk: duplicate QuestionnaireResponse in bundle")
			}
			seenQR = true
			b.QRJSON = []byte(e.Resource)
			var qr struct {
				Id      string `json:"id"`
				Subject struct {
					Reference string `json:"reference"`
				} `json:"subject"`
			}
			if err := json.Unmarshal(e.Resource, &qr); err != nil {
				return ClaimBundle{}, fmt.Errorf("shnsdk: parse QR entry: %w", err)
			}
			b.QRSubject = qr.Subject.Reference
			b.QRID = qr.Id

		case "ServiceRequest":
			if seenSR {
				return ClaimBundle{}, fmt.Errorf("shnsdk: duplicate ServiceRequest in bundle")
			}
			seenSR = true
			b.SRJSON = []byte(e.Resource)
			var sr struct {
				Subject struct {
					Reference string `json:"reference"`
				} `json:"subject"`
			}
			if err := json.Unmarshal(e.Resource, &sr); err != nil {
				return ClaimBundle{}, fmt.Errorf("shnsdk: parse SR entry: %w", err)
			}
			b.SRSubject = sr.Subject.Reference

		case "DiagnosticReport":
			if seenDR {
				return ClaimBundle{}, fmt.Errorf("shnsdk: duplicate DiagnosticReport in bundle")
			}
			seenDR = true
			b.DiagnosticReportJSON = []byte(e.Resource)
			b.HasDiagnosticReport = true
			var dr struct {
				Id      string `json:"id"`
				Subject struct {
					Reference string `json:"reference"`
				} `json:"subject"`
			}
			if err := json.Unmarshal(e.Resource, &dr); err != nil {
				return ClaimBundle{}, fmt.Errorf("shnsdk: parse DiagnosticReport entry: %w", err)
			}
			b.DiagnosticReportSubject = dr.Subject.Reference
			b.DiagnosticReportID = dr.Id

		case "Provenance":
			if seenProv {
				return ClaimBundle{}, fmt.Errorf("shnsdk: duplicate Provenance in bundle")
			}
			seenProv = true
			b.ProvenanceJSON = []byte(e.Resource)
			var prov struct {
				Target []struct {
					Reference string `json:"reference"`
				} `json:"target"`
				Agent []struct {
					Who struct {
						Reference string `json:"reference"`
					} `json:"who"`
				} `json:"agent"`
				Policy []string `json:"policy"`
			}
			if err := json.Unmarshal(e.Resource, &prov); err != nil {
				return ClaimBundle{}, fmt.Errorf("shnsdk: parse Provenance entry: %w", err)
			}
			for _, t := range prov.Target {
				if t.Reference != "" {
					b.ProvenanceTargets = append(b.ProvenanceTargets, t.Reference)
				}
			}
			for _, a := range prov.Agent {
				if a.Who.Reference != "" {
					b.ProvenanceAgents = append(b.ProvenanceAgents, a.Who.Reference)
				}
			}
			for _, p := range prov.Policy {
				if p != "" {
					b.ProvenancePolicies = append(b.ProvenancePolicies, p)
				}
			}

		default:
			return ClaimBundle{}, fmt.Errorf("shnsdk: unexpected/missing resourceType in bundle entry: %q", rt.ResourceType)
		}
	}

	if !seenClaim {
		return ClaimBundle{}, fmt.Errorf("shnsdk: bundle missing required Claim entry")
	}
	if !seenQR {
		return ClaimBundle{}, fmt.Errorf("shnsdk: bundle missing required QuestionnaireResponse entry")
	}
	if !seenSR {
		return ClaimBundle{}, fmt.Errorf("shnsdk: bundle missing required ServiceRequest entry")
	}
	if b.ClaimPatient == "" {
		return ClaimBundle{}, fmt.Errorf("shnsdk: claim missing patient.reference")
	}
	if b.SRSubject == "" {
		return ClaimBundle{}, fmt.Errorf("shnsdk: ServiceRequest missing subject.reference")
	}

	return b, nil
}

// SandboxAdjudicate applies the sandbox adjudication rules to the QR.
// hasDiagnosticReport reports whether the bundle carried an operative
// DiagnosticReport. Returns a PASDecision with the outcome and — on
// Approved — a generated preAuthRef + validUntil. randSource seeds the auth
// number (nil → crypto/rand, matching the nil-safe internal default).
//
// SANDBOX adjudication policy — the reference implementation for
// quickstarts/tests/feedsmoke. A real payer implements its own PriorAuth. DEF-4
// stub (AI-9 holds).
//
// PORTED-standalone: internal/pas.Adjudicate (:379–407).
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

// parseSandboxAdjudicationInputs reads the QR items the sandbox rules need.
// Extension URLs for clinician-attestation and QR-signature are ported
// standalone from internal/dtr constants (byte-identical).
//
// PORTED-standalone: internal/pas.parseAdjudicationInputs (:418–513).
func parseSandboxAdjudicationInputs(qrJSON []byte) (weeks int, attested, priorSurgery, highDisability, patientReportedRequired, patientAttested bool, err error) {
	// Extension URL constants — ported byte-for-byte from internal/dtr.
	const (
		clinicianAttestationExt = "http://smarthealth.network/fhir/StructureDefinition/clinician-attestation"
		qrSignatureExt          = "http://hl7.org/fhir/StructureDefinition/questionnaireresponse-signature"
	)
	var qr struct {
		Item []struct {
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
				ValueInteger *int    `json:"valueInteger"`
				ValueBoolean *bool   `json:"valueBoolean"`
				ValueString  *string `json:"valueString"`
			} `json:"answer"`
		} `json:"item"`
	}
	if e := json.Unmarshal(qrJSON, &qr); e != nil {
		return 0, false, false, false, false, false, fmt.Errorf("parse QuestionnaireResponse: %w", e)
	}
	for _, it := range qr.Item {
		switch it.LinkId {
		case "conservative-therapy-weeks":
			if len(it.Answer) > 0 && it.Answer[0].ValueInteger != nil {
				weeks = *it.Answer[0].ValueInteger
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
				if ext.Url == clinicianAttestationExt {
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
				if ext.Url == qrSignatureExt && ext.ValueSignature != nil {
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

// BuildClaimResponse builds a FHIR ClaimResponse with outcome "complete",
// use "preauthorization", the preAuthRef, preAuthPeriod ending validUntil,
// patient ref, and identifier carrying correlationID.
//
// PORTED-standalone: internal/pas.BuildClaimResponse (:518–550).
func BuildClaimResponse(preAuthRef, validUntil, patientRef, correlationID string, created time.Time) ([]byte, error) {
	cr := fhir.ClaimResponse{
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
		Outcome: fhir.ClaimProcessingCodesComplete,
		Identifier: []fhir.Identifier{{
			System: strPtr("urn:shn:correlation"),
			Value:  strPtr(correlationID),
		}},
	}
	if preAuthRef != "" {
		cr.PreAuthRef = strPtr(preAuthRef)
		cr.PreAuthPeriod = &fhir.Period{
			End: strPtr(validUntil),
		}
	}
	raw, err := json.Marshal(cr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal ClaimResponse: %w", err)
	}
	return pasInjectResourceType(raw, "ClaimResponse")
}

// BuildPendedResponse builds the exchange-1 PENDED response (FR-20): a
// collection Bundle holding a ClaimResponse (outcome=queued,
// use=preauthorization) and a Task (status=requested) whose inputs enumerate
// the supplemental items the payer needs. The provider distinguishes this from
// an approved bare ClaimResponse by resourceType (Bundle ⇒ pended). The
// pended/approved business outcome stays in the payload — the payload-blind Hub
// never sees it (AI-2).
//
// PORTED-standalone: internal/pas.BuildPendedResponse (:617–669).
func BuildPendedResponse(patientRef, correlationID string, needed []string, created time.Time) ([]byte, error) {
	cr := fhir.ClaimResponse{
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
		Outcome: fhir.ClaimProcessingCodesQueued,
		Identifier: []fhir.Identifier{{
			System: strPtr("urn:shn:correlation"),
			Value:  strPtr(correlationID),
		}},
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
		Type: fhir.BundleTypeCollection,
		Entry: []fhir.BundleEntry{
			{FullUrl: strPtr(crURL), Resource: json.RawMessage(crJSON)},
			{FullUrl: strPtr(taskURL), Resource: json.RawMessage(taskJSON)},
		},
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
// PORTED-standalone: internal/pas.taskInputJSON (:676–683).
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
// PORTED-standalone: internal/pas.taskJSON (:686–699).
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
// PORTED-standalone: internal/pas.buildTask (:707–729).
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

// Denied ClaimResponse types — ported standalone from internal/pas (:958–1014).

// pasDeniedCR is a minimal FHIR R4 ClaimResponse expressing a Da Vinci PAS
// DENIAL: outcome=complete (the request was processed; denial is a decision,
// not an error), the reviewAction extension on the item carrying reviewActionCode
// A3 (Not Certified), a plain-language disposition (rationale), and a processNote
// carrying the appeal window + peer-to-peer instruction. NO preAuthRef — a denial
// issues no authorization number, so ParseClaimResponse reads it as not-approved.
//
// PORTED-standalone: internal/pas.claimResponseDeniedJSON (:958).
type pasDeniedCR struct {
	ResourceType string                   `json:"resourceType"`
	Status       string                   `json:"status"`
	Type         pasDeniedCodeableConcept `json:"type"`
	Use          string                   `json:"use"`
	Patient      pasDeniedReference       `json:"patient"`
	Created      string                   `json:"created"`
	Insurer      pasDeniedReference       `json:"insurer"`
	Outcome      string                   `json:"outcome"`
	Disposition  string                   `json:"disposition"`
	Identifier   []pasDeniedIdentifier    `json:"identifier"`
	Item         []pasDeniedItem          `json:"item"`
	ProcessNote  []pasDeniedProcessNote   `json:"processNote"`
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

const (
	// pasSystemX12ReviewAction is the X12 278 review-action code system used by
	// the Da Vinci PAS reviewAction extension. A3 = "Not Certified" (denied).
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
// PORTED-standalone: internal/pas.BuildDeniedResponse (:1019–1060).
func BuildDeniedResponse(patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	cr := pasDeniedCR{
		ResourceType: "ClaimResponse",
		Status:       "active",
		Type:         pasDeniedCodeableConcept{Coding: []pasDeniedCoding{{System: "http://terminology.hl7.org/CodeSystem/claim-type", Code: "professional"}}},
		Use:          "preauthorization",
		Patient:      pasDeniedReference{Reference: patientRef},
		Created:      created.UTC().Format(time.RFC3339),
		Insurer:      pasDeniedReference{Reference: "Organization/payer"},
		Outcome:      "complete",
		Disposition:  rationale,
		Identifier:   []pasDeniedIdentifier{{System: "urn:shn:correlation", Value: correlationID}},
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
