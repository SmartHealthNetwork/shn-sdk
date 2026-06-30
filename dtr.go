package shnsdk

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// SupportedQuestionnaireCanonical is the ONE DTR questionnaire FillQuestionnaire
// recognizes: the sandbox lumbar-MRI PA questionnaire. It MUST equal the
// substrate's dtr.LumbarMRICanonical / crd.QuestionnaireCanonicalLumbarMRI so the
// SDK fills exactly the questionnaire the sandbox payer returns. FillQuestionnaire
// FAILS LOUDLY on any other canonical (it is a sandbox-targeted stub, not a general
// SDC engine).
const SupportedQuestionnaireCanonical = "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri"

// DTR extension URLs — ported byte-for-byte from internal/dtr so the QR wire shape
// is identical (test/sdkparity/dtr_parity_test.go).
const (
	informationOriginExt = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/information-origin"
	qrContextExt         = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/qr-context"
	intendedUseExt       = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/intendedUse"
	// crdTempCodeSystem is the CRD CodeSystem the DocReason value set draws from;
	// "withpa" = information needed for a prior authorization.
	crdTempCodeSystem = "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp"
)

// ClinicalContext is the provider-LOCAL clinical data FillQuestionnaire answers
// from. Ported standalone from internal/dtr.ClinicalContext (the sandbox
// auto-approval fields). The *Ref fields are carried for parity with the substrate struct even
// though the auto information-origin extension no longer emits a sourceReference
// (DTR 2.0.1 source="auto" carries only the source sub-extension).
type ClinicalContext struct {
	ConditionCode, ConditionRef              string
	ConservativeTherapyWeeks                 int
	ConservativeTherapyRef, ConservativeDate string
	NeuroDeficit                             bool
	NeuroDeficitRef                          string
	PriorImaging                             bool
	PriorImagingRef                          string
	PriorSurgery                             bool
	PriorSurgeryRef                          string
	HighDisability                           bool
	HighDisabilityRef                        string
	// PatientReported signals that a functional-status item must be patient-reported
	// (patient portal attestation flow). When set, FillQuestionnaire emits the
	// patient-reported-required=true trigger item, matching the substrate's AutoFill.
	PatientReported bool
}

// QRContext carries the DTR context the QuestionnaireResponse is completed in: the
// patient subject, the coverage + order qr-context references, and the authoring
// time. Ported standalone from internal/dtr.QRContext. Authored is INJECTED so the
// QR is deterministic (the SDK never reads the wall clock).
type QRContext struct {
	PatientRef  string
	CoverageRef string
	OrderRef    string
	Authored    time.Time
}

// QuestionnaireFetchRequest is the request body for fetching a Questionnaire by
// canonical URL. Ported standalone from internal/dtr.QuestionnaireFetchRequest.
//
// Coverage is OPTIONAL: when a Da Vinci $questionnaire-package ingress carried a
// `coverage` parameter (the provider's Coverage resource, e.g. with a contained
// cms-payer Organization), the gateway carries that resource VERBATIM through this leg
// so the native-forward rebuild can re-emit it as the payer-required `coverage`
// parameter (a real Da Vinci payer 400s "The 'coverage' parameter is required (min=1)"
// otherwise). It is NEVER fabricated at the payer edge — only carried through
// (non-aggregation). The `omitempty` is load-bearing: when Coverage is nil the marshal
// is byte-identical to the canonical-only request, so BuildQuestionnaireFetch and the
// 8-scenario demo originator (which set no coverage) are unaffected (test/sdkparity).
type QuestionnaireFetchRequest struct {
	Canonical string          `json:"canonical"`
	Coverage  json.RawMessage `json:"coverage,omitempty"`
}

// BuildQuestionnaireFetch builds the DTR questionnaire-fetch request bytes for a
// canonical. Reimplements the substrate's json.Marshal(dtr.QuestionnaireFetchRequest{...})
// standalone (byte-identical; test/sdkparity asserts it).
func BuildQuestionnaireFetch(canonical string) ([]byte, error) {
	return json.Marshal(QuestionnaireFetchRequest{Canonical: canonical})
}

// BuildQuestionnairePackage wraps a bare FHIR Questionnaire into a one-entry Da Vinci
// $questionnaire-package collection Bundle — the SDK responder's UNIFORM DTR-fetch wire
// shape (§6.2). It is byte-identical to the substrate's
// gateway/engine.buildQuestionnairePackage (test/sdkparity asserts byte-parity): the
// canonical bytes are json.Marshal of map[string]any{"resourceType":"Bundle",
// "type":"collection","entry":[{"fullUrl":<url>,"resource":<questionnaire>}]} (Go sorts
// map keys, so the wire is {"entry":[{"fullUrl":<url>,"resource":<q>}],"resourceType":
// "Bundle","type":"collection"}). A FHIR collection Bundle requires every entry to carry a
// fullUrl (IG-HAPI $validate enforces it); the Questionnaire's canonical url is the entry
// identity. The sandbox payer carries no dependent Libraries/ValueSets, so this wrap is
// honestly deps-free; a real partner's package carries them.
func BuildQuestionnairePackage(questionnaire []byte) ([]byte, error) {
	url, err := ParseQuestionnaireURL(questionnaire) // validates resourceType + non-empty url
	if err != nil {
		return nil, fmt.Errorf("shnsdk: BuildQuestionnairePackage: %w", err)
	}
	pkg := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry": []map[string]any{
			{"fullUrl": url, "resource": json.RawMessage(questionnaire)},
		},
	}
	return json.Marshal(pkg)
}

// ExtractQuestionnaireFromPackage pulls the first Questionnaire entry out of a Da Vinci
// $questionnaire-package response Bundle, returning its bytes VERBATIM. STRICT and
// package-ONLY: the SDK consumer expects the uniform package shape (§6.2), so a bare
// Questionnaire — which has no entry array — naturally yields no Questionnaire entry and
// errors. There is NO dual-shape tolerance branch by design (full-uniform contract).
func ExtractQuestionnaireFromPackage(data []byte) ([]byte, error) {
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("shnsdk: parse $questionnaire-package bundle: %w", err)
	}
	for _, e := range bundle.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &probe); err != nil {
			continue
		}
		if probe.ResourceType == "Questionnaire" {
			return e.Resource, nil
		}
	}
	return nil, fmt.Errorf("shnsdk: $questionnaire-package response contains no Questionnaire")
}

// ParseQuestionnaireURL returns the url field from a marshalled FHIR Questionnaire.
// Ported standalone from internal/dtr.ParseQuestionnaireURL: errors if the
// resourceType is not "Questionnaire" or the url is absent/empty.
func ParseQuestionnaireURL(data []byte) (string, error) {
	var probe struct {
		ResourceType string  `json:"resourceType"`
		URL          *string `json:"url"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", fmt.Errorf("shnsdk: parse questionnaire url: %w", err)
	}
	if probe.ResourceType != "Questionnaire" {
		return "", fmt.Errorf("shnsdk: expected resourceType Questionnaire, got %q", probe.ResourceType)
	}
	if probe.URL == nil || *probe.URL == "" {
		return "", fmt.Errorf("shnsdk: questionnaire url is missing or empty")
	}
	return *probe.URL, nil
}

// FillQuestionnaire fills the sandbox DTR questionnaire into a conformant
// QuestionnaireResponse (answers + provider-LOCAL information-origin attribution),
// matching internal/dtr.AutoFill's wire output. SANDBOX-TARGETED STUB (DEF): NOT a
// general SDC engine — it handles the sandbox questionnaire's known items. It MUST
// FAIL LOUDLY (a clear error naming the supported canonical) on a questionnaire whose
// canonical/url it does not recognize, and NEVER emit a half-filled QR.
func FillQuestionnaire(questionnaireJSON []byte, cc ClinicalContext, qc QRContext) ([]byte, error) {
	var q fhir.Questionnaire
	if err := json.Unmarshal(questionnaireJSON, &q); err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaire: parse questionnaire: %w", err)
	}

	// Fail loud on an unrecognized questionnaire: the sandbox stub only knows the
	// lumbar-MRI questionnaire's items. NEVER emit a half-filled QR — return (nil, err)
	// before constructing anything.
	got := ""
	if q.Url != nil {
		got = *q.Url
	}
	if got != SupportedQuestionnaireCanonical {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaire: unsupported questionnaire %q (sandbox supports %q)", got, SupportedQuestionnaireCanonical)
	}

	var items []fhir.QuestionnaireResponseItem
	for _, qi := range q.Item {
		answer, ok := answerFor(qi.LinkId, cc)
		if !ok {
			continue
		}
		answer.Extension = []fhir.Extension{originExtension()}
		items = append(items, fhir.QuestionnaireResponseItem{
			LinkId: qi.LinkId,
			Answer: []fhir.QuestionnaireResponseItemAnswer{answer},
		})
	}

	authored := qc.Authored.UTC().Format(time.RFC3339)
	qr := fhir.QuestionnaireResponse{
		Status:        fhir.QuestionnaireResponseStatusCompleted,
		Questionnaire: questionnaireCanonical(q),
		Authored:      &authored,
		Subject:       &fhir.Reference{Reference: &qc.PatientRef},
		Extension:     dtrQRContextExtensions(qc),
		Item:          items,
	}
	raw, err := json.Marshal(qr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaire: marshal questionnaire response: %w", err)
	}
	return raw, nil
}

// answerFor maps a linkId to a QR answer from LOCAL data. ok=false means there is no
// local mapping for the linkId (the item is OMITTED — unanswered, never fabricated).
// Ported byte-for-byte from internal/dtr.answerFor (the SDK drops the FilledItem
// summary, which is an internal attribution surface).
func answerFor(linkID string, cc ClinicalContext) (fhir.QuestionnaireResponseItemAnswer, bool) {
	switch linkID {
	case "conservative-therapy-weeks":
		return fhir.QuestionnaireResponseItemAnswer{ValueInteger: intPtr(cc.ConservativeTherapyWeeks)}, true
	case "neuro-deficit":
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(cc.NeuroDeficit)}, true
	case "prior-imaging":
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(cc.PriorImaging)}, true
	case "prior-surgery":
		// Trigger flag (prior-surgery path): present ONLY when positive; a negative finding is OMITTED.
		if !cc.PriorSurgery {
			return fhir.QuestionnaireResponseItemAnswer{}, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true
	case "high-disability":
		// Trigger flag (high-disability path): present ONLY when positive.
		if !cc.HighDisability {
			return fhir.QuestionnaireResponseItemAnswer{}, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true
	case "patient-reported-required":
		// Trigger flag (patient attestation path): present ONLY when positive.
		if !cc.PatientReported {
			return fhir.QuestionnaireResponseItemAnswer{}, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true
	// functional-status-oswestry has no local source (intentionally omitted) — it must
	// be supplied by a clinician/patient attestation, so AutoFill leaves it blank and
	// the PAS pends.
	default:
		return fhir.QuestionnaireResponseItemAnswer{}, false
	}
}

// originExtension builds the FR-17 information-origin extension with source="auto".
// DTR 2.0.1 source="auto" carries only the "source" sub-extension. Byte-identical to
// internal/dtr.originExtension.
func originExtension() fhir.Extension {
	return fhir.Extension{
		Url:       informationOriginExt,
		Extension: []fhir.Extension{{Url: "source", ValueCode: strPtr("auto")}},
	}
}

// questionnaireCanonical returns q's canonical url, versioned when a version is set.
// Byte-identical to internal/dtr.questionnaireCanonical.
func questionnaireCanonical(q fhir.Questionnaire) *string {
	if q.Url == nil || *q.Url == "" {
		return nil
	}
	c := *q.Url
	if q.Version != nil && *q.Version != "" {
		c += "|" + *q.Version
	}
	return &c
}

// dtrQRContextExtensions builds the DTR QR-level extensions: 2 qr-context references
// (coverage + order) + intendedUse=withpa. Byte-identical to internal/dtr.
func dtrQRContextExtensions(qc QRContext) []fhir.Extension {
	return []fhir.Extension{
		{Url: qrContextExt, ValueReference: &fhir.Reference{Reference: strPtr(qc.CoverageRef)}},
		{Url: qrContextExt, ValueReference: &fhir.Reference{Reference: strPtr(qc.OrderRef)}},
		{Url: intendedUseExt, ValueCodeableConcept: &fhir.CodeableConcept{Coding: []fhir.Coding{{
			System:  strPtr(crdTempCodeSystem),
			Code:    strPtr("withpa"),
			Display: strPtr("Information needed for a prior authorization"),
		}}}},
	}
}

// sandboxLumbarQuestionnaireBytes holds the precomputed sandbox lumbar-MRI
// questionnaire bytes. Computed once at package init and served as fresh copies by
// SandboxLumbarQuestionnaire. The fixture is a fixed, compile-time-known struct, so
// json.Marshal cannot fail — a failure would be a programmer error and panics (matching
// the substrate's dtr.QuestionnaireFor panic posture).
var sandboxLumbarQuestionnaireBytes []byte

// cqlLibraryCanonical is the operated-CQL-engine Library the questionnaire's cqf-library points at.
// MUST MATCH internal/fhirseed (Library.url) — drift → CR can't resolve the Library → smoke red.
const cqlLibraryCanonical = "http://smarthealth.network/fhir/Library/LumbarMRICQL"

// cqlQuestionnaireExtensions builds the questionnaire-level SDC extension for CQL-backed
// population: cqf-library → the operated CQL Library. Byte-parallel with internal/dtr.
//
// NO launchContext: the operated $populate engine binds the CQL `context Patient` from the
// `subject` parameter (validated against HAPI CR — population works with subject alone). The SDC
// launchContext CodeSystem is also unresolvable by the US-Core runtime egress validator (it errors
// on the unknown code system) — so omitting it keeps the questionnaire egress-validatable without
// entangling the validator role with SDC. Declaring launchContext is a deferred realism item.
func cqlQuestionnaireExtensions() []fhir.Extension {
	return []fhir.Extension{
		{Url: "http://hl7.org/fhir/StructureDefinition/cqf-library", ValueCanonical: strPtr(cqlLibraryCanonical)},
	}
}

// initialExpression builds the per-item SDC initialExpression (text/cql) referencing a define in
// the LumbarMRICQL Library.
func initialExpression(define string) []fhir.Extension {
	return []fhir.Extension{{
		Url:             "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-initialExpression",
		ValueExpression: &fhir.Expression{Language: "text/cql", Expression: strPtr(define)},
	}}
}

func init() {
	q := fhir.Questionnaire{
		Id:      strPtr("pa-lumbar-mri"),
		Url:     strPtr(QuestionnaireCanonicalLumbarMRI),
		Version: strPtr("1.0.0"),
		Status:  fhir.PublicationStatusActive,
		// CQL-backed DTR questionnaire (operated $populate engine populates each item from the
		// LumbarMRICQL Library; FillQuestionnaire ignores these extensions and fills by linkId).
		// Byte-parallel with internal/dtr.QuestionnaireFor.
		Extension: cqlQuestionnaireExtensions(),
		Item: []fhir.QuestionnaireItem{
			{
				LinkId:    "conservative-therapy-weeks",
				Type:      fhir.QuestionnaireItemTypeInteger,
				Text:      strPtr("Weeks of conservative therapy completed"),
				Extension: initialExpression("ConservativeTherapyWeeks"),
			},
			{
				LinkId:    "neuro-deficit",
				Type:      fhir.QuestionnaireItemTypeBoolean,
				Text:      strPtr("Progressive neurological deficit present?"),
				Extension: initialExpression("NeuroDeficit"),
			},
			{
				LinkId:    "prior-imaging",
				Type:      fhir.QuestionnaireItemTypeBoolean,
				Text:      strPtr("Prior imaging performed?"),
				Extension: initialExpression("PriorImaging"),
			},
			{
				LinkId:    "prior-surgery",
				Type:      fhir.QuestionnaireItemTypeBoolean,
				Text:      strPtr("Prior lumbar surgery?"),
				Extension: initialExpression("PriorSurgery"),
			},
			{
				LinkId:    "high-disability",
				Type:      fhir.QuestionnaireItemTypeBoolean,
				Text:      strPtr("High disability index flag?"),
				Extension: initialExpression("HighDisability"),
			},
			{
				// Patient attestation trigger flag: when true, the functional-status-oswestry
				// item must be patient-attested. Absent / false means no patient-authorship
				// leg is needed (auto-approval and clinician-attestation paths are unchanged).
				LinkId:    "patient-reported-required",
				Type:      fhir.QuestionnaireItemTypeBoolean,
				Text:      strPtr("Patient-reported functional status required?"),
				Extension: initialExpression("PatientReportedRequired"),
			},
			{
				// No initialExpression — clinician/patient attestation (filled by the
				// attestation resume flow, not the operated engine).
				LinkId: "functional-status-oswestry",
				Type:   fhir.QuestionnaireItemTypeText,
				Text:   strPtr("Oswestry disability index (clinician-attested)"),
			},
		},
	}
	raw, err := json.Marshal(q)
	if err != nil {
		panic("shnsdk: marshal fixed sandbox lumbar questionnaire fixture: " + err.Error())
	}
	sandboxLumbarQuestionnaireBytes = raw
}

// SandboxLumbarQuestionnaire returns the FHIR Questionnaire JSON for the sandbox
// lumbar-MRI PA questionnaire. SANDBOX fixture — exported so reference
// adjudicators (tests, feedsmoke, the quickstart) can serve the sandbox PA flow; a
// real payer serves its own questionnaires from its Adjudicator. The bytes are
// byte-identical to dtr.QuestionnaireFor(crd.QuestionnaireCanonicalLumbarMRI)
// (proven by test/sdkparity/dtr_parity_test.go). Each call returns a fresh copy so
// callers may mutate the slice without affecting future calls.
func SandboxLumbarQuestionnaire() []byte {
	cp := make([]byte, len(sandboxLumbarQuestionnaireBytes))
	copy(cp, sandboxLumbarQuestionnaireBytes)
	return cp
}

func intPtr(i int) *int    { return &i }
func boolPtr(b bool) *bool { return &b }

// ClinicianAttestationExt is the attestation extension URL placed on a manually-
// entered QuestionnaireResponse.item (FR-16): clinician NPI + attestation text +
// date. It accompanies the FR-17 information-origin source="clinician" attribution.
const ClinicianAttestationExt = "http://smarthealth.network/fhir/StructureDefinition/clinician-attestation"

// Attestation is the clinician's attestation captured on a manual entry (FR-16).
type Attestation struct {
	NPI  string
	Text string
	When string // YYYY-MM-DD
}

// clinicianOriginExtension builds the FR-17 information-origin extension for a
// manually-entered clinician item. The clinician enters the value by hand (it was
// never auto-populated), so the DTR 2.0.1 informationOrigins code is "manual"
// (not "override", which means auto-populated-then-changed); dtrx-1 requires an
// author when source is "manual" or "override".
// practitionerRef is the Practitioner reference (e.g. "Practitioner/{NPI}").
func clinicianOriginExtension(practitionerRef string) fhir.Extension {
	sub := []fhir.Extension{
		{Url: "source", ValueCode: strPtr("manual")},
		// dtrx-1: author required when source="manual". The author sub-extension
		// is a complex extension (nested url="reference") per DTR 2.0.1 Extension.author.
		{Url: "author", Extension: []fhir.Extension{
			{Url: "reference", ValueString: strPtr(practitionerRef)},
		}},
	}
	return fhir.Extension{
		Url:       informationOriginExt,
		Extension: sub,
	}
}

// BuildManualAttestedItem returns the JSON of a single QuestionnaireResponseItem
// for a linkId answered by a clinician (FR-16/17). The item carries:
//   - a valueString answer of answer,
//   - the FR-17 information-origin extension with source="manual" + author=Practitioner/{NPI}
//     (the clinician hand-enters the value; dtrx-1 requires author),
//   - the clinician attestation extension (ClinicianAttestationExt) with the
//     NPI, attestation text, and date from att.
func BuildManualAttestedItem(linkID, answer string, att Attestation) ([]byte, error) {
	attestExt := fhir.Extension{
		Url: ClinicianAttestationExt,
		Extension: []fhir.Extension{
			{Url: "npi", ValueString: strPtr(att.NPI)},
			{Url: "text", ValueString: strPtr(att.Text)},
			{Url: "date", ValueDate: strPtr(att.When)},
		},
	}
	// FR-17 attribution rides on the ANSWER (DTR 2.0.1 context = item.answer).
	// clinician-attestation stays at item level (its declared context).
	// source="manual" is the DTR code for hand-entered data ("clinician" is not in
	// the informationOrigins value set; "override" would imply auto-then-changed).
	item := fhir.QuestionnaireResponseItem{
		LinkId: linkID,
		Answer: []fhir.QuestionnaireResponseItemAnswer{
			{
				ValueString: strPtr(answer),
				Extension:   []fhir.Extension{clinicianOriginExtension("Practitioner/" + att.NPI)},
			},
		},
		Extension: []fhir.Extension{attestExt},
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("dtr: marshal manual attested item %q: %w", linkID, err)
	}
	return raw, nil
}

// AmendQRWithItem appends a single QuestionnaireResponseItem (itemJSON) to the
// item array of a QuestionnaireResponse (qrJSON) and returns the re-marshalled
// QR. The operation preserves all other QR fields intact by operating at the
// JSON level: the QR is unmarshalled into a map of raw JSON values, the item
// array is extended, and the map is re-marshalled. This avoids lossy struct
// round-trips on fields the Go FHIR model may not capture.
// SetQuestionnaireResponseID sets the top-level id on a QuestionnaireResponse JSON
// so it can be the EXACT target of a Provenance reference ("QuestionnaireResponse/
// <id>"). An amended QR carrying clinician-entered supplemental evidence gets a
// stable id so the payer resolves the Provenance target to this resource, not just
// any QR (FR-32 attribution).
func SetQuestionnaireResponseID(qrJSON []byte, id string) ([]byte, error) {
	var qrMap map[string]json.RawMessage
	if err := json.Unmarshal(qrJSON, &qrMap); err != nil {
		return nil, fmt.Errorf("dtr: set qr id: unmarshal qr: %w", err)
	}
	idRaw, err := json.Marshal(id)
	if err != nil {
		return nil, fmt.Errorf("dtr: set qr id: marshal id: %w", err)
	}
	qrMap["id"] = json.RawMessage(idRaw)
	raw, err := json.Marshal(qrMap)
	if err != nil {
		return nil, fmt.Errorf("dtr: set qr id: marshal qr: %w", err)
	}
	return raw, nil
}

func AmendQRWithItem(qrJSON, itemJSON []byte) ([]byte, error) {
	// Unmarshal QR into a map preserving all fields as raw JSON.
	var qrMap map[string]json.RawMessage
	if err := json.Unmarshal(qrJSON, &qrMap); err != nil {
		return nil, fmt.Errorf("dtr: amend qr: unmarshal qr: %w", err)
	}

	// Unmarshal the existing item array (may be absent / null).
	var items []json.RawMessage
	if existing, ok := qrMap["item"]; ok && string(existing) != "null" {
		if err := json.Unmarshal(existing, &items); err != nil {
			return nil, fmt.Errorf("dtr: amend qr: unmarshal items: %w", err)
		}
	}

	// Append the new item.
	items = append(items, json.RawMessage(itemJSON))

	// Re-marshal the items array and put it back.
	itemsRaw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: marshal items: %w", err)
	}
	qrMap["item"] = json.RawMessage(itemsRaw)

	// Re-marshal the full QR.
	raw, err := json.Marshal(qrMap)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: marshal qr: %w", err)
	}
	return raw, nil
}

// QRSignatureExt is the standard FHIR/SDC extension carrying a Signature for a
// QuestionnaireResponse item — the conformant mechanism for a PATIENT attesting
// "these are my own responses" (FR-27): Signature.type Author's Signature, who =
// the Patient, when = timestamp. DTR's Standard Questionnaire derives from US Core
// QR + these CDex signature elements.
const QRSignatureExt = "http://hl7.org/fhir/StructureDefinition/questionnaireresponse-signature"

const (
	signatureTypeSystem    = "urn:iso-astm:E1762-95:2013"
	signatureAuthorCode    = "1.2.840.10065.1.12.1.1" // Author's Signature
	signatureAuthorDisplay = "Author's Signature"
)

// patientAttestedItemJSON is a minimal QR item carrying the patient's answer, the
// information-origin source="patient" attribution (FR-17), and the standard
// questionnaireresponse-signature (Author's Signature, who=Patient) — the patient
// attestation (FR-27). A custom struct keeps the Signature value[x] clean.
type patientAttestedItemJSON struct {
	LinkId    string                 `json:"linkId"`
	Answer    []patientAnswerJSON    `json:"answer"`
	Extension []patientItemExtension `json:"extension"`
}
type patientAnswerJSON struct {
	ValueString string                 `json:"valueString"`
	Extension   []patientItemExtension `json:"extension,omitempty"`
}
type patientItemExtension struct {
	URL            string                `json:"url"`
	Extension      []originSubExtension  `json:"extension,omitempty"`
	ValueSignature *patientSignatureJSON `json:"valueSignature,omitempty"`
}
type originSubExtension struct {
	URL         string               `json:"url"`
	ValueCode   string               `json:"valueCode,omitempty"`
	ValueDate   string               `json:"valueDate,omitempty"`
	ValueString string               `json:"valueString,omitempty"`
	Extension   []originSubExtension `json:"extension,omitempty"`
}
type patientSignatureJSON struct {
	Type []patientSigCoding `json:"type"`
	When string             `json:"when"`
	Who  patientSigWho      `json:"who"`
	Data string             `json:"data"`
}
type patientSigCoding struct {
	System  string `json:"system"`
	Code    string `json:"code"`
	Display string `json:"display"`
}
type patientSigWho struct {
	Reference string `json:"reference"`
}

// BuildPatientAttestedItem builds the patient-authored, attested QR item: the
// answer, an information-origin source="manual" + author=Patient/{patientRef} extension
// (FR-17; the patient enters the value by hand → DTR 2.0.1 code "manual", dtrx-1 requires author),
// and the standard questionnaireresponse-signature (Author's Signature, who=patientRef,
// when=when). data is a demo identity-token stub (IAL2 proofing is DEF-9).
func BuildPatientAttestedItem(linkID, answer, patientRef, when string) ([]byte, error) {
	// FR-17 attribution rides on the ANSWER (DTR 2.0.1 context = item.answer).
	// source="manual" is the DTR code for hand-entered data ("patient" is not in
	// the informationOrigins value set; "override" would imply auto-then-changed).
	// dtrx-1: author required when source="manual".
	// questionnaireresponse-signature stays at item level (its declared context).
	item := patientAttestedItemJSON{
		LinkId: linkID,
		Answer: []patientAnswerJSON{{
			ValueString: answer,
			Extension: []patientItemExtension{
				{
					URL: informationOriginExt,
					Extension: []originSubExtension{
						{URL: "source", ValueCode: "manual"},
						{URL: "author", Extension: []originSubExtension{{URL: "reference", ValueString: patientRef}}},
					},
				},
			},
		}},
		Extension: []patientItemExtension{
			{
				URL: QRSignatureExt,
				ValueSignature: &patientSignatureJSON{
					Type: []patientSigCoding{{System: signatureTypeSystem, Code: signatureAuthorCode, Display: signatureAuthorDisplay}},
					When: when + "T00:00:00Z",
					Who:  patientSigWho{Reference: patientRef},
					// Demo identity-token stub (DEF-9: IAL2 proofing deferred). base64 "patient-attest".
					Data: "cGF0aWVudC1hdHRlc3Q=",
				},
			},
		},
	}
	return json.Marshal(item)
}

// ValidatePatientAnswer checks that a patient-authored answer conforms to the
// known constraint for its Questionnaire item, BEFORE the Trust surface signs it.
// The Oswestry Disability Index (functional-status-oswestry) is a 0–100 integer
// percentage. An item with no known rule is rejected: the patient-authorship
// signer must not attest an item whose constraint it cannot enforce. Registering a
// new patient item means adding its rule here (additive). Full value-set/profile
// binding across all resources is the deferred FR-36 IG-validation slice.
func ValidatePatientAnswer(linkID, answer string) error {
	switch linkID {
	case "functional-status-oswestry":
		n, err := strconv.Atoi(answer)
		if err != nil {
			return fmt.Errorf("patient answer %q for %s is not an integer", answer, linkID)
		}
		if n < 0 || n > 100 {
			return fmt.Errorf("patient answer %d for %s is out of range [0,100]", n, linkID)
		}
		return nil
	case "3.2":
		// HomeHealthAssessment free-text functional-status item ("Functional limitations",
		// type text, 0-CQL) — the patient-authored narrative provider-data UC-07 attests. The
		// conformance constraint for a free-text item is a non-empty answer: the patient-authorship
		// signer must not attest an empty functional-status item. (The composite/sandbox UC-07 path
		// uses functional-status-oswestry above; this rule is the provider-data HHA analog.)
		if strings.TrimSpace(answer) == "" {
			return fmt.Errorf("patient answer for %s (HHA functional-status) must not be empty", linkID)
		}
		return nil
	default:
		return fmt.Errorf("no attestation rule for patient item %q", linkID)
	}
}
