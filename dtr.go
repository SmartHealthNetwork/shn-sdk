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

// DTR extension URLs the QR builders stamp (information-origin on answers; the
// QR-level qr-context / qr-coverage / intendedUse set).
const (
	informationOriginExt = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/information-origin"
	qrContextExt         = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/qr-context"
	// qrCoverageExt is the DTR 2.2-only dedicated Coverage-reference extension
	// (StructureDefinition-qr-coverage.json, min=1 max=* at 2.2 — DTR package differential).
	// At 2.0/2.1 the coverage reference instead rides a qrContextExt entry, as before.
	qrCoverageExt  = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/qr-coverage"
	intendedUseExt = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/intendedUse"
	// crdTempCodeSystem is the CRD CodeSystem the DocReason value set draws from at
	// CRD 2.0.1/2.1.0; "withpa" = information needed for a prior authorization.
	crdTempCodeSystem = "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp"
	// crdCoverageInformationCodeSystem is where CRD 2.2.1 moved those same DocReason
	// concepts (CodeSystem-coverage-information-codes.json — {withpa, withclaim,
	// withorder, retain-doc, …}; CodeSystem-temp at 2.2.1 no longer defines them).
	// Selected per line by DTRDef.IntendedUseCodeSystem.
	crdCoverageInformationCodeSystem = "http://hl7.org/fhir/us/davinci-crd/CodeSystem/coverage-information-codes"
)

// dtrBundleBaseURL is the deterministic base for a $questionnaire-package entry
// fullUrl when embedding a caller-supplied QuestionnaireResponse (DTR line 2.2's
// DTR-QPackageBundle profile requires one — DTR package differential). Same
// convention/value as pasBundleBaseURL (sdk/pas.go); kept as its own named constant
// here so dtr.go stays self-contained.
const dtrBundleBaseURL = "https://shn.example/fhir"

// questionnaireResponseEntryFullURL derives the resolvable fullUrl for a
// QuestionnaireResponse $questionnaire-package Bundle entry (FHIR bdl-7-style:
// fullUrl consistent with Resource.id, mirroring pasFullURLFor's convention).
// Errors — never fabricates an id — if the resource is not a QuestionnaireResponse
// or carries no id.
func questionnaireResponseEntryFullURL(questionnaireResponse []byte) (string, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(questionnaireResponse, &probe); err != nil {
		return "", fmt.Errorf("questionnaireResponse is not valid json: %w", err)
	}
	if probe.ResourceType != "QuestionnaireResponse" {
		return "", fmt.Errorf("expected resourceType QuestionnaireResponse, got %q", probe.ResourceType)
	}
	if probe.ID == "" {
		return "", fmt.Errorf("questionnaireResponse has no id for entry fullUrl")
	}
	return dtrBundleBaseURL + "/QuestionnaireResponse/" + probe.ID, nil
}

// ClinicalContext is the provider-LOCAL clinical data FillQuestionnaire answers
// from (the sandbox auto-approval fields). The *Ref fields are carried even
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
	// patient-reported-required=true trigger item.
	PatientReported bool
}

// QRContext carries the DTR context the QuestionnaireResponse is completed in: the
// patient subject, the coverage + order qr-context references, and the authoring
// time. Authored is INJECTED so the
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

// BuildQuestionnaireFetchWithCoverage builds the DTR questionnaire-fetch request
// bytes for a canonical, additionally carrying coverageJSON in the EXISTING
// QuestionnaireFetchRequest.Coverage field. coverageJSON must carry a Coverage.id — a
// 2.2 (`qr-required`) responder derives the QR shell's coverage reference from it and
// fails closed without it (the qr-required id obligation). BuildQuestionnaireFetch (the
// canonical-only builder) REMAINS for 2.0-only callers that carry no coverage.
func BuildQuestionnaireFetchWithCoverage(canonical string, coverageJSON []byte) ([]byte, error) {
	if canonical == "" {
		return nil, fmt.Errorf("shnsdk: BuildQuestionnaireFetchWithCoverage: canonical is required")
	}
	if len(coverageJSON) == 0 {
		return nil, fmt.Errorf("shnsdk: BuildQuestionnaireFetchWithCoverage: coverageJSON is required")
	}
	return json.Marshal(QuestionnaireFetchRequest{Canonical: canonical, Coverage: json.RawMessage(coverageJSON)})
}

// BuildQuestionnairePackage wraps a bare FHIR Questionnaire into a one-entry Da Vinci
// $questionnaire-package collection Bundle — the SDK responder's UNIFORM DTR-fetch wire
// shape (§6.2). It is BuildQuestionnairePackageAtLine("2.0", questionnaire, nil),
// byte-identical (regression-fenced by sdk/dtr_test.go; the pinned wire-shape string is
// also independently held by the substrate's gateway/engine.buildQuestionnairePackage —
// gateway/engine/davincimap_test.go — since that function is unexported and cannot be
// cross-imported). The canonical bytes are json.Marshal of
// map[string]any{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":<url>,
// "resource":<questionnaire>}]} (Go sorts map keys, so the wire is
// {"entry":[{"fullUrl":<url>,"resource":<q>}],"resourceType":"Bundle","type":"collection"}).
// A FHIR collection Bundle requires every entry to carry a fullUrl (IG-HAPI $validate
// enforces it); the Questionnaire's canonical url is the entry identity. The sandbox
// payer carries no dependent Libraries/ValueSets, so this wrap is honestly deps-free; a
// real partner's package carries them. Use BuildQuestionnairePackageAtLine to target
// 2.1/2.2 (DTR package differential: DTR 2.2 requires a QuestionnaireResponse entry too).
func BuildQuestionnairePackage(questionnaire []byte) ([]byte, error) {
	return BuildQuestionnairePackageAtLine("2.0", questionnaire, nil)
}

// BuildQuestionnairePackageAtLine is BuildQuestionnairePackage parameterized by DTR
// line ("2.0", "2.1", "2.2"). questionnaireResponse is OPTIONAL at "2.0"/"2.1" and,
// when supplied, is embedded VERBATIM as a second Bundle entry (never fabricated) —
// its fullUrl is derived from its own resourceType+id (questionnaireResponseEntryFullURL).
// At "2.2" (DTRLineDef's QuestionnairePackageReturnShape=="qr-required") it is
// MANDATORY: the DTR-QPackageBundle profile requires Bundle.entry:questionnaireResponse
// min=1 (DTR package differential) — an empty questionnaireResponse errors rather than emit a
// non-conformant package. Unknown line -> error (fail-closed, never a silent 2.0 fallback).
func BuildQuestionnairePackageAtLine(line string, questionnaire, questionnaireResponse []byte) ([]byte, error) {
	def, ok := DTRLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: BuildQuestionnairePackageAtLine: unknown DTR line %q", line)
	}
	return buildQuestionnairePackage(def, questionnaire, questionnaireResponse)
}

func buildQuestionnairePackage(def DTRDef, questionnaire, questionnaireResponse []byte) ([]byte, error) {
	url, err := ParseQuestionnaireURL(questionnaire) // validates resourceType + non-empty url
	if err != nil {
		return nil, fmt.Errorf("shnsdk: BuildQuestionnairePackageAtLine: %w", err)
	}
	if def.QuestionnairePackageReturnShape == "qr-required" && len(questionnaireResponse) == 0 {
		return nil, fmt.Errorf("shnsdk: BuildQuestionnairePackageAtLine: DTR line %q (profile DTR-QPackageBundle) requires a QuestionnaireResponse entry (Bundle.entry:questionnaireResponse min=1) but none was supplied", def.Line)
	}
	entries := []map[string]any{
		{"fullUrl": url, "resource": json.RawMessage(questionnaire)},
	}
	if len(questionnaireResponse) > 0 {
		qrURL, err := questionnaireResponseEntryFullURL(questionnaireResponse)
		if err != nil {
			return nil, fmt.Errorf("shnsdk: BuildQuestionnairePackageAtLine: %w", err)
		}
		entries = append(entries, map[string]any{"fullUrl": qrURL, "resource": json.RawMessage(questionnaireResponse)})
	}
	pkg := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry":        entries,
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
// QuestionnaireResponse (answers + provider-LOCAL information-origin attribution).
// It is the ONE sandbox QR builder: the gateway's managed populator, the conformance
// corpus generator, and its stale-guard all call it. SANDBOX-TARGETED STUB (DEF): NOT a
// general SDC engine — it handles the sandbox questionnaire's known items. It MUST
// FAIL LOUDLY (a clear error naming the supported canonical) on a questionnaire whose
// canonical/url it does not recognize, and NEVER emit a half-filled QR. It is
// FillQuestionnaireAtLine("2.0", …), byte-identical (regression-fenced by
// sdk/dtr_test.go). Use FillQuestionnaireAtLine to target 2.1/2.2 (DTR package differential:
// the qr-coverage extension + the "auto-client" origin code at 2.2).
func FillQuestionnaire(questionnaireJSON []byte, cc ClinicalContext, qc QRContext) ([]byte, error) {
	return FillQuestionnaireAtLine("2.0", questionnaireJSON, cc, qc)
}

// FillQuestionnaireAtLine is FillQuestionnaire parameterized by DTR line ("2.0",
// "2.1", "2.2"). Unknown line -> error (fail-closed, never a silent 2.0 fallback).
func FillQuestionnaireAtLine(line string, questionnaireJSON []byte, cc ClinicalContext, qc QRContext) ([]byte, error) {
	def, ok := DTRLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireAtLine: unknown DTR line %q", line)
	}
	return fillQuestionnaire(def, questionnaireJSON, cc, qc)
}

func fillQuestionnaire(def DTRDef, questionnaireJSON []byte, cc ClinicalContext, qc QRContext) ([]byte, error) {
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

	// ONE structure-driven walk (shared with FillQuestionnaireFromAnswers): groups
	// recurse and mirror their nesting into the QR, display items vanish, and each
	// leaf is answered from LOCAL data by linkId. A leaf this stub does not know is
	// fixture drift inside the ONE canonical it is fenced to, so it is refused —
	// never skipped — because a skipped leaf is a quietly incomplete QR, which is
	// the half-fill this function exists to rule out. A leaf it knows but
	// has no local value for (a negative trigger flag; the clinician/patient-
	// attested Oswestry item) is omitted, never fabricated.
	items, err := walkQuestionnaireItems(q.Item, func(qi fhir.QuestionnaireItem) (fhir.QuestionnaireResponseItemAnswer, bool, error) {
		answer, known, answered := answerFor(qi.LinkId, cc)
		if !known {
			return fhir.QuestionnaireResponseItemAnswer{}, false, fmt.Errorf("unknown item %q in sandbox questionnaire %q (the sandbox fill knows its leaves by linkId and never half-fills)", qi.LinkId, SupportedQuestionnaireCanonical)
		}
		if !answered {
			return fhir.QuestionnaireResponseItemAnswer{}, false, nil
		}
		answer.Extension = []fhir.Extension{originExtension(def)}
		return answer, true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaire: %w", err)
	}

	authored := qc.Authored.UTC().Format(time.RFC3339)
	qr := fhir.QuestionnaireResponse{
		Status:        fhir.QuestionnaireResponseStatusCompleted,
		Questionnaire: questionnaireCanonical(q),
		Authored:      &authored,
		Subject:       &fhir.Reference{Reference: &qc.PatientRef},
		Extension:     dtrQRContextExtensions(def, qc),
		Item:          items,
	}
	raw, err := json.Marshal(qr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaire: marshal questionnaire response: %w", err)
	}
	return raw, nil
}

// answerFor maps a sandbox leaf linkId to a QR answer from LOCAL data. Three-way,
// not two-way, because the walker must tell "this stub does not know the leaf"
// apart from "the stub knows it and has nothing local to say":
//
//   - known=false: the linkId is not a sandbox leaf at all — fixture drift, refused
//     by the caller (never a silent skip);
//   - known=true, answered=false: a sandbox leaf with no local value — a negative
//     trigger flag, or functional-status-oswestry, which has no local source and is
//     supplied by the clinician/patient attestation — OMITTED, never fabricated;
//   - known=true, answered=true: the answer.
func answerFor(linkID string, cc ClinicalContext) (answer fhir.QuestionnaireResponseItemAnswer, known, answered bool) {
	switch linkID {
	case "conservative-therapy-weeks":
		return fhir.QuestionnaireResponseItemAnswer{ValueInteger: intPtr(cc.ConservativeTherapyWeeks)}, true, true
	case "neuro-deficit":
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(cc.NeuroDeficit)}, true, true
	case "prior-imaging":
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(cc.PriorImaging)}, true, true
	case "prior-surgery":
		// Trigger flag (prior-surgery path): present ONLY when positive; a negative finding is OMITTED.
		if !cc.PriorSurgery {
			return fhir.QuestionnaireResponseItemAnswer{}, true, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true, true
	case "high-disability":
		// Trigger flag (high-disability path): present ONLY when positive.
		if !cc.HighDisability {
			return fhir.QuestionnaireResponseItemAnswer{}, true, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true, true
	case "patient-reported-required":
		// Trigger flag (patient attestation path): present ONLY when positive.
		if !cc.PatientReported {
			return fhir.QuestionnaireResponseItemAnswer{}, true, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true, true
	case "functional-status-oswestry":
		// No local source (intentionally omitted) — it must be supplied by a
		// clinician/patient attestation, so the fill leaves it blank and the PAS pends.
		return fhir.QuestionnaireResponseItemAnswer{}, true, false
	default:
		return fhir.QuestionnaireResponseItemAnswer{}, false, false
	}
}

// originExtension builds the FR-17 information-origin extension for a CQL/EHR-
// auto-populated answer, with the source code taken from def.AutoOriginSourceCode
// ("auto" at 2.0/2.1's CodeSystem/temp; "auto-client" at 2.2's renamed
// CodeSystem/dtr-informationorigin-codes — DTR package differential). DTR
// source="auto"/"auto-client" carries only the "source" sub-extension.
func originExtension(def DTRDef) fhir.Extension {
	return fhir.Extension{
		Url:       informationOriginExt,
		Extension: []fhir.Extension{{Url: "source", ValueCode: strPtr(def.AutoOriginSourceCode)}},
	}
}

// questionnaireCanonical returns q's canonical url, versioned when a version is set.
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

// dtrQRContextExtensions builds the DTR QR-level extensions. At 2.0/2.1
// (!def.SingleCoverageConstraint): 2 qr-context references (coverage + order) +
// intendedUse=withpa (3 total — matches dtr-questionnaireresponse 2.0.1/2.1.0's
// extension min=3). At 2.2 (def.SingleCoverageConstraint): the coverage reference
// moves to its own required qr-coverage extension (StructureDefinition-qr-
// coverage.json, min=1 max=*); the qr-context slice — now optional (min=0) — carries
// the order reference only; intendedUse keeps min=1 (it gained mustSupport=true,
// which is profile metadata, not a wire field) but its REQUIRED DocReason binding
// resolves to a CRD 2.2.1 value set that draws "withpa" from a renamed CodeSystem —
// so the coding's system comes from def.IntendedUseCodeSystem (DTR package differential +
// the 2.2-lane conformance fix wave).
func dtrQRContextExtensions(def DTRDef, qc QRContext) []fhir.Extension {
	var coverage fhir.Extension
	if def.SingleCoverageConstraint {
		coverage = fhir.Extension{Url: qrCoverageExt, ValueReference: &fhir.Reference{Reference: strPtr(qc.CoverageRef)}}
	} else {
		coverage = fhir.Extension{Url: qrContextExt, ValueReference: &fhir.Reference{Reference: strPtr(qc.CoverageRef)}}
	}
	return []fhir.Extension{
		coverage,
		{Url: qrContextExt, ValueReference: &fhir.Reference{Reference: strPtr(qc.OrderRef)}},
		{Url: intendedUseExt, ValueCodeableConcept: &fhir.CodeableConcept{Coding: []fhir.Coding{{
			System:  strPtr(def.IntendedUseCodeSystem),
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
// MUST MATCH gateway/fhirseed.SandboxLumbarLibrary's Library.url — pinned by the substrate drift
// test TestSDKQuestionnaireCanonicalMatchesLibrary.
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
		// GROUPED the way real Da Vinci payer questionnaires are grouped (the captured
		// reference-implementation package nests to depth 2; this fixture nests to depth
		// 3 on the item.item axis): a flat fixture hid every one-level item walker for
		// a whole release line. Leaf linkIds, order, texts, and CQL initialExpressions
		// are unchanged — only the structure is new. No group repeats (a repeating
		// group would legitimately yield duplicate linkIds, which the adjudicator
		// refuses as ambiguous; that refusal is exercised by its own tests, not here).
		Item: []fhir.QuestionnaireItem{
			{
				LinkId: "clinical-history",
				Type:   fhir.QuestionnaireItemTypeGroup,
				Text:   strPtr("Clinical history"),
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
						LinkId: "prior-treatment",
						Type:   fhir.QuestionnaireItemTypeGroup,
						Text:   strPtr("Prior treatment"),
						Item: []fhir.QuestionnaireItem{
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
						},
					},
				},
			},
			{
				LinkId: "functional-status",
				Type:   fhir.QuestionnaireItemTypeGroup,
				Text:   strPtr("Functional status"),
				Item: []fhir.QuestionnaireItem{
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

// SetQuestionnaireResponseID sets the top-level id on a QuestionnaireResponse JSON
// so it can be the EXACT target of a Provenance reference ("QuestionnaireResponse/
// <id>"). An amended QR carrying clinician-entered supplemental evidence gets a
// stable id so the payer resolves the Provenance target to this resource, not just
// any QR (FR-32 attribution). Operates at the JSON level (a map of raw values) so
// every other field survives byte-for-byte.
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

// AmendQRWithItem amends a QuestionnaireResponse with a single item WITHOUT the
// questionnaire: an existing item with the amended linkId — at any depth, on either
// nesting axis — is superseded in place (two occurrences are refused as ambiguous);
// when absent, the item is appended at the TOP LEVEL, which is only right for a
// questionnaire whose item is top-level.
//
// Deprecated: use AmendQRWithItemIn, which places an absent item where the
// questionnaire puts it (inside its group, or under its parent question's answer).
// This form stays so a gateway pinned to an sdk that predates AmendQRWithItemIn
// keeps building; it is removed once no published gateway calls it.
func AmendQRWithItem(qrJSON, itemJSON []byte) ([]byte, error) {
	var qrMap map[string]json.RawMessage
	if err := json.Unmarshal(qrJSON, &qrMap); err != nil {
		return nil, fmt.Errorf("dtr: amend qr: unmarshal qr: %w", err)
	}
	newLink := qrRawLinkID(itemJSON)
	if newLink == "" {
		return nil, fmt.Errorf("dtr: amend qr: amended item has no linkId")
	}
	var items []json.RawMessage
	if existing, ok := qrMap["item"]; ok && string(existing) != "null" {
		if err := json.Unmarshal(existing, &items); err != nil {
			return nil, fmt.Errorf("dtr: amend qr: unmarshal items: %w", err)
		}
	}
	items, n, err := qrSupersede(items, newLink, itemJSON)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: %w", err)
	}
	switch {
	case n > 1:
		return nil, fmt.Errorf("dtr: amend qr: %d items share linkId %q (a repeating group): which one the amendment supersedes is ambiguous", n, newLink)
	case n == 0:
		items = append(items, json.RawMessage(itemJSON))
	}
	itemsRaw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: marshal items: %w", err)
	}
	qrMap["item"] = json.RawMessage(itemsRaw)
	raw, err := json.Marshal(qrMap)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: marshal qr: %w", err)
	}
	return raw, nil
}

// AmendQRWithItemIn places a single QuestionnaireResponseItem (itemJSON) into a
// QuestionnaireResponse (qrJSON) WHERE ITS QUESTIONNAIRE (questionnaireJSON) PUTS
// THAT ITEM, and returns the re-marshalled QR:
//
//   - SUPERSEDE, don't duplicate. If the QR already holds an item with the
//     amended linkId — at any depth, on either nesting axis — that item is
//     replaced where it sits. An amendment carries the authoritative answer for
//     its linkId: the clinician's or patient's attested value replaces whatever
//     the populate step left there (an operated $populate engine may emit the
//     item's unanswered shell inside its group). Appending instead produced a QR
//     asserting two items for one question, which the adjudicator refuses rather
//     than guessing which is the clinical fact. Two existing occurrences (a
//     repeating group) are ambiguous and refused for the same reason.
//   - Otherwise PLACE by the questionnaire's structure: descend the QR along the
//     item's ancestor path, creating a missing GROUP shell (it now has an answered
//     descendant) in questionnaire order among its siblings; an ancestor that is a
//     QUESTION must already carry exactly one answer in the QR — the child rides
//     under answer.item, and a missing or multiple parent answer is refused, never
//     fabricated or chosen. Within its group the item is inserted in questionnaire
//     order, so the QR keeps mirroring the questionnaire after the amendment.
//   - A linkId the questionnaire does not define is refused.
//
// Appending at the top level was wrong for any grouped questionnaire — including
// every Da Vinci reference questionnaire, whose attested items live under groups.
//
// Operates at the JSON level (maps of raw values; only the items on the touched
// path are re-marshalled) so fields the Go FHIR model does not capture survive.
// All three inputs are validated by resourceType / linkId, so swapped arguments
// fail loudly rather than amend the wrong document.
func AmendQRWithItemIn(qrJSON, questionnaireJSON, itemJSON []byte) ([]byte, error) {
	var qrMap map[string]json.RawMessage
	if err := json.Unmarshal(qrJSON, &qrMap); err != nil {
		return nil, fmt.Errorf("dtr: amend qr: unmarshal qr: %w", err)
	}
	if rt := rawResourceType(qrMap); rt != "QuestionnaireResponse" {
		return nil, fmt.Errorf("dtr: amend qr: qrJSON is a %q, want a QuestionnaireResponse", rt)
	}
	var qProbe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(questionnaireJSON, &qProbe); err != nil {
		return nil, fmt.Errorf("dtr: amend qr: unmarshal questionnaire: %w", err)
	}
	if qProbe.ResourceType != "Questionnaire" {
		return nil, fmt.Errorf("dtr: amend qr: questionnaireJSON is a %q, want a Questionnaire", qProbe.ResourceType)
	}
	var q fhir.Questionnaire
	if err := json.Unmarshal(questionnaireJSON, &q); err != nil {
		return nil, fmt.Errorf("dtr: amend qr: parse questionnaire: %w", err)
	}
	newLink := qrRawLinkID(itemJSON)
	if newLink == "" {
		return nil, fmt.Errorf("dtr: amend qr: amended item has no linkId")
	}
	path, ok := questionnaireItemPath(q.Item, newLink)
	if !ok {
		canonical := ""
		if q.Url != nil {
			canonical = *q.Url
		}
		return nil, fmt.Errorf("dtr: amend qr: item %q is not an item of questionnaire %q", newLink, canonical)
	}

	// Unmarshal the existing item array (may be absent / null).
	var items []json.RawMessage
	if existing, ok := qrMap["item"]; ok && string(existing) != "null" {
		if err := json.Unmarshal(existing, &items); err != nil {
			return nil, fmt.Errorf("dtr: amend qr: unmarshal items: %w", err)
		}
	}

	items, n, err := qrSupersede(items, newLink, itemJSON)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: %w", err)
	}
	switch {
	case n > 1:
		return nil, fmt.Errorf("dtr: amend qr: %d items share linkId %q (a repeating group): which one the amendment supersedes is ambiguous", n, newLink)
	case n == 0:
		items, err = qrPlace(items, path, itemJSON)
		if err != nil {
			return nil, fmt.Errorf("dtr: amend qr: %w", err)
		}
	}

	itemsRaw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: marshal items: %w", err)
	}
	qrMap["item"] = json.RawMessage(itemsRaw)
	raw, err := json.Marshal(qrMap)
	if err != nil {
		return nil, fmt.Errorf("dtr: amend qr: marshal qr: %w", err)
	}
	return raw, nil
}

// qrPathStep is one level of an item's ancestry in a Questionnaire, root first,
// ending with the item itself. siblings is every linkId at that level in
// questionnaire order — what placement uses to keep the QR mirroring it.
type qrPathStep struct {
	linkID   string
	group    bool
	siblings []string
}

// questionnaireItemPath finds linkID anywhere in items (depth-first) and returns
// its ancestry as qrPathSteps, root first, ending with the item's own step.
func questionnaireItemPath(items []fhir.QuestionnaireItem, linkID string) ([]qrPathStep, bool) {
	siblings := make([]string, 0, len(items))
	for _, qi := range items {
		siblings = append(siblings, qi.LinkId)
	}
	for _, qi := range items {
		step := qrPathStep{linkID: qi.LinkId, group: qi.Type == fhir.QuestionnaireItemTypeGroup, siblings: siblings}
		if qi.LinkId == linkID {
			return []qrPathStep{step}, true
		}
		if rest, ok := questionnaireItemPath(qi.Item, linkID); ok {
			return append([]qrPathStep{step}, rest...), true
		}
	}
	return nil, false
}

func rawResourceType(m map[string]json.RawMessage) string {
	var rt string
	_ = json.Unmarshal(m["resourceType"], &rt)
	return rt
}

func qrRawLinkID(item json.RawMessage) string {
	var probe struct {
		LinkId string `json:"linkId"`
	}
	_ = json.Unmarshal(item, &probe)
	return probe.LinkId
}

// qrSupersede replaces every item with linkID anywhere under items — on both
// nesting axes — with replacement, and returns the count it replaced. Only the
// containers on a touched path are re-marshalled; untouched items keep their bytes.
func qrSupersede(items []json.RawMessage, linkID string, replacement json.RawMessage) ([]json.RawMessage, int, error) {
	total := 0
	for i, raw := range items {
		if qrRawLinkID(raw) == linkID {
			items[i] = replacement
			total++
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, 0, fmt.Errorf("unmarshal item %d: %w", i, err)
		}
		touched := 0
		if sub, ok := m["item"]; ok && string(sub) != "null" {
			var children []json.RawMessage
			if err := json.Unmarshal(sub, &children); err != nil {
				return nil, 0, fmt.Errorf("unmarshal item %q children: %w", qrRawLinkID(raw), err)
			}
			children, n, err := qrSupersede(children, linkID, replacement)
			if err != nil {
				return nil, 0, err
			}
			if n > 0 {
				m["item"], _ = json.Marshal(children)
				touched += n
			}
		}
		if ans, ok := m["answer"]; ok && string(ans) != "null" {
			var answers []map[string]json.RawMessage
			if err := json.Unmarshal(ans, &answers); err != nil {
				return nil, 0, fmt.Errorf("unmarshal item %q answers: %w", qrRawLinkID(raw), err)
			}
			changed := false
			for _, a := range answers {
				sub, ok := a["item"]
				if !ok || string(sub) == "null" {
					continue
				}
				var children []json.RawMessage
				if err := json.Unmarshal(sub, &children); err != nil {
					return nil, 0, fmt.Errorf("unmarshal item %q answer children: %w", qrRawLinkID(raw), err)
				}
				children, n, err := qrSupersede(children, linkID, replacement)
				if err != nil {
					return nil, 0, err
				}
				if n > 0 {
					a["item"], _ = json.Marshal(children)
					changed = true
					touched += n
				}
			}
			if changed {
				m["answer"], _ = json.Marshal(answers)
			}
		}
		if touched > 0 {
			items[i], _ = json.Marshal(m)
			total += touched
		}
	}
	return items, total, nil
}

// qrPlace inserts item into items along path (the item's questionnaire ancestry,
// root first, ending with the item itself), creating missing group shells and
// hanging answer-axis children under their parent's single answer.
func qrPlace(items []json.RawMessage, path []qrPathStep, item json.RawMessage) ([]json.RawMessage, error) {
	step := path[0]
	if len(path) == 1 {
		return qrInsertOrdered(items, step, item), nil
	}
	idx := -1
	for i, raw := range items {
		if qrRawLinkID(raw) == step.linkID {
			idx = i
			break
		}
	}
	if idx < 0 {
		if !step.group {
			return nil, fmt.Errorf("item %q is a child of question %q, which the QR does not answer: refusing to fabricate a parent answer to hang it from", path[len(path)-1].linkID, step.linkID)
		}
		children, err := qrPlace(nil, path[1:], item)
		if err != nil {
			return nil, err
		}
		shell, err := json.Marshal(map[string]json.RawMessage{
			"linkId": mustRawJSON(step.linkID),
			"item":   mustRawJSON(children),
		})
		if err != nil {
			return nil, fmt.Errorf("marshal group shell %q: %w", step.linkID, err)
		}
		return qrInsertOrdered(items, step, shell), nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(items[idx], &m); err != nil {
		return nil, fmt.Errorf("unmarshal item %q: %w", step.linkID, err)
	}
	if step.group {
		var children []json.RawMessage
		if sub, ok := m["item"]; ok && string(sub) != "null" {
			if err := json.Unmarshal(sub, &children); err != nil {
				return nil, fmt.Errorf("unmarshal group %q children: %w", step.linkID, err)
			}
		}
		children, err := qrPlace(children, path[1:], item)
		if err != nil {
			return nil, err
		}
		m["item"] = mustRawJSON(children)
	} else {
		var answers []map[string]json.RawMessage
		if ans, ok := m["answer"]; ok && string(ans) != "null" {
			if err := json.Unmarshal(ans, &answers); err != nil {
				return nil, fmt.Errorf("unmarshal question %q answers: %w", step.linkID, err)
			}
		}
		switch len(answers) {
		case 0:
			return nil, fmt.Errorf("item %q is a child of question %q, which the QR does not answer: refusing to fabricate a parent answer to hang it from", path[len(path)-1].linkID, step.linkID)
		case 1:
		default:
			return nil, fmt.Errorf("item %q is a child of question %q, which carries %d answers: which answer it belongs under is ambiguous", path[len(path)-1].linkID, step.linkID, len(answers))
		}
		var children []json.RawMessage
		if sub, ok := answers[0]["item"]; ok && string(sub) != "null" {
			if err := json.Unmarshal(sub, &children); err != nil {
				return nil, fmt.Errorf("unmarshal question %q answer children: %w", step.linkID, err)
			}
		}
		children, err := qrPlace(children, path[1:], item)
		if err != nil {
			return nil, err
		}
		answers[0]["item"] = mustRawJSON(children)
		m["answer"] = mustRawJSON(answers)
	}
	items[idx] = mustRawJSON(m)
	return items, nil
}

// qrInsertOrdered inserts item among items in questionnaire order: before the
// first existing sibling the questionnaire lists AFTER it; at the end when there
// is none (or when no existing item is known to the questionnaire).
func qrInsertOrdered(items []json.RawMessage, step qrPathStep, item json.RawMessage) []json.RawMessage {
	rank := map[string]int{}
	for i, l := range step.siblings {
		rank[l] = i
	}
	mine := rank[step.linkID]
	at := len(items)
	for i, raw := range items {
		if r, ok := rank[qrRawLinkID(raw)]; ok && r > mine {
			at = i
			break
		}
	}
	out := make([]json.RawMessage, 0, len(items)+1)
	out = append(out, items[:at]...)
	out = append(out, item)
	out = append(out, items[at:]...)
	return out
}

// mustRawJSON marshals a value the caller already unmarshalled from JSON (so a
// marshal failure would be a programmer error, not an input error).
func mustRawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("shnsdk: re-marshal of parsed JSON failed: " + err.Error())
	}
	return b
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
