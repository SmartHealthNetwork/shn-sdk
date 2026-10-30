package shnsdk

// The v0.61.1 builders the shn-kit still calls.
//
// This file is the shn-sdk v0.61.1 source of the three builders below, which
// v0.62.0 replaced with Exact builders, copied verbatim. A private helper that the
// v0.62.0 source changed, or that calls one, is carried here with the suffix
// V061, so these builders write exactly the bytes v0.61.1 writes. They write
// values the caller never stated: Claim.type professional, priority normal,
// the item category, location and request type, an amendment's
// certificationType I and infoChanged marker, and the payer
// Organization/payer. The other v0.61.1 builders are removed; these three stay,
// Deprecated, while the shn-kit, which builds against an SDK before the Exact
// builders, still calls them. A later SDK release, named in that release's
// notes, removes this file. Use the Exact builder each one names.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// buildPASClaimV061 constructs a FHIR Claim JSON for a preauthorization request
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
// as it names the insurer, which this Claim leaves unset, via repointInsurerToEntry.
// Nothing conformant reaches the wire carrying this coverage value.
func buildPASClaimV061(patientRef, coverageRef, providerRef, correlationID string, created time.Time) ([]byte, error) {
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

// addPASLineItemDetailV061 appends the PAS 2.1+ Claim.item line-detail — the
// certificationType + requestType extensions and locationCodeableConcept — to
// Claim.item[0], gated on def.ClaimItemLineDetailRequired. No-op (byte-identical)
// when the flag is false (line 2.0). Shared by the submit and update conformant
// builders; called AFTER conformantizePASClaim so it APPENDS to (rather than is
// clobbered by) the existing extension-requestedService slice.
func addPASLineItemDetailV061(claimJSON []byte, def PASDef) ([]byte, error) {
	if !def.ClaimItemLineDetailRequired {
		return claimJSON, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("addPASLineItemDetailV061: parse claim: %w", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(m["item"], &items); err != nil {
		return nil, fmt.Errorf("addPASLineItemDetailV061: parse claim.item: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("addPASLineItemDetailV061: claim has no item")
	}
	var exts []map[string]any
	if raw, ok := items[0]["extension"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &exts); err != nil {
			return nil, fmt.Errorf("addPASLineItemDetailV061: parse item extension: %w", err)
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
		return nil, fmt.Errorf("addPASLineItemDetailV061: marshal extension: %w", err)
	}
	items[0]["extension"] = extJSON
	locJSON, err := json.Marshal(map[string]any{"coding": []map[string]any{{
		"system": pasSystemCMSPlaceOfService, "code": "11",
	}}})
	if err != nil {
		return nil, fmt.Errorf("addPASLineItemDetailV061: marshal location: %w", err)
	}
	items[0]["locationCodeableConcept"] = locJSON
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("addPASLineItemDetailV061: marshal items: %w", err)
	}
	m["item"] = itemsJSON
	return json.Marshal(m)
}

// BuildConformantClaimBundle assembles a LEAN, generic, demo-persona-derived CONFORMANT
// Da Vinci $submit Claim Bundle — the only PA $submit contract (the minimized
// BuildClaimBundle has been removed). The entry set is exactly what the
// payer-side parseConformantPASSubjects (the gateway's native PAS bind) + the SHN-native
// adjudicator + the platform's IG-profile $validate gate require, with NO br-payer foreign seed:
//
//	Claim (use preauthorization; item[].productOrService = the passed order's own code (CPT
//	      72148 for this doc's example demo persona) + extension-requestedService → the
//	      ServiceRequest; insurer → the payer Organization entry),
//	Patient (minimal, id = the bound member),
//	Coverage (the caller's own record, payor → the payer Organization entry,
//	      beneficiary → member),
//	ServiceRequest (the passed SR — CPT 72148, ICD-10 M51.16, for this doc's example),
//	Organization (the requesting provider, the caller's own record),
//	Organization (the payer, the caller's own Insurer record),
//	QuestionnaireResponse (the passed answered QR — id convergence-qr).
//
// meta.profile: the PAS $submit bundle + EVERY entry carry NO meta.profile (a Da
// Vinci profile is an ERROR-severity $validate fail against the US-Core-only validator;
// even the US Core meta.profile on the Coverage/SR is stripped in the PAS context). This
// DIFFERS from the CRD builder, which KEEPS US Core meta.profile. From shn-sdk v0.59.0
// the Claim's insurer is always the caller's own payer record (see
// ConformantClaimInputs.Insurer); without one the builder refuses. Deterministic (no
// time.Now/random); the QR/SR/refs are demo-persona-derived by the caller.
//
// BuildConformantClaimBundle speaks PAS line 2.0. Its other-line form is gone:
// BuildConformantClaimBundleAtLineExact targets 2.1/2.2.
//
// Deprecated: it writes values the caller never stated. Use
// BuildConformantClaimBundleExact, which carries the claim facts you state
// (PASClaimFacts) and writes no default. A later SDK release removes it, named in that release's notes.
func BuildConformantClaimBundle(in ConformantClaimInputs) ([]byte, error) {
	def, _ := PASLineDef("2.0")
	return buildConformantClaimBundleV061(def, in)
}

func buildConformantClaimBundleV061(def PASDef, in ConformantClaimInputs) ([]byte, error) {

	providerRef, providerURL, err := pasProviderEntry(in.Provider)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	claimJSON, err := buildPASClaimV061(in.PatientRef, in.CoverageRef, providerRef, in.Corr, in.Created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: build claim: %w", err)
	}

	orderID, srRef := orderEntryRef(in.SR)
	claimJSON, err = conformantizePASClaim(claimJSON, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: conformantize claim: %w", err)
	}

	claimJSON, err = addPASLineItemDetailV061(claimJSON, def)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: add line item detail: %w", err)
	}

	claimJSON, err = setClaimItemProductFromSR(claimJSON, in.SR)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: set claim product from SR: %w", err)
	}

	payerOrg, err := pasPayerOrgEntry(in.Insurer, in.Payer)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	claimJSON, err = repointInsurerToEntry(claimJSON, payerOrg.id)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: repoint insurer to entry: %w", err)
	}

	if in.InfoChanged {
		claimJSON, err = appendInfoChangedToClaimItemsV061(claimJSON)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: append infoChanged: %w", err)
		}
	}

	claimJSON, err = stampItemTraceNumbers(claimJSON, in.Corr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}

	if in.MemberID == "" {
		return nil, fmt.Errorf("shnsdk: MemberID is required (bare member id; see the v0.42.0 identifier-semantics release note)")
	}
	coverage, err := pasCoverageEntry(in.Coverage, in.PatientRef, payerOrg)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	coverageJSON, coverageRef := coverage.raw, "Coverage/"+coverage.id

	claimJSON, err = setInsuranceCoverageEntryRef(claimJSON, coverageRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}

	srJSON, err := withResourceID(in.SR, orderID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: id order: %w", err)
	}
	srJSON, err = stripMetaProfile(srJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: strip order meta: %w", err)
	}

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
		qrJSON, err = rewriteQRContextRefsV061(qrJSON, coverageRef, srRef)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: conformant submit: rewrite qr-context: %w", err)
		}
	}

	entryFor := func(resourceJSON []byte) (fhir.BundleEntry, error) {
		u, err := pasFullURLFor(resourceJSON)
		if err != nil {
			return fhir.BundleEntry{}, err
		}
		return fhir.BundleEntry{FullUrl: strPtr(u), Resource: json.RawMessage(resourceJSON)}, nil
	}

	if providerURL == pasBundleBaseURL+"/Organization/"+payerOrg.id {
		return nil, fmt.Errorf("shnsdk: conformant submit: the requesting provider and the payer organization are the same bundle entry (%s)", providerRef)
	}
	resources := [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, in.Provider, payerOrg.raw}
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

	if err := checkPASContainedReferenced(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	if err := checkPASInsurerResolves(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	return bundleOut, nil
}

// rewriteQRContextRefsV061 rewrites the QuestionnaireResponse's top-level qr-context
// and DTR 2.2 qr-coverage valueReferences so Coverage points at coverageRef and
// the ServiceRequest- or DeviceRequest-typed qr-context points at srRef — matching each qr-context
// extension by the resourceType PREFIX of its existing valueReference.reference (a ref
// starting "Coverage/" → coverageRef; one starting "ServiceRequest/" → srRef). This makes
// BuildConformantClaimBundle SELF-CONSISTENT: the QR's qr-context refs resolve to the
// bundle-local Coverage/SR regardless of what the caller put in the QR (closing the
// dangling-ref hazard parseConformantPASSubjects does not catch). Other extensions
// (e.g. intendedUse) and all other fields are left verbatim. Deterministic.
func rewriteQRContextRefsV061(qrJSON []byte, coverageRef, srRef string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(qrJSON, &m); err != nil {
		return nil, fmt.Errorf("parse QR: %w", err)
	}
	extRaw, ok := m["extension"]
	if !ok {
		return qrJSON, nil
	}
	var exts []map[string]json.RawMessage
	if err := json.Unmarshal(extRaw, &exts); err != nil {
		return nil, fmt.Errorf("parse QR.extension: %w", err)
	}
	for _, ext := range exts {
		var url string
		if err := json.Unmarshal(ext["url"], &url); err != nil {
			continue
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
			continue
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

// buildPASUpdateClaimV061 constructs the FHIR Claim for an UPDATE bundle: identical to
// buildPASClaimV061 but with related[] referencing the original claim by correlation
// identifier (FR-21). Ported byte-for-byte from the platform's PAS claim builder's related path.
// coverageRef carries the same caveat as buildPASClaimV061's: BuildConformantClaimUpdateBundle
// overwrites insurance[0].coverage with a logical reference before the bundle is assembled.
func buildPASUpdateClaimV061(patientRef, coverageRef, providerRef, correlationID, originalCorrelationID string, created time.Time) ([]byte, error) {
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

// setPriorClaimReferenceV061 repoints Claim.related[0].claim.reference at the prior Claim BUNDLE
// ENTRY every amendment carries (see the call site in buildConformantClaimUpdateBundleV061). br-payer's
// resolvePriorClaim (PasSubmitService.java:379-403) reads .reference, NOT .identifier, and 400s
// "The prior Claim referenced in Claim.related.claim must be included in the Bundle" without it.
// The existing .identifier is preserved. infoChanged is a SEPARATE, unconditional concern (every
// lane) — see appendInfoChangedToClaimItemsV061; this function used to fold both together as
// setPriorClaimReferenceAndInfoChanged, which forced infoChanged to share a lane gate the
// reference no longer has.
//
// Generic-map round-trip, mirroring repointInsurerToEntry.
func setPriorClaimReferenceV061(claimJSON []byte, priorClaimRef string) ([]byte, error) {
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

// appendInfoChangedToClaimItemsV061 appends the Da Vinci PAS infoChanged item extension
// ({"url": pasInfoChangedExtensionURL, "valueCode": "changed"}) to every Claim.item[*] of a
// marshalled Claim JSON. It is the SINGLE source of the exact infoChanged extension shape — the
// UPDATE builder (buildConformantClaimUpdateBundleV061, unconditionally, every lane) and the
// single-shot SUBMIT (BuildConformantClaimBundle InfoChanged) both emit the identical element
// through it, so the gateway's requestClaimHasInfoChanged poll discriminator fires the same way
// for both. Errors when the Claim has no item[]. Generic-map round-trip (mirrors the other
// reference-payer-lane post-processors).
func appendInfoChangedToClaimItemsV061(claimJSON []byte) ([]byte, error) {
	var claim map[string]interface{}
	if err := json.Unmarshal(claimJSON, &claim); err != nil {
		return nil, fmt.Errorf("appendInfoChanged: unmarshal claim: %w", err)
	}
	if err := appendInfoChangedToClaimItemsMapV061(claim); err != nil {
		return nil, err
	}
	out, err := json.Marshal(claim)
	if err != nil {
		return nil, fmt.Errorf("appendInfoChanged: marshal claim: %w", err)
	}
	return out, nil
}

// appendInfoChangedToClaimItemsMapV061 mutates a decoded Claim map in place, appending the infoChanged
// item extension to every item. Factored out of appendInfoChangedToClaimItemsV061 (its only caller) so
// the extension shape has one definition even if a future generic-map caller needs it without a
// marshal round-trip (setPriorClaimReferenceV061 used to be such a caller, before infoChanged became
// an unconditional, separate concern — see buildConformantClaimUpdateBundleV061).
func appendInfoChangedToClaimItemsMapV061(claim map[string]interface{}) error {
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

// buildPriorClaimEntryV061 synthesizes the prior Claim every amendment carries as a resolvable bundle
// ENTRY (see setPriorClaimReferenceV061). It stands for the original submit's claim:
// br-payer's resolvePriorClaim finds it via related[0].claim.reference, then searches the stored
// authorization by its FIRST identifier — so it carries urn:shn:correlation|OriginalCorr (the
// initial submit's stored Claim identifier). br-payer reads only the identifier, but the bundle is
// SHN-produced, so this entry must be a base-FHIR-VALID Claim (FR-36 egress $validate): it carries
// every required Claim element (status/type/use/patient/created/provider/priority/insurance),
// mirroring the conformant submit/update Claim shape. NOT first in the bundle, so PasBundleValidator's
// first-entry profile checks do not apply.
//
// KNOWN SEAM, kept visible rather than hidden: this is a restatement this package synthesizes, not
// the requester's original Claim. Its created is the AMENDMENT's time (the created argument), not
// the time the original was created, and its type and priority restate this builder's own
// constants rather than anything read from the original request. Only the correlation identifier
// is the original's. Until it is replaced by the requester's own prior Claim (or omitted where no
// payer needs it), a reader must not treat this entry's other values as the original's.
func buildPriorClaimEntryV061(patientRef, coverageRef, providerRef, payerOrgID, originalCorr string, created time.Time) ([]byte, error) {
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
// buildPASUpdateClaimV061 helper.
//
// BuildConformantClaimUpdateBundle speaks PAS line 2.0. Its other-line form is
// gone: BuildConformantClaimUpdateBundleAtLineExact targets 2.1/2.2.
//
// Deprecated: it writes values the caller never stated. Use
// BuildConformantClaimUpdateBundleExact, which carries the claim facts,
// certification type and prior Claim you state. A later SDK release removes it, named in that release's notes.
func BuildConformantClaimUpdateBundle(in ConformantClaimUpdateInputs) ([]byte, error) {
	def, _ := PASLineDef("2.0")
	return buildConformantClaimUpdateBundleV061(def, in)
}

func buildConformantClaimUpdateBundleV061(def PASDef, in ConformantClaimUpdateInputs) ([]byte, error) {

	providerRef, providerURL, err := pasProviderEntry(in.Provider)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	claimJSON, err := buildPASUpdateClaimV061(in.PatientRef, in.CoverageRef, providerRef, in.Corr, in.OriginalCorr, in.Created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: build claim: %w", err)
	}
	srRef := "ServiceRequest/" + conformantPASServiceRequestID
	claimJSON, err = conformantizePASClaim(claimJSON, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: conformantize claim: %w", err)
	}

	claimJSON, err = withResourceID(claimJSON, conformantPASClaimUpdateID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id claim update: %w", err)
	}

	claimJSON, err = addPASLineItemDetailV061(claimJSON, def)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: add line item detail: %w", err)
	}
	claimJSON, err = addPASLineRelatedRelationship(claimJSON, def)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: add related relationship: %w", err)
	}

	claimJSON, err = setClaimItemProductFromSR(claimJSON, in.SR)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: set claim product from SR: %w", err)
	}

	payerOrg, err := pasPayerOrgEntry(in.Insurer, in.Payer)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	claimJSON, err = repointInsurerToEntry(claimJSON, payerOrg.id)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: repoint insurer to entry: %w", err)
	}

	claimJSON, err = appendInfoChangedToClaimItemsV061(claimJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: append infoChanged: %w", err)
	}

	traceCorr := in.OriginalCorr
	if traceCorr == "" {
		traceCorr = in.Corr
	}
	claimJSON, err = stampItemTraceNumbers(claimJSON, traceCorr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}

	claimJSON, err = setPriorClaimReferenceV061(claimJSON, "Claim/"+conformantPASClaimID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: prior-claim ref: %w", err)
	}

	if in.MemberID == "" {
		return nil, fmt.Errorf("shnsdk: MemberID is required (bare member id; see the v0.42.0 identifier-semantics release note)")
	}
	coverage, err := pasCoverageEntry(in.Coverage, in.PatientRef, payerOrg)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	coverageJSON, coverageRef := coverage.raw, "Coverage/"+coverage.id
	claimJSON, err = setInsuranceCoverageEntryRef(claimJSON, coverageRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}

	srJSON, err := withResourceID(in.SR, conformantPASServiceRequestID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id sr: %w", err)
	}
	srJSON, err = stripMetaProfile(srJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: strip sr meta: %w", err)
	}

	patientJSON, err := pasMemberPatient(in.PatientRef, in.MemberIDSystem, in.MemberID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: build patient: %w", err)
	}

	qrJSON, err := withResourceID(in.QR, conformantPASUpdateQRID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: id qr: %w", err)
	}
	qrJSON, err = rewriteQRContextRefsV061(qrJSON, coverageRef, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: rewrite qr-context: %w", err)
	}

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

	entryFor := func(resourceJSON []byte) (fhir.BundleEntry, error) {
		u, err := pasFullURLFor(resourceJSON)
		if err != nil {
			return fhir.BundleEntry{}, err
		}
		return fhir.BundleEntry{FullUrl: strPtr(u), Resource: json.RawMessage(resourceJSON)}, nil
	}
	entries := make([]fhir.BundleEntry, 0, 9)

	baseResources := [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, qrJSON, in.Provider}

	if providerURL == pasBundleBaseURL+"/Organization/"+payerOrg.id {
		return nil, fmt.Errorf("shnsdk: conformant update: the requesting provider and the payer organization are the same bundle entry (%s)", providerRef)
	}
	baseResources = append(baseResources, payerOrg.raw)

	priorClaimJSON, err := buildPriorClaimEntryV061(in.PatientRef, coverageRef, providerRef, payerOrg.id, in.OriginalCorr, in.Created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: build prior claim entry: %w", err)
	}
	baseResources = append(baseResources, priorClaimJSON)
	for _, rj := range baseResources {
		e, err := entryFor(rj)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}

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

	if err := checkPASContainedReferenced(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	if err := checkPASInsurerResolves(bundleOut); err != nil {
		return nil, fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	return bundleOut, nil
}

type pasDeniedCRV061 struct {
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

// BuildDeniedResponse builds the Da Vinci PAS denied ClaimResponse (FR-22).
// The rationale is the human-readable disposition, carried exactly; an empty
// rationale emits no disposition. It carries no processNote (the Exact
// builders carry the partner's own notes). No preAuthRef is issued. Outcome is "complete" — denial is a decision, not an
// error.
//
// BuildDeniedResponse speaks PAS line 2.0.
//
// Deprecated: it writes values the caller never stated. Use
// BuildDeniedResponseExact, which restates the request Claim's own type and
// insurer (PASAnswerRequest). A later SDK release removes it, named in that release's notes.
func BuildDeniedResponse(patientRef, correlationID, rationale string, created time.Time) ([]byte, error) {
	def, _ := PASLineDef("2.0")
	return buildDeniedResponseV061(def, patientRef, correlationID, rationale, nil, created)
}

func buildDeniedResponseV061(def PASDef, patientRef, correlationID, rationale string, notes []PASProcessNote, created time.Time) ([]byte, error) {
	processNotes, err := pasDeniedNotes(notes)
	if err != nil {
		return nil, err
	}
	cr := pasDeniedCRV061{
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
