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
	Outcome    string // "approved" | "pended" | "denied" | "no-pa-required"
	PreAuthRef string // set when approved
	ValidUntil string // set when approved

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
// reviewActionCode (X12 306) — "A2" (Not Certified) from a conformant payer like
// br-payer, or the legacy "A3" SHN's sandbox still emits.
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

// pasEnsureID returns resourceJSON with "id" set to fallbackID if the JSON does
// not already carry a non-empty id. Used by bundle builders to guarantee every
// entry has a stable, resolvable id before computing its fullUrl. Ported standalone
// from internal/pas.ensureID.
func pasEnsureID(resourceJSON []byte, fallbackID string) ([]byte, error) {
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resourceJSON, &probe); err != nil {
		return nil, fmt.Errorf("shnsdk: ensureID: parse: %w", err)
	}
	if probe.ID != "" {
		return resourceJSON, nil // already has an id
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(resourceJSON, &m); err != nil {
		return nil, fmt.Errorf("shnsdk: ensureID: unmarshal map: %w", err)
	}
	idJSON, _ := json.Marshal(fallbackID)
	m["id"] = json.RawMessage(idJSON)
	return json.Marshal(m)
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
)

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
// "Organization/payer" — byte-identical to the sandbox-proven path. Set true ONLY for the
// composite origination lane (OriginationProfile == "composite").
//
// AbsoluteRefs: when true every internal reference whose value matches a bundle-entry
// relative form ("<resourceType>/<id>") is rewritten to its absolute fullUrl
// (pasBundleBaseURL + "/" + "<resourceType>/<id>"). This makes the bundle self-consistent
// for real payers (e.g. real br-payer HAPI-1094 "not found") that do not resolve relative
// refs against absolute entry fullUrls in a $submit collection bundle. Contained #fragment
// refs and refs that do not match any bundle entry are left untouched. When false (the
// default) the bundle is byte-identical to the sandbox-proven path. Set true ONLY for the
// composite origination lane (OriginationProfile == "composite").
type ConformantClaimInputs struct {
	QR               []byte
	SR               []byte
	PatientRef       string
	CoverageRef      string
	Corr             string
	Created          time.Time
	ContainedInsurer bool // composite lane only; false = byte-identical sandbox path
	AbsoluteRefs     bool // composite lane only; false = byte-identical sandbox path
	// PayerOrgEntry (composite lane): emit the cms-payer Organization as a resolvable bundle
	// ENTRY (not contained) and repoint Coverage.payor + Claim.insurer at it. REQUIRED for a
	// real Da Vinci PAS payer (br-payer): its PAS payor resolution (PayorIdentifierUtil →
	// ResourceResolver.findInBundle) reads bundle ENTRIES only, so a contained #cms-payer
	// yields 0 payor identifiers → empty PlanDefinition search → A3 "Not Required" for every
	// code (the verdict CQL never fires). CRD is unaffected (it resolves contained fragments).
	// Takes precedence over ContainedInsurer when both set. Default false = sandbox byte-identical.
	PayerOrgEntry bool
	// InfoChanged (single-shot resolve discriminator): when true the submit Claim's item[*]
	// carries the Da Vinci PAS infoChanged item extension ({"url": pasInfoChangedExtensionURL,
	// "valueCode": "changed"} — the SAME shape setPriorClaimReferenceAndInfoChanged appends on the
	// composite UPDATE Claim). It is the gateway payer-side POLL DISCRIMINATOR, not a verdict input:
	// the payer gate polls the timer-resolved terminal A1 (GET ClaimResponse/{id}) when the order is
	// a single-shot ServiceRequest signalling "resolve to terminal" via this extension, instead of
	// returning the A4 pend for a composite amendment leg. On a FRESH submit (no Claim.related[prior],
	// which this builder never emits) infoChanged is benign on br-payer — its re-evaluation path is
	// gated on a prior claim, absent here — so br-payer still does A4→timer→A1. Default false →
	// byte-identical to every existing caller. NO prior-claim ref is added (this is a submit, not an
	// update).
	InfoChanged bool
}

// BuildConformantClaimBundle assembles a LEAN, generic, demo-persona-derived CONFORMANT
// Da Vinci $submit Claim Bundle — the only PA $submit contract (the minimized
// BuildClaimBundle has been removed). The entry set is exactly what the
// payer-side parseConformantPASSubjects (gateway/engine/pas_native.go) + the sandbox
// adjudicator + `make validate` require, with NO br-payer foreign seed:
//
//	Claim (use preauthorization; item[].productOrService = CPT 72148 + extension-requestedService
//	      → the ServiceRequest; insurer = generic Organization/payer),
//	Patient (minimal, id = the bound member),
//	Coverage (contained cms-payer Org, payor → #cms-payer, beneficiary → member),
//	ServiceRequest (the passed SR — CPT 72148, ICD-10 M51.16),
//	QuestionnaireResponse (the passed answered QR — id convergence-qr).
//
// meta.profile: the PAS $submit bundle + EVERY entry carry NO meta.profile (a Da
// Vinci profile is an ERROR-severity $validate fail against the US-Core-only validator;
// even the US Core meta.profile on the Coverage/SR is stripped in the PAS context). This
// DIFFERS from the CRD builder, which KEEPS US Core meta.profile. The Claim's insurer stays
// the generic Organization/payer (NOT a named br-payer insurer). Deterministic (no
// time.Now/random); the QR/SR/refs are demo-persona-derived by the caller.
func BuildConformantClaimBundle(in ConformantClaimInputs) ([]byte, error) {
	// --- Claim: reuse the byte-parity-locked buildPASClaim, then post-process to carry
	// the conformant CPT 72148 (buildPASClaim natively emits X12 1365 "Medical Care") +
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
	// Composite lane: override the hardcoded CPT 72148 with the order's actual code (the
	// composite HCPCS code, e.g. L8000 or E0431) — br-payer keys PAS on Claim.item.productOrService.
	if in.PayerOrgEntry {
		claimJSON, err = setClaimItemProductFromSR(claimJSON, in.SR)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: set claim product from SR: %w", err)
		}
	}
	// Composite lane: make the Claim insurer ref resolvable. PayerOrgEntry is the
	// br-payer-correct form — point insurer at the cms-payer Organization ENTRY added below
	// (br-payer's PAS payor resolution reads bundle entries, not contained), and it takes
	// precedence over the legacy ContainedInsurer (contained #cms-payer) approach. Both default
	// false → the sandbox path stays byte-identical.
	switch {
	case in.PayerOrgEntry:
		claimJSON, err = repointInsurerToEntry(claimJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: repoint insurer to entry: %w", err)
		}
	case in.ContainedInsurer:
		claimJSON, err = containInsurer(claimJSON)
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
	// to the bundle-local conformant id, and STRIP meta.profile (PAS context). ---
	coverageJSON, err := BuildCoverageWithPayer(in.PatientRef, in.CoverageRef)
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
	// Composite lane: repoint Coverage.payor at the cms-payer Organization ENTRY (added
	// to the bundle below) and drop the contained #cms-payer. This is the load-bearing fix:
	// br-payer's PAS payor lookup follows Coverage.payor → findInBundle (bundle entries only).
	if in.PayerOrgEntry {
		coverageJSON, err = repointPayorToEntry(coverageJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: repoint coverage payor to entry: %w", err)
		}
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
	// Composite lane: the cms-payer Organization is a first-class bundle ENTRY (the
	// payor refs above resolve to it). Build it here so entryFor stamps its absolute fullUrl,
	// which absolutizeBundleRefs (when AbsoluteRefs) makes Coverage.payor/Claim.insurer match.
	var payerOrgJSON []byte
	if in.PayerOrgEntry {
		payerOrgJSON, err = buildPayerOrgResource()
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
	// Composite lane only: rewrite internal refs to their absolute fullUrl form so real
	// payers that do not resolve relative refs against absolute entry fullUrls accept the
	// bundle (HAPI-1094). Default false keeps the sandbox path byte-identical.
	if in.AbsoluteRefs {
		bundleOut, err = absolutizeBundleRefs(bundleOut)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: absolutize refs: %w", err)
		}
	}
	return bundleOut, nil
}

// conformantizePASClaim takes buildPASClaim's output and (1) overrides item[0].
// productOrService to the conformant CPT 72148 (buildPASClaim natively puts X12 1365
// "Medical Care" there — see buildPASClaim's comment), (2) adds the Da Vinci PAS
// extension-requestedService → the ServiceRequest on item[0], and (3) restamps the id to
// the stable conformant id. The Claim's category (X12 1365), insurer (generic
// Organization/payer), and all other fields stay buildPASClaim's. Deterministic.
func conformantizePASClaim(claimJSON []byte, serviceRequestRef string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("parse claim: %w", err)
	}
	// Restamp id.
	idJSON, _ := json.Marshal(conformantPASClaimID)
	m["id"] = idJSON

	// Override item[0].productOrService → CPT 72148 + add extension-requestedService.
	// Guard the missing-item case BEFORE unmarshal so a nil m["item"] yields a
	// self-explanatory error rather than the opaque "unexpected end of JSON input"
	// EOF (unreachable in practice — buildPASClaim always emits an item — but a
	// public-SDK robustness nicety).
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
	cpt := fhir.CodeableConcept{Coding: []fhir.Coding{{
		System:  strPtr(systemCPT),
		Code:    strPtr("72148"),
		Display: strPtr("MRI lumbar spine w/o contrast"),
	}}}
	cptJSON, err := json.Marshal(cpt)
	if err != nil {
		return nil, err
	}
	items[0]["productOrService"] = cptJSON
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
// Mirrors BuildCoverageWithPayer's contained-org splice: the same four constants
// (conformantPayerOrgID, conformantPayerOrgName, systemCMSPayerID, conformantPayerOrgValue)
// produce an identical Organization shape, ensuring the Claim's contained payer and the
// Coverage's contained payer are consistent.
//
// If the Claim already has a "contained" array (not typical) the new org is appended.
// Every other field is left verbatim. Deterministic.
func containInsurer(claimJSON []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("containInsurer: parse claim: %w", err)
	}

	// Build the contained Organization (identical shape to BuildCoverageWithPayer's org).
	org := fhir.Organization{
		Id:   strPtr(conformantPayerOrgID),
		Name: strPtr(conformantPayerOrgName),
		Identifier: []fhir.Identifier{{
			System: strPtr(systemCMSPayerID),
			Value:  strPtr(conformantPayerOrgValue),
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
// requested-service code. conformantizePASClaim hardcodes CPT 72148 (the sandbox lumbar code)
// on the Claim item, but br-payer's PAS keys the PlanDefinition lookup on
// Claim.item.productOrService (PasSubmitService.evaluateAllItems — NOT the SR / requestedService
// extension). The composite lane originates HCPCS codes (e.g. L8000) on the SR, so the Claim item
// MUST carry the same code or br-payer adjudicates the wrong PlanDefinition.
//
// Order-type-aware: for a DeviceRequest the code lives in codeCodeableConcept; for a
// ServiceRequest (and any unrecognised type) it lives in code. The extension-requestedService
// (added by conformantizePASClaim) is preserved. Composite-only; the sandbox SR is also CPT
// 72148 so this would be a no-op there, but it is gated on the composite flag to keep the
// sandbox bytes provably identical.
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
// bundle ENTRY). The composite lane lifts the payer org out of contained into an entry because
// br-payer's PAS payor resolution (findInBundle) reads bundle entries only.
func buildPayerOrgResource() ([]byte, error) {
	org := fhir.Organization{
		Id:   strPtr(conformantPayerOrgID),
		Name: strPtr(conformantPayerOrgName),
		Identifier: []fhir.Identifier{{
			System: strPtr(systemCMSPayerID),
			Value:  strPtr(conformantPayerOrgValue),
		}},
	}
	orgJSON, err := json.Marshal(org)
	if err != nil {
		return nil, fmt.Errorf("buildPayerOrgResource: marshal: %w", err)
	}
	// fhir.Organization marshals without resourceType; inject it (mirrors containInsurer).
	return pasInjectResourceType(orgJSON, "Organization")
}

// repointPayorToEntry rewrites a composite-lane Coverage so Coverage.payor[0] references the
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

// repointInsurerToEntry rewrites a composite-lane Claim so Claim.insurer references the cms-payer
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
// Pure function — input is not mutated. Re-marshal determinism is fine (composite bundles
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
	// PasCoverageEvaluator falls through to A3 "Not Required" (live-proven vs br-payer a8bece4:
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

// setPriorClaimReferenceAndInfoChanged makes the operative update Claim a CONFORMANT Da Vinci PAS
// Claim Update that real br-payer ACCEPTS (composite lane only). Two changes:
//   - Claim.related[0].claim.reference = priorClaimRef (the prior Claim BUNDLE ENTRY). br-payer's
//     resolvePriorClaim (PasSubmitService.java:379-403) reads .reference, NOT .identifier, and 400s
//     "The prior Claim referenced in Claim.related.claim must be included in the Bundle" without it.
//     The existing .identifier is preserved.
//   - Claim.item[*].extension += infoChanged ("changed"). br-payer re-evaluates an updated item only
//     when it carries infoChanged (hasInfoChanged, PasSubmitService.java:316/449); otherwise it
//     carries-forward the prior decision unchanged.
//
// Generic-map round-trip (composite has no byte-parity golden), mirroring containInsurer/
// repointInsurerToEntry. Out of the sandbox path (gated on PayerOrgEntry at the call site).
func setPriorClaimReferenceAndInfoChanged(claimJSON []byte, priorClaimRef string) ([]byte, error) {
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

	if err := appendInfoChangedToClaimItemsMap(claim); err != nil {
		return nil, err
	}
	out, err := json.Marshal(claim)
	if err != nil {
		return nil, fmt.Errorf("marshal claim: %w", err)
	}
	return out, nil
}

// appendInfoChangedToClaimItems appends the Da Vinci PAS infoChanged item extension
// ({"url": pasInfoChangedExtensionURL, "valueCode": "changed"}) to every Claim.item[*] of a
// marshalled Claim JSON. It is the SINGLE source of the exact infoChanged extension shape — both
// the composite UPDATE (setPriorClaimReferenceAndInfoChanged) and the single-shot SUBMIT
// (BuildConformantClaimBundle InfoChanged) emit the identical element through it, so the gateway's
// requestClaimHasInfoChanged poll discriminator fires the same way for both. Errors when the Claim
// has no item[]. Generic-map round-trip (mirrors the other composite-lane post-processors).
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
// item extension to every item. Shared by the marshalled-bytes wrapper (appendInfoChangedToClaimItems,
// the submit path) and setPriorClaimReferenceAndInfoChanged (the update path) so the extension shape
// is defined once.
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
// composite lane (see setPriorClaimReferenceAndInfoChanged). It is the original submit's claim:
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
// the composite lane so the update Claim's insurer is also resolvable.
//
// AbsoluteRefs: same semantics as ConformantClaimInputs.AbsoluteRefs — set true for the
// composite lane so update bundle internal refs are absolute. Out-of-bundle refs (e.g.
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
	ContainedInsurer bool // composite lane only; false = byte-identical sandbox path
	AbsoluteRefs     bool // composite lane only; false = byte-identical sandbox path
	// PayerOrgEntry: same semantics as ConformantClaimInputs.PayerOrgEntry — the cms-payer
	// Organization is a resolvable bundle ENTRY (not contained) so br-payer's PAS re-evaluation
	// of the update resolves the payor (findInBundle, entries only). Composite-only; precedence
	// over ContainedInsurer.
	PayerOrgEntry bool
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
func BuildConformantClaimUpdateBundle(in ConformantClaimUpdateInputs) ([]byte, error) {
	// --- Claim: reuse buildPASUpdateClaim (emits related[] by OriginalCorr), then
	// conformantize (CPT 72148 + extension-requestedService) and restamp id to the
	// update-specific conformant id. ---
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
	// Composite lane: override the hardcoded CPT 72148 with the SR's actual code (the
	// composite HCPCS code) — br-payer keys PAS on Claim.item.productOrService.
	if in.PayerOrgEntry {
		claimJSON, err = setClaimItemProductFromSR(claimJSON, in.SR)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: set claim product from SR: %w", err)
		}
	}
	// Composite lane: PayerOrgEntry — insurer references the payer org ENTRY; takes
	// precedence over the legacy contained-insurer splice. Same as BuildConformantClaimBundle.
	switch {
	case in.PayerOrgEntry:
		claimJSON, err = repointInsurerToEntry(claimJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: repoint insurer to entry: %w", err)
		}
	case in.ContainedInsurer:
		claimJSON, err = containInsurer(claimJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: contain insurer: %w", err)
		}
	}
	// Composite lane: make the amended re-POST a CONFORMANT Da Vinci PAS Claim Update that
	// real br-payer ACCEPTS — Claim.related[0].claim.reference to the prior Claim ENTRY (added to
	// the bundle below) + infoChanged on the item. br-payer's resolvePriorClaim
	// (PasSubmitService.java:379-403) reads .reference (NOT .identifier) and requires the prior
	// Claim in-bundle (else HTTP 400 "The prior Claim referenced in Claim.related.claim must be
	// included in the Bundle"); hasInfoChanged (:316/449) gates re-evaluation. The relative ref is
	// absolutized to the entry's fullUrl by absolutizeBundleRefs (AbsoluteRefs) — what findInBundle
	// keys on. Sandbox path keeps the lean identifier-only related (byte-identical to golden).
	if in.PayerOrgEntry {
		claimJSON, err = setPriorClaimReferenceAndInfoChanged(claimJSON, "Claim/"+conformantPASClaimID)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: prior-claim ref + infoChanged: %w", err)
		}
	}

	// --- Coverage: identical to the submit builder. ---
	coverageJSON, err := BuildCoverageWithPayer(in.PatientRef, in.CoverageRef)
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
	// Composite lane: repoint Coverage.payor at the cms-payer Organization ENTRY (added
	// below) + drop the contained org, so br-payer's PAS update re-evaluation resolves the payor.
	if in.PayerOrgEntry {
		coverageJSON, err = repointPayorToEntry(coverageJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant update: repoint coverage payor to entry: %w", err)
		}
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
	// Composite lane: add the cms-payer Organization as a resolvable bundle ENTRY so the
	// repointed Coverage.payor/Claim.insurer resolve (br-payer findInBundle, entries only).
	if in.PayerOrgEntry {
		payerOrgJSON, err := buildPayerOrgResource()
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
	// Composite lane only: same absolute-ref rewrite as BuildConformantClaimBundle.
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
	// system (https://codesystem.x12.org/005010/306) defines A2 = "Not Certified" (the
	// DENIAL) and A3 = "Not Required" (no PA needed — NOT a denial). A real Da Vinci PAS
	// payer (br-payer a8bece4) denies with A2. SHN's own sandbox producer historically
	// emits A3 for its denials (a non-conformant legacy alias kept transitional here so the
	// sandbox roundtrip and goldens stay green); the full A2/A3 reconciliation — make SHN
	// EMIT A2 and demote A3 to its true "Not Required"/no-PA meaning — is a follow-up
	// conformance slice (D-S2-5). The SDK only needs the two extension URLs + the code to
	// PARSE a denial; the denied ClaimResponse's outcome stays "complete" — the
	// reviewActionCode is the authoritative denial signal, not preAuthRef absence.
	reviewActionExtURL     = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	reviewActionCodeExtURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"
	reviewActionDeniedCode = "A2" // X12 "Not Certified" — the conformant denial code (br-payer)
	// reviewActionDeniedCodeLegacy is SHN's own non-conformant sandbox denial code, accepted
	// as a transitional alias until the D-S2-5 reconciliation flips the producer to A2.
	reviewActionDeniedCodeLegacy = "A3"
)

// isReviewActionDenied reports whether an X12 reviewActionCode signals a denial: A2 ("Not
// Certified", the conformant code br-payer emits) or the legacy A3 SHN's sandbox still emits.
func isReviewActionDenied(code string) bool {
	return code == reviewActionDeniedCode || code == reviewActionDeniedCodeLegacy
}

// ParseClaimResponse parses a bare PAS ClaimResponse into a PriorAuthResult by EXPLICIT
// signals — approved, denied, and pended are each keyed on an explicit marker:
//   - reviewActionCode == "A2" (X12 "Not Certified", the conformant denial; or the legacy
//     sandbox alias "A3") ⇒ Outcome "denied" + Denial{ReasonCode, Rationale, AppealNote}.
//   - non-empty preAuthRef AND outcome "complete" ⇒ Outcome "approved" + PreAuthRef + ValidUntil.
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

	// Denial: navigate item[].adjudication[].extension[reviewAction].extension[reviewActionCode]
	// .valueCodeableConcept.coding[].code == "A3".
	// Also collect the "number" sub-extension (real Da Vinci RI preAuthRef placement):
	// item[].adjudication[].extension[reviewAction].extension[url="number"].valueString.
	var reviewActionPreAuthRef string
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
							if isReviewActionDenied(c.Code) {
								notes := make([]string, 0, len(probe.ProcessNote))
								for _, n := range probe.ProcessNote {
									if n.Text != "" {
										notes = append(notes, n.Text)
									}
								}
								// Rationale = the payer's disposition; fall back to the
								// reviewActionCode display ("Not Certified") when absent — a
								// conformant payer (br-payer) carries no disposition/processNote on
								// a coverage-exclusion A2, so the reviewAction display is its only
								// denial text. A denial always surfaces SOME payer-sourced reason.
								rationale := probe.Disposition
								if rationale == "" {
									rationale = c.Display
								}
								return PriorAuthResult{
									Outcome: "denied",
									Denial: &Denial{
										ReasonCode: c.Code, // the ACTUAL review code (A2 br-payer / A3 sandbox legacy)
										Rationale:  rationale,
										AppealNote: notes,
									},
								}, nil
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
