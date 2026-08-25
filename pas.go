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
}

// NeededItem is one supplemental item the payer's FR-20 Task asks for on a pended
// PA. Code is the Task.input value (e.g. "operative-diagnostic-report"); Display is
// its human-readable label (Task.input.type.text). Typed so a dev/CLI sees exactly
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

	// MemberID is the bare member id the pended submit stamped as the Coverage's
	// urn:shn:coverage MB identifier value (the bare-member-id identifier rule) and as the
	// Claim's insurance[0].coverage LOGICAL reference (the logical-reference shape); the resume
	// ClaimUpdate must stamp the SAME value in both places, so it rides the handle
	// beside CoverageRef (which stays the Reference-shaped value the QR-context /
	// native-lane roles need). ADDITIVE serialized field, same grow-only local-file
	// rules as PayerID above.
	MemberID string `json:"memberId,omitempty"`
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

// buildPASClaim constructs a FHIR Claim JSON for a preauthorization request.
// Ported byte-for-byte from internal/pas.buildClaim (related is nil for the initial
// submit bundle; the conformant builder BuildConformantClaimBundle reuses it. The X12
// 1365 service-type coding on Claim.item carries the licensed binding target, the actual
// procedure stays on the referenced ServiceRequest — see internal/pas for the binding rationale).
//
// coverageRef is stamped as insurance[0].coverage.reference here to keep this port
// byte-identical to its internal/pas twin, but BuildConformantClaimBundle OVERWRITES that
// element bundle-side with a logical reference (the logical-reference shape — see
// setInsuranceCoverageLogicalRef), exactly as it overwrites insurer via repointInsurerToEntry.
// Nothing conformant reaches the wire carrying this value.
func buildPASClaim(patientRef, coverageRef, correlationID string, created time.Time) ([]byte, error) {
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
		Provider: fhir.Reference{Display: strPtr("provider")},
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
	conformantPASCoverageID       = "convergence-coverage"
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
	QR               []byte
	SR               []byte
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
	// MemberID is the bare member id stamped BOTH as the Coverage entry's urn:shn:coverage
	// MB identifier value AND — per the logical-reference shape — as the value of the
	// Claim's insurance[0].coverage LOGICAL reference (see setInsuranceCoverageLogicalRef).
	// CoverageRef is Reference-shaped and stays for the caller's other roles (QR context,
	// the native/minimized lanes); this builder no longer lands it on the wire — it stamps
	// insurance[0].coverage bundle-side from MemberID instead.
	MemberID string
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
	claimJSON, err := buildPASClaim(in.PatientRef, in.CoverageRef, in.Corr, in.Created)
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
	switch {
	case in.PayerOrgEntry:
		claimJSON, err = repointInsurerToEntry(claimJSON)
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

	// --- Coverage: reuse BuildCoverageWithPayer (contained cms-payer Org), restamp the id
	// to the bundle-local conformant id, and STRIP meta.profile (PAS context). The
	// urn:shn:coverage MB identifier value is the BARE member id (the identifier-semantics rule) — fail
	// CLOSED rather than silently stamping the Reference-shaped CoverageRef. ---
	if in.MemberID == "" {
		return nil, fmt.Errorf("shnsdk: MemberID is required (bare member id; see the v0.42.0 identifier-semantics release note)")
	}
	coverageJSON, err := BuildCoverageWithPayer(in.PatientRef, in.MemberID, in.Payer)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: build coverage: %w", err)
	}
	coverageJSON, err = withResourceID(coverageJSON, conformantPASCoverageID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: id coverage: %w", err)
	}
	coverageJSON, err = stripMetaProfile(coverageJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: strip coverage meta: %w", err)
	}
	// Reference-payer lane: repoint Coverage.payor at the cms-payer Organization ENTRY (added
	// to the bundle below) and drop the contained #cms-payer. This is the load-bearing fix:
	// br-payer's PAS payor lookup follows Coverage.payor → findInBundle (bundle entries only).
	if in.PayerOrgEntry {
		coverageJSON, err = repointPayorToEntry(coverageJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: repoint coverage payor to entry: %w", err)
		}
	}
	// The logical-reference shape: the Claim's insurance[0].coverage becomes a LOGICAL
	// reference to the bundle's own Coverage — the urn:shn:coverage business identifier the
	// Coverage entry above carries — replacing the caller's member-keyed literal, which
	// resolved nowhere. A literal to the bundle ENTRY was tried first and was refuted live at
	// a real Da Vinci RI payer; see setInsuranceCoverageLogicalRef.
	claimJSON, err = setInsuranceCoverageLogicalRef(claimJSON, in.MemberID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: set claim insurance coverage logical ref: %w", err)
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

	// --- Patient: minimal — id only (the bind tolerates a bare Patient; no foreign
	// demographics). resourceType + id is enough for the three-way bind to resolve. ---
	patientID := strings.TrimPrefix(in.PatientRef, "Patient/")
	patientJSON, err := json.Marshal(map[string]string{"resourceType": "Patient", "id": patientID})
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
		qrJSON, err = rewriteQRContextRefs(qrJSON, "Coverage/"+conformantPASCoverageID, srRef)
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
		payerOrgJSON, err = buildPayerOrgResource(in.Payer)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: build payer org entry: %w", err)
		}
	}
	resources := [][]byte{claimJSON, patientJSON, coverageJSON, srJSON}
	if payerOrgJSON != nil {
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
// extension valueReferences so the Coverage-typed qr-context points at coverageRef and
// the ServiceRequest-typed qr-context points at srRef — matching each qr-context
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
		if url != extQRContext {
			continue
		}
		vrRaw, ok := ext["valueReference"]
		if !ok {
			continue
		}
		var vr struct {
			Reference string `json:"reference"`
		}
		if err := json.Unmarshal(vrRaw, &vr); err != nil {
			continue
		}
		var want string
		switch {
		case strings.HasPrefix(vr.Reference, "Coverage/"):
			want = coverageRef
		case strings.HasPrefix(vr.Reference, "ServiceRequest/"):
			want = srRef
		default:
			continue // a qr-context ref we don't own — leave it verbatim
		}
		vrJSON, err := json.Marshal(map[string]string{"reference": want})
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

// buildPayerOrgResource returns the standalone cms-payer Organization JSON (the same identity
// BuildCoverageWithPayer/containInsurer splice as contained, but as a top-level resource for a
// bundle ENTRY). The reference-payer lane lifts the payer org out of contained into an entry because
// br-payer's PAS payor resolution (findInBundle) reads bundle entries only. The identifier
// system|value come from payer; the cosmetic id/name stay conformantPayerOrgID/Name.
func buildPayerOrgResource(payer PayerIdentifier) ([]byte, error) {
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
		return nil, fmt.Errorf("buildPayerOrgResource: marshal: %w", err)
	}
	// fhir.Organization marshals without resourceType; inject it (mirrors containInsurer).
	return pasInjectResourceType(orgJSON, "Organization")
}

// repointPayorToEntry rewrites a reference-payer-lane Coverage so Coverage.payor[0] references the
// cms-payer Organization ENTRY (Organization/cms-payer) instead of the contained #cms-payer, and
// drops the now-redundant contained org. The Organization lives as a bundle entry
// (buildPayerOrgResource); absolutizeBundleRefs then makes the ref the entry's absolute fullUrl so
// br-payer's PAS findInBundle resolves it.
func repointPayorToEntry(coverageJSON []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(coverageJSON, &m); err != nil {
		return nil, fmt.Errorf("repointPayorToEntry: parse coverage: %w", err)
	}
	payorJSON, err := json.Marshal([]map[string]string{{"reference": "Organization/" + conformantPayerOrgID}})
	if err != nil {
		return nil, fmt.Errorf("repointPayorToEntry: marshal payor: %w", err)
	}
	m["payor"] = payorJSON
	if err := dropContainedPayerOrg(m); err != nil {
		return nil, fmt.Errorf("repointPayorToEntry: %w", err)
	}
	return json.Marshal(m)
}

// repointInsurerToEntry rewrites a reference-payer-lane Claim so Claim.insurer references the cms-payer
// Organization ENTRY (Organization/cms-payer), and drops any contained #cms-payer.
func repointInsurerToEntry(claimJSON []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("repointInsurerToEntry: parse claim: %w", err)
	}
	insurerJSON, err := json.Marshal(map[string]string{"reference": "Organization/" + conformantPayerOrgID})
	if err != nil {
		return nil, fmt.Errorf("repointInsurerToEntry: marshal insurer: %w", err)
	}
	m["insurer"] = insurerJSON
	if err := dropContainedPayerOrg(m); err != nil {
		return nil, fmt.Errorf("repointInsurerToEntry: %w", err)
	}
	return json.Marshal(m)
}

// setInsuranceCoverageLogicalRef rewrites a conformant PAS Claim so
// Claim.insurance[0].coverage is a LOGICAL reference — a FHIR Reference carrying ONLY
// {identifier: {system: urn:shn:coverage, value: <bare member id>}}, with no literal
// `reference` at all — regardless of the CoverageRef the caller passed.
//
// WHY NOT the caller's ref, and WHY NOT a literal to the bundle entry. Three shapes were
// tried on this element; only this one is both correct and portable:
//
//  1. The gateway callers' member-keyed literal ("Coverage/<member id>") matched NO bundle
//     entry and is unprocessable-by-construction on ANY receiver: FHIR ids are server-local,
//     so it resolves to nothing in the bundle and to nothing — or, structurally worse, to an
//     UNRELATED subscriber's Coverage — on the payer's own server. Claim.insurance.coverage
//     is min=1 MustSupport on every PAS line (2.0.1/2.1.0/2.2.1), so the receiving payer is
//     entitled to process it. This was the shape ruled out first.
//  2. The next attempt — a literal to the bundle's own Coverage ENTRY
//     ("Coverage/convergence-coverage") — was REFUTED LIVE by the real Da Vinci RI payer
//     (br-payer a8bece4, fresh containers, 7 red smoke-two-ri rows). Naming a bundle entry
//     makes PasSubmitService's findInBundle resolve it and PERSIST that Coverage into HAPI
//     JPA, which then RI-checks its beneficiary — and that beneficiary MUST stay RELATIVE
//     (see absolutizeBundleRefs's exclusion block below; an absolute one collapses every
//     verdict to A3, re-proven live). A relative Patient/<SHN member> does not exist on the
//     payer's server ⇒ HTTP 400 "HAPI-1094: Resource Patient/<member> not found, specified
//     in path: Coverage.beneficiary". Leaving the ref relative rather than absolutized does
//     NOT help: findInBundle matches on Type/id either way (live-proven).
//  3. This shape. Base FHIR R4 explicitly permits a Reference carrying only an identifier,
//     no PAS line puts any invariant on the element beyond base ele-1, and the request
//     bundle is type `collection` (so no bdl-9…12 document/message resolution rule applies).
//     The bundle's own Coverage entry carries exactly this urn:shn:coverage MB identifier
//     (the same bare-member-id business identifier), so the bundle is SELF-CONSISTENT with nothing dangling,
//     and it is portable to any payer, strict or lenient — no receiver is asked to host a
//     resource at an SHN-local id. Live-proven 200/A4 against br-payer and validator-clean
//     (plain AND profile-asserted) against the pinned PAS IG.
//
// Boundary: the element stays a FHIR Reference datatype (its identifier arm), the value
// is the BARE member id (never Coverage/-prefixed), ConformantClaimInputs.CoverageRef stays a
// field for its other roles, and buildPASClaim's signature is untouched — the stamp happens
// bundle-side, exactly as the two payer-org repoints do. Unconditional: the Coverage entry
// always carries this identifier, in every lane.
func setInsuranceCoverageLogicalRef(claimJSON []byte, memberID string) ([]byte, error) {
	if memberID == "" {
		return nil, fmt.Errorf("setInsuranceCoverageLogicalRef: memberID is empty")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("setInsuranceCoverageLogicalRef: parse claim: %w", err)
	}
	// Typed round-trip (not the map surgery the payer-org repoints use): fhir.ClaimInsurance
	// models the element in full, and re-marshalling through it preserves the builders' exact
	// key order for the untouched siblings (sequence, focal).
	var insurance []fhir.ClaimInsurance
	if err := json.Unmarshal(m["insurance"], &insurance); err != nil {
		return nil, fmt.Errorf("setInsuranceCoverageLogicalRef: parse insurance: %w", err)
	}
	if len(insurance) == 0 {
		return nil, fmt.Errorf("setInsuranceCoverageLogicalRef: Claim.insurance is empty")
	}
	// Whole-value replacement, so any literal buildPASClaim stamped from CoverageRef is GONE
	// (fhir.Reference.Reference is omitempty — no `reference` key is emitted).
	insurance[0].Coverage = fhir.Reference{Identifier: &fhir.Identifier{
		System: strPtr(systemSHNCoverage),
		Value:  strPtr(memberID),
	}}
	insuranceJSON, err := json.Marshal(insurance)
	if err != nil {
		return nil, fmt.Errorf("setInsuranceCoverageLogicalRef: marshal insurance: %w", err)
	}
	m["insurance"] = insuranceJSON
	return json.Marshal(m)
}

// dropContainedPayerOrg removes any contained Organization with id == conformantPayerOrgID from
// the resource map's "contained" array (the org lives as a bundle entry instead), deleting the
// array if it becomes empty. No-op when there is no such contained resource.
func dropContainedPayerOrg(m map[string]json.RawMessage) error {
	raw, ok := m["contained"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var contained []json.RawMessage
	if err := json.Unmarshal(raw, &contained); err != nil {
		return fmt.Errorf("parse contained: %w", err)
	}
	kept := make([]json.RawMessage, 0, len(contained))
	for _, c := range contained {
		var probe struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if err := json.Unmarshal(c, &probe); err == nil &&
			probe.ResourceType == "Organization" && probe.ID == conformantPayerOrgID {
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
func buildPASUpdateClaim(patientRef, coverageRef, correlationID, originalCorrelationID string, created time.Time) ([]byte, error) {
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
		Provider: fhir.Reference{Display: strPtr("provider")},
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
func buildPriorClaimEntry(patientRef, coverageRef, originalCorr string, created time.Time) ([]byte, error) {
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
		Provider: fhir.Reference{Display: strPtr("provider")},
		Insurer:  &fhir.Reference{Reference: strPtr("Organization/" + conformantPayerOrgID)},
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
	QR               []byte
	SR               []byte
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
	// MemberID is the bare member id stamped BOTH as the Coverage entry's urn:shn:coverage
	// MB identifier value AND — per the logical-reference shape — as the value of the
	// Claim's insurance[0].coverage LOGICAL reference (see setInsuranceCoverageLogicalRef).
	// CoverageRef is Reference-shaped and stays for the caller's other roles (QR context,
	// the native/minimized lanes); this builder no longer lands it on the wire — it stamps
	// insurance[0].coverage bundle-side from MemberID instead.
	MemberID string
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
	claimJSON, err := buildPASUpdateClaim(in.PatientRef, in.CoverageRef, in.Corr, in.OriginalCorr, in.Created)
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
	switch {
	case in.PayerOrgEntry:
		claimJSON, err = repointInsurerToEntry(claimJSON)
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

	// --- Coverage: identical to the submit builder (bare-member-id urn:shn:coverage
	// identifier value — fail CLOSED on an empty MemberID). ---
	if in.MemberID == "" {
		return nil, fmt.Errorf("shnsdk: MemberID is required (bare member id; see the v0.42.0 identifier-semantics release note)")
	}
	coverageJSON, err := BuildCoverageWithPayer(in.PatientRef, in.MemberID, in.Payer)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: build coverage: %w", err)
	}
	coverageJSON, err = withResourceID(coverageJSON, conformantPASCoverageID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id coverage: %w", err)
	}
	coverageJSON, err = stripMetaProfile(coverageJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: strip coverage meta: %w", err)
	}
	// Reference-payer lane: repoint Coverage.payor at the cms-payer Organization ENTRY (added
	// below) + drop the contained org, so br-payer's PAS update re-evaluation resolves the payor.
	if in.PayerOrgEntry {
		coverageJSON, err = repointPayorToEntry(coverageJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: repoint coverage payor to entry: %w", err)
		}
	}
	// The logical-reference shape, identical to the submit builder — insurance[0].coverage
	// becomes the LOGICAL reference to the bundle's own Coverage (urn:shn:coverage | member).
	claimJSON, err = setInsuranceCoverageLogicalRef(claimJSON, in.MemberID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: set claim insurance coverage logical ref: %w", err)
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

	// --- Patient: minimal — identical to the submit builder. ---
	patientID := strings.TrimPrefix(in.PatientRef, "Patient/")
	patientJSON, err := json.Marshal(map[string]string{"resourceType": "Patient", "id": patientID})
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
	qrJSON, err = rewriteQRContextRefs(qrJSON, "Coverage/"+conformantPASCoverageID, srRef)
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
	entries := make([]fhir.BundleEntry, 0, 8)
	baseResources := [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, qrJSON}
	// Reference-payer lane: add the cms-payer Organization as a resolvable bundle ENTRY so the
	// repointed Coverage.payor/Claim.insurer resolve (br-payer findInBundle, entries only).
	if in.PayerOrgEntry {
		payerOrgJSON, err := buildPayerOrgResource(in.Payer)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: build payer org entry: %w", err)
		}
		baseResources = append(baseResources, payerOrgJSON)
		// The prior Claim as a resolvable bundle ENTRY (NOT first → not profile-validated by
		// PasBundleValidator; carries urn:shn:correlation|OriginalCorr, what br-payer searches the
		// stored authorization on). The operative update Claim's related.reference resolves to it.
		priorClaimJSON, err := buildPriorClaimEntry(in.PatientRef, "Coverage/"+conformantPASCoverageID, in.OriginalCorr, in.Created)
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
	return bundleOut, nil
}

// ParsePendedResponse inspects a PAS submit/update response shape. A Bundle ⇒ PENDED:
// returns pended=true and the typed NeededItems parsed from the Task.input[] (Code =
// the input value, Display = the input type.text). A non-Bundle ⇒ pended=false (the
// caller then parses the bare ClaimResponse via ParseClaimResponse). Mirrors
// internal/pas.ParsePendedOrApproved; the typed NeededItem is the SDK surface.
func ParsePendedResponse(data []byte) (pended bool, needed []NeededItem, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err = json.Unmarshal(data, &probe); err != nil {
		return false, nil, fmt.Errorf("shnsdk: parse PAS response: %w", err)
	}
	if probe.ResourceType != "Bundle" {
		return false, nil, nil
	}
	for _, e := range probe.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
			Input        []struct {
				Type struct {
					Text string `json:"text"`
				} `json:"type"`
				ValueString string `json:"valueString"`
			} `json:"input"`
		}
		if err = json.Unmarshal(e.Resource, &rt); err != nil {
			return false, nil, fmt.Errorf("shnsdk: parse PAS response entry: %w", err)
		}
		if rt.ResourceType == "Task" {
			for _, in := range rt.Input {
				if in.ValueString != "" {
					needed = append(needed, NeededItem{Code: in.ValueString, Display: in.Type.Text})
				}
			}
		}
	}
	return true, needed, nil
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

// ParseClaimResponse parses a bare PAS ClaimResponse into a PriorAuthResult by EXPLICIT
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
// NOTE: a PENDED response is a Bundle, not a bare ClaimResponse — callers detect it with
// ParsePendedResponse FIRST; this function is for the bare-ClaimResponse case.
func ParseClaimResponse(data []byte) (PriorAuthResult, error) {
	var probe struct {
		ResourceType  string `json:"resourceType"`
		Outcome       string `json:"outcome"`
		PreAuthRef    string `json:"preAuthRef"`
		Disposition   string `json:"disposition"`
		PreAuthPeriod *struct {
			End string `json:"end"`
		} `json:"preAuthPeriod"`
		ProcessNote []struct {
			Text string `json:"text"`
		} `json:"processNote"`
		Item []struct {
			Adjudication []struct {
				Extension []struct {
					URL       string `json:"url"`
					Extension []struct {
						URL                  string `json:"url"`
						ValueString          string `json:"valueString"`
						ValueCodeableConcept *struct {
							Coding []struct {
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
	var sawA3, sawA2 bool
	var a3Code, a3Display, a2Code, a2Display string
	for _, it := range probe.Item {
		for _, adj := range it.Adjudication {
			for _, ext := range adj.Extension {
				if ext.URL != reviewActionExtURL {
					continue
				}
				for _, sub := range ext.Extension {
					switch sub.URL {
					case reviewActionCodeExtURL:
						if sub.ValueCodeableConcept == nil {
							continue
						}
						for _, c := range sub.ValueCodeableConcept.Coding {
							switch c.Code {
							case reviewActionDeniedCode: // "A3"
								if !sawA3 {
									sawA3, a3Code, a3Display = true, c.Code, c.Display
								}
							case reviewActionDeniedCodeObservedRI: // "A2"
								if !sawA2 {
									sawA2, a2Code, a2Display = true, c.Code, c.Display
								}
							}
						}
					case "number":
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

	// A3 is SHN's own X12-conformant denial code — unconditional. A "number" sub-extension
	// present alongside it does NOT flip this to approved (see
	// TestParseClaimResponse_DeniedWithNumberStaysDenied): A3 has no "partial" reading in
	// X12 306, so a number riding along with it is not this function's to interpret.
	if sawA3 {
		return PriorAuthResult{
			Outcome: "denied",
			Denial: &Denial{
				ReasonCode: a3Code,
				Rationale:  dispositionText(a3Display),
				AppealNote: processNotes(),
			},
		}, nil
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
			return PriorAuthResult{
				Outcome:     "approved",
				PreAuthRef:  reviewActionPreAuthRef,
				ValidUntil:  validUntil,
				Partial:     true,
				Disposition: dispositionText(a2Display),
			}, nil
		}
		// A2 WITHOUT an auth number: the observed br-payer denial shape — a code/display
		// self-contradiction in that RI (code A2, "Certified – partial", but display "Not
		// Certified"). We parse it as a denial so we can read a real RI's output; SHN
		// itself never emits this.
		return PriorAuthResult{
			Outcome: "denied",
			Denial: &Denial{
				ReasonCode: a2Code,
				Rationale:  dispositionText(a2Display),
				AppealNote: processNotes(),
			},
		}, nil
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
		return PriorAuthResult{Outcome: "approved", PreAuthRef: preAuthRef, ValidUntil: validUntil}, nil
	}

	// Anything else is ambiguous — fail loud rather than guess.
	return PriorAuthResult{}, fmt.Errorf("shnsdk: ClaimResponse is neither approved (no preAuthRef) nor denied (no reviewActionCode A2/A3); ambiguous outcome=%q", probe.Outcome)
}

// parsePASOutcome dispatches a PAS submit/update response on shape: a Bundle ⇒ PENDED
// (Outcome "pended" + NeededItems; the caller fills Resume from its leg context), a
// bare ClaimResponse ⇒ approved/denied (via ParseClaimResponse). Shared by RunPriorAuth
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
