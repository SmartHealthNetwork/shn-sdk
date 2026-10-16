package shnsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

const (
	// systemLOINC is the LOINC code system URI.
	// Mirrors internal/fhirmap.systemLOINC (consent.go).
	systemLOINC = "http://loinc.org"

	// systemUSCoreDocClass is the US Core DocumentReference category code system.
	// Mirrors internal/fhirmap.systemUSCoreDocClass (documentreference.go).
	systemUSCoreDocClass = "http://hl7.org/fhir/us/core/CodeSystem/us-core-documentreference-category"

	// docRefDateUC05 is the fixed instant for the operative DocumentReference
	// disclosed by the external facility (the date of the network's synthetic operative
	// report, 2026-05-15). FHIR DocumentReference.date is an instant
	// type, so a full ISO 8601 timestamp is required. recordClinicalDate truncates to
	// YYYY-MM-DD for InRange comparison (FR-24). Fixed value keeps the golden
	// byte-deterministic (FR-35/FR-39). Mirrors internal/fhirmap.docRefDateUC05.
	docRefDateUC05 = "2026-05-15T00:00:00Z"

	// X12ReviewDecisionSystem is the X12 306 review decision code system (for
	// example A1 Certified in total, A3 Not Certified) a payer's review action
	// is coded in, in PAS and in the PDex prior authorization EOB.
	X12ReviewDecisionSystem = "https://codesystem.x12.org/005010/306"
	// X12ReviewDecisionReasonSystem is the X12 886 review decision reason code
	// system a review action's reasons are coded in.
	X12ReviewDecisionReasonSystem = "https://codesystem.x12.org/external/886"
	// CARCSystem is the X12 Claim Adjustment Reason Code system and
	// RARCSystem the CMS Remittance Advice Remark Code system: the codes a PDex
	// prior authorization EOB's denial reason adjudication may carry.
	CARCSystem = "https://x12.org/codes/claim-adjustment-reason-codes"
	RARCSystem = "https://x12.org/codes/remittance-advice-remark-codes"

	// systemAdjudication is the standard FHIR adjudication codesystem; its
	// "submitted" code carries the request item's net on a decision EOB.
	// Mirrors internal/fhirmap.systemAdjudication (eob.go).
	systemAdjudication = "http://terminology.hl7.org/CodeSystem/adjudication"
)

// BuildEligibilityRequestExact constructs a FHIR R4 CoverageEligibilityRequest for the
// member, ordering NPI, and date of service (now). Returns FHIR JSON. Its
// v0.61.1 form, the Deprecated BuildEligibilityRequest, is the one the
// cross-module parity test fences against internal/fhirmap; this builder names
// the payer by identifier, which neither of them does.
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
// standalone from internal/fhirmap.ParseEligibilityResponse; the parity test
// proves it agrees with the substrate parser on a substrate-built response (both
// covered and not-covered branches).
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
// Its v0.61.1 form, the Deprecated BuildEligibilityResponse, is the one the
// cross-module parity test fences against internal/fhirmap; this builder refuses
// an insurer without both a system and a value, which neither of them does.
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
	// (the promoted gateway/engine/inbound.go handleEligibilityInbound calls
	// this copy directly, not internal/fhirmap's) — ported-copy drift between this
	// function and internal/fhirmap.BuildEligibilityResponse is exactly how the
	// MBR-UC04/MBR-UC08 empty-disposition 502s reached production while the substrate
	// twin looked fine; keep both guards in sync (test/sdkparity's
	// TestEligibilityResponseParity proves byte-parity, but only for the cases it
	// exercises — check it when changing either copy).
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
// CoverageEligibilityRequest's patient.reference, stripping the leading "Patient/"
// prefix. It errors if the resource is not a CoverageEligibilityRequest or the
// patient reference is missing. PORTED standalone from
// internal/fhirmap.ParseEligibilityRequestMember; the parity test proves it agrees
// with the substrate on both SDK-built and substrate-built requests
// (test/sdkparity/fhir_parity_test.go).
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
// Promoted verbatim from internal/fhirmap.BuildDocumentReference; parity
// proven by test/sdkparity/fhirmap_builders_parity_test.go (byte-equal). FR-36
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

// The payer's review action is re-expressed in the EOB's own extension. PAS
// defines extension-reviewAction for ClaimResponse.item.adjudication (and
// Claim.item) only, so carried onto ExplanationOfBenefit.item.adjudication it
// is an extension used out of its context, which base FHIR R4 validation
// refuses. PDex defines the same extension, with the same sub-extensions, for
// the ExplanationOfBenefit adjudication contexts. It is used here only because
// it is the one defined for that context: the EOB still claims no PDex profile
// (no meta.profile, no CapabilityStatement claim). Every value is copied
// exactly; only the extension and sub-extension URLs change, and only as this
// registered table maps them, field for field (PAS 2.0.1/2.1.0/2.2.1 and PDex
// 2.1.0 both define code, number, reasonCode and secondSurgicalOpinionFlag).
// A payer sub-extension the table does not map writes no EOB
// (PADecisionEOBUnmappedError): it is never dropped.
// Mirrors internal/fhirmap.reviewActionSubExtensionPDex (eob.go).
const (
	pasReviewActionURL  = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	pdexReviewActionURL = "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/extension-reviewAction"
)

var reviewActionSubExtensionPDex = map[string]string{
	"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode": "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/extension-reviewActionCode",
	"number":                    "number",
	"reasonCode":                "reasonCode",
	"secondSurgicalOpinionFlag": "secondSurgicalOpinionFlag",
}

// PADecisionEOBUnmappedError reports a sub-extension of the payer's review
// action that the EOB's review action has no counterpart for: the EOB is not
// built, and the sub-extension is never dropped. URL is the payer's;
// Position is the sub-extension's 1-based place in the review action. The
// error text names the position only, never the payer's URL, so a finding or
// log line built from it carries no payload value.
type PADecisionEOBUnmappedError struct {
	URL      string
	Position int
}

func (e *PADecisionEOBUnmappedError) Error() string {
	return fmt.Sprintf("shnsdk: review action sub-extension %d has no counterpart in the EOB review action", e.Position)
}

// jsonMember is one member of a JSON object, its value as written.
type jsonMember struct {
	Key   string
	Value json.RawMessage
}

// jsonObjectMembers reads a JSON object's members in order, each value as
// written; ok is false for anything that is not an object.
func jsonObjectMembers(b json.RawMessage) ([]jsonMember, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var out []jsonMember
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := t.(string)
		if !ok {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		out = append(out, jsonMember{Key: key, Value: v})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	return out, true
}

// jsonObject writes members back as one object, in order.
func jsonObject(members []jsonMember) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.Key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// withURL rewrites an extension object's url member, every other member as
// written. ok is false when the object has no string url.
func withURL(ext json.RawMessage, rewrite func(string) (string, error)) (json.RawMessage, error) {
	members, ok := jsonObjectMembers(ext)
	if !ok {
		return nil, fmt.Errorf("shnsdk: payer review action extension is not a JSON object")
	}
	for i, m := range members {
		if m.Key != "url" {
			continue
		}
		var u string
		if err := json.Unmarshal(m.Value, &u); err != nil {
			return nil, fmt.Errorf("shnsdk: payer review action extension url is not a string")
		}
		to, err := rewrite(u)
		if err != nil {
			return nil, err
		}
		members[i].Value, _ = json.Marshal(to)
	}
	return jsonObject(members), nil
}

// transcribeReviewAction re-expresses one payer adjudication's PAS review
// action in the EOB's own extension (see the table above). Every other member
// of the adjudication, and every value inside the review action, is carried as
// written.
func transcribeReviewAction(adj json.RawMessage) (json.RawMessage, error) {
	members, ok := jsonObjectMembers(adj)
	if !ok {
		return nil, fmt.Errorf("shnsdk: payer adjudication is not a JSON object")
	}
	for i, m := range members {
		if m.Key != "extension" {
			continue
		}
		var exts []json.RawMessage
		if err := json.Unmarshal(m.Value, &exts); err != nil {
			return nil, fmt.Errorf("shnsdk: payer adjudication extension is not a list")
		}
		for j, ext := range exts {
			var head struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(ext, &head) != nil || head.URL != pasReviewActionURL {
				continue
			}
			outer, ok := jsonObjectMembers(ext)
			if !ok {
				return nil, fmt.Errorf("shnsdk: payer review action is not a JSON object")
			}
			for k, om := range outer {
				switch om.Key {
				case "url":
					outer[k].Value, _ = json.Marshal(pdexReviewActionURL)
				case "extension":
					var subs []json.RawMessage
					if err := json.Unmarshal(om.Value, &subs); err != nil {
						return nil, fmt.Errorf("shnsdk: payer review action sub-extensions are not a list")
					}
					parts := make([]string, 0, len(subs))
					for n, sub := range subs {
						t, err := withURL(sub, func(u string) (string, error) {
							if to, ok := reviewActionSubExtensionPDex[u]; ok {
								return to, nil
							}
							return "", &PADecisionEOBUnmappedError{URL: u, Position: n + 1}
						})
						if err != nil {
							return nil, err
						}
						parts = append(parts, string(t))
					}
					outer[k].Value = json.RawMessage("[" + strings.Join(parts, ",") + "]")
				}
			}
			exts[j] = jsonObject(outer)
		}
		parts := make([]string, 0, len(exts))
		for _, e := range exts {
			parts = append(parts, string(e))
		}
		members[i].Value = json.RawMessage("[" + strings.Join(parts, ",") + "]")
	}
	return jsonObject(members), nil
}

// eobJSON and its supporting types are package-local structs for the decision
// ExplanationOfBenefit wire shape (base FHIR R4; no profile is claimed).
// Promoted verbatim from internal/fhirmap/eob.go.
type eobJSON struct {
	ResourceType string            `json:"resourceType"`
	Id           string            `json:"id"`
	Status       string            `json:"status"`
	Type         json.RawMessage   `json:"type"`
	Use          string            `json:"use"`
	Patient      eobReference      `json:"patient"`
	PreAuthRef   []string          `json:"preAuthRef,omitempty"`
	Created      string            `json:"created"`
	Insurer      eobReference      `json:"insurer"`
	Provider     eobReference      `json:"provider"`
	Outcome      string            `json:"outcome"`
	Insurance    []eobInsurance    `json:"insurance"`
	Item         []eobItem         `json:"item"`
	AddItem      []json.RawMessage `json:"addItem,omitempty"`
	Adjudication []json.RawMessage `json:"adjudication,omitempty"`
	ProcessNote  []eobProcessNote  `json:"processNote,omitempty"`
}

// eobProcessNote is one payer note: its number only when the payer stated
// one (R4 processNote.number is 0..1), never a number of this builder's.
type eobProcessNote struct {
	Number int    `json:"number,omitempty"`
	Type   string `json:"type,omitempty"`
	Text   string `json:"text"`
}

// eobNoteTypes is the FHIR R4 NoteType value set (required binding on
// ExplanationOfBenefit.processNote.type).
var eobNoteTypes = map[string]bool{"display": true, "print": true, "printoper": true}

type eobInsurance struct {
	Focal    bool         `json:"focal"`
	Coverage eobReference `json:"coverage"`
}

type eobItem struct {
	Sequence         int               `json:"sequence"`
	ProductOrService json.RawMessage   `json:"productOrService"`
	Adjudication     []json.RawMessage `json:"adjudication,omitempty"`
}

type eobCodeableConcept struct {
	Coding []eobCoding `json:"coding,omitempty"`
	Text   string      `json:"text,omitempty"`
}
type eobCoding struct {
	System  string `json:"system,omitempty"`
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`
}
type eobReference struct {
	Reference  string          `json:"reference,omitempty"`
	Identifier json.RawMessage `json:"identifier,omitempty"`
	Display    string          `json:"display,omitempty"`
}

// eobSubmittedAmount is the adjudication the request item's net is carried
// on when the payer stated no "submitted" adjudication of its own.
type eobSubmittedAmount struct {
	Category eobCodeableConcept `json:"category"`
	Amount   json.RawMessage    `json:"amount"`
}

// PADecisionEOBReference is a reference a decision EOB states exactly as its
// source wrote it. When Identifier is set — the source reference carried one,
// or the resource it names in the source Bundle does (for example a
// provider's NPI or a Coverage's member identifier) — the EOB writes
// Reference.identifier, as written, instead of the literal reference, which
// may not resolve in the payer's Patient Access store. Otherwise it writes
// Reference as written. Display is carried when the source reference has one.
// Nothing here is minted: a reference with neither a Reference nor an
// Identifier is absent.
type PADecisionEOBReference struct {
	Reference  string
	Identifier json.RawMessage
	Display    string
}

func (r PADecisionEOBReference) absent() bool {
	return r.Reference == "" && !rawPresent(r.Identifier)
}

func (r PADecisionEOBReference) wire() eobReference {
	if rawPresent(r.Identifier) {
		return eobReference{Identifier: r.Identifier, Display: r.Display}
	}
	return eobReference{Reference: r.Reference, Display: r.Display}
}

// PADecisionEOBInsurance is one of the request Claim's insurance entries:
// its focal flag and its coverage, as written.
type PADecisionEOBInsurance struct {
	Focal    *bool
	Coverage PADecisionEOBReference
}

// PADecisionEOBAbsentError reports an element the decision EOB requires
// whose source supplied no value: the EOB is not built, and nothing is
// filled in for it. Element names the EOB element.
type PADecisionEOBAbsentError struct{ Element string }

func (e *PADecisionEOBAbsentError) Error() string {
	return "shnsdk: decision EOB " + e.Element + " absent at its source"
}

// rawPresent reports whether a raw JSON value states something (not empty,
// not null).
func rawPresent(b json.RawMessage) bool {
	s := strings.TrimSpace(string(b))
	return s != "" && s != "null"
}

// adjudicationStatesSubmitted reports whether a payer adjudication entry's
// category is the standard "submitted" adjudication: the adjudication code
// system's "submitted", or that code stated with no system.
func adjudicationStatesSubmitted(raw json.RawMessage) bool {
	var a struct {
		Category struct {
			Coding []eobCoding `json:"coding"`
		} `json:"category"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	for _, c := range a.Category.Coding {
		if (c.System == systemAdjudication || c.System == "") && c.Code == "submitted" {
			return true
		}
	}
	return false
}

// PADecisionEOBExactParams groups the inputs to BuildPADecisionEOBExact; always
// construct it with keyed fields.
type PADecisionEOBExactParams struct {
	ID         string
	PatientRef string
	// ClaimType is the request Claim's type (a CodeableConcept), as written.
	ClaimType json.RawMessage
	// Created is the payer's ClaimResponse.created, as written (on the
	// inquiry leg, the ClaimResponse that carried the decision). Never a
	// clock reading.
	Created string
	// Insurer is the payer's ClaimResponse.insurer.
	Insurer PADecisionEOBReference
	// Provider is the request Claim's provider.
	Provider PADecisionEOBReference
	// Insurance is the request Claim's insurance entries, in order.
	Insurance []PADecisionEOBInsurance
	// Outcome is the payer's ClaimResponse.outcome, as written.
	Outcome string
	// ItemSequence is the request Claim item's sequence (the item the
	// ClaimResponse's item.itemSequence answers).
	ItemSequence int
	// ProductOrService is the request Claim item's productOrService (a
	// CodeableConcept), as written. When set it is carried exactly and
	// CPTCode, CPTDisplay and ProcedureSystem are not read.
	ProductOrService json.RawMessage
	CPTCode          string
	CPTDisplay       string
	// ProcedureSystem is the system URI of item[].productOrService.coding
	// (FR-28), as the source states it. It is never defaulted: empty writes
	// a coding with no system (R4 Coding.system is 0..1).
	ProcedureSystem string
	// PreAuthRefs are the payer's own authorization numbers, exactly as it
	// stated them: its ClaimResponse.preAuthRef and any review action number,
	// in that order. Written as EOB.preAuthRef, duplicates once; never
	// inferred from the decision.
	PreAuthRefs []string
	// ProcessNotes are the payer's own notes (for example its appeal window),
	// written as EOB.processNote in order, on either decision, each with the
	// number the payer stated (none when it stated none). A note without
	// text, or with a type other than "display", "print" or "printoper", is
	// refused. Nil or empty writes no processNote.
	ProcessNotes []PASProcessNote
	// Adjudications are the payer's own ClaimResponse item.adjudication
	// entries for the item, each a JSON object carried as written: its
	// category, its amount only when the payer stated one and its reason value
	// for value, and its PAS review action re-expressed, value for value, in
	// the EOB's own review action extension (see reviewActionSubExtensionPDex).
	Adjudications []json.RawMessage
	// SubmittedAmount is the request Claim item's net (a Money), as written.
	// It is carried as a "submitted" adjudication only when no entry of
	// Adjudications states "submitted".
	SubmittedAmount json.RawMessage
	// HeaderAdjudications are the payer's ClaimResponse.adjudication entries
	// (PAS lets a payer state its review action there), carried onto the
	// EOB's header adjudication with the same transcription as the item's.
	HeaderAdjudications []json.RawMessage
	// AddItems are the payer's ClaimResponse.addItem entries for the item,
	// each a JSON object carried as written onto EOB.addItem, its
	// adjudication transcribed as the item's.
	AddItems []json.RawMessage
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
// Shared by the two declared-set-driven CapabilityStatement builders below.
func davinciIGCanonical(ig, pkgVersion string) string {
	return "http://hl7.org/fhir/us/" + ig + "/ImplementationGuide/hl7.fhir.us." + ig + "|" + pkgVersion
}

// pasProfileClaimBase is the unversioned Da Vinci PAS Claim profile canonical;
// per-declared-line pins append "|<pkgVersion>" (davinci-pas manifest package
// version, resolved via PASLineDef).
const pasProfileClaimBase = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim"

// BuildPatientAccessCapabilityStatementExact returns the CMS-0057 Patient Access
// CapabilityStatement the payer serves at GET /metadata (FR-37): a kind=instance
// server statement declaring the ExplanationOfBenefit read+search surface, gated
// by the per-operation patient-access token (AI-11). It mirrors the real payer
// routes (GET /ExplanationOfBenefit?patient= and /ExplanationOfBenefit/{id}) and
// validates as a base FHIR CapabilityStatement (0 errors).
//
// It claims neither a supportedProfile nor an implementationGuide: the decision
// EOBs are base FHIR R4 and do not meet Da Vinci PDex, whose closed
// adjudication slicing needs an amount, a reason or units on every
// adjudication and so cannot carry a payer's amountless decision without an
// amount the payer never stated. declared (the deployment's declared
// contract-version token set) is accepted for signature stability and does not
// change the statement. From shn-gateway v0.62.0 no gateway declares a
// pa.pdex line either (ContractPAPDex21), so neither the statement nor the
// declaration claims PDex.
//
// Its v0.61.1 form, the Deprecated BuildPatientAccessCapabilityStatement, is
// the one internal/fhirmap delegates to. fhircodes-clean — no value-set references; dependency-light.
func BuildPatientAccessCapabilityStatementExact(created time.Time, declared []string) ([]byte, error) {
	doc := "Patient Access to prior-authorization decisions. Each request carries a per-operation patient-access authority token bound to the patient (AI-11) — no blanket access."
	sec := "Per-operation patient-access bearer token (substrate Authorization Framework); subject-bound, signature-covered."
	_ = declared // the statement names no implementation guide (see the doc comment)
	cs := fhir.CapabilityStatement{
		Status:      fhir.PublicationStatusActive,
		Date:        created.UTC().Format(time.RFC3339),
		Kind:        fhir.CapabilityStatementKindInstance,
		FhirVersion: fhir.FHIRVersion4_0_1,
		Format:      []string{"json"},
		Software:    &fhir.CapabilityStatementSoftware{Name: "SHN Payer Patient Access API"},
		Implementation: &fhir.CapabilityStatementImplementation{
			Description: "SHN CMS-0057 Patient Access API (prior-authorization decision ExplanationOfBenefit, base FHIR R4).",
		},
		Rest: []fhir.CapabilityStatementRest{{
			Mode:          fhir.RestfulCapabilityModeServer,
			Documentation: &doc,
			Security:      &fhir.CapabilityStatementRestSecurity{Description: &sec},
			Resource: []fhir.CapabilityStatementRestResource{{
				Type: fhir.ResourceTypeExplanationOfBenefit,
				Interaction: []fhir.CapabilityStatementRestResourceInteraction{
					{Code: fhir.TypeRestfulInteractionRead},
					{Code: fhir.TypeRestfulInteractionSearchType},
				},
				SearchParam: []fhir.CapabilityStatementRestResourceSearchParam{
					{Name: "_id", Type: fhir.SearchParamTypeToken},
					{Name: "patient", Type: fhir.SearchParamTypeReference},
				},
			}},
		}},
	}
	return json.Marshal(cs) // fhir.CapabilityStatement.MarshalJSON injects resourceType
}

// BuildProviderIngressCapabilityStatement returns the provider gateway's
// Da Vinci ingress CapabilityStatement, served at GET /metadata on the
// ingress edge (FR-37 per-role statements). It
// declares the CRD/DTR/PAS generation(s) the deployment DECLARES (not merely
// natively builds) via versioned IG canonicals, and the two FHIR operations the
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
	doc := "Da Vinci ingress for foreign EHR/CDS clients: PAS Claim/$submit and Claim/$inquire, DTR $questionnaire-package (FHIR operations below), and CRD CDS Hooks discovered at /cds-services. Version-specific endpoint codes are published at /.well-known/davinci-configuration (HRex 1.2.0)."
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

// BuildPADecisionEOBExact builds the ExplanationOfBenefit a payer gateway keeps as
// the patient-facing record of a payer's prior-authorization decision (FR-28),
// stating only what the payer's answer and the request supply.
//
//   - From the request Claim, carried as written: type, provider, insurance
//     (focal and coverage), the item's sequence and its net.
//   - From the payer's ClaimResponse, carried as written: created, insurer,
//     outcome, the item's adjudication entries (category, amount and reasons
//     value for value; the review action re-expressed value for value in the
//     EOB's own extension), its notes and its own authorization numbers
//     (preAuthRef), whatever the decision.
//   - The record's own: status active, use preauthorization, the bound
//     patient, and the order's product coding.
//
// The request's net is added as a "submitted" adjudication only when the
// payer stated no "submitted" entry of its own. With no payer adjudication
// and no net the item carries no adjudication. No amount, review action,
// reason code or note the sources did not state is ever written.
//
// The resource is base FHIR R4 and claims no profile: Da Vinci PDex
// pdex-priorauthorization's closed adjudication slicing needs an amount, a
// reason or units on every adjudication, so it cannot carry a payer's
// amountless decision without an amount the payer never stated.
//
// A required element with no value (ClaimType, Created, Insurer, Provider,
// Insurance and each entry's focal and coverage, Outcome, ItemSequence) is
// refused with *PADecisionEOBAbsentError, and a payer review action
// sub-extension the EOB's extension has no counterpart for with
// *PADecisionEOBUnmappedError: the EOB is not built.
//
// Its v0.61.1 form, the Deprecated BuildPADecisionEOB, is the one
// test/sdkparity fences against internal/fhirmap; this builder is not that
// shape.
func BuildPADecisionEOBExact(p PADecisionEOBExactParams) ([]byte, error) {
	absent := func(element string) ([]byte, error) { return nil, &PADecisionEOBAbsentError{Element: element} }
	switch {
	case !rawPresent(p.ClaimType):
		return absent("type")
	case p.Created == "":
		return absent("created")
	case p.Insurer.absent():
		return absent("insurer")
	case p.Provider.absent():
		return absent("provider")
	case len(p.Insurance) == 0:
		return absent("insurance")
	case p.Outcome == "":
		return absent("outcome")
	case p.ItemSequence < 1:
		return absent("item.sequence")
	}
	insurance := make([]eobInsurance, 0, len(p.Insurance))
	for _, in := range p.Insurance {
		if in.Focal == nil {
			return absent("insurance.focal")
		}
		if in.Coverage.absent() {
			return absent("insurance.coverage")
		}
		insurance = append(insurance, eobInsurance{Focal: *in.Focal, Coverage: in.Coverage.wire()})
	}
	product := p.ProductOrService
	if !rawPresent(product) {
		b, err := json.Marshal(eobCodeableConcept{Coding: []eobCoding{{System: p.ProcedureSystem, Code: p.CPTCode, Display: p.CPTDisplay}}})
		if err != nil {
			return nil, err
		}
		product = b
	}
	var notes []eobProcessNote
	for i, n := range p.ProcessNotes {
		if n.Text == "" {
			return nil, fmt.Errorf("shnsdk: process note %d has no text", i+1)
		}
		if n.Type != "" && !eobNoteTypes[n.Type] {
			return nil, fmt.Errorf("shnsdk: process note %d type is not display, print or printoper", i+1)
		}
		notes = append(notes, eobProcessNote{Number: n.Number, Type: n.Type, Text: n.Text})
	}
	header, err := transcribeAdjudications("header adjudication", p.HeaderAdjudications)
	if err != nil {
		return nil, err
	}
	addItems := make([]json.RawMessage, 0, len(p.AddItems))
	for i, ai := range p.AddItems {
		t, err := transcribeAddItem(ai)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: payer addItem %d: %w", i+1, err)
		}
		addItems = append(addItems, t)
	}
	// The payer's own adjudication, value for value; the request's net only
	// where the payer stated no submitted entry.
	var adjudication []json.RawMessage
	payerSubmitted := false
	for i, a := range p.Adjudications {
		if !json.Valid(a) || !strings.HasPrefix(strings.TrimSpace(string(a)), "{") {
			return nil, fmt.Errorf("shnsdk: payer adjudication %d is not a JSON object", i+1)
		}
		payerSubmitted = payerSubmitted || adjudicationStatesSubmitted(a)
	}
	if !payerSubmitted && rawPresent(p.SubmittedAmount) {
		b, err := json.Marshal(eobSubmittedAmount{
			Category: eobCodeableConcept{Coding: []eobCoding{{System: systemAdjudication, Code: "submitted", Display: "Submitted Amount"}}},
			Amount:   p.SubmittedAmount,
		})
		if err != nil {
			return nil, err
		}
		adjudication = append(adjudication, b)
	}
	for _, a := range p.Adjudications {
		t, err := transcribeReviewAction(a)
		if err != nil {
			return nil, err
		}
		adjudication = append(adjudication, t)
	}
	eob := eobJSON{
		ResourceType: "ExplanationOfBenefit",
		Id:           p.ID,
		Status:       "active",
		Type:         p.ClaimType,
		Use:          "preauthorization",
		Patient:      eobReference{Reference: p.PatientRef},
		Created:      p.Created,
		Insurer:      p.Insurer.wire(),
		Provider:     p.Provider.wire(),
		Outcome:      p.Outcome,
		Insurance:    insurance,
		Item: []eobItem{{
			Sequence:         p.ItemSequence,
			ProductOrService: product,
			Adjudication:     adjudication,
		}},
		Adjudication: header,
		ProcessNote:  notes,
	}
	if len(addItems) > 0 {
		eob.AddItem = addItems
	}
	// The payer's own authorization numbers travel ON the resource as
	// EOB.preAuthRef so the patient surface renders them from the EOB (FR-28).
	seen := map[string]bool{}
	for _, ref := range p.PreAuthRefs {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			eob.PreAuthRef = append(eob.PreAuthRef, ref)
		}
	}
	return json.Marshal(eob)
}

// transcribeAdjudications transcribes a list of payer adjudication entries
// (each a JSON object) as the item's are; what names the list in a refusal.
func transcribeAdjudications(what string, in []json.RawMessage) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for i, a := range in {
		if !json.Valid(a) || !strings.HasPrefix(strings.TrimSpace(string(a)), "{") {
			return nil, fmt.Errorf("shnsdk: payer %s %d is not a JSON object", what, i+1)
		}
		t, err := transcribeReviewAction(a)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// transcribeAddItem carries one payer addItem as written, its adjudication
// transcribed as the item's and its subdetailSequence under the EOB's member
// name, subDetailSequence.
func transcribeAddItem(raw json.RawMessage) (json.RawMessage, error) {
	members, ok := jsonObjectMembers(raw)
	if !ok {
		return nil, fmt.Errorf("not a JSON object")
	}
	for i, m := range members {
		if m.Key == "subdetailSequence" {
			// R4 names this member ClaimResponse.addItem.subdetailSequence and
			// ExplanationOfBenefit.addItem.subDetailSequence: the name is
			// transcribed, the value carried as written.
			members[i].Key = "subDetailSequence"
			continue
		}
		if m.Key != "adjudication" {
			continue
		}
		var adjs []json.RawMessage
		if err := json.Unmarshal(m.Value, &adjs); err != nil {
			return nil, fmt.Errorf("adjudication is not a list")
		}
		t, err := transcribeAdjudications("addItem adjudication", adjs)
		if err != nil {
			return nil, err
		}
		parts := make([]string, 0, len(t))
		for _, a := range t {
			parts = append(parts, string(a))
		}
		members[i].Value = json.RawMessage("[" + strings.Join(parts, ",") + "]")
	}
	return jsonObject(members), nil
}
