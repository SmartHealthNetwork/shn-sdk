package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// PriorAuthResult is the prior-auth orchestrator outcome. Outcome is the SAME
// vocabulary the discovery descriptor's expectedPriorAuth speaks.
type PriorAuthResult struct {
	// Outcome is one of:
	//   "approved" | "pended" | "denied" — the payer's PAS determination
	//   "no-pa-required" — the CRD card said no prior authorization is needed
	//   "not-covered"    — the CRD card said the plan does not cover this service and
	//                      the request did not set ProceedOnNotCovered, so no PAS leg
	//                      ran. DISTINCT from "no-pa-required" (v0.46.0): before the
	//                      split, a coverage refusal reported as no-PA-required.
	Outcome    string
	PreAuthRef string // set when approved
	ValidUntil string // set when approved

	// Partial (v0.46.0, additive) is true when Outcome=="approved" but the payer's X12 306
	// reviewActionCode was A2 ("Certified – partial") rather than a full certification: an
	// authorization number WAS issued, but coverage is not complete. False (the zero value)
	// for every full approval — it never leaks onto an A1/preAuthRef-only approved result.
	// See ParseClaimResponse's doc comment: this branch is parse-side only, not currently
	// live-proven against any producer this SDK can drive.
	Partial bool
	// Disposition (v0.46.0, additive) carries the payer's own free-text disposition/display
	// text onto the result. Currently set only when Partial is true, so a partial
	// certification's scope (what was and was not certified) is not lost — PriorAuthResult
	// had no existing field that fit an approved-outcome's payer-sourced text.
	Disposition string

	// NeededItems + Resume are set when Outcome=="pended": the supplemental
	// items the payer's Task enumerates, and a serializable handle to ResumePriorAuth.
	NeededItems []NeededItem
	Resume      *PriorAuthResume

	// Denial is set when Outcome=="denied": the FR-22 denial content.
	Denial *Denial

	// ProcessNotes are the payer's processNote entries on an approved or
	// denied decision, in order, each with its type as sent ("" when the
	// payer gave none); notes without text are skipped. Nil when there are
	// none.
	ProcessNotes []PASProcessNote
	// ReviewAction is the payer's review action that decided the outcome
	// (the A3 or A2 action of a denial, the A2 action of a partial approval,
	// the A1 action of an approval), with its X12 886 reasons, exactly as
	// sent. When several items carry review actions, it is the first action
	// carrying that code, in item and adjudication order, across all items.
	// Nil when the payer stated none.
	ReviewAction *PASReviewAction
	// DenialReasons are the claim adjustment reason (CARCSystem) and
	// remittance advice remark (RARCSystem) codes on the payer's item
	// adjudications, in order, exactly as sent. Nil when there are none.
	DenialReasons []PASCoding
}

// NeededItem is one supplemental item the payer's FR-20 Task asks for on a pended
// PA. Code is the Task.input value (e.g. "operative-diagnostic-report"); Display is
// its human-readable label (Task.input.type.text or questionnaire coding display).
// Questionnaire requests use valueIdentifier.value; routing inputs are excluded.
// Typed so a dev/CLI sees exactly
// what the payer is asking for.
type NeededItem struct {
	Code    string
	Display string
}

// Denial is the FR-22 denied-PA content, parsed from the PAS denied ClaimResponse
// (reviewActionCode + disposition + processNote). ReasonCode is the actual PAS
// reviewActionCode (X12 306) — "A3" ("Not Certified"), the conformant denial code SHN's
// own producer emits, or the real reference payer's (br-payer) observed "A2" denial shape
// (a code/display self-contradiction in that RI — see sdk/pas.go's reviewAction* consts).
type Denial struct {
	ReasonCode string
	Rationale  string   // ClaimResponse.disposition
	AppealNote []string // ClaimResponse.processNote[].text (repeatable)
}

// PriorAuthResume is a SERIALIZABLE handle to resume a pended prior auth. It JSON
// round-trips and carries NO live state — a real integration persists it across the
// hours-or-days gap between pend and amend. The fields are exactly what the exchange-2
// ClaimUpdate needs: the original submit correlation the Claim.related[] references
// (FR-21), the patient/coverage refs, the bound subject PCI, and the submit QR/SR the
// update re-includes unchanged.
type PriorAuthResume struct {
	OriginalCorrelationID string          `json:"originalCorrelationId"`
	PatientRef            string          `json:"patientRef"`
	CoverageRef           string          `json:"coverageRef"`
	SubjectPCI            string          `json:"subjectPci"`
	QRJSON                json.RawMessage `json:"qrJson"`
	SRJSON                json.RawMessage `json:"srJson"`
	NeededItems           []NeededItem    `json:"neededItems"`

	// PayerID is the payer-identity claim the origination resolved its test
	// counterparty from (persona payerId — stamped by the shn CLI before the
	// handle is written). Resume re-resolves it through the directory with the
	// same refusal semantics. ADDITIVE optional field: an older handle simply
	// lacks it and resume falls back to the legacy demoResponders path.
	// Local-file format (grow-only); NOT part of any signed/sealed wire payload.
	PayerID *PayerIdentifier `json:"payerId,omitempty"`

	// MemberID is the bare member id the pended submit named; the resume ClaimUpdate
	// names the SAME member, so it rides the handle beside CoverageRef (which stays
	// the Reference-shaped value the QR-context / native-lane roles need). ADDITIVE
	// serialized field, same grow-only local-file rules as PayerID above.
	MemberID string `json:"memberId,omitempty"`

	// MemberIDSystem is the namespace the requester's own records name that
	// member under — read from its Patient at submit time, and carried so the
	// amendment names the member exactly as the submission did. ADDITIVE
	// serialized field, same grow-only local-file rules as PayerID above.
	MemberIDSystem string `json:"memberIdSystem,omitempty"`

	// ProviderJSON is the requesting provider's own record the pended submit
	// named (see ConformantClaimInputs.Provider). The amendment names the SAME
	// party — a payer that stored the authorization under one provider and is
	// amended under another has two requests, not one — so the record rides the
	// handle rather than being supplied again at resume time. ADDITIVE serialized
	// field, same grow-only local-file rules as PayerID and MemberID above.
	ProviderJSON json.RawMessage `json:"providerJson,omitempty"`

	// CoverageJSON is the member's own Coverage record the pended submit named
	// (see ConformantClaimInputs.Coverage). The amendment is made under the SAME
	// policy — a payer that stored the authorization under one coverage and is
	// amended under another has two requests, not one — so the record rides the
	// handle for the same reason ProviderJSON does. ADDITIVE serialized field,
	// same grow-only local-file rules as the fields above.
	CoverageJSON json.RawMessage `json:"coverageJson,omitempty"`

	// Continuation holds the facts an inquiry needs (Identity.Inquire):
	// metadata of the request and the payer's answer, no clinical content.
	// ADDITIVE serialized field: a handle written before inquiry support lacks
	// it, and Inquire refuses such a handle (ErrNoContinuation).
	Continuation *PriorAuthContinuation `json:"continuation,omitempty"`
}

// pasBundleBaseURL is the deterministic base for entry fullUrls. A non-urn:uuid
// fullUrl SHALL be a URL consistent with Resource.id (FHIR bdl-7 / reference
// resolution): with fullUrl "<base>/ServiceRequest/sr-x", a relative reference
// "ServiceRequest/sr-x" elsewhere in the bundle resolves to this entry. Ported
// byte-for-byte from internal/pas.bundleBaseURL.
const pasBundleBaseURL = "https://shn.example/fhir"

// pasBundleIdentifierSystem is the Bundle.identifier.system stamped on every PAS
// Bundle this package builds (submit, conformant submit, update). Promoted from the
// repeated literal once a sibling builder (BuildConformantClaimBundle) landed a third
// use (a prior review deferral). The value is identical to the prior literal, so the
// byte-parity-locked builders stay byte-identical.
const pasBundleIdentifierSystem = "urn:shn:pas:bundle"

// pasCorrelationSystem is the business-identifier system every SHN-built PAS
// Claim / ClaimResponse carries (Claim.identifier, ClaimResponse.identifier and —
// from PAS line 2.1 on — ClaimResponse.request.identifier).
const pasCorrelationSystem = "urn:shn:correlation"

// pasFullURLFor returns the resolvable fullUrl for a bundle entry resource, derived
// from its resourceType + id. Errors if either is missing. Ported standalone from
// internal/pas.fullURLFor.
func pasFullURLFor(resourceJSON []byte) (string, error) {
	var meta struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(resourceJSON, &meta); err != nil {
		return "", fmt.Errorf("shnsdk: fullURLFor: parse: %w", err)
	}
	if meta.ResourceType == "" || meta.ID == "" {
		return "", fmt.Errorf("shnsdk: fullURLFor: resource missing resourceType (%q) or id (%q)", meta.ResourceType, meta.ID)
	}
	return pasBundleBaseURL + "/" + meta.ResourceType + "/" + meta.ID, nil
}

// pasInjectResourceType adds "resourceType":"<rt>" to a marshalled JSON object.
// samply structs do not include resourceType in their JSON tags. Ported standalone
// from internal/pas.injectResourceType.
func pasInjectResourceType(raw []byte, rt string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("shnsdk: inject resourceType: %w", err)
	}
	rtJSON, _ := json.Marshal(rt)
	m["resourceType"] = json.RawMessage(rtJSON)
	return json.Marshal(m)
}

// buildPASClaim constructs a FHIR Claim JSON for a preauthorization request
// (related is nil for the initial submit bundle; the conformant builder
// BuildConformantClaimBundle reuses it. The X12 1365 service-type coding on Claim.item
// carries the licensed binding target, the actual procedure stays on the referenced
// ServiceRequest).
//
// providerRef names the requesting provider — the party the payer matches a later
// inquiry on, together with the member id. It is a reference the assembled Bundle
// resolves; see pasProviderEntry and checkPASProviderResolves.
//
// coverageRef is stamped as insurance[0].coverage.reference here, but
// BuildConformantClaimBundle OVERWRITES that element bundle-side with a reference to
// the Coverage ENTRY the request carries (see setInsuranceCoverageEntryRef), exactly
// as it overwrites insurer via repointInsurerToEntry. Nothing conformant reaches the
// wire carrying this value.
func buildPASClaim(patientRef, coverageRef, providerRef, correlationID string, created time.Time) ([]byte, error) {
	claim := fhir.Claim{
		Id:     strPtr("claim-" + correlationID),
		Status: fhir.FinancialResourceStatusCodesActive,
		Type: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System: strPtr("http://terminology.hl7.org/CodeSystem/claim-type"),
				Code:   strPtr("professional"),
			}},
		},
		Use:      fhir.UsePreauthorization,
		Patient:  fhir.Reference{Reference: strPtr(patientRef)},
		Created:  created.UTC().Format(time.RFC3339),
		Provider: fhir.Reference{Reference: strPtr(providerRef)},
		Insurer:  &fhir.Reference{Reference: strPtr("Organization/payer")},
		Priority: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				Code: strPtr("normal"),
			}},
		},
		Insurance: []fhir.ClaimInsurance{{
			Sequence: 1,
			Focal:    true,
			Coverage: fhir.Reference{Reference: strPtr(coverageRef)},
		}},
		Item: []fhir.ClaimItem{{
			Sequence: 1,
			Category: &fhir.CodeableConcept{
				Coding: []fhir.Coding{{
					System:  strPtr("https://codesystem.x12.org/005010/1365"),
					Code:    strPtr("1"),
					Display: strPtr("Medical Care"),
				}},
			},
			ProductOrService: fhir.CodeableConcept{
				Coding: []fhir.Coding{{
					System:  strPtr("https://codesystem.x12.org/005010/1365"),
					Code:    strPtr("1"),
					Display: strPtr("Medical Care"),
				}},
			},
		}},
		Identifier: []fhir.Identifier{{
			System: strPtr("urn:shn:correlation"),
			Value:  strPtr(correlationID),
		}},
	}

	raw, err := json.Marshal(claim)
	if err != nil {
		return nil, err
	}
	return pasInjectResourceType(raw, "Claim")
}

// Conformant $submit fixed (deterministic) resource ids. The LEAN conformant Claim
// Bundle (BuildConformantClaimBundle) uses these so the bundle-local references are
// stable + internally consistent. Demo-persona only — no br-payer foreign seed.
const (
	conformantPASClaimID          = "convergence-claim"
	conformantPASServiceRequestID = "convergence-sr"
	conformantPASDeviceRequestID  = "convergence-dr"
	conformantPASQRID             = "convergence-qr"

	// Conformant amended re-POST fixed ids (BuildConformantClaimUpdateBundle).
	conformantPASClaimUpdateID = "convergence-claim-update"
	conformantPASUpdateQRID    = "convergence-qr-amended"
	conformantPASDRID          = "convergence-dr-operative"
	conformantPASProvID        = "convergence-prov"

	// pasInfoChangedExtensionURL is the Da Vinci PAS Claim-item infoChanged extension. A real PAS
	// payer (br-payer hasInfoChanged, PasSubmitService.java:316/449) re-evaluates an updated item
	// only when it carries this; otherwise it carries-forward the prior decision unchanged.
	pasInfoChangedExtensionURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-infoChanged"

	// extReqService is the Da Vinci PAS extension naming the ServiceRequest the Claim
	// item requests. The conformant Claim carries it (the minimized buildPASClaim does
	// not); it is an EXTENSION URL (not a meta.profile), so it $validates clean against
	// the US-Core-only validator.
	extReqService = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-requestedService"

	// extQRContext is the Da Vinci DTR QuestionnaireResponse-level extension whose
	// valueReference points at the Coverage / ServiceRequest the QR was completed in
	// (FillQuestionnaire emits one per ref). BuildConformantClaimBundle rewrites these to
	// the bundle-local Coverage/SR ids so the builder owns them. MUST match dtr.qrContextExt.
	extQRContext = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/qr-context"

	// -- PAS 2.1+ Claim.item line-detail + Claim.related relationship --
	// (line-conditional, gated on PASDef.ClaimItemLineDetailRequired /
	// ClaimRelatedRelationshipRequired; see linedef.go's delta-table comment).

	// pasExtCertificationType / pasExtServiceItemRequestType are the Da Vinci PAS
	// extension canonicals for Claim.item.extension:certificationType (sliceName
	// "certificationType") and Claim.item.extension:requestType (sliceName
	// "requestType", canonical extension-serviceItemRequestType — the sliceName and
	// the extension's own canonical URL differ, per the PAS package). Verified against
	// the PAS 2.1.0/2.2.1 StructureDefinition-profile-claim(-update).json differential
	// (both slices min=1 starting 2.1.0, unchanged into 2.2.1).
	pasExtCertificationType      = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-certificationType"
	pasExtServiceItemRequestType = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-serviceItemRequestType"

	// pasSystemX12CertificationType / pasSystemX12ServiceItemRequestType are the
	// licensed X12 code systems the two extensions above bind to (required binding,
	// unexpandable offline — same "curated code, allowlisted" posture as the existing
	// X12 1365 productOrService/category and X12 306 reviewAction codes in this file).
	// The codes below (certificationType "I" Initial; requestType "IN" Initial Medical
	// Services Reservation) are copied VERBATIM from the PAS 2.2.1 package's own
	// conformant example instance (example/Claim-MedicalServicesAuthorizationExample.json)
	// — not invented (FR-36 no-hallucination).
	pasSystemX12CertificationType      = "https://codesystem.x12.org/005010/1322"
	pasSystemX12ServiceItemRequestType = "https://codesystem.x12.org/005010/1525"

	// pasSystemCMSPlaceOfService is the CMS place-of-service code system
	// Claim.item.location[x]'s X12278LocationType required binding includes
	// (ValueSet-X12278LocationType.json compose — NOT X12-licensed, a public CMS code
	// set). Code "11" ("Office") is copied verbatim from the same PAS 2.2.1 example
	// instance's locationCodeableConcept (no display given there — omitted here too,
	// rather than inventing one).
	pasSystemCMSPlaceOfService = "https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set"

	// pasRelatedClaimRelationshipSystem is the STANDARD (non-licensed) HL7 terminology
	// CodeSystem Claim.related.relationship's PAS 2.1+ patternCodeableConcept pins to
	// (code "prior") — verified against the PAS 2.1.0/2.2.1
	// StructureDefinition-profile-claim-update.json differential (patternCodeableConcept
	// on Claim.related.relationship, min=1) and confirmed by the same package's
	// example/Claim-HomecareAuthorizationUpdateExample.json.
	pasRelatedClaimRelationshipSystem = "http://terminology.hl7.org/CodeSystem/ex-relatedclaimrelationship"
)

// addPASLineItemDetail appends the PAS 2.1+ Claim.item line-detail — the
// certificationType + requestType extensions and locationCodeableConcept — to
// Claim.item[0], gated on def.ClaimItemLineDetailRequired. No-op (byte-identical)
// when the flag is false (line 2.0). Shared by the submit and update conformant
// builders; called AFTER conformantizePASClaim so it APPENDS to (rather than is
// clobbered by) the existing extension-requestedService slice.
func addPASLineItemDetail(claimJSON []byte, def PASDef) ([]byte, error) {
	if !def.ClaimItemLineDetailRequired {
		return claimJSON, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("addPASLineItemDetail: parse claim: %w", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(m["item"], &items); err != nil {
		return nil, fmt.Errorf("addPASLineItemDetail: parse claim.item: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("addPASLineItemDetail: claim has no item")
	}
	var exts []map[string]any
	if raw, ok := items[0]["extension"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &exts); err != nil {
			return nil, fmt.Errorf("addPASLineItemDetail: parse item extension: %w", err)
		}
	}
	exts = append(exts,
		map[string]any{
			"url": pasExtCertificationType,
			"valueCodeableConcept": map[string]any{"coding": []map[string]any{{
				"system": pasSystemX12CertificationType, "code": "I", "display": "Initial",
			}}},
		},
		map[string]any{
			"url": pasExtServiceItemRequestType,
			"valueCodeableConcept": map[string]any{"coding": []map[string]any{{
				"system": pasSystemX12ServiceItemRequestType, "code": "IN", "display": "Initial Medical Services Reservation",
			}}},
		},
	)
	extJSON, err := json.Marshal(exts)
	if err != nil {
		return nil, fmt.Errorf("addPASLineItemDetail: marshal extension: %w", err)
	}
	items[0]["extension"] = extJSON
	locJSON, err := json.Marshal(map[string]any{"coding": []map[string]any{{
		"system": pasSystemCMSPlaceOfService, "code": "11",
	}}})
	if err != nil {
		return nil, fmt.Errorf("addPASLineItemDetail: marshal location: %w", err)
	}
	items[0]["locationCodeableConcept"] = locJSON
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("addPASLineItemDetail: marshal items: %w", err)
	}
	m["item"] = itemsJSON
	return json.Marshal(m)
}

// addPASLineRelatedRelationship sets Claim.related[0].relationship to the PAS 2.1+
// pattern (ex-relatedclaimrelationship#prior), gated on
// def.ClaimRelatedRelationshipRequired. No-op (byte-identical) when the flag is
// false (line 2.0). Update-builder only (a fresh submit Claim carries no
// related[]; profile-claim in fact FORBIDS it — max=0 — from 2.1 forward, unchanged
// by this build since buildPASClaim never sets Related).
func addPASLineRelatedRelationship(claimJSON []byte, def PASDef) ([]byte, error) {
	if !def.ClaimRelatedRelationshipRequired {
		return claimJSON, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("addPASLineRelatedRelationship: parse claim: %w", err)
	}
	if len(m["related"]) == 0 {
		return nil, fmt.Errorf("addPASLineRelatedRelationship: claim has no related[]")
	}
	var related []map[string]json.RawMessage
	if err := json.Unmarshal(m["related"], &related); err != nil {
		return nil, fmt.Errorf("addPASLineRelatedRelationship: parse related: %w", err)
	}
	if len(related) == 0 {
		return nil, fmt.Errorf("addPASLineRelatedRelationship: claim has empty related[]")
	}
	relJSON, err := json.Marshal(map[string]any{"coding": []map[string]any{{
		"system": pasRelatedClaimRelationshipSystem, "code": "prior",
	}}})
	if err != nil {
		return nil, fmt.Errorf("addPASLineRelatedRelationship: marshal relationship: %w", err)
	}
	related[0]["relationship"] = relJSON
	relatedJSON, err := json.Marshal(related)
	if err != nil {
		return nil, fmt.Errorf("addPASLineRelatedRelationship: marshal related: %w", err)
	}
	m["related"] = relatedJSON
	return json.Marshal(m)
}

// ConformantClaimInputs are the inputs the conformant $submit builder needs from the
// Originator: the answered DTR QuestionnaireResponse + the order ServiceRequest (both
// already demo-persona-bound), the patient/coverage references, the correlation id, and
// the injected clock. The lean bundle uses a contained payor Org (no referenced
// Practitioner). Created drives the deterministic Bundle timestamp/Claim.created.
//
// ContainedInsurer: when true the Claim's insurer is rewritten to reference a CONTAINED
// #cms-payer Organization (mirroring BuildCoverageWithPayer), making the reference
// resolvable by real payers that validate bundle-internal refs (e.g. real br-payer 400s
// "Organization/payer not found"). When false (the default) the insurer stays the generic
// "Organization/payer" — byte-identical to the SHN-native path. Set true ONLY for the
// reference-payer origination lane (targetsBrPayer: ORIGINATION_PROFILE=provider-data, and the Kit's conformant rows).
//
// AbsoluteRefs: when true every internal reference whose value matches a bundle-entry
// relative form ("<resourceType>/<id>") is rewritten to its absolute fullUrl
// (pasBundleBaseURL + "/" + "<resourceType>/<id>"). This makes the bundle self-consistent
// for real payers (e.g. real br-payer HAPI-1094 "not found") that do not resolve relative
// refs against absolute entry fullUrls in a $submit collection bundle. Contained #fragment
// refs and refs that do not match any bundle entry are left untouched. When false (the
// default) the bundle is byte-identical to the SHN-native path. Set true ONLY for the
// reference-payer origination lane (targetsBrPayer: ORIGINATION_PROFILE=provider-data, and the Kit's conformant rows).
type ConformantClaimInputs struct {
	QR []byte
	SR []byte
	// Provider is the requesting provider's own record — the Organization or
	// PractitionerRole the participant's system holds for the party this request
	// comes from. REQUIRED, and it rides the Bundle as a resolvable entry that
	// Claim.provider references.
	//
	// It is not decoration. Da Vinci PAS says a payer SHALL match an inquiry on
	// the member or subscriber id PLUS the ordering and/or rendering provider
	// identifier, so a request that identifies no provider is one no conformant
	// inquiry can ever find again — and a Reference carrying only display text
	// satisfies Claim.provider 1..1 at every line, so $validate certifies such a
	// request while it identifies nobody. Select it with SelectPASProvider, which
	// the inquiry builder's input comes from too, so the submission and the
	// inquiry about it can never name different parties.
	Provider []byte
	// Coverage is the member's own Coverage record — the one the participant's
	// system holds, read from it. REQUIRED, and it rides the Bundle as the
	// resolvable entry Claim.insurance[0].coverage references.
	//
	// It is not decoration either. Claim.insurance.coverage is min=1 mustSupport
	// at every PAS line and the insurer locates the policy from that Coverage's
	// details; a payer that stores what it is given matches a later inquiry
	// against the coverage it stored, so a submission and an inquiry that name
	// different Coverage records find each other only by accident. Pass the
	// record, not a reference: a Coverage this package minted is one no inquiry
	// built from your own system can name again, and a minted id shared by every
	// member is one member's coverage overwriting another's.
	//
	// Either the Coverage resource or the search result holding it (the usual
	// OpenCoverage shape) is accepted. Its own id and identifiers travel
	// unchanged; its beneficiary and payor — references into your own server,
	// which resolve to nothing at the payer — are pointed at this request's own
	// Patient and payer Organization entries.
	// Insurer is the participant's own Organization record for the payer — the one
	// its member's Coverage names as payor, read from its own system. REQUIRED
	// whenever PayerOrgEntry is set (the lane that carries the payer as a resolvable
	// entry); ignored on the lanes that contain it.
	//
	// It is the third element of the same rule Provider and Coverage follow. The
	// reference payer scopes its inquiry search by the insurer and resolves an
	// Organization to one it holds only through an NPI, which a payer organization
	// does not carry — so whatever this request names is what a later inquiry must
	// name. A request naming a payer Organization this package minted, while the
	// inquiry named the one the participant's records hold, matched nothing.
	Insurer          []byte
	Coverage         []byte
	PatientRef       string
	CoverageRef      string
	Corr             string
	Created          time.Time
	ContainedInsurer bool // reference-payer lane only; false = byte-identical SHN-native path
	AbsoluteRefs     bool // reference-payer lane only; false = byte-identical SHN-native path
	// PayerOrgEntry (reference-payer lane): emit the cms-payer Organization as a resolvable bundle
	// ENTRY (not contained) and repoint Coverage.payor + Claim.insurer at it. REQUIRED for a
	// real Da Vinci PAS payer (br-payer): its PAS payor resolution (PayorIdentifierUtil →
	// ResourceResolver.findInBundle) reads bundle ENTRIES only, so a contained #cms-payer
	// yields 0 payor identifiers → empty PlanDefinition search → A3 "Not Certified" (br-payer's
	// no-match fallback) for every code (the verdict CQL never fires). CRD is unaffected (it
	// resolves contained fragments).
	// Takes precedence over ContainedInsurer when both set. Default false = SHN-native byte-identical.
	PayerOrgEntry bool
	// InfoChanged (single-shot resolve discriminator): when true the submit Claim's item[*]
	// carries the Da Vinci PAS infoChanged item extension ({"url": pasInfoChangedExtensionURL,
	// "valueCode": "changed"} — the SAME shape the UPDATE builder appends unconditionally via
	// appendInfoChangedToClaimItems). It is the gateway payer-side POLL DISCRIMINATOR, not a verdict input:
	// the payer gate polls the timer-resolved terminal A1 (GET ClaimResponse/{id}) when the order is
	// a single-shot ServiceRequest signalling "resolve to terminal" via this extension, instead of
	// returning the A4 pend for a reference-payer amendment leg. On a FRESH submit (no Claim.related[prior],
	// which this builder never emits) infoChanged is benign on br-payer — its re-evaluation path is
	// gated on a prior claim, absent here — so br-payer still does A4→timer→A1. Default false →
	// byte-identical to every existing caller. NO prior-claim ref is added (this is a submit, not an
	// update).
	InfoChanged bool
	// Payer is the payer Organization identifier (system|value) emitted on the payer
	// Organization (contained or bundle-entry, depending on PayerOrgEntry/ContainedInsurer)
	// and on the Coverage built via BuildCoverageWithPayer. Pass the identity read from the
	// patient's Coverage, or shnsdk.CMSPayerIdentity for the conformance payer.
	Payer PayerIdentifier
	// MemberID is the bare member id this request is for — the value the Patient
	// carries as its member identifier, and the one a payer matches an inquiry on.
	// CoverageRef is Reference-shaped and stays for the caller's other roles (QR
	// context, the native/minimized lanes); this builder does not land it on the
	// wire — the Claim names the Coverage ENTRY above instead.
	MemberID string
	// MemberIDSystem is the namespace the participant's OWN system names this
	// member under — the system of the member identifier its Patient carries.
	// REQUIRED, and read from that record rather than assumed: it is the other
	// half of what a payer matches an inquiry on, and the Patient this request
	// carries is otherwise an id with nothing identifying the person behind it.
	MemberIDSystem string
}

// BuildConformantClaimBundle assembles a LEAN, generic, demo-persona-derived CONFORMANT
// Da Vinci $submit Claim Bundle — the only PA $submit contract (the minimized
// BuildClaimBundle has been removed). The entry set is exactly what the
// payer-side parseConformantPASSubjects (gateway/engine/pas_native.go) + the SHN-native
// adjudicator + `make validate` require, with NO br-payer foreign seed:
//
//	Claim (use preauthorization; item[].productOrService = the passed order's own code (CPT
//	      72148 for this doc's example demo persona) + extension-requestedService → the
//	      ServiceRequest; insurer = generic Organization/payer),
//	Patient (minimal, id = the bound member),
//	Coverage (contained cms-payer Org, payor → #cms-payer, beneficiary → member),
//	ServiceRequest (the passed SR — CPT 72148, ICD-10 M51.16, for this doc's example),
//	QuestionnaireResponse (the passed answered QR — id convergence-qr).
//
// meta.profile: the PAS $submit bundle + EVERY entry carry NO meta.profile (a Da
// Vinci profile is an ERROR-severity $validate fail against the US-Core-only validator;
// even the US Core meta.profile on the Coverage/SR is stripped in the PAS context). This
// DIFFERS from the CRD builder, which KEEPS US Core meta.profile. The Claim's insurer stays
// the generic Organization/payer (NOT a named br-payer insurer). Deterministic (no
// time.Now/random); the QR/SR/refs are demo-persona-derived by the caller.
//
// BuildConformantClaimBundle speaks PAS line 2.0 — it is BuildConformantClaimBundleAtLine("2.0", in),
// byte-identical (regression-fenced by test/sdkparity). Use BuildConformantClaimBundleAtLine to
// target 2.1/2.2 (PAS package differential: Claim.item certificationType/requestType/location[x]).
func BuildConformantClaimBundle(in ConformantClaimInputs) ([]byte, error) {
	def, _ := PASLineDef("2.0") // always present — pinned by manifest + parity-tested
	return buildConformantClaimBundle(def, in)
}

// BuildConformantClaimBundleAtLine is BuildConformantClaimBundle parameterized by PAS
// line ("2.0"|"2.1"|"2.2"). Unknown line errors (fail-closed — never a silent 2.0
// fallback). ONE code path: the line-conditional sections are driven entirely by the
// resolved PASDef (see addPASLineItemDetail), not inline per-line branches.
func BuildConformantClaimBundleAtLine(line string, in ConformantClaimInputs) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildConformantClaimBundleAtLine: unknown PAS line %q", line)
	}
	return buildConformantClaimBundle(def, in)
}

func buildConformantClaimBundle(def PASDef, in ConformantClaimInputs) ([]byte, error) {
	// --- Claim: reuse the byte-parity-locked buildPASClaim, then post-process to carry
	// the order's own productOrService code (buildPASClaim natively emits X12 1365
	// "Medical Care" there; setClaimItemProductFromSR below overrides it unconditionally) +
	// the extension-requestedService → ServiceRequest. The id is overridden to the stable
	// conformant id. ---
	// The party this request comes from, checked BEFORE anything is assembled: a
	// request that names nobody is refused here rather than sent to a payer that
	// would store it unfindable.
	providerRef, providerURL, err := pasProviderEntry(in.Provider)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	claimJSON, err := buildPASClaim(in.PatientRef, in.CoverageRef, providerRef, in.Corr, in.Created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: build claim: %w", err)
	}
	// orderID/srRef are type-aware: a DeviceRequest gets "convergence-dr"/"DeviceRequest/convergence-dr";
	// a ServiceRequest (baseline) gets "convergence-sr"/"ServiceRequest/convergence-sr" — byte-identical
	// to the existing locked path.
	orderID, srRef := orderEntryRef(in.SR)
	claimJSON, err = conformantizePASClaim(claimJSON, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: conformantize claim: %w", err)
	}
	// PAS 2.1+ (def-driven, PAS package differential): append the item-detail extensions +
	// location[x]. No-op at line 2.0 (def.ClaimItemLineDetailRequired false).
	claimJSON, err = addPASLineItemDetail(claimJSON, def)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: add line item detail: %w", err)
	}
	// Every caller: set item[0].productOrService from the order's actual code (the order
	// governs correctness, not just the reference-payer lane's HCPCS keying — every payer
	// on this network decides on productOrService, so a caller with no PayerOrgEntry must
	// still get the real requested service, not the X12 1365 placeholder buildPASClaim
	// left there). Fails loud (not a silent placeholder) when the order carries no code.
	claimJSON, err = setClaimItemProductFromSR(claimJSON, in.SR)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: set claim product from SR: %w", err)
	}
	// Reference-payer lane: make the Claim insurer ref resolvable. PayerOrgEntry is the
	// br-payer-correct form — point insurer at the cms-payer Organization ENTRY added below
	// (br-payer's PAS payor resolution reads bundle entries, not contained), and it takes
	// precedence over the legacy ContainedInsurer (contained #cms-payer) approach. Both default
	// false → the SHN-native path stays byte-identical.
	// The payer this request names, on the lane that carries it as a resolvable
	// entry: the participant's OWN Organization record for the payer its member's
	// Coverage names. A caller that cannot supply one is refused — see
	// pasPayerOrgEntry for what naming a minted one cost.
	var payerOrg pasPayerOrgRecord
	if in.PayerOrgEntry {
		if payerOrg, err = pasPayerOrgEntry(in.Insurer, in.Payer); err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
		}
	}
	switch {
	case in.PayerOrgEntry:
		claimJSON, err = repointInsurerToEntry(claimJSON, payerOrg.id)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: repoint insurer to entry: %w", err)
		}
	case in.ContainedInsurer:
		claimJSON, err = containInsurer(claimJSON, in.Payer)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: contain insurer: %w", err)
		}
	}
	// Single-shot resolve discriminator (poll discriminator, NOT a verdict input): append the PAS
	// infoChanged item extension when requested. NO prior-claim ref is added (fresh submit) — so it
	// stays benign on br-payer (whose re-evaluation is gated on a prior claim) while still flipping
	// the SHN payer gate into the timer-poll lane. Default false → byte-identical.
	if in.InfoChanged {
		claimJSON, err = appendInfoChangedToClaimItems(claimJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: append infoChanged: %w", err)
		}
	}
	// One item trace number per item (PAS Claim.item.extension:itemTraceNumber,
	// allowed at 2.0.1, 2.1.0 and 2.2.1): the payer echoes it on the matching
	// ClaimResponse item, so a later answer or inquiry can be matched by line.
	claimJSON, err = stampItemTraceNumbers(claimJSON, in.Corr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}

	// --- Coverage: the member's OWN Coverage record, read from the participant's
	// system — its own id and its own identifiers — prepared to ride this request
	// (see pasCoverageEntry). A caller with no such record is refused here; this
	// builder does not mint a coverage, because a minted one is a policy the payer
	// cannot locate and a record no later inquiry can name again. ---
	if in.MemberID == "" {
		return nil, fmt.Errorf("shnsdk: MemberID is required (bare member id; see the v0.42.0 identifier-semantics release note)")
	}
	coverage, err := pasCoverageEntry(in.Coverage, in.PatientRef, in.Payer, payerOrg)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	coverageJSON, coverageRef := coverage.raw, "Coverage/"+coverage.id
	// The Claim names that Coverage entry, by a literal reference — the form every
	// published PAS example uses on a submission, an update and an inquiry alike, and
	// the only form the receiving payer can store and then match an inquiry against.
	claimJSON, err = setInsuranceCoverageEntryRef(claimJSON, coverageRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}

	// --- Order resource (ServiceRequest or DeviceRequest): stamp the type-aware conformant id
	// (so the Claim's requestedService + the QR's qr-context resolve to it) + strip meta.profile.
	// orderID is "convergence-sr" for a ServiceRequest (byte-identical to the locked path) and
	// "convergence-dr" for a DeviceRequest. ---
	srJSON, err := withResourceID(in.SR, orderID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: id order: %w", err)
	}
	srJSON, err = stripMetaProfile(srJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: strip order meta: %w", err)
	}

	// --- Patient: the member, named by the identifier the participant's own
	// system names them under, and no foreign demographics. The identifier is
	// load-bearing: a payer matches a later inquiry on the member id PLUS the
	// provider identifier, so an id-only Patient is stored under a member the
	// payer cannot key on (see pasMemberPatient). ---
	patientJSON, err := pasMemberPatient(in.PatientRef, in.MemberIDSystem, in.MemberID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: build patient: %w", err)
	}

	// --- QuestionnaireResponse: the passed answered QR — stamp the conformant id (the raw
	// FillQuestionnaire QR carries none) AND rewrite its qr-context refs to the bundle-local
	// Coverage/SR ids the builder just stamped. The builder OWNS these refs (mirroring how it
	// owns the SR/Coverage ids): a caller's QRContext CoverageRef/OrderRef need NOT match —
	// otherwise a mismatched QR would emit dangling qr-context refs parseConformantPASSubjects
	// does not catch (it binds QR.subject, never qr-context), surfacing only later at validate /
	// a real br-payer.
	//
	// The answered QR is OPTIONAL here. A PA whose payer advertises NO DTR questionnaire
	// (genuine no-documentation) has no answered QR; a Da Vinci PAS Claim is valid WITHOUT a
	// QuestionnaireResponse (the payer-side parse treats the QR as optional, R-5): omit the
	// QR entry entirely. The Claim never references the QR (no supportingInfo → QR), so nothing
	// dangles. (NB br-payer's L8000 is PA-required and DOES advertise a manual-entry
	// questionnaire — filled via attestation, not auto-population — so that path carries an
	// answered QR and takes the with-QR branch below.) The with-QR path is byte-unchanged. ---
	var qrJSON []byte
	if len(in.QR) > 0 {
		qrJSON, err = withResourceID(in.QR, conformantPASQRID)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: id qr: %w", err)
		}
		qrJSON, err = rewriteQRContextRefs(qrJSON, coverageRef, srRef)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: rewrite qr-context: %w", err)
		}
	}

	// Derive resolvable absolute fullUrls (FHIR bdl-7 / AI-11), mirror BuildClaimBundle.
	entryFor := func(resourceJSON []byte) (fhir.BundleEntry, error) {
		u, err := pasFullURLFor(resourceJSON)
		if err != nil {
			return fhir.BundleEntry{}, err
		}
		return fhir.BundleEntry{FullUrl: strPtr(u), Resource: json.RawMessage(resourceJSON)}, nil
	}
	// Reference-payer lane: the cms-payer Organization is a first-class bundle ENTRY (the
	// payor refs above resolve to it). Build it here so entryFor stamps its absolute fullUrl,
	// which absolutizeBundleRefs (when AbsoluteRefs) makes Coverage.payor/Claim.insurer match.
	var payerOrgJSON []byte
	if in.PayerOrgEntry {
		payerOrgJSON = payerOrg.raw
	}
	// The requesting provider rides as its own entry, so Claim.provider resolves
	// inside the Bundle the payer adjudicates.
	resources := [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, in.Provider}
	if payerOrgJSON != nil {
		if providerURL == pasBundleBaseURL+"/Organization/"+payerOrg.id {
			return nil, fmt.Errorf("shnsdk: conformant submit: the requesting provider and the payer organization are the same bundle entry (%s)", providerRef)
		}
		resources = append(resources, payerOrgJSON)
	}
	if qrJSON != nil {
		resources = append(resources, qrJSON)
	}
	entries := make([]fhir.BundleEntry, 0, len(resources))
	for _, rj := range resources {
		e, err := entryFor(rj)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}

	bundle := fhir.Bundle{
		Type:       fhir.BundleTypeCollection,
		Identifier: &fhir.Identifier{System: strPtr(pasBundleIdentifierSystem), Value: strPtr(in.Corr)},
		Timestamp:  strPtr(in.Created.UTC().Format(time.RFC3339)),
		Entry:      entries,
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: marshal bundle: %w", err)
	}
	bundleOut, err := pasInjectResourceType(raw, "Bundle")
	if err != nil {
		return nil, err
	}
	// Reference-payer lane only: rewrite internal refs to their absolute fullUrl form so real
	// payers that do not resolve relative refs against absolute entry fullUrls accept the
	// bundle (HAPI-1094). Default false keeps the SHN-native path byte-identical.
	if in.AbsoluteRefs {
		bundleOut, err = absolutizeBundleRefs(bundleOut)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: absolutize refs: %w", err)
		}
	}
	if err := checkPASProviderResolves(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	if err := checkPASMemberIdentified(bundleOut, in.MemberID); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	if err := checkPASCoverageResolves(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	// dom-3 over the coverage and the claim: each re-point above moves the one
	// reference that named a contained record, and a record left inside asserting
	// nothing is refused here. See checkPASContainedReferenced for its scope.
	if err := checkPASContainedReferenced(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	if in.PayerOrgEntry || in.ContainedInsurer {
		if err := checkPASInsurerResolves(bundleOut); err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
		}
	}
	return bundleOut, nil
}

// conformantizePASClaim takes buildPASClaim's output and (1) adds the Da Vinci PAS
// extension-requestedService → the ServiceRequest on item[0], and (2) restamps the id to
// the stable conformant id. It does NOT touch item[0].productOrService — buildPASClaim
// natively puts X12 1365 "Medical Care" there (see buildPASClaim's comment), and this
// function leaves that as-is. Every caller of this function derives the real
// productOrService from the order and stamps it via setClaimItemProductFromSR
// immediately afterward, unconditionally — there is no placeholder service code that
// survives to a payer; an order with no code fails loud there instead. The Claim's
// category (X12 1365), insurer (generic Organization/payer), and all other fields stay
// buildPASClaim's. Deterministic.
func conformantizePASClaim(claimJSON []byte, serviceRequestRef string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("parse claim: %w", err)
	}
	// Restamp id.
	idJSON, _ := json.Marshal(conformantPASClaimID)
	m["id"] = idJSON

	// Add extension-requestedService on item[0]. Guard the missing-item case BEFORE
	// unmarshal so a nil m["item"] yields a self-explanatory error rather than the
	// opaque "unexpected end of JSON input" EOF (unreachable in practice — buildPASClaim
	// always emits an item — but a public-SDK robustness nicety).
	if len(m["item"]) == 0 {
		return nil, fmt.Errorf("claim has no item to conformantize")
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(m["item"], &items); err != nil {
		return nil, fmt.Errorf("parse claim.item: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("claim has no item to conformantize")
	}
	reqExt := []map[string]any{{
		"url":            extReqService,
		"valueReference": map[string]string{"reference": serviceRequestRef},
	}}
	reqExtJSON, err := json.Marshal(reqExt)
	if err != nil {
		return nil, err
	}
	items[0]["extension"] = reqExtJSON
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	m["item"] = itemsJSON
	return json.Marshal(m)
}

// stripMetaProfile removes meta.profile from a FHIR resource JSON (deleting an
// empty meta object entirely), leaving every other field verbatim. The PAS $submit
// bundle declares NO meta.profile on any SHN-produced entry (a Da Vinci profile
// is an ERROR-severity $validate fail, and the US-Core-only validator can't resolve PAS
// profiles; even US Core profiles are dropped in the PAS context for a uniform "no
// profile declared" $submit). Deterministic.
func stripMetaProfile(resourceJSON []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(resourceJSON, &m); err != nil {
		return nil, fmt.Errorf("parse resource: %w", err)
	}
	metaRaw, ok := m["meta"]
	if !ok {
		return resourceJSON, nil // no meta at all
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	delete(meta, "profile")
	if len(meta) == 0 {
		delete(m, "meta")
	} else {
		mj, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		m["meta"] = mj
	}
	return json.Marshal(m)
}

// rewriteQRContextRefs rewrites the QuestionnaireResponse's top-level qr-context
// and DTR 2.2 qr-coverage valueReferences so Coverage points at coverageRef and
// the ServiceRequest- or DeviceRequest-typed qr-context points at srRef — matching each qr-context
// extension by the resourceType PREFIX of its existing valueReference.reference (a ref
// starting "Coverage/" → coverageRef; one starting "ServiceRequest/" → srRef). This makes
// BuildConformantClaimBundle SELF-CONSISTENT: the QR's qr-context refs resolve to the
// bundle-local Coverage/SR regardless of what the caller put in the QR (closing the
// dangling-ref hazard parseConformantPASSubjects does not catch). Other extensions
// (e.g. intendedUse) and all other fields are left verbatim. Deterministic.
func rewriteQRContextRefs(qrJSON []byte, coverageRef, srRef string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(qrJSON, &m); err != nil {
		return nil, fmt.Errorf("parse QR: %w", err)
	}
	extRaw, ok := m["extension"]
	if !ok {
		return qrJSON, nil // no extensions — nothing to rewrite
	}
	var exts []map[string]json.RawMessage
	if err := json.Unmarshal(extRaw, &exts); err != nil {
		return nil, fmt.Errorf("parse QR.extension: %w", err)
	}
	for _, ext := range exts {
		var url string
		if err := json.Unmarshal(ext["url"], &url); err != nil {
			continue // non-string url — leave it alone
		}
		if url != extQRContext && url != qrCoverageExt {
			continue
		}
		vrRaw, ok := ext["valueReference"]
		if !ok {
			continue
		}
		var vr map[string]json.RawMessage
		if err := json.Unmarshal(vrRaw, &vr); err != nil || vr == nil {
			continue
		}
		var reference string
		if err := json.Unmarshal(vr["reference"], &reference); err != nil {
			continue
		}
		var want string
		switch {
		case strings.HasPrefix(reference, "Coverage/"):
			want = coverageRef
		case strings.HasPrefix(reference, "ServiceRequest/"), strings.HasPrefix(reference, "DeviceRequest/"):
			want = srRef
		default:
			continue // a qr-context ref we don't own — leave it verbatim
		}
		vr["reference"], _ = json.Marshal(want)
		vrJSON, err := json.Marshal(vr)
		if err != nil {
			return nil, err
		}
		ext["valueReference"] = vrJSON
	}
	extJSON, err := json.Marshal(exts)
	if err != nil {
		return nil, err
	}
	m["extension"] = extJSON
	return json.Marshal(m)
}

// rewriteProvenanceTarget replaces the Provenance.target with a single reference to wantTarget
// (the bundle-local supplemental resource: DiagnosticReport/<id> for the DR variant, else
// QuestionnaireResponse/<id>). The conformant update builder restamps the supplemental resource's
// id to a stable bundle-local id, so a caller's Provenance — built against the PRE-restamp id —
// must be re-pointed here or it dangles (the FR-32 inbound gate resolves the target by id). Agent,
// policy, recorded and every other field are left verbatim (BuildProvenance emits a single-target
// Provenance, so replacing target is faithful). Deterministic.
func rewriteProvenanceTarget(provJSON []byte, wantTarget string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(provJSON, &m); err != nil {
		return nil, fmt.Errorf("parse provenance: %w", err)
	}
	targetJSON, err := json.Marshal([]map[string]string{{"reference": wantTarget}})
	if err != nil {
		return nil, err
	}
	m["target"] = targetJSON
	return json.Marshal(m)
}

// containInsurer rewrites a conformant Claim JSON so the Claim's insurer references a
// CONTAINED #cms-payer Organization — making the reference resolvable by real payers that
// validate bundle-internal refs (e.g. real br-payer 400s "Organization/payer not found").
// Mirrors BuildCoverageWithPayer's contained-org splice: the identifier system|value come
// from payer (the cosmetic id/name stay conformantPayerOrgID/conformantPayerOrgName),
// ensuring the Claim's contained payer and the Coverage's contained payer are consistent.
//
// If the Claim already has a "contained" array (not typical) the new org is appended.
// Every other field is left verbatim. Deterministic.
func containInsurer(claimJSON []byte, payer PayerIdentifier) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("containInsurer: parse claim: %w", err)
	}

	// Build the contained Organization (identical shape to BuildCoverageWithPayer's org).
	org := fhir.Organization{
		Id:   strPtr(conformantPayerOrgID),
		Name: strPtr(conformantPayerOrgName),
		Identifier: []fhir.Identifier{{
			System: strPtr(payer.System),
			Value:  strPtr(payer.Value),
		}},
	}
	orgJSON, err := json.Marshal(org)
	if err != nil {
		return nil, fmt.Errorf("containInsurer: marshal org: %w", err)
	}
	// Inject resourceType (fhir.Organization has none in output).
	var orgMap map[string]json.RawMessage
	if err := json.Unmarshal(orgJSON, &orgMap); err != nil {
		return nil, fmt.Errorf("containInsurer: parse org: %w", err)
	}
	rtJSON, _ := json.Marshal("Organization")
	orgMap["resourceType"] = rtJSON
	orgJSON, err = json.Marshal(orgMap)
	if err != nil {
		return nil, fmt.Errorf("containInsurer: re-marshal org: %w", err)
	}

	// Append (or create) the contained array.
	var contained []json.RawMessage
	if raw, ok := m["contained"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &contained); err != nil {
			return nil, fmt.Errorf("containInsurer: parse existing contained: %w", err)
		}
	}
	contained = append(contained, json.RawMessage(orgJSON))
	containedJSON, err := json.Marshal(contained)
	if err != nil {
		return nil, fmt.Errorf("containInsurer: marshal contained: %w", err)
	}
	m["contained"] = containedJSON

	// Rewrite insurer to reference the contained org.
	insurerJSON, err := json.Marshal(map[string]string{"reference": "#" + conformantPayerOrgID})
	if err != nil {
		return nil, fmt.Errorf("containInsurer: marshal insurer: %w", err)
	}
	m["insurer"] = insurerJSON

	return json.Marshal(m)
}

// orderEntryRef picks the conformant bundle-local id and typed reference for the order
// resource, selecting on its resourceType. A DeviceRequest (DME/home-oxygen) uses
// conformantPASDeviceRequestID ("convergence-dr"); any other type (ServiceRequest, the
// baseline) uses conformantPASServiceRequestID ("convergence-sr") — keeping the SR output
// byte-identical to the existing byte-parity-locked path.
func orderEntryRef(order []byte) (id, ref string) {
	var p struct {
		ResourceType string `json:"resourceType"`
	}
	_ = json.Unmarshal(order, &p)
	if p.ResourceType == "DeviceRequest" {
		return conformantPASDeviceRequestID, "DeviceRequest/" + conformantPASDeviceRequestID
	}
	return conformantPASServiceRequestID, "ServiceRequest/" + conformantPASServiceRequestID
}

// setClaimItemProductFromSR sets the Claim's item[0].productOrService to the order resource's
// requested-service code. buildPASClaim leaves X12 1365 "Medical Care" on the Claim item (its
// own default line code), but br-payer's PAS keys the PlanDefinition lookup on
// Claim.item.productOrService (PasSubmitService.evaluateAllItems — NOT the SR / requestedService
// extension). Every payer on this network decides on productOrService, so the Claim item MUST
// carry the order's real code or the response is a determination about a different service, with
// no error anywhere to notice it.
//
// Order-type-aware: for a DeviceRequest the code lives in codeCodeableConcept; for a
// ServiceRequest (and any unrecognised type) it lives in code. The extension-requestedService
// (added by conformantizePASClaim) is preserved. Called UNCONDITIONALLY by both
// buildConformantClaimBundle and buildConformantClaimUpdateBundle — not gated on PayerOrgEntry.
// PayerOrgEntry gates only the payer-Organization bundle-entry shape (contained vs. resolvable
// entry); it must never again gate the correctness of the requested service. Fails loud
// (rather than silently carrying forward the placeholder) when the order has no code.
func setClaimItemProductFromSR(claimJSON, orderJSON []byte) ([]byte, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	_ = json.Unmarshal(orderJSON, &probe)

	var cc json.RawMessage
	switch probe.ResourceType {
	case "DeviceRequest":
		var dr struct {
			Code json.RawMessage `json:"codeCodeableConcept"`
		}
		if err := json.Unmarshal(orderJSON, &dr); err != nil {
			return nil, fmt.Errorf("setClaimItemProductFromSR: parse DeviceRequest: %w", err)
		}
		if len(dr.Code) == 0 {
			return nil, fmt.Errorf("setClaimItemProductFromSR: DeviceRequest has no codeCodeableConcept")
		}
		cc = dr.Code
	default: // ServiceRequest (and any other type)
		var sr struct {
			Code json.RawMessage `json:"code"`
		}
		if err := json.Unmarshal(orderJSON, &sr); err != nil {
			return nil, fmt.Errorf("setClaimItemProductFromSR: parse SR: %w", err)
		}
		if len(sr.Code) == 0 {
			return nil, fmt.Errorf("setClaimItemProductFromSR: SR has no code")
		}
		cc = sr.Code
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("setClaimItemProductFromSR: parse claim: %w", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(m["item"], &items); err != nil {
		return nil, fmt.Errorf("setClaimItemProductFromSR: parse claim.item: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("setClaimItemProductFromSR: claim has no item")
	}
	items[0]["productOrService"] = cc
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("setClaimItemProductFromSR: marshal items: %w", err)
	}
	m["item"] = itemsJSON
	return json.Marshal(m)
}

// repointPayorToEntry rewrites a reference-payer-lane Coverage so Coverage.payor[0] references the
// payer Organization ENTRY the request carries — the participant's own record — instead of the
// payer organization the record contained, and drops that now-redundant contained copy (see
// dropContainedPayerOrg). absolutizeBundleRefs then makes the ref the entry's absolute fullUrl
// so a real payer's PAS findInBundle resolves it.
func repointPayorToEntry(coverageJSON []byte, payerOrgID string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(coverageJSON, &m); err != nil {
		return nil, fmt.Errorf("repointPayorToEntry: parse coverage: %w", err)
	}
	payorJSON, err := json.Marshal([]map[string]string{{"reference": "Organization/" + payerOrgID}})
	if err != nil {
		return nil, fmt.Errorf("repointPayorToEntry: marshal payor: %w", err)
	}
	m["payor"] = payorJSON
	if err := dropContainedPayerOrg(m, payerOrgID); err != nil {
		return nil, fmt.Errorf("repointPayorToEntry: %w", err)
	}
	return json.Marshal(m)
}

// repointInsurerToEntry rewrites a reference-payer-lane Claim so Claim.insurer references the payer
// Organization ENTRY the request carries — the participant's own record — and drops the contained
// payer organization it no longer names (see dropContainedPayerOrg).
func repointInsurerToEntry(claimJSON []byte, payerOrgID string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("repointInsurerToEntry: parse claim: %w", err)
	}
	insurerJSON, err := json.Marshal(map[string]string{"reference": "Organization/" + payerOrgID})
	if err != nil {
		return nil, fmt.Errorf("repointInsurerToEntry: marshal insurer: %w", err)
	}
	m["insurer"] = insurerJSON
	if err := dropContainedPayerOrg(m, payerOrgID); err != nil {
		return nil, fmt.Errorf("repointInsurerToEntry: %w", err)
	}
	return json.Marshal(m)
}

// dropContainedPayerOrg removes the contained payer Organization the repointed
// payor/insurer reference no longer names, deleting the "contained" array if it
// becomes empty. No-op when there is no such contained resource.
//
// entryOrgID is the id of the payer Organization ENTRY the request now carries.
// It is the id to drop, not just the minted conformantPayerOrgID, because the
// record this request carries is the PARTICIPANT'S OWN Coverage, and a
// participant's Coverage may carry its payer organization INSIDE itself — the
// shape the demo roster, the bridging personas and the Cambia member are all
// seeded in, and the shape the gateway's one payer-organization reader lifts the
// entry out of (memberPayerOrganization). So the entry and the contained copy are
// the SAME record under the SAME id: once payor/insurer names the entry, the
// contained copy is referenced by nothing, which is FHIR dom-3 — a contained
// resource SHALL be referred to from elsewhere in the resource that contains it.
// A pinned IG-profile $validate rejects that at egress, which is how it surfaced:
// only the members whose contained org id was NOT conformantPayerOrgID were hit,
// so every cms-payer persona passed and the bridging one did not.
//
// Nothing is lost by the drop: the very same Organization rides the request as
// the entry both references now name. conformantPayerOrgID stays in the set for
// the minted org the non-entry lanes splice in under that fixed id.
func dropContainedPayerOrg(m map[string]json.RawMessage, entryOrgID string) error {
	raw, ok := m["contained"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var contained []json.RawMessage
	if err := json.Unmarshal(raw, &contained); err != nil {
		return fmt.Errorf("parse contained: %w", err)
	}
	// What the resource still points at locally AFTER the re-point, read without
	// descending into the records it contains. A candidate that is still named
	// here is not the displaced copy and stays: dropping it would leave the
	// reference that names it pointing at nothing.
	stillNamed := map[string]bool{}
	outer := map[string]json.RawMessage{}
	for k, v := range m {
		if k != "contained" {
			outer[k] = v
		}
	}
	if outerJSON, err := json.Marshal(outer); err == nil {
		for _, ref := range localReferences(outerJSON, true) {
			stillNamed[ref] = true
		}
	}
	kept := make([]json.RawMessage, 0, len(contained))
	for _, c := range contained {
		var probe struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if err := json.Unmarshal(c, &probe); err == nil && probe.ResourceType == "Organization" &&
			!stillNamed[probe.ID] &&
			(probe.ID == conformantPayerOrgID || (entryOrgID != "" && probe.ID == entryOrgID)) {
			continue // drop the payer org — it lives as a bundle entry now
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		delete(m, "contained")
		return nil
	}
	keptJSON, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("marshal contained: %w", err)
	}
	m["contained"] = keptJSON
	return nil
}

// absolutizeBundleRefs rewrites every internal reference in a conformant PAS Bundle
// whose value matches a bundle-entry relative form ("<resourceType>/<id>") to its
// absolute fullUrl (pasBundleBaseURL + "/" + value). This makes the bundle
// self-consistent for real payers (e.g. real br-payer HAPI-1094) that do not resolve
// relative refs against absolute entry fullUrls in a $submit collection bundle.
//
// Rules:
//   - Only refs that match a bundle entry relative id are rewritten; out-of-bundle refs
//     (e.g. "Organization/payer", "Practitioner/<npi>") are left untouched.
//   - Contained #fragment refs are never rewritten (they start with "#").
//   - All "reference" string fields anywhere in the JSON tree (nested objects + arrays)
//     are visited recursively.
//
// Pure function — input is not mutated. Re-marshal determinism is fine (reference-payer bundles
// have no byte-parity golden). Called ONLY when AbsoluteRefs == true.
func absolutizeBundleRefs(bundleJSON []byte) ([]byte, error) {
	// Unmarshal the bundle into a generic map so we can walk the full tree.
	var root interface{}
	if err := json.Unmarshal(bundleJSON, &root); err != nil {
		return nil, fmt.Errorf("absolutizeBundleRefs: unmarshal: %w", err)
	}
	rootMap, ok := root.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("absolutizeBundleRefs: bundle is not a JSON object")
	}

	// Build the set of bundle-entry relative ids: "<resourceType>/<id>" for each entry.
	entrySet := make(map[string]struct{})
	entriesRaw, _ := rootMap["entry"].([]interface{})
	for _, e := range entriesRaw {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		res, ok := em["resource"].(map[string]interface{})
		if !ok {
			continue
		}
		rt, _ := res["resourceType"].(string)
		id, _ := res["id"].(string)
		if rt != "" && id != "" {
			entrySet[rt+"/"+id] = struct{}{}
		}
	}

	// Walk the entire tree, rewriting matching "reference" values. `protect` carries
	// down a "leave references in this subtree RELATIVE" flag — set for the patient-compartment
	// anchor of each clinical resource the payer's CRD Rule CQL retrieves (see below).
	//
	// EXCLUDE the patient-compartment anchor references from absolutization. A real Da Vinci
	// PAS payer (br-payer) computes the verdict by running the CRD Rule CQL (`context Patient`)
	// over the $submit bundle via cqf-fhir — e.g. PriorAuthRequiredRule's `First([Coverage])`
	// and HomeHealthAssessmentRule's `First([Coverage])` + `First([ServiceRequest])`. Those
	// in-memory patient-compartment retrieves match each resource on its Patient-anchor search
	// param (Coverage→beneficiary, ServiceRequest/DeviceRequest→subject). An ABSOLUTE anchor
	// ref breaks the compartment match → the retrieve is empty → no coverage-info extension →
	// PasCoverageEvaluator falls through to A3 "Not Certified" (live-proven vs br-payer a8bece4:
	// absolute beneficiary → every code A3; absolute SR.subject → G0151 A3 instead of A4).
	// Everything else MUST stay absolute: br-payer resolves relative refs against its own SERVER
	// base, not the bundle fullUrls, so e.g. Claim.insurer→Organization/cms-payer and
	// Claim.patient 404 (HAPI-1094) unless absolute. Claim.patient is NOT a CQL-retrieved
	// resource (it is the $submit envelope; cqf takes the subject id from it regardless of
	// abs/rel), so it stays absolute. Do NOT "tidy" these anchors back to absolute — it
	// re-introduces the uniform A3.
	var walk func(v interface{}, protect bool) interface{}
	walk = func(v interface{}, protect bool) interface{} {
		switch val := v.(type) {
		case map[string]interface{}:
			// The Patient-anchor field for each clinical resource the Rule CQL retrieves in
			// `context Patient`. Its reference must stay relative for the compartment match.
			var anchorField string
			switch val["resourceType"] {
			case "Coverage":
				anchorField = "beneficiary"
			case "ServiceRequest", "DeviceRequest":
				anchorField = "subject"
			}
			for k, child := range val {
				if k == "reference" {
					if s, ok := child.(string); ok && s != "" && !strings.HasPrefix(s, "#") && !protect {
						if _, inSet := entrySet[s]; inSet {
							val[k] = pasBundleBaseURL + "/" + s
						}
					}
				} else {
					val[k] = walk(child, protect || (anchorField != "" && k == anchorField))
				}
			}
			return val
		case []interface{}:
			for i, elem := range val {
				val[i] = walk(elem, protect)
			}
			return val
		default:
			return v
		}
	}
	walk(rootMap, false)

	out, err := json.Marshal(rootMap)
	if err != nil {
		return nil, fmt.Errorf("absolutizeBundleRefs: re-marshal: %w", err)
	}
	return out, nil
}

// BuildProvenanceWithPolicy additionally cites the authorizing policy via
// Provenance.policy (a uri — the base-FHIR-correct slot for "patient consent",
// NOT Provenance.entity which marks derived-from inputs) and the PurposeOfUse via
// Provenance.reason. policyRef ("Consent/<id>") and purposeOfUse are omitted when
// empty. Used by an external facility to make the disclosure provably consent-anchored
// in a federated-query prior-auth flow. Promoted from internal/pas.BuildProvenanceWithPolicy;
// parity-tested in test/sdkparity/pas_provenance_parity_test.go.
func BuildProvenanceWithPolicy(targetRef, agentWho, policyRef, purposeOfUse string, recorded time.Time) ([]byte, error) {
	prov := fhir.Provenance{
		Target:   []fhir.Reference{{Reference: strPtr(targetRef)}},
		Recorded: recorded.UTC().Format(time.RFC3339),
		Agent:    []fhir.ProvenanceAgent{{Who: fhir.Reference{Reference: strPtr(agentWho)}}},
	}
	if policyRef != "" {
		prov.Policy = []string{policyRef}
	}
	if purposeOfUse != "" {
		prov.Reason = []fhir.CodeableConcept{{
			Coding: []fhir.Coding{{
				System: strPtr("http://terminology.hl7.org/CodeSystem/v3-ActReason"),
				Code:   strPtr(purposeOfUse),
			}},
		}}
	}
	raw, err := json.Marshal(prov)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal Provenance: %w", err)
	}
	return pasInjectResourceType(raw, "Provenance")
}

// ProvenanceIdentifier names the source of supplemental evidence using an explicit
// identifier namespace and value; it is not a reference to a bundled resource.
type ProvenanceIdentifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

func (source ProvenanceIdentifier) validate() error {
	if (source.System != "http://smarthealth.network/ids/holder" && source.System != "http://hl7.org/fhir/sid/us-npi") || strings.TrimSpace(source.Value) == "" {
		return fmt.Errorf("ProvenanceAgent requires a holder or NPI identifier system and nonblank value")
	}
	return nil
}

// BuildProvenanceWithIdentifier attributes supplemental evidence to a logical
// source identifier, such as a registered holder id or an NPI. It does not invent
// an Organization resource or reinterpret a literal resource reference.
func BuildProvenanceWithIdentifier(targetRef string, source ProvenanceIdentifier, recorded time.Time) ([]byte, error) {
	if err := source.validate(); err != nil {
		return nil, err
	}
	prov := fhir.Provenance{
		Target:   []fhir.Reference{{Reference: strPtr(targetRef)}},
		Recorded: recorded.UTC().Format(time.RFC3339),
		Agent:    []fhir.ProvenanceAgent{{Who: fhir.Reference{Identifier: &fhir.Identifier{System: strPtr(source.System), Value: strPtr(source.Value)}}}},
	}
	raw, err := json.Marshal(prov)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal Provenance: %w", err)
	}
	return pasInjectResourceType(raw, "Provenance")
}

// BuildProvenance builds a Provenance attributing supplemental data to its source
// (FR-32) — the no-policy form used for local supplemental evidence (no consent URI).
// Reimplements internal/pas.BuildProvenance standalone; test/sdkparity asserts
// byte-identity. recorded is injected (deterministic).
func BuildProvenance(targetRef, agentWho string, recorded time.Time) ([]byte, error) {
	return BuildProvenanceWithPolicy(targetRef, agentWho, "", "", recorded)
}

// buildPASUpdateClaim constructs the FHIR Claim for an UPDATE bundle: identical to
// buildPASClaim but with related[] referencing the original claim by correlation
// identifier (FR-21). Ported byte-for-byte from internal/pas.buildClaim's related path.
// coverageRef carries the same caveat as buildPASClaim's: BuildConformantClaimUpdateBundle
// overwrites insurance[0].coverage with a logical reference before the bundle is assembled.
func buildPASUpdateClaim(patientRef, coverageRef, providerRef, correlationID, originalCorrelationID string, created time.Time) ([]byte, error) {
	claim := fhir.Claim{
		Id:     strPtr("claim-" + correlationID),
		Status: fhir.FinancialResourceStatusCodesActive,
		Type: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System: strPtr("http://terminology.hl7.org/CodeSystem/claim-type"),
				Code:   strPtr("professional"),
			}},
		},
		Use:      fhir.UsePreauthorization,
		Patient:  fhir.Reference{Reference: strPtr(patientRef)},
		Created:  created.UTC().Format(time.RFC3339),
		Provider: fhir.Reference{Reference: strPtr(providerRef)},
		Insurer:  &fhir.Reference{Reference: strPtr("Organization/payer")},
		Priority: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				Code: strPtr("normal"),
			}},
		},
		Insurance: []fhir.ClaimInsurance{{
			Sequence: 1,
			Focal:    true,
			Coverage: fhir.Reference{Reference: strPtr(coverageRef)},
		}},
		Item: []fhir.ClaimItem{{
			Sequence: 1,
			Category: &fhir.CodeableConcept{
				Coding: []fhir.Coding{{
					System:  strPtr("https://codesystem.x12.org/005010/1365"),
					Code:    strPtr("1"),
					Display: strPtr("Medical Care"),
				}},
			},
			ProductOrService: fhir.CodeableConcept{
				Coding: []fhir.Coding{{
					System:  strPtr("https://codesystem.x12.org/005010/1365"),
					Code:    strPtr("1"),
					Display: strPtr("Medical Care"),
				}},
			},
		}},
		Identifier: []fhir.Identifier{{
			System: strPtr("urn:shn:correlation"),
			Value:  strPtr(correlationID),
		}},
		Related: []fhir.ClaimRelated{{
			Claim: &fhir.Reference{
				Identifier: &fhir.Identifier{
					System: strPtr("urn:shn:correlation"),
					Value:  strPtr(originalCorrelationID),
				},
			},
		}},
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		return nil, err
	}
	return pasInjectResourceType(raw, "Claim")
}

// setPriorClaimReference repoints Claim.related[0].claim.reference at the prior Claim BUNDLE
// ENTRY (reference-payer lane only — the non-PayerOrgEntry lane never adds that entry, so calling
// this there would dangle; see the call site in buildConformantClaimUpdateBundle). br-payer's
// resolvePriorClaim (PasSubmitService.java:379-403) reads .reference, NOT .identifier, and 400s
// "The prior Claim referenced in Claim.related.claim must be included in the Bundle" without it.
// The existing .identifier is preserved. infoChanged is a SEPARATE, unconditional concern (every
// lane) — see appendInfoChangedToClaimItems; this function used to fold both together as
// setPriorClaimReferenceAndInfoChanged, which forced infoChanged to share the reference's
// PayerOrgEntry gate even though only the reference is lane-shape-dependent.
//
// Generic-map round-trip (the reference-payer lane has no byte-parity golden), mirroring containInsurer/
// repointInsurerToEntry.
func setPriorClaimReference(claimJSON []byte, priorClaimRef string) ([]byte, error) {
	var claim map[string]interface{}
	if err := json.Unmarshal(claimJSON, &claim); err != nil {
		return nil, fmt.Errorf("unmarshal claim: %w", err)
	}
	related, _ := claim["related"].([]interface{})
	if len(related) == 0 {
		return nil, fmt.Errorf("update claim has no related[]")
	}
	rel0, ok := related[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("related[0] is not an object")
	}
	relClaim, ok := rel0["claim"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("related[0].claim is not an object")
	}
	relClaim["reference"] = priorClaimRef

	out, err := json.Marshal(claim)
	if err != nil {
		return nil, fmt.Errorf("marshal claim: %w", err)
	}
	return out, nil
}

// appendInfoChangedToClaimItems appends the Da Vinci PAS infoChanged item extension
// ({"url": pasInfoChangedExtensionURL, "valueCode": "changed"}) to every Claim.item[*] of a
// marshalled Claim JSON. It is the SINGLE source of the exact infoChanged extension shape — the
// UPDATE builder (buildConformantClaimUpdateBundle, unconditionally, every lane) and the
// single-shot SUBMIT (BuildConformantClaimBundle InfoChanged) both emit the identical element
// through it, so the gateway's requestClaimHasInfoChanged poll discriminator fires the same way
// for both. Errors when the Claim has no item[]. Generic-map round-trip (mirrors the other
// reference-payer-lane post-processors).
func appendInfoChangedToClaimItems(claimJSON []byte) ([]byte, error) {
	var claim map[string]interface{}
	if err := json.Unmarshal(claimJSON, &claim); err != nil {
		return nil, fmt.Errorf("appendInfoChanged: unmarshal claim: %w", err)
	}
	if err := appendInfoChangedToClaimItemsMap(claim); err != nil {
		return nil, err
	}
	out, err := json.Marshal(claim)
	if err != nil {
		return nil, fmt.Errorf("appendInfoChanged: marshal claim: %w", err)
	}
	return out, nil
}

// PASItemTraceSystem is the identifier system of the item trace numbers the
// PAS submit and update builders write: "<correlation>.<item sequence>".
const PASItemTraceSystem = "urn:shn:pas:item-trace"

// pasExtItemTraceNumber is the PAS item trace number extension.
const pasExtItemTraceNumber = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-itemTraceNumber"

// stampItemTraceNumbers appends one itemTraceNumber extension to every Claim
// item: system PASItemTraceSystem, value "<corr>.<sequence>". An item without
// a positive sequence, or a Claim without items, is an error.
func stampItemTraceNumbers(claimJSON []byte, corr string) ([]byte, error) {
	if corr == "" {
		return nil, fmt.Errorf("item trace numbers need the correlation id")
	}
	var claim map[string]interface{}
	if err := json.Unmarshal(claimJSON, &claim); err != nil {
		return nil, fmt.Errorf("item trace numbers: unmarshal claim: %w", err)
	}
	items, _ := claim["item"].([]interface{})
	if len(items) == 0 {
		return nil, fmt.Errorf("item trace numbers: claim has no item[]")
	}
	for i, it := range items {
		im, ok := it.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("item trace numbers: item %d is not an object", i)
		}
		seq, ok := im["sequence"].(float64)
		if !ok || seq < 1 || seq != float64(int(seq)) {
			return nil, fmt.Errorf("item trace numbers: item %d has no positive sequence", i)
		}
		ext, _ := im["extension"].([]interface{})
		im["extension"] = append(ext, map[string]interface{}{
			"url": pasExtItemTraceNumber,
			"valueIdentifier": map[string]interface{}{
				"system": PASItemTraceSystem,
				"value":  fmt.Sprintf("%s.%d", corr, int(seq)),
			},
		})
	}
	out, err := json.Marshal(claim)
	if err != nil {
		return nil, fmt.Errorf("item trace numbers: marshal claim: %w", err)
	}
	return out, nil
}

// appendInfoChangedToClaimItemsMap mutates a decoded Claim map in place, appending the infoChanged
// item extension to every item. Factored out of appendInfoChangedToClaimItems (its only caller) so
// the extension shape has one definition even if a future generic-map caller needs it without a
// marshal round-trip (setPriorClaimReference used to be such a caller, before infoChanged became
// an unconditional, separate concern — see buildConformantClaimUpdateBundle).
func appendInfoChangedToClaimItemsMap(claim map[string]interface{}) error {
	items, _ := claim["item"].([]interface{})
	if len(items) == 0 {
		return fmt.Errorf("claim has no item[] to mark infoChanged")
	}
	for _, it := range items {
		im, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		ext, _ := im["extension"].([]interface{})
		im["extension"] = append(ext, map[string]interface{}{
			"url":       pasInfoChangedExtensionURL,
			"valueCode": "changed",
		})
	}
	return nil
}

// buildPriorClaimEntry synthesizes the prior Claim included as a resolvable bundle ENTRY on the
// reference-payer lane (see setPriorClaimReference). It is the original submit's claim:
// br-payer's resolvePriorClaim finds it via related[0].claim.reference, then searches the stored
// authorization by its FIRST identifier — so it carries urn:shn:correlation|OriginalCorr (the
// initial submit's stored Claim identifier). br-payer reads only the identifier, but the bundle is
// SHN-produced, so this entry must be a base-FHIR-VALID Claim (FR-36 egress $validate): it carries
// every required Claim element (status/type/use/patient/created/provider/priority/insurance),
// mirroring the conformant submit/update Claim shape. NOT first in the bundle, so PasBundleValidator's
// first-entry profile checks do not apply.
func buildPriorClaimEntry(patientRef, coverageRef, providerRef, payerOrgID, originalCorr string, created time.Time) ([]byte, error) {
	claim := fhir.Claim{
		Id:     strPtr(conformantPASClaimID),
		Status: fhir.FinancialResourceStatusCodesActive,
		Type: fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System: strPtr("http://terminology.hl7.org/CodeSystem/claim-type"),
				Code:   strPtr("professional"),
			}},
		},
		Use:      fhir.UsePreauthorization,
		Patient:  fhir.Reference{Reference: strPtr(patientRef)},
		Created:  created.UTC().Format(time.RFC3339),
		Provider: fhir.Reference{Reference: strPtr(providerRef)},
		Insurer:  &fhir.Reference{Reference: strPtr("Organization/" + payerOrgID)},
		Priority: fhir.CodeableConcept{Coding: []fhir.Coding{{Code: strPtr("normal")}}},
		Insurance: []fhir.ClaimInsurance{{
			Sequence: 1,
			Focal:    true,
			Coverage: fhir.Reference{Reference: strPtr(coverageRef)},
		}},
		Identifier: []fhir.Identifier{{
			System: strPtr("urn:shn:correlation"),
			Value:  strPtr(originalCorr),
		}},
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		return nil, err
	}
	return pasInjectResourceType(raw, "Claim")
}

// ConformantClaimUpdateInputs are the inputs the conformant amended re-POST builder needs from
// the Originator. QR is the answered amended QuestionnaireResponse; SR is the order
// ServiceRequest; Provenance is REQUIRED (FR-32 — the inbound gate 403s if absent);
// DiagnosticReport is optional (nil on QR-targeted paths). Corr is this amendment's
// correlation; OriginalCorr is the original submit's correlation (→ Claim.related[0]). Created
// drives the deterministic Bundle timestamp/Claim.created. Demo-persona only — no br-payer
// foreign seed.
//
// ContainedInsurer: same semantics as ConformantClaimInputs.ContainedInsurer — set true for
// the reference-payer lane so the update Claim's insurer is also resolvable.
//
// AbsoluteRefs: same semantics as ConformantClaimInputs.AbsoluteRefs — set true for the
// reference-payer lane so update bundle internal refs are absolute. Out-of-bundle refs (e.g.
// Provenance.agent Organization/provider or Practitioner/<npi>) are left untouched.
type ConformantClaimUpdateInputs struct {
	QR []byte
	SR []byte
	// Provider is the requesting provider's own record, with the same meaning and
	// the same requirement as ConformantClaimInputs.Provider: an amendment names
	// the party the original submission named, so the payer matching an inquiry
	// finds one authorization rather than none.
	Provider []byte
	// Coverage is the member's own Coverage record, with the same meaning and the
	// same requirement as ConformantClaimInputs.Coverage: an amendment is made
	// under the policy the submission named, so it names the same record.
	// Insurer is the participant's own Organization record for the payer, with the
	// same meaning and the same requirement as ConformantClaimInputs.Insurer: an
	// amendment names the payer the submission named.
	Insurer          []byte
	Coverage         []byte
	PatientRef       string
	CoverageRef      string
	Provenance       []byte // FR-32 — REQUIRED (the inbound gate 403s if absent)
	DiagnosticReport []byte // optional — nil on QR-targeted paths
	Corr             string // this amendment's correlation
	OriginalCorr     string // → Claim.related[0].claim.identifier.value
	Created          time.Time
	ContainedInsurer bool // reference-payer lane only; false = byte-identical SHN-native path
	AbsoluteRefs     bool // reference-payer lane only; false = byte-identical SHN-native path
	// PayerOrgEntry: same semantics as ConformantClaimInputs.PayerOrgEntry — the cms-payer
	// Organization is a resolvable bundle ENTRY (not contained) so br-payer's PAS re-evaluation
	// of the update resolves the payor (findInBundle, entries only). Reference-payer-lane-only; precedence
	// over ContainedInsurer.
	PayerOrgEntry bool
	// Payer is the payer Organization identifier (system|value) — same semantics as
	// ConformantClaimInputs.Payer. Pass the identity read from the patient's Coverage,
	// or shnsdk.CMSPayerIdentity for the conformance payer.
	Payer PayerIdentifier
	// MemberID is the bare member id this amendment is for — the same value the
	// submission named, so the payer matching an inquiry finds one authorization
	// rather than none. CoverageRef stays for the caller's other roles (QR
	// context, the native/minimized lanes) and does not land on the wire.
	MemberID string
	// MemberIDSystem has the same meaning and the same requirement as
	// ConformantClaimInputs.MemberIDSystem: an amendment names the member the
	// same way the submission did, so the payer matching an inquiry finds one
	// authorization rather than none.
	MemberIDSystem string
}

// BuildConformantClaimUpdateBundle assembles a LEAN, generic, demo-persona-derived CONFORMANT
// Da Vinci amended re-POST Claim Bundle — the conformant update sibling of BuildConformantClaimBundle
// (which stays untouched). It carries the conformant $submit lean shape PLUS:
//   - Claim.related[prior] referencing the original submit correlation (FR-21)
//   - a Provenance entry (FR-32 — REQUIRED)
//   - an optional DiagnosticReport entry (present when DiagnosticReport != nil)
//
// Entry order: Claim, Patient, Coverage, ServiceRequest, QuestionnaireResponse,
// DiagnosticReport (when present), Provenance.
//
// meta.profile: NO meta.profile on any entry (identical to BuildConformantClaimBundle).
// Deterministic (no time.Now/random); caller injects Created. It reuses the
// byte-parity-locked buildPASUpdateClaim helper (the minimized BuildClaimUpdateBundle
// public builder has been removed — this is the sole PA-update builder).
//
// BuildConformantClaimUpdateBundle speaks PAS line 2.0 — it is
// BuildConformantClaimUpdateBundleAtLine("2.0", in), byte-identical (regression-fenced
// by test/sdkparity). Use BuildConformantClaimUpdateBundleAtLine to target 2.1/2.2
// (PAS package differential: Claim.item line-detail + Claim.related.relationship).
func BuildConformantClaimUpdateBundle(in ConformantClaimUpdateInputs) ([]byte, error) {
	def, _ := PASLineDef("2.0") // always present — pinned by manifest + parity-tested
	return buildConformantClaimUpdateBundle(def, in)
}

// BuildConformantClaimUpdateBundleAtLine is BuildConformantClaimUpdateBundle
// parameterized by PAS line ("2.0"|"2.1"|"2.2"). Unknown line errors (fail-closed).
func BuildConformantClaimUpdateBundleAtLine(line string, in ConformantClaimUpdateInputs) ([]byte, error) {
	def, ok := PASLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildConformantClaimUpdateBundleAtLine: unknown PAS line %q", line)
	}
	return buildConformantClaimUpdateBundle(def, in)
}

func buildConformantClaimUpdateBundle(def PASDef, in ConformantClaimUpdateInputs) ([]byte, error) {
	// --- Claim: reuse buildPASUpdateClaim (emits related[] by OriginalCorr), then
	// conformantize (extension-requestedService; productOrService is set from the order
	// below, unconditionally) and restamp id to the update-specific conformant id. ---
	providerRef, providerURL, err := pasProviderEntry(in.Provider)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	claimJSON, err := buildPASUpdateClaim(in.PatientRef, in.CoverageRef, providerRef, in.Corr, in.OriginalCorr, in.Created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: build claim: %w", err)
	}
	srRef := "ServiceRequest/" + conformantPASServiceRequestID
	claimJSON, err = conformantizePASClaim(claimJSON, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: conformantize claim: %w", err)
	}
	// conformantizePASClaim stamps id to conformantPASClaimID ("convergence-claim");
	// the update bundle uses conformantPASClaimUpdateID ("convergence-claim-update").
	claimJSON, err = withResourceID(claimJSON, conformantPASClaimUpdateID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id claim update: %w", err)
	}
	// PAS 2.1+ (def-driven, PAS package differential): item-detail extensions + location[x],
	// and Claim.related[0].relationship (ex-relatedclaimrelationship#prior). No-op at
	// line 2.0.
	claimJSON, err = addPASLineItemDetail(claimJSON, def)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: add line item detail: %w", err)
	}
	claimJSON, err = addPASLineRelatedRelationship(claimJSON, def)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: add related relationship: %w", err)
	}
	// Every caller: set item[0].productOrService from the order's actual code — same
	// unconditional derivation as the submit builder (see its comment). Fails loud when
	// the order carries no code.
	claimJSON, err = setClaimItemProductFromSR(claimJSON, in.SR)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: set claim product from SR: %w", err)
	}
	// Reference-payer lane: PayerOrgEntry — insurer references the payer org ENTRY; takes
	// precedence over the legacy contained-insurer splice. Same as BuildConformantClaimBundle.
	// The payer this amendment names — the participant's own record, the same one
	// the submission named. See pasPayerOrgEntry.
	var payerOrg pasPayerOrgRecord
	if in.PayerOrgEntry {
		if payerOrg, err = pasPayerOrgEntry(in.Insurer, in.Payer); err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
		}
	}
	switch {
	case in.PayerOrgEntry:
		claimJSON, err = repointInsurerToEntry(claimJSON, payerOrg.id)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: repoint insurer to entry: %w", err)
		}
	case in.ContainedInsurer:
		claimJSON, err = containInsurer(claimJSON, in.Payer)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: contain insurer: %w", err)
		}
	}
	// infoChanged is UNCONDITIONAL — every lane (register R5/Task-A finding, 2026-08-25):
	// PayerOrgEntry may gate only the payer-Organization bundle-entry SHAPE (contained vs
	// resolvable), never correctness/conformance content — the same ruling as the §13
	// productOrService fix above (setClaimItemProductFromSR runs unconditionally too). br-payer's
	// hasInfoChanged (PasSubmitService.java:316/449) gates re-evaluation on this marker; a demo/
	// hermetic amendment that never carried it was previously resolved ONLY by the mirror's own
	// bare-Provenance tolerance (internal/brpayermirror), a shape no real Da Vinci PAS payer
	// accepts. Converging here retires that tolerance's justification (see
	// amendmentRequestsResolution).
	claimJSON, err = appendInfoChangedToClaimItems(claimJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: append infoChanged: %w", err)
	}
	// The item trace numbers are the SUBMISSION'S, not this amendment's. An item
	// trace number identifies the service line, and an amendment is about the lines
	// the payer already holds: restating them under a fresh correlation would name
	// items the payer has never seen, and a later Claim/$inquire narrowed by them
	// matches nothing — measured against the reference payer mirror, an amended
	// authorization could not be found at all. The amendment's own correlation
	// stays what it is everywhere else (its identifier, its Provenance, the leg);
	// only the line identity is the one already on file. OriginalCorr is empty only
	// where no prior submission is named, and then this amendment's own correlation
	// is the only line identity there is.
	traceCorr := in.OriginalCorr
	if traceCorr == "" {
		traceCorr = in.Corr
	}
	claimJSON, err = stampItemTraceNumbers(claimJSON, traceCorr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	// Reference-payer lane ONLY: repoint Claim.related[0].claim.reference at the prior Claim
	// ENTRY (added to the bundle below, also PayerOrgEntry-gated). br-payer's resolvePriorClaim
	// (PasSubmitService.java:379-403) reads .reference (NOT .identifier) and requires the prior
	// Claim in-bundle (else HTTP 400 "The prior Claim referenced in Claim.related.claim must be
	// included in the Bundle"). The relative ref is absolutized to the entry's fullUrl by
	// absolutizeBundleRefs (AbsoluteRefs) — what findInBundle keys on. This part stays lane-gated
	// (unlike infoChanged above) because it is NOT correctness content — it is a bundle-shape
	// fact: the non-PayerOrgEntry (SHN-native/demo) lane never adds the prior Claim as a bundle
	// entry (see below), so rewriting the reference there would DANGLE. That lane keeps the lean
	// identifier-only related[0] buildPASUpdateClaim already set (byte-identical to golden).
	if in.PayerOrgEntry {
		claimJSON, err = setPriorClaimReference(claimJSON, "Claim/"+conformantPASClaimID)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: prior-claim ref: %w", err)
		}
	}

	// --- Coverage: identical to the submit builder — the member's OWN Coverage
	// record, prepared to ride this request. An amendment is made under the same
	// policy the submission was, so it names the same record. ---
	if in.MemberID == "" {
		return nil, fmt.Errorf("shnsdk: MemberID is required (bare member id; see the v0.42.0 identifier-semantics release note)")
	}
	coverage, err := pasCoverageEntry(in.Coverage, in.PatientRef, in.Payer, payerOrg)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	coverageJSON, coverageRef := coverage.raw, "Coverage/"+coverage.id
	claimJSON, err = setInsuranceCoverageEntryRef(claimJSON, coverageRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}

	// --- ServiceRequest: identical to the submit builder. ---
	srJSON, err := withResourceID(in.SR, conformantPASServiceRequestID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id sr: %w", err)
	}
	srJSON, err = stripMetaProfile(srJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: strip sr meta: %w", err)
	}

	// --- Patient: identical to the submit builder — the member, named. ---
	patientJSON, err := pasMemberPatient(in.PatientRef, in.MemberIDSystem, in.MemberID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: build patient: %w", err)
	}

	// --- QuestionnaireResponse: stamp the update-specific QR id + rewrite qr-context refs
	// (same dangling-ref rationale as the submit builder — parseConformantPASSubjects binds
	// QR.subject, never qr-context, so the builder owns the bundle-local refs). ---
	qrJSON, err := withResourceID(in.QR, conformantPASUpdateQRID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id qr: %w", err)
	}
	qrJSON, err = rewriteQRContextRefs(qrJSON, coverageRef, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: rewrite qr-context: %w", err)
	}

	// --- Provenance (FR-32 — REQUIRED): stamp stable id, strip meta.profile, AND rewrite its
	// target to the bundle-local supplemental resource id. The builder restamps the supplemental
	// resource (the DR, or in the QR-targeted variant the amended QR) to a stable bundle-local id,
	// so a caller's Provenance — which targets the PRE-restamp id (e.g. the SoR DiagnosticReport id
	// or the per-UC QR id) — would otherwise DANGLE and the FR-32 inbound gate (engine payer) would
	// 403 "Provenance does not target the supplemental data". rewriteProvenanceTarget makes the
	// builder SELF-CONSISTENT — the same dangling-ref-hazard close as rewriteQRContextRefs for
	// qr-context (the builder OWNS the bundle-local refs). ---
	provJSON, err := withResourceID(in.Provenance, conformantPASProvID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id provenance: %w", err)
	}
	provJSON, err = stripMetaProfile(provJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: strip provenance meta: %w", err)
	}
	wantTarget := "QuestionnaireResponse/" + conformantPASUpdateQRID
	if in.DiagnosticReport != nil {
		wantTarget = "DiagnosticReport/" + conformantPASDRID
	}
	provJSON, err = rewriteProvenanceTarget(provJSON, wantTarget)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: rewrite provenance target: %w", err)
	}

	// Derive resolvable absolute fullUrls (FHIR bdl-7 / AI-11), mirror BuildClaimBundle;
	// entry order: Claim, Patient, Coverage, SR, QR, [DR,] Provenance.
	entryFor := func(resourceJSON []byte) (fhir.BundleEntry, error) {
		u, err := pasFullURLFor(resourceJSON)
		if err != nil {
			return fhir.BundleEntry{}, err
		}
		return fhir.BundleEntry{FullUrl: strPtr(u), Resource: json.RawMessage(resourceJSON)}, nil
	}
	entries := make([]fhir.BundleEntry, 0, 9)
	// The requesting provider rides as its own entry, exactly as on the submit:
	// an amendment names the same party, and it resolves in the Bundle.
	baseResources := [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, qrJSON, in.Provider}
	// Reference-payer lane: add the cms-payer Organization as a resolvable bundle ENTRY so the
	// repointed Coverage.payor/Claim.insurer resolve (br-payer findInBundle, entries only).
	if in.PayerOrgEntry {
		if providerURL == pasBundleBaseURL+"/Organization/"+payerOrg.id {
			return nil, fmt.Errorf("shnsdk: conformant update: the requesting provider and the payer organization are the same bundle entry (%s)", providerRef)
		}
		baseResources = append(baseResources, payerOrg.raw)
		// The prior Claim as a resolvable bundle ENTRY (NOT first → not profile-validated by
		// PasBundleValidator; carries urn:shn:correlation|OriginalCorr, what br-payer searches the
		// stored authorization on). The operative update Claim's related.reference resolves to it.
		priorClaimJSON, err := buildPriorClaimEntry(in.PatientRef, coverageRef, providerRef, payerOrg.id, in.OriginalCorr, in.Created)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: build prior claim entry: %w", err)
		}
		baseResources = append(baseResources, priorClaimJSON)
	}
	for _, rj := range baseResources {
		e, err := entryFor(rj)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}

	// DiagnosticReport entry is optional (nil on QR-targeted paths).
	if in.DiagnosticReport != nil {
		drJSON, err := withResourceID(in.DiagnosticReport, conformantPASDRID)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: id dr: %w", err)
		}
		drJSON, err = stripMetaProfile(drJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: strip dr meta: %w", err)
		}
		e, err := entryFor(drJSON)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}

	// Provenance is always last.
	provEntry, err := entryFor(provJSON)
	if err != nil {
		return nil, err
	}
	entries = append(entries, provEntry)

	bundle := fhir.Bundle{
		Type:       fhir.BundleTypeCollection,
		Identifier: &fhir.Identifier{System: strPtr(pasBundleIdentifierSystem), Value: strPtr(in.Corr)},
		Timestamp:  strPtr(in.Created.UTC().Format(time.RFC3339)),
		Entry:      entries,
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: marshal bundle: %w", err)
	}
	bundleOut, err := pasInjectResourceType(raw, "Bundle")
	if err != nil {
		return nil, err
	}
	// Reference-payer lane only: same absolute-ref rewrite as BuildConformantClaimBundle.
	// Out-of-bundle refs (e.g. Provenance.agent Organization/provider or
	// Practitioner/<npi>) are left untouched — they don't appear in the entry set.
	if in.AbsoluteRefs {
		bundleOut, err = absolutizeBundleRefs(bundleOut)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: absolutize refs: %w", err)
		}
	}
	if err := checkPASProviderResolves(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	if err := checkPASMemberIdentified(bundleOut, in.MemberID); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	if err := checkPASCoverageResolves(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	// dom-3 over the coverage and the claim — the same guard the submit builder runs.
	if err := checkPASContainedReferenced(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	if in.PayerOrgEntry || in.ContainedInsurer {
		if err := checkPASInsurerResolves(bundleOut); err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
		}
	}
	return bundleOut, nil
}

// ParsePendedResponse classifies the uniquely selected ClaimResponse by decision
// content, in either a response Bundle or a bare polling resource. It is
// ParsePendedResponseDetail with the needs flattened to NeededItems
// (PendedResponseDetail.NeededItems).
func ParsePendedResponse(data []byte) (pended bool, needed []NeededItem, err error) {
	pended, detail, err := ParsePendedResponseDetail(data)
	if err != nil || !pended {
		return false, nil, err
	}
	return true, detail.NeededItems(), nil
}

// PendedResponseDetail is what a pended PAS response asks for.
type PendedResponseDetail struct {
	// Tasks are the response's PAS pended-response Tasks, in Bundle order: a
	// Task coded from PASTaskCodes, or a Task with no code at all.
	Tasks []PendedTask
	// OtherTasks are the response's other Tasks (for example a CDex data
	// request), exactly as received.
	OtherTasks []json.RawMessage
}

// PendedTask is one PAS pended-response Task as the payer sent it.
type PendedTask struct {
	// Raw is the Task exactly as received.
	Raw         json.RawMessage
	ID          string
	Status      string
	Code        string // the PASTempCodes code, "" when the Task has none
	Profiles    []string
	Identifiers []PASIdentifier
	// Requester and Owner are the identifiers the Task names (nil when it
	// names none, or only by reference).
	Requester *PASIdentifier
	Owner     *PASIdentifier
	// PayerURL is the payer-url input's valueUrl ("" when the input is
	// absent or not a valueUrl, in which case Nonconformant reports it).
	PayerURL string
	// Items are the conformant needs, grouped by request line (Sequence 0
	// when an input carries no line number).
	Items []PendedItem
	// Nonconformant are the inputs this parser could not read as their line
	// defines them. They are reported, never dropped and never converted.
	Nonconformant []NonconformantTaskInput

	needed []NeededItem
}

// NonconformantTaskInput is a Task input whose value is not in the form the
// PAS profile defines for its input type (for example a questionnaires-needed
// input with a valueCanonical, where PAS 2.0.1 defines a valueIdentifier), or
// an input whose type is not a PAS input type.
type NonconformantTaskInput struct {
	// Index is the input's position in Task.input.
	Index int
	// Type is the input's type code (a PASTempCodes code, or a code given
	// without a system), or its type text when it has neither.
	Type string
	// ValueType is the value's element name (for example "valueCanonical"),
	// "" when the input has no value.
	ValueType string
	// Value is the value exactly as received.
	Value json.RawMessage
	// Text is the value's text when the value is a JSON string (for a
	// canonical, the canonical itself).
	Text string
	// Sequence is the input's line number, 0 when it has none.
	Sequence int
}

// NeededItems flattens the needs into NeededItem values, in Task and input
// order: each attachment code, questionnaire identifier value and
// questionnaire context, and each nonconformant value with text other than
// the payer URL. Display is the coding's display, else the input type's
// display or text.
func (d PendedResponseDetail) NeededItems() []NeededItem {
	var out []NeededItem
	for _, t := range d.Tasks {
		out = append(out, t.needed...)
	}
	return out
}

// ParsePendedResponseDetail reads a PAS response. pended is true when the
// uniquely selected ClaimResponse's decision is pending (queued outcome or the
// A4 review action); detail then lists the response's Tasks. Each PAS Task
// input is read by the type its line defines: attachments-needed as a
// CodeableConcept, questionnaires-needed as an Identifier (PAS 2.0.1),
// questionnaire-context as a string (PAS 2.1.0 and 2.2.1), payer-url as a
// url, and the line number from extension-paLineNumber (2.0.1, 2.1.0) or
// extension-serviceLineNumber (2.2.1). Any other form is reported in
// Nonconformant. The input bytes are never changed.
func ParsePendedResponseDetail(data []byte) (pended bool, detail PendedResponseDetail, err error) {
	response, tasks, err := selectPASClaimResponse(data)
	if err != nil {
		return false, PendedResponseDetail{}, err
	}
	result, err := parsePASClaimDecision(response)
	if err != nil {
		return false, PendedResponseDetail{}, err
	}
	if result.Outcome != "pended" {
		return false, PendedResponseDetail{}, nil
	}
	for _, raw := range tasks {
		t, pas, err := parsePendedTask(raw)
		if err != nil {
			return false, PendedResponseDetail{}, err
		}
		if !pas {
			detail.OtherTasks = append(detail.OtherTasks, raw)
			continue
		}
		detail.Tasks = append(detail.Tasks, t)
	}
	return true, detail, nil
}

type pendedTaskCoding struct {
	System  string `json:"system"`
	Code    string `json:"code"`
	Display string `json:"display"`
}

type pendedTaskProbe struct {
	ID   string `json:"id"`
	Meta struct {
		Profile []string `json:"profile"`
	} `json:"meta"`
	Identifier []PASIdentifier `json:"identifier"`
	Status     string          `json:"status"`
	Code       *struct {
		Coding []pendedTaskCoding `json:"coding"`
	} `json:"code"`
	Requester *struct {
		Identifier *PASIdentifier `json:"identifier"`
	} `json:"requester"`
	Owner *struct {
		Identifier *PASIdentifier `json:"identifier"`
	} `json:"owner"`
	Input []map[string]json.RawMessage `json:"input"`
}

// pasInputType returns an input type's code, whether it is a PASTempCodes
// code (a coding without a system yields its code with pas false), the type's
// text and the coding's display.
func pasInputType(raw json.RawMessage) (code string, pas bool, text, display string) {
	var t struct {
		Text   string             `json:"text"`
		Coding []pendedTaskCoding `json:"coding"`
	}
	_ = json.Unmarshal(raw, &t)
	for _, c := range t.Coding {
		if c.System == PASTempCodesSystem {
			return c.Code, true, t.Text, c.Display
		}
	}
	for _, c := range t.Coding {
		if c.System == "" && c.Code != "" {
			return c.Code, false, t.Text, c.Display
		}
	}
	return "", false, t.Text, ""
}

// pasInputLine returns the input's line number (0 when it carries none).
func pasInputLine(raw json.RawMessage) int {
	var exts []struct {
		URL              string `json:"url"`
		ValueInteger     *int   `json:"valueInteger"`
		ValuePositiveInt *int   `json:"valuePositiveInt"`
	}
	_ = json.Unmarshal(raw, &exts)
	for _, e := range exts {
		switch {
		case e.URL == pasExtPALineNumber && e.ValueInteger != nil:
			return *e.ValueInteger
		case e.URL == pasExtServiceLineNumber && e.ValuePositiveInt != nil:
			return *e.ValuePositiveInt
		}
	}
	return 0
}

// parsePendedTask reads one Task; pas is false for a Task coded from another
// code system.
func parsePendedTask(raw json.RawMessage) (t PendedTask, pas bool, err error) {
	var p pendedTaskProbe
	if err := json.Unmarshal(raw, &p); err != nil {
		return PendedTask{}, false, fmt.Errorf("shnsdk: parse PAS Task: %w", err)
	}
	if p.Code != nil {
		for _, c := range p.Code.Coding {
			if c.System == PASTempCodesSystem {
				t.Code = c.Code
			}
		}
		if t.Code == "" {
			return PendedTask{}, false, nil
		}
	}
	t.Raw, t.ID, t.Status, t.Profiles, t.Identifiers = raw, p.ID, p.Status, p.Meta.Profile, p.Identifier
	if p.Requester != nil {
		t.Requester = p.Requester.Identifier
	}
	if p.Owner != nil {
		t.Owner = p.Owner.Identifier
	}
	bySeq := map[int]*PendedItem{}
	var order []int
	item := func(seq int) *PendedItem {
		if it, ok := bySeq[seq]; ok {
			return it
		}
		bySeq[seq] = &PendedItem{Sequence: seq}
		order = append(order, seq)
		return bySeq[seq]
	}
	for i, in := range p.Input {
		typeCode, pasType, text, display := pasInputType(in["type"])
		if display == "" {
			display = text
		}
		code := ""
		if pasType {
			code = typeCode
		}
		seq := pasInputLine(in["extension"])
		valueType := ""
		for k := range in {
			if strings.HasPrefix(k, "value") {
				if valueType != "" {
					valueType = "" // two values: not one readable value
					break
				}
				valueType = k
			}
		}
		value := in[valueType]
		ok := false
		switch {
		case code == pasTaskInputPayerURL && valueType == "valueUrl":
			ok = json.Unmarshal(value, &t.PayerURL) == nil
		case code == pasTaskInputAttachments && valueType == "valueCodeableConcept":
			var cc struct {
				Coding []pendedTaskCoding `json:"coding"`
			}
			if json.Unmarshal(value, &cc) == nil && len(cc.Coding) > 0 {
				ok = true
				it := item(seq)
				for _, c := range cc.Coding {
					it.AttachmentCodes = append(it.AttachmentCodes, PASCoding(c))
					d := c.Display
					if d == "" {
						d = display
					}
					t.needed = append(t.needed, NeededItem{Code: c.Code, Display: d})
				}
			}
		case code == pasTaskInputQuestionnaires && valueType == "valueIdentifier":
			var id PASIdentifier
			if json.Unmarshal(value, &id) == nil && id.Value != "" {
				ok = true
				it := item(seq)
				it.QuestionnaireIDs = append(it.QuestionnaireIDs, id)
				t.needed = append(t.needed, NeededItem{Code: id.Value, Display: display})
			}
		case code == pasTaskInputQuestionnaireCtx && valueType == "valueString":
			var v string
			if json.Unmarshal(value, &v) == nil && v != "" {
				ok = true
				it := item(seq)
				it.QuestionnaireContexts = append(it.QuestionnaireContexts, v)
				t.needed = append(t.needed, NeededItem{Code: v, Display: display})
			}
		}
		if ok {
			continue
		}
		name := typeCode
		if name == "" {
			name = text
		}
		nc := NonconformantTaskInput{Index: i, Type: name, ValueType: valueType, Value: value, Sequence: seq}
		var str string
		if len(value) > 0 && json.Unmarshal(value, &str) == nil {
			nc.Text = str
		}
		t.Nonconformant = append(t.Nonconformant, nc)
		if nc.Text != "" && typeCode != pasTaskInputPayerURL {
			t.needed = append(t.needed, NeededItem{Code: nc.Text, Display: display})
		}
	}
	for _, seq := range order {
		t.Items = append(t.Items, *bySeq[seq])
	}
	return t, true, nil
}

// selectPASClaimResponse rejects ambiguous or malformed envelopes before either
// decision parser can interpret them. Graph/profile validation remains separate.
func selectPASClaimResponse(data []byte) (json.RawMessage, []json.RawMessage, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, nil, fmt.Errorf("shnsdk: parse PAS response: %w", err)
	}
	if probe.ResourceType == "ClaimResponse" {
		return data, nil, nil
	}
	if probe.ResourceType != "Bundle" {
		return nil, nil, fmt.Errorf("shnsdk: expected PAS Bundle or ClaimResponse, got %q", probe.ResourceType)
	}
	var response json.RawMessage
	var tasks []json.RawMessage
	for i, entry := range probe.Entry {
		var resource struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(entry.Resource, &resource); err != nil {
			return nil, nil, fmt.Errorf("shnsdk: parse PAS entry %d: %w", i, err)
		}
		if resource.ResourceType == "" {
			return nil, nil, fmt.Errorf("shnsdk: PAS entry %d has no resourceType", i)
		}
		switch resource.ResourceType {
		case "ClaimResponse":
			if response != nil {
				return nil, nil, fmt.Errorf("shnsdk: PAS Bundle has multiple ClaimResponses")
			}
			response = entry.Resource
		case "Task":
			tasks = append(tasks, entry.Resource)
		}
	}
	if response == nil {
		return nil, nil, fmt.Errorf("shnsdk: PAS Bundle has no ClaimResponse")
	}
	return response, tasks, nil
}

const (
	// PAS reviewAction extension URLs (mirror internal/pas). The X12 review-action code
	// system (https://codesystem.x12.org/005010/306) defines A1 = Certified in total,
	// A2 = Certified – partial, A3 = Not Certified (the denial), A4 = Pended. There is no
	// "Not Required" code in the A-series. SHN's own producer (sdk/pasresponder.go) emits
	// the conformant A3 for its denials — this is correct, not a legacy stand-in for A2.
	//
	// The real reference payer (br-payer a8bece4) denies with reviewActionCode A2 but
	// display text "Not Certified" — a code/display self-contradiction, i.e. a bug in the
	// RI (worth reporting upstream to HL7 Da Vinci; see
	// docs/workstreams/prior-authorization/mode-a-onboarding.md §"Denial review-action
	// code" for the recorded divergence). The SDK PARSES A2-as-a-denial so it can read a
	// real RI's output, but never EMITS it — SHN's own denials always carry A3.
	//
	// The SDK only needs the two extension URLs + the code to PARSE a denial; the denied
	// ClaimResponse's outcome stays "complete" — the reviewActionCode is the authoritative
	// denial signal, not preAuthRef absence.
	reviewActionExtURL     = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	reviewActionCodeExtURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"
	// reviewActionDeniedCode is the X12-conformant denial code (A3, "Not Certified") — the
	// code SHN's own producer emits and the one ParseClaimResponse treats as an
	// unconditional denial regardless of any auth number present.
	reviewActionDeniedCode = "A3"
	// reviewActionDeniedCodeObservedRI is br-payer's own denial code (A2) — observed RI
	// behavior, PARSED as a denial only when no auth number accompanies it (see
	// ParseClaimResponse: a real X12-conformant sender using A2 means "Certified –
	// partial", not a denial, and carries an auth number). Never emitted by SHN.
	reviewActionDeniedCodeObservedRI = "A2"
)

// ParseClaimResponse parses a PAS response Bundle or bare polling ClaimResponse into a PriorAuthResult by EXPLICIT
// signals — approved, denied, partial, and pended are each keyed on an explicit marker:
//   - reviewActionCode == "A3" (X12 "Not Certified", SHN's own conformant denial code) ⇒
//     Outcome "denied" + Denial{ReasonCode, Rationale, AppealNote}, UNCONDITIONALLY — a
//     "number" sub-extension present alongside it does not change this (see
//     TestParseClaimResponse_DeniedWithNumberStaysDenied).
//   - reviewActionCode == "A2" WITHOUT a "number" sub-extension (the observed br-payer
//     denial shape — a code/display self-contradiction in that RI, see the reviewAction*
//     const doc comment above) ⇒ Outcome "denied" + Denial{...}, same as A3.
//   - reviewActionCode == "A2" WITH a "number" sub-extension present anywhere in the
//     response ⇒ NOT a denial: X12 306 defines A2 as "Certified – partial", so this is a
//     partial certification. Outcome "approved" + PreAuthRef (from the number) +
//     Partial:true + Disposition (the payer's own disposition/display text, so the
//     partial's scope is not lost). HONESTY NOTE: no producer this SDK can drive (SHN's
//     own, or br-payer live) emits this shape — br-payer's only observed A2 is the
//     no-number denial above. This branch is parse-side only, hermetically tested against
//     synthetic fixtures, NOT live-proven against a real payer.
//   - non-empty preAuthRef AND outcome "complete" (and no reviewActionCode A2/A3 above)
//     ⇒ Outcome "approved" + PreAuthRef + ValidUntil.
//   - anything else ⇒ error (fail loud on an ambiguous/malformed shape — never infer a
//     confident outcome from absence).
//
// Pending content is rejected here; use ParsePendedResponse to read pending decisions.
func ParseClaimResponse(data []byte) (PriorAuthResult, error) {
	response, _, err := selectPASClaimResponse(data)
	if err != nil {
		return PriorAuthResult{}, err
	}
	result, err := parsePASClaimDecision(response)
	if err != nil {
		return PriorAuthResult{}, err
	}
	if result.Outcome == "pended" {
		return PriorAuthResult{}, fmt.Errorf("shnsdk: ClaimResponse is pending")
	}
	return result, nil
}

func parsePASClaimDecision(data []byte) (PriorAuthResult, error) {
	var probe struct {
		ResourceType  string `json:"resourceType"`
		Outcome       string `json:"outcome"`
		PreAuthRef    string `json:"preAuthRef"`
		Disposition   string `json:"disposition"`
		PreAuthPeriod *struct {
			End string `json:"end"`
		} `json:"preAuthPeriod"`
		ProcessNote []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"processNote"`
		Item []struct {
			ItemSequence int `json:"itemSequence"`
			Adjudication []struct {
				Reason *struct {
					Coding []PASCoding `json:"coding"`
				} `json:"reason"`
				Extension []struct {
					URL       string `json:"url"`
					Extension []struct {
						URL                  string `json:"url"`
						ValueString          string `json:"valueString"`
						ValueCodeableConcept *struct {
							Coding []struct {
								System  string `json:"system"`
								Code    string `json:"code"`
								Display string `json:"display"`
							} `json:"coding"`
						} `json:"valueCodeableConcept"`
					} `json:"extension"`
				} `json:"extension"`
			} `json:"adjudication"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return PriorAuthResult{}, fmt.Errorf("shnsdk: parse ClaimResponse: %w", err)
	}

	// Navigate item[].adjudication[].extension[reviewAction].extension[reviewActionCode]
	// .valueCodeableConcept.coding[] for A3/A2, AND independently collect the "number"
	// sub-extension (real Da Vinci RI preAuthRef placement):
	// item[].adjudication[].extension[reviewAction].extension[url="number"].valueString.
	//
	// The code and the number can land on DIFFERENT item/adjudication/extension entries
	// (observed in real RI output), so the walk collects BOTH across the entire response
	// before any denial/partial/approved decision is made — deciding as soon as a code is
	// seen (the old shape) would return on an A2 before a later-walked "number" entry was
	// ever read, silently reporting a partial certification as a denial with the auth
	// number discarded.
	var reviewActionPreAuthRef string
	var sawA3, sawA2, sawA4 bool
	type itemDecision struct {
		a1, a2, a3, a4 bool
		hasNumber      bool
	}
	decisions := make(map[int]*itemDecision)
	numbers := make(map[string]bool)
	if probe.PreAuthRef != "" {
		numbers[probe.PreAuthRef] = true
	}
	var a3Code, a3Display, a2Code, a2Display string
	// actions keeps, per decision code, the first review action that carried
	// it, with that action's reasons.
	actions := map[string]*PASReviewAction{}
	var denialReasons []PASCoding
	for _, it := range probe.Item {
		decision := decisions[it.ItemSequence]
		if decision == nil {
			decision = &itemDecision{}
			decisions[it.ItemSequence] = decision
		}
		for _, adj := range it.Adjudication {
			if adj.Reason != nil {
				for _, c := range adj.Reason.Coding {
					if c.System == CARCSystem || c.System == RARCSystem {
						denialReasons = append(denialReasons, c)
					}
				}
			}
			for _, ext := range adj.Extension {
				if ext.URL != reviewActionExtURL {
					continue
				}
				var action PASReviewAction
				for _, sub := range ext.Extension {
					if sub.URL == reviewActionCodeExtURL && sub.ValueCodeableConcept != nil && action.Code.Code == "" {
						for _, c := range sub.ValueCodeableConcept.Coding {
							if c.System == X12ReviewDecisionSystem && c.Code != "" {
								action.Code = PASCoding{System: c.System, Code: c.Code, Display: c.Display}
								break
							}
						}
					}
					if sub.URL == "reasonCode" && sub.ValueCodeableConcept != nil {
						for _, c := range sub.ValueCodeableConcept.Coding {
							action.Reasons = append(action.Reasons, PASCoding{System: c.System, Code: c.Code, Display: c.Display})
						}
					}
				}
				if action.Code.Code != "" && actions[action.Code.Code] == nil {
					actions[action.Code.Code] = &action
				}
				for _, sub := range ext.Extension {
					switch sub.URL {
					case reviewActionCodeExtURL:
						if sub.ValueCodeableConcept == nil {
							continue
						}
						for _, c := range sub.ValueCodeableConcept.Coding {
							switch c.Code {
							case "A1":
								decision.a1 = true
							case "A4":
								decision.a4 = decision.a4 || c.System == "https://codesystem.x12.org/005010/306"
								sawA4 = sawA4 || decision.a4
							case reviewActionDeniedCode: // "A3"
								decision.a3 = true
								if !sawA3 {
									sawA3, a3Code, a3Display = true, c.Code, c.Display
								}
							case reviewActionDeniedCodeObservedRI: // "A2"
								decision.a2 = true
								if !sawA2 {
									sawA2, a2Code, a2Display = true, c.Code, c.Display
								}
							}
						}
					case "number":
						if sub.ValueString != "" {
							decision.hasNumber = true
							numbers[sub.ValueString] = true
						}
						// Real Da Vinci PAS RIs place the auth number in the reviewAction
						// "number" sub-extension rather than the top-level preAuthRef field
						// (observed in real RI output). Take the first non-empty value seen.
						if reviewActionPreAuthRef == "" && sub.ValueString != "" {
							reviewActionPreAuthRef = sub.ValueString
						}
					}
				}
			}
		}
	}

	// Review actions belong to an item sequence. Different items may complete at
	// different times; only contradictory signals on the same item are invalid.
	for _, decision := range decisions {
		if (decision.a4 && (decision.a1 || decision.a2 || decision.a3 || decision.hasNumber)) ||
			(decision.a1 && (decision.a2 || decision.a3)) || (decision.a2 && decision.a3) {
			return PriorAuthResult{}, fmt.Errorf("shnsdk: contradictory PAS item decision")
		}
	}
	if probe.Outcome == "queued" || sawA4 {
		if probe.Outcome != "queued" && probe.Outcome != "complete" {
			return PriorAuthResult{}, fmt.Errorf("shnsdk: invalid pending ClaimResponse outcome %q", probe.Outcome)
		}
		// Without an explicit pending item, terminal markers contradict queued.
		if !sawA4 {
			for _, decision := range decisions {
				if decision.a1 || decision.a2 || decision.a3 || decision.hasNumber {
					return PriorAuthResult{}, fmt.Errorf("shnsdk: contradictory pending and terminal PAS decision")
				}
			}
		}
		if probe.PreAuthRef != "" && len(decisions) <= 1 {
			return PriorAuthResult{}, fmt.Errorf("shnsdk: contradictory pending and terminal PAS decision")
		}
		return PriorAuthResult{Outcome: "pended"}, nil
	}
	if len(numbers) > 1 {
		return PriorAuthResult{}, fmt.Errorf("shnsdk: multiple PAS authorization numbers cannot be represented")
	}

	// dispositionText builds the payer-sourced rationale/disposition: probe.Disposition,
	// falling back to the reviewActionCode's own display (e.g. "Not Certified") when
	// absent — a conformant payer (br-payer) carries no disposition/processNote on a
	// coverage-exclusion A2, so the reviewAction display is its only denial text.
	dispositionText := func(display string) string {
		if probe.Disposition != "" {
			return probe.Disposition
		}
		return display
	}
	processNotes := func() []string {
		notes := make([]string, 0, len(probe.ProcessNote))
		for _, n := range probe.ProcessNote {
			if n.Text != "" {
				notes = append(notes, n.Text)
			}
		}
		return notes
	}
	var typedNotes []PASProcessNote
	for _, n := range probe.ProcessNote {
		if n.Text != "" {
			typedNotes = append(typedNotes, PASProcessNote{Type: n.Type, Text: n.Text})
		}
	}
	// detail adds the payer's decision detail to a terminal result.
	detail := func(r PriorAuthResult, code string) PriorAuthResult {
		r.ProcessNotes, r.ReviewAction, r.DenialReasons = typedNotes, actions[code], denialReasons
		return r
	}

	// A3 is SHN's own X12-conformant denial code — unconditional. A "number" sub-extension
	// present alongside it does NOT flip this to approved (see
	// TestParseClaimResponse_DeniedWithNumberStaysDenied): A3 has no "partial" reading in
	// X12 306, so a number riding along with it is not this function's to interpret.
	if sawA3 {
		return detail(PriorAuthResult{
			Outcome: "denied",
			Denial: &Denial{
				ReasonCode: a3Code,
				Rationale:  dispositionText(a3Display),
				AppealNote: processNotes(),
			},
		}, a3Code), nil
	}

	if sawA2 {
		if reviewActionPreAuthRef != "" {
			// A2 WITH an auth number: this is NOT a denial. X12 306 defines A2 as
			// "Certified – partial" — an authorization WAS issued, just not in full. See
			// the ParseClaimResponse doc comment for the honesty note: no producer this
			// SDK can drive emits this shape today (br-payer's only observed A2 carries no
			// number); this branch is parse-side only, proven by
			// TestParseClaimResponse_A2CertifiedPartial, not live.
			validUntil := ""
			if probe.PreAuthPeriod != nil {
				validUntil = probe.PreAuthPeriod.End
			}
			return detail(PriorAuthResult{
				Outcome:     "approved",
				PreAuthRef:  reviewActionPreAuthRef,
				ValidUntil:  validUntil,
				Partial:     true,
				Disposition: dispositionText(a2Display),
			}, a2Code), nil
		}
		// A2 WITHOUT an auth number: the observed br-payer denial shape — a code/display
		// self-contradiction in that RI (code A2, "Certified – partial", but display "Not
		// Certified"). We parse it as a denial so we can read a real RI's output; SHN
		// itself never emits this.
		return detail(PriorAuthResult{
			Outcome: "denied",
			Denial: &Denial{
				ReasonCode: a2Code,
				Rationale:  dispositionText(a2Display),
				AppealNote: processNotes(),
			},
		}, a2Code), nil
	}

	// Approved: explicit preAuthRef (top-level SHN convention) OR reviewAction "number"
	// sub-extension (real Da Vinci RI convention) + outcome complete.
	preAuthRef := probe.PreAuthRef
	if preAuthRef == "" {
		preAuthRef = reviewActionPreAuthRef
	}
	if probe.Outcome == "complete" && preAuthRef != "" {
		validUntil := ""
		if probe.PreAuthPeriod != nil {
			validUntil = probe.PreAuthPeriod.End
		}
		return detail(PriorAuthResult{Outcome: "approved", PreAuthRef: preAuthRef, ValidUntil: validUntil}, "A1"), nil
	}

	// Anything else is ambiguous — fail loud rather than guess.
	return PriorAuthResult{}, fmt.Errorf("shnsdk: ClaimResponse is neither approved (no preAuthRef) nor denied (no reviewActionCode A2/A3); ambiguous outcome=%q", probe.Outcome)
}

// parsePASOutcome classifies PAS decision content in Bundles and polling resources.
// The caller fills Resume from its leg context. Shared by RunPriorAuth
// (submit response) and ResumePriorAuth (update response) so both stay consistent.
func parsePASOutcome(data []byte) (PriorAuthResult, error) {
	pended, needed, err := ParsePendedResponse(data)
	if err != nil {
		return PriorAuthResult{}, err
	}
	if pended {
		return PriorAuthResult{Outcome: "pended", NeededItems: needed}, nil
	}
	return ParseClaimResponse(data)
}
