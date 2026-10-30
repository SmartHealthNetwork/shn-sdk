package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

const (
	// systemLOINC is the LOINC code system URI.
	// Mirrors the platform's FHIR builders' LOINC system.
	systemLOINC = "http://loinc.org"

	// systemUSCoreDocClass is the US Core DocumentReference category code system.
	// Mirrors the platform's DocumentReference builder's category system.
	systemUSCoreDocClass = "http://hl7.org/fhir/us/core/CodeSystem/us-core-documentreference-category"

	// docRefDateUC05 is the fixed instant for the operative DocumentReference
	// disclosed by the external facility (the date of the network's synthetic operative
	// report, 2026-05-15). FHIR DocumentReference.date is an instant
	// type, so a full ISO 8601 timestamp is required. recordClinicalDate truncates to
	// YYYY-MM-DD for InRange comparison (FR-24). Fixed value keeps the golden
	// byte-deterministic (FR-35/FR-39). Mirrors the platform's DocumentReference builder's date.
	docRefDateUC05 = "2026-05-15T00:00:00Z"

	// X12ReviewDecisionSystem is the X12 306 review decision code system (for
	// example A1 Certified in total, A3 Not Certified) a payer's PAS review
	// action is coded in (PASReviewAction).
	X12ReviewDecisionSystem = "https://codesystem.x12.org/005010/306"
	// X12ReviewDecisionReasonSystem is the X12 886 review decision reason code
	// system a review action's reasons are coded in.
	X12ReviewDecisionReasonSystem = "https://codesystem.x12.org/external/886"
	// CARCSystem is the X12 Claim Adjustment Reason Code system and
	// RARCSystem the CMS Remittance Advice Remark Code system: the codes a
	// payer's PAS item adjudication may carry as its denial reasons
	// (PriorAuthResult.DenialReasons).
	CARCSystem = "https://x12.org/codes/claim-adjustment-reason-codes"
	RARCSystem = "https://x12.org/codes/remittance-advice-remark-codes"
)

// BuildEligibilityRequestExact constructs a FHIR R4 CoverageEligibilityRequest for the
// member, ordering NPI, and date of service (now). Returns FHIR JSON. It names
// the payer by identifier, which its v0.61.1 form did not.
//
// payer is the payer the member's own Coverage names (its payor's identifier),
// the one the request is routed to. The request's insurer names it as a
// logical reference (Reference.identifier). Without one the builder refuses
// (ErrPayerRecordAbsent): it never writes a placeholder (through shn-sdk
// v0.61.x the insurer was the literal Organization/payer, which named no
// resource and no participant's record).
func BuildEligibilityRequestExact(memberID, npi string, payer PayerIdentifier, now time.Time) ([]byte, error) {
	if !payer.named() {
		return nil, fmt.Errorf("shnsdk: eligibility request insurer: %w", ErrPayerRecordAbsent)
	}
	created := now.UTC().Format(time.RFC3339)
	patientRef := "Patient/" + memberID
	providerRef := "Practitioner/" + npi
	cer := fhir.CoverageEligibilityRequest{
		Status:   fhir.FinancialResourceStatusCodesActive,
		Purpose:  []fhir.EligibilityRequestPurpose{fhir.EligibilityRequestPurposeBenefits},
		Patient:  fhir.Reference{Reference: strPtr(patientRef)},
		Created:  created,
		Insurer:  payer.reference(),
		Provider: &fhir.Reference{Reference: strPtr(providerRef)},
	}
	return json.Marshal(cer)
}

// ParseEligibilityResponse returns whether coverage is in force and, if not, the
// reason carried in the disposition of a CoverageEligibilityResponse. PORTED
// standalone from the platform's eligibility-response parser; the platform's
// parity test proves it agrees with that parser on a platform-built response
// (both covered and not-covered branches).
func ParseEligibilityResponse(b []byte) (covered bool, reason string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err = json.Unmarshal(b, &probe); err != nil {
		return false, "", err
	}
	if probe.ResourceType != "CoverageEligibilityResponse" {
		return false, "", fmt.Errorf("shnsdk: expected CoverageEligibilityResponse, got %q", probe.ResourceType)
	}
	var resp fhir.CoverageEligibilityResponse
	if err = json.Unmarshal(b, &resp); err != nil {
		return false, "", err
	}
	if len(resp.Insurance) > 0 && resp.Insurance[0].Inforce != nil {
		covered = *resp.Insurance[0].Inforce
	}
	if !covered && resp.Disposition != nil {
		reason = *resp.Disposition
	}
	return covered, reason, nil
}

// BuildEligibilityResponseExact constructs a FHIR R4 CoverageEligibilityResponse for the
// payer side of a coverage-eligibility check. When covered, insurance is in force;
// when not covered, inforce is false and reason is carried in the disposition.
// It refuses an insurer without both a system and a value, which its v0.61.1
// form did not.
//
// insurer names the member's real Coverage payor — the promoted engine handler
// derives it from SoR.OpenCoverage via shnsdk.ParsePayerIdentifier, one Coverage
// read feeding both the verdict and the identity. It is stamped as a LOGICAL reference
// (Reference.identifier), the standard FHIR idiom for an identifier known but not locally
// resolvable as a URL. Without one the builder refuses (ErrPayerRecordAbsent): through
// shn-sdk v0.61.x a zero insurer wrote the placeholder Organization/payer, which named no
// resource and no participant's record.
//
// The "req-" prefix on the request reference is applied here so callers pass only the
// bare correlationID. created is supplied by the caller so the resource is
// byte-deterministic for a given clock.
func BuildEligibilityResponseExact(correlationID, patientRef string, covered bool, reason string, insurer PayerIdentifier, created time.Time) ([]byte, error) {
	if !insurer.named() {
		return nil, fmt.Errorf("shnsdk: eligibility response insurer: %w", ErrPayerRecordAbsent)
	}
	return buildEligibilityResponse(correlationID, patientRef, covered, reason, insurer.reference(), created)
}

// BuildEligibilityAnswer is BuildEligibilityResponseExact for an answer that names the
// payer its request named: requestInsurer is the request's insurer Reference as
// written (ParseEligibilityRequestInsurer), in whatever form it takes, a literal
// reference included, and it is restated JSON-equal, every member carried (a
// primitive's extension and a member the FHIR model does not know included).
// Absent, null or empty, the builder refuses (ErrPayerRecordAbsent) rather than
// name a payer of its own.
func BuildEligibilityAnswer(correlationID, patientRef string, covered bool, reason string, requestInsurer json.RawMessage, created time.Time) ([]byte, error) {
	if !isJSONObjectWithMembers(requestInsurer) {
		return nil, fmt.Errorf("shnsdk: eligibility answer insurer, from the request it answers: %w", ErrPayerRecordAbsent)
	}
	// The request's insurer is carried as raw JSON, never decoded into the FHIR
	// model: the model has no place for a primitive's extension (_reference,
	// _display) or a member it does not know, and would drop it. This field takes
	// the base's place in the encoding (the outer field wins).
	type answerBase fhir.CoverageEligibilityResponse
	answer := struct {
		answerBase
		Insurer json.RawMessage `json:"insurer"`
	}{answerBase: answerBase(eligibilityResponse(correlationID, patientRef, covered, reason, fhir.Reference{}, created)), Insurer: requestInsurer}
	raw, err := json.Marshal(answer)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal eligibility answer: %w", err)
	}
	return pasInjectResourceType(raw, "CoverageEligibilityResponse")
}

func buildEligibilityResponse(correlationID, patientRef string, covered bool, reason string, insurerRef fhir.Reference, created time.Time) ([]byte, error) {
	return json.Marshal(eligibilityResponse(correlationID, patientRef, covered, reason, insurerRef, created))
}

func eligibilityResponse(correlationID, patientRef string, covered bool, reason string, insurerRef fhir.Reference, created time.Time) fhir.CoverageEligibilityResponse {
	member := strings.TrimPrefix(patientRef, "Patient/")
	resp := fhir.CoverageEligibilityResponse{
		Status:  fhir.FinancialResourceStatusCodesActive,
		Purpose: []fhir.EligibilityResponsePurpose{fhir.EligibilityResponsePurposeBenefits},
		Patient: fhir.Reference{Reference: strPtr(patientRef)},
		Created: created.UTC().Format(time.RFC3339),
		Request: fhir.Reference{Reference: strPtr("CoverageEligibilityRequest/req-" + correlationID)},
		Outcome: fhir.ClaimProcessingCodesComplete,
		Insurer: insurerRef,
		Insurance: []fhir.CoverageEligibilityResponseInsurance{{
			Coverage: fhir.Reference{Reference: strPtr("Coverage/" + member)},
			Inforce:  boolPtr(covered),
		}},
	}
	// disposition is optional (0..1) in R4. Only set it when there's actual reason
	// text — an empty-string disposition is an invalid FHIR primitive value
	// ("Attribute value must not be empty"), which a real $validate egress gate
	// rejects. A not-covered member with no reason (e.g. no Coverage found at all)
	// must still produce a valid resource: omit the field. This is the LIVE path
	// (the Smart Gateway's inbound eligibility handler calls this copy directly,
	// not the platform's own builder) — ported-copy drift between this function and
	// the platform's eligibility-response builder is exactly how the
	// MBR-UC04/MBR-UC08 empty-disposition 502s reached production while the platform
	// twin looked fine; keep both guards in sync (the platform's SDK parity suite
	// proves byte-parity, but only for the cases it exercises — check it when
	// changing either copy).
	if !covered && reason != "" {
		resp.Disposition = strPtr(reason)
	}
	return resp
}

// ParseEligibilityRequestInsurer returns the insurer Reference a
// CoverageEligibilityRequest names, exactly as written: a logical reference
// (Reference.identifier, as BuildEligibilityRequestExact writes it) or a literal one,
// as earlier releases sent. ok is false when the request names no insurer.
func ParseEligibilityRequestInsurer(data []byte) (insurer json.RawMessage, ok bool) {
	var req struct {
		ResourceType string          `json:"resourceType"`
		Insurer      json.RawMessage `json:"insurer"`
	}
	if json.Unmarshal(data, &req) != nil || req.ResourceType != "CoverageEligibilityRequest" || !isJSONObjectWithMembers(req.Insurer) {
		return nil, false
	}
	return req.Insurer, true
}

// isJSONObjectWithMembers reports whether raw is a JSON object with at least one member.
func isJSONObjectWithMembers(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && len(obj) > 0
}

// ParseEligibilityRequestMember extracts the member ID from a
// CoverageEligibilityRequest's patient.reference literally, stripping a leading
// "Patient/" prefix and keeping the rest. It errors if the resource is not a
// CoverageEligibilityRequest or the patient reference is missing.
//
// It is not a payer's Smart Gateway's reading of the request, which is
// ParseEligibilityRequestMemberExact: a Responder answers with that one, and a
// payer reading an inbound request should too. This function differs from it in
// two ways:
//   - it reads the reference as written: an absolute reference is kept whole, a
//     versioned one is kept as "<id>/_history/<version>", and a "#id" reference
//     is kept as "#id", where the exact reader resolves each to the member;
//   - it reads leniently, as encoding/json does: a member name matches in any
//     case, and the last of a repeated name wins, where the exact reader refuses
//     a request that can be read two ways (a repeated member name, or its type,
//     patient or reference named only in another case).
//
// For a request naming its patient by an unversioned relative reference and
// read one way only, the two bind the same member.
func ParseEligibilityRequestMember(data []byte) (memberID string, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err = json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	if probe.ResourceType != "CoverageEligibilityRequest" {
		return "", fmt.Errorf("shnsdk: expected CoverageEligibilityRequest, got %q", probe.ResourceType)
	}
	var req fhir.CoverageEligibilityRequest
	if err = json.Unmarshal(data, &req); err != nil {
		return "", err
	}
	if req.Patient.Reference == nil {
		return "", fmt.Errorf("shnsdk: CoverageEligibilityRequest missing patient.reference")
	}
	ref := *req.Patient.Reference
	return strings.TrimPrefix(ref, "Patient/"), nil
}

func strPtr(s string) *string { return &s }

// BuildDocumentReference builds a base-R4 DocumentReference for the operative
// report disclosed by an external facility during a federated-query prior-auth flow.
// type = LOINC 28570-0 (Procedure note); category = clinical-note;
// content.attachment.url references the companion DiagnosticReport.
// drRef is "DiagnosticReport/<id>".
//
// NOTE: no meta.profile is set — the US Core DocumentReference Type value set
// requires LOINC codes, which the IG-enabled HAPI does not load, causing
// validation failures. Base-R4 conformance is the pinned posture here; US Core
// profile pinning for DocumentReference is a tracked fast-follow.
//
// Promoted verbatim from the platform's DocumentReference builder; parity
// proven by the platform's SDK parity suite (byte-equal). FR-36
// boundary guard: no fhircodes value-set is referenced — terminology-agnostic.
func BuildDocumentReference(id, patientRef, drRef string) ([]byte, error) {
	date := docRefDateUC05
	dr := fhir.DocumentReference{
		Id:     strPtr(id),
		Status: fhir.DocumentReferenceStatusCurrent,
		Type: &fhir.CodeableConcept{
			Coding: []fhir.Coding{{System: strPtr(systemLOINC), Code: strPtr("28570-0"), Display: strPtr("Procedure note")}},
		},
		Category: []fhir.CodeableConcept{
			{Coding: []fhir.Coding{{System: strPtr(systemUSCoreDocClass), Code: strPtr("clinical-note"), Display: strPtr("Clinical Note")}}},
		},
		Subject: &fhir.Reference{Reference: strPtr(patientRef)},
		// Date is the creation/indexing instant of the document — used for FR-24
		// range filtering (recordClinicalDate reads this field). Fixed to the date of the
		// network's synthetic operative report (2026-05-15, FR-35/FR-39).
		Date: &date,
		Content: []fhir.DocumentReferenceContent{
			{Attachment: fhir.Attachment{
				ContentType: strPtr("application/fhir+json"),
				Url:         strPtr(drRef),
				Title:       strPtr("Operative report — lumbar microdiscectomy"),
			}},
		},
	}
	// fhir.DocumentReference.MarshalJSON injects "resourceType":"DocumentReference"
	// automatically (confirmed in samply v0.3.2).
	return json.Marshal(dr)
}

// PASReviewAction is a payer's review decision on a prior-authorization
// item: the X12 306 decision code and the X12 886 reasons for it, as the
// payer coded them.
type PASReviewAction struct {
	Code    PASCoding
	Reasons []PASCoding
}

// davinciIGCanonical builds a versioned Da Vinci implementationGuide canonical
// ("http://hl7.org/fhir/us/<ig>/ImplementationGuide/hl7.fhir.us.<ig>|<pkgVersion>").
// The declared-set-driven provider ingress CapabilityStatement builder below uses it.
func davinciIGCanonical(ig, pkgVersion string) string {
	return "http://hl7.org/fhir/us/" + ig + "/ImplementationGuide/hl7.fhir.us." + ig + "|" + pkgVersion
}

// pasProfileClaimBase is the unversioned Da Vinci PAS Claim profile canonical;
// per-declared-line pins append "|<pkgVersion>" (davinci-pas manifest package
// version, resolved via PASLineDef).
const pasProfileClaimBase = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim"

// BuildProviderIngressCapabilityStatement returns the provider gateway's
// Da Vinci ingress CapabilityStatement, served at GET /metadata on the
// ingress edge (FR-37 per-role statements). It
// declares the CRD/DTR/PAS generation(s) the deployment DECLARES (not merely
// natively builds) via versioned IG canonicals, and the FHIR operations the
// ingress serves. CRD is CDS Hooks, not FHIR REST — it is named in documentation
// and implementationGuide only; its discovery document lives at /cds-services.
//
// declared is the deployment's declared contract-version token set — the gateway
// passes Gateway.declaredContractVersions() (SHN_CONTRACT_VERSIONS, D1a); an SDK
// caller with no declared-set config passes SupportedContractVersions(), whose
// output is byte-identical to the pre-declared-set hardcoded CRD/DTR/PAS 2.0.1
// canonicals. For each declared
// pa.crd/pa.dtr/pa.pas@<line> token, the corresponding *LineDef resolves the
// manifest-pinned package version (fail-silent skip on a line this build does not
// speak natively — never a fabricated canonical); pa.pas additionally contributes a
// per-line `profile-claim|<pkgver>` supportedProfile pin on the Claim resource (one
// per declared PAS line). A declared token for an unrelated contract (a future
// workstream's) is ignored — this statement is CRD/DTR/PAS-only.
func BuildProviderIngressCapabilityStatement(created time.Time, declared []string) ([]byte, error) {
	doc := "Da Vinci ingress for foreign EHR/CDS clients: PAS Claim/$submit and Claim/$inquire, DTR $questionnaire-package and coverage eligibility CoverageEligibilityRequest/$submit (FHIR operations below), and CRD CDS Hooks discovered at /cds-services. Version-specific endpoint codes are published at /.well-known/davinci-configuration (HRex 1.2.0)."
	eligibilityDoc := "Carries the input to the payer its request names, exactly as sent, and relays the payer's answer exactly: a CoverageEligibilityRequest, a Bundle of one request with the resources it references, or a Parameters whose one resource parameter holds either. A batch Bundle of at most 17 requests is carried too, but answered only by a payer whose own endpoint answers batches: a payer gateway answering from the payer's records answers one request at a time and refuses a batch 501."
	sec := "SMART Backend Services (client_credentials, private_key_jwt); configuration at /.well-known/smart-configuration."
	var igs, pasProfiles []string
	seenIG, seenProfile := map[string]bool{}, map[string]bool{}
	for _, tok := range declared {
		contract, line, ok := strings.Cut(tok, "@")
		if !ok {
			continue
		}
		switch contract {
		case "pa.crd":
			if def, ok := CRDLineDef(line); ok {
				if ig := davinciIGCanonical("davinci-crd", def.PackageVersion); !seenIG[ig] {
					igs = append(igs, ig)
					seenIG[ig] = true
				}
			}
		case "pa.dtr":
			if def, ok := DTRLineDef(line); ok {
				if ig := davinciIGCanonical("davinci-dtr", def.PackageVersion); !seenIG[ig] {
					igs = append(igs, ig)
					seenIG[ig] = true
				}
			}
		case "pa.pas":
			if def, ok := PASLineDef(line); ok {
				if ig := davinciIGCanonical("davinci-pas", def.PackageVersion); !seenIG[ig] {
					igs = append(igs, ig)
					seenIG[ig] = true
				}
				if profile := pasProfileClaimBase + "|" + def.PackageVersion; !seenProfile[profile] {
					pasProfiles = append(pasProfiles, profile)
					seenProfile[profile] = true
				}
			}
		}
	}
	cs := fhir.CapabilityStatement{
		Status:      fhir.PublicationStatusActive,
		Date:        created.UTC().Format(time.RFC3339),
		Kind:        fhir.CapabilityStatementKindInstance,
		FhirVersion: fhir.FHIRVersion4_0_1,
		Format:      []string{"json"},
		Software:    &fhir.CapabilityStatementSoftware{Name: "SHN Provider Da Vinci Ingress"},
		Implementation: &fhir.CapabilityStatementImplementation{
			Description: "SHN provider gateway Da Vinci ingress (CRD 2.0.1, DTR 2.0.1, PAS 2.0.1).",
		},
		ImplementationGuide: igs,
		Rest: []fhir.CapabilityStatementRest{{
			Mode:          fhir.RestfulCapabilityModeServer,
			Documentation: &doc,
			Security:      &fhir.CapabilityStatementRestSecurity{Description: &sec},
			Resource: []fhir.CapabilityStatementRestResource{
				{
					Type:             fhir.ResourceTypeClaim,
					SupportedProfile: pasProfiles,
					Operation: []fhir.CapabilityStatementRestResourceOperation{
						{Name: "submit", Definition: "http://hl7.org/fhir/us/davinci-pas/OperationDefinition/Claim-submit"},
						// The follow-up that asks for the decision on an
						// authorization the payer pended. Declared here because
						// the ingress serves it: a statement that named only
						// $submit would advertise one surface and route another.
						{Name: "inquire", Definition: "http://hl7.org/fhir/us/davinci-pas/OperationDefinition/Claim-inquiry"},
					},
				},
				{
					Type: fhir.ResourceTypeQuestionnaire,
					Operation: []fhir.CapabilityStatementRestResourceOperation{
						{Name: "questionnaire-package", Definition: "http://hl7.org/fhir/us/davinci-dtr/OperationDefinition/questionnaire-package"},
					},
				},
				{
					// Not tied to a declared line: the base R4 operation
					// (IHE FAIS ITI-124) is served whatever lines are declared.
					Type: fhir.ResourceTypeCoverageEligibilityRequest,
					Operation: []fhir.CapabilityStatementRestResourceOperation{
						{Name: "submit", Definition: "http://hl7.org/fhir/OperationDefinition/CoverageEligibilityRequest-submit", Documentation: &eligibilityDoc},
					},
				},
			},
		}},
	}
	return json.Marshal(cs) // fhir.CapabilityStatement.MarshalJSON injects resourceType
}

// BuildHubCapabilityStatement returns the Hub's CapabilityStatement, served
// at GET /metadata (FR-37 per-role statements). Deliberately minimal: the Hub
// is the payload-blind routing plane (OWD-2 — the envelope stays
// version-blind), so this statement declares NO implementationGuide, NO
// profiles, and NO rest resources — only that a JSON service exists here and
// where its real surface is documented. Its two routes (POST /route,
// GET /transport-key) are substrate wire contracts, not FHIR REST.
func BuildHubCapabilityStatement(created time.Time) ([]byte, error) {
	doc := "SHN Hub — payload-blind exchange routing plane. No FHIR REST surface: substrate routes are POST /route and GET /transport-key (see PARTICIPANT_PROTOCOL). The Hub never inspects payloads and advertises no payload versions."
	cs := fhir.CapabilityStatement{
		Status:      fhir.PublicationStatusActive,
		Date:        created.UTC().Format(time.RFC3339),
		Kind:        fhir.CapabilityStatementKindInstance,
		FhirVersion: fhir.FHIRVersion4_0_1,
		Format:      []string{"json"},
		Software:    &fhir.CapabilityStatementSoftware{Name: "SHN Hub"},
		Implementation: &fhir.CapabilityStatementImplementation{
			Description: "SHN Hub routing plane (payload-blind; no FHIR resources served).",
		},
		Rest: []fhir.CapabilityStatementRest{{
			Mode:          fhir.RestfulCapabilityModeServer,
			Documentation: &doc,
		}},
	}
	return json.Marshal(cs)
}
