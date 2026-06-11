package shnsdk

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// PriorAuthResult is the prior-auth orchestrator outcome. Outcome is the SAME
// vocabulary the discovery descriptor's expectedPriorAuth speaks.
type PriorAuthResult struct {
	Outcome    string // "approved" | "pended" | "denied" | "no-pa-required"
	PreAuthRef string // set when approved
	ValidUntil string // set when approved

	// NeededItems + Resume are set when Outcome=="pended" (UC-04): the supplemental
	// items the payer's Task enumerates, and a serializable handle to ResumePriorAuth.
	NeededItems []NeededItem
	Resume      *PriorAuthResume

	// Denial is set when Outcome=="denied" (UC-08): the FR-22 denial content.
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

// PriorAuthResume is a SERIALIZABLE handle to resume a pended PA (UC-04). It JSON
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
// submit bundle BuildClaimBundle emits; the X12 1365 service-type coding on
// Claim.item carries the licensed binding target, the actual procedure stays on the
// referenced ServiceRequest — see internal/pas for the binding rationale).
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

// BuildClaimBundle builds the PAS claim Bundle bytes. Reimplements
// internal/pas.BuildClaimBundle standalone; test/sdkparity asserts byte-identity.
// created drives the deterministic Bundle timestamp/ids (inject vecClock for goldens).
//
// The bundle is a FHIR Bundle (type "collection") with EXACTLY three entries —
// Claim (use preauthorization; patient + insurance carried as references INSIDE the
// Claim), the QuestionnaireResponse, and the ServiceRequest.
func BuildClaimBundle(qrJSON, serviceRequestJSON []byte, patientRef, coverageRef, correlationID string, created time.Time) ([]byte, error) {
	// Build the Claim resource (no related[] for initial submit bundles).
	claimJSON, err := buildPASClaim(patientRef, coverageRef, correlationID, created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: build claim: %w", err)
	}

	// Ensure QR and SR carry ids (fallbacks are correlationID-scoped) so that
	// pasFullURLFor can derive resolvable absolute URLs.
	qrJSON, err = pasEnsureID(qrJSON, "qr-"+correlationID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: ensureID qr: %w", err)
	}
	serviceRequestJSON, err = pasEnsureID(serviceRequestJSON, "sr-"+correlationID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: ensureID sr: %w", err)
	}

	// Derive resolvable absolute fullUrls (FHIR bdl-7 / AI-11).
	claimURL, err := pasFullURLFor(claimJSON)
	if err != nil {
		return nil, err
	}
	qrURL, err := pasFullURLFor(qrJSON)
	if err != nil {
		return nil, err
	}
	srURL, err := pasFullURLFor(serviceRequestJSON)
	if err != nil {
		return nil, err
	}

	bundle := fhir.Bundle{
		Type:       fhir.BundleTypeCollection,
		Identifier: &fhir.Identifier{System: strPtr("urn:shn:pas:bundle"), Value: strPtr(correlationID)},
		Timestamp:  strPtr(created.UTC().Format(time.RFC3339)),
		Entry: []fhir.BundleEntry{
			{FullUrl: strPtr(claimURL), Resource: json.RawMessage(claimJSON)},
			{FullUrl: strPtr(qrURL), Resource: json.RawMessage(qrJSON)},
			{FullUrl: strPtr(srURL), Resource: json.RawMessage(serviceRequestJSON)},
		},
	}

	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal bundle: %w", err)
	}
	return pasInjectResourceType(raw, "Bundle")
}

// BuildProvenance builds a Provenance attributing supplemental data to its source
// (FR-32) — the no-policy form UC-04 uses. Reimplements internal/pas.BuildProvenance
// standalone; test/sdkparity asserts byte-identity. recorded is injected (deterministic).
func BuildProvenance(targetRef, agentWho string, recorded time.Time) ([]byte, error) {
	prov := fhir.Provenance{
		Target:   []fhir.Reference{{Reference: strPtr(targetRef)}},
		Recorded: recorded.UTC().Format(time.RFC3339),
		Agent:    []fhir.ProvenanceAgent{{Who: fhir.Reference{Reference: strPtr(agentWho)}}},
	}
	raw, err := json.Marshal(prov)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal Provenance: %w", err)
	}
	return pasInjectResourceType(raw, "Provenance")
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

// BuildClaimUpdateBundle assembles the exchange-2 ClaimUpdate amendment (FR-21): a
// collection Bundle with a Claim (related[] → the original claim by correlation
// identifier) + QR + SR + the supplemental DiagnosticReport + Provenance (FR-32).
// Reimplements internal/pas.BuildClaimUpdateBundle standalone for the UC-04 shape
// (diagnosticReportJSON non-nil); test/sdkparity asserts byte-identity. created drives
// the deterministic Bundle timestamp/ids (inject vecClock for goldens).
func BuildClaimUpdateBundle(qrJSON, srJSON, diagnosticReportJSON, provenanceJSON []byte, patientRef, coverageRef, correlationID, originalCorrelationID string, created time.Time) ([]byte, error) {
	claimJSON, err := buildPASUpdateClaim(patientRef, coverageRef, correlationID, originalCorrelationID, created)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: build update claim: %w", err)
	}
	qrJSON, err = pasEnsureID(qrJSON, "qr-"+correlationID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: ensureID qr: %w", err)
	}
	srJSON, err = pasEnsureID(srJSON, "sr-"+correlationID)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: ensureID sr: %w", err)
	}
	claimURL, err := pasFullURLFor(claimJSON)
	if err != nil {
		return nil, err
	}
	qrURL, err := pasFullURLFor(qrJSON)
	if err != nil {
		return nil, err
	}
	srURL, err := pasFullURLFor(srJSON)
	if err != nil {
		return nil, err
	}
	entries := []fhir.BundleEntry{
		{FullUrl: strPtr(claimURL), Resource: json.RawMessage(claimJSON)},
		{FullUrl: strPtr(qrURL), Resource: json.RawMessage(qrJSON)},
		{FullUrl: strPtr(srURL), Resource: json.RawMessage(srJSON)},
	}
	if diagnosticReportJSON != nil {
		drURL, err := pasFullURLFor(diagnosticReportJSON)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fhir.BundleEntry{
			FullUrl:  strPtr(drURL),
			Resource: json.RawMessage(diagnosticReportJSON),
		})
	}
	if provenanceJSON != nil {
		provJSON, err := pasEnsureID(provenanceJSON, "prov-"+correlationID)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: ensureID prov: %w", err)
		}
		provURL, err := pasFullURLFor(provJSON)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fhir.BundleEntry{
			FullUrl:  strPtr(provURL),
			Resource: json.RawMessage(provJSON),
		})
	}
	bundle := fhir.Bundle{
		Type:       fhir.BundleTypeCollection,
		Identifier: &fhir.Identifier{System: strPtr("urn:shn:pas:bundle"), Value: strPtr(correlationID)},
		Timestamp:  strPtr(created.UTC().Format(time.RFC3339)),
		Entry:      entries,
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal update bundle: %w", err)
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
	for _, it := range probe.Item {
		for _, adj := range it.Adjudication {
			for _, ext := range adj.Extension {
				if ext.URL != reviewActionExtURL {
					continue
				}
				for _, sub := range ext.Extension {
					if sub.URL != reviewActionCodeExtURL || sub.ValueCodeableConcept == nil {
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
				}
			}
		}
	}

	// Approved: explicit preAuthRef + outcome complete.
	if probe.Outcome == "complete" && probe.PreAuthRef != "" {
		validUntil := ""
		if probe.PreAuthPeriod != nil {
			validUntil = probe.PreAuthPeriod.End
		}
		return PriorAuthResult{Outcome: "approved", PreAuthRef: probe.PreAuthRef, ValidUntil: validUntil}, nil
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
