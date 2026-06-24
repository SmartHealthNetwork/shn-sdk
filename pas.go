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
// (reviewActionCode A3 + disposition + processNote). ReasonCode is the PAS
// reviewActionCode (X12 306) — "A3" (Not Certified).
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
	conformantPASCoverageID       = "convergence-coverage"
	conformantPASQRID             = "convergence-qr"

	// Conformant amended re-POST fixed ids (BuildConformantClaimUpdateBundle).
	conformantPASClaimUpdateID = "convergence-claim-update"
	conformantPASUpdateQRID    = "convergence-qr-amended"
	conformantPASDRID          = "convergence-dr-operative"
	conformantPASProvID        = "convergence-prov"

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
type ConformantClaimInputs struct {
	QR          []byte
	SR          []byte
	PatientRef  string
	CoverageRef string
	Corr        string
	Created     time.Time
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
	srRef := "ServiceRequest/" + conformantPASServiceRequestID
	claimJSON, err = conformantizePASClaim(claimJSON, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: conformantize claim: %w", err)
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

	// --- ServiceRequest: the passed demo-persona SR — stamp the conformant id (so the
	// Claim's requestedService + the QR's qr-context resolve to it) + strip meta.profile. ---
	srJSON, err := withResourceID(in.SR, conformantPASServiceRequestID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: id sr: %w", err)
	}
	srJSON, err = stripMetaProfile(srJSON)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: strip sr meta: %w", err)
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
	// a real br-payer. ---
	qrJSON, err := withResourceID(in.QR, conformantPASQRID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: id qr: %w", err)
	}
	qrJSON, err = rewriteQRContextRefs(qrJSON, "Coverage/"+conformantPASCoverageID, srRef)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: conformant submit: rewrite qr-context: %w", err)
	}

	// Derive resolvable absolute fullUrls (FHIR bdl-7 / AI-11), mirror BuildClaimBundle.
	entryFor := func(resourceJSON []byte) (fhir.BundleEntry, error) {
		u, err := pasFullURLFor(resourceJSON)
		if err != nil {
			return fhir.BundleEntry{}, err
		}
		return fhir.BundleEntry{FullUrl: strPtr(u), Resource: json.RawMessage(resourceJSON)}, nil
	}
	entries := make([]fhir.BundleEntry, 0, 5)
	for _, rj := range [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, qrJSON} {
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
	return pasInjectResourceType(raw, "Bundle")
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

// ConformantClaimUpdateInputs are the inputs the conformant amended re-POST builder needs from
// the Originator. QR is the answered amended QuestionnaireResponse; SR is the order
// ServiceRequest; Provenance is REQUIRED (FR-32 — the inbound gate 403s if absent);
// DiagnosticReport is optional (nil on QR-targeted paths). Corr is this amendment's
// correlation; OriginalCorr is the original submit's correlation (→ Claim.related[0]). Created
// drives the deterministic Bundle timestamp/Claim.created. Demo-persona only — no br-payer
// foreign seed.
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
	entries := make([]fhir.BundleEntry, 0, 7)
	for _, rj := range [][]byte{claimJSON, patientJSON, coverageJSON, srJSON, qrJSON} {
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
	return pasInjectResourceType(raw, "Bundle")
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
	// PAS reviewAction extension URLs (mirror internal/pas). A3 = "Not Certified"
	// (denied). The SDK only needs the two extension URLs + the code to PARSE a denial;
	// it does not carry the X12 306 system URL (that's a write-side concern). The denied
	// ClaimResponse's outcome stays "complete" — A3 is the authoritative denial signal,
	// not preAuthRef absence.
	reviewActionExtURL     = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	reviewActionCodeExtURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"
	reviewActionDeniedCode = "A3"
)

// ParseClaimResponse parses a bare PAS ClaimResponse into a PriorAuthResult by EXPLICIT
// signals — approved, denied, and pended are each keyed on an explicit marker:
//   - reviewActionCode == "A3" ⇒ Outcome "denied" + Denial{ReasonCode, Rationale, AppealNote}.
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
								Code string `json:"code"`
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
							if c.Code == reviewActionDeniedCode {
								notes := make([]string, 0, len(probe.ProcessNote))
								for _, n := range probe.ProcessNote {
									if n.Text != "" {
										notes = append(notes, n.Text)
									}
								}
								return PriorAuthResult{
									Outcome: "denied",
									Denial: &Denial{
										ReasonCode: reviewActionDeniedCode,
										Rationale:  probe.Disposition,
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
	return PriorAuthResult{}, fmt.Errorf("shnsdk: ClaimResponse is neither approved (no preAuthRef) nor denied (no reviewActionCode A3); ambiguous outcome=%q", probe.Outcome)
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
