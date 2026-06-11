package shnsdk

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// SupportedQuestionnaireCanonical is the ONE DTR questionnaire FillQuestionnaire
// recognizes: the sandbox UC-03 lumbar-MRI PA questionnaire. It MUST equal the
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
// from. Ported standalone from internal/dtr.ClinicalContext (the sandbox UC-03
// fields). The *Ref fields are carried for parity with the substrate struct even
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
	// (UC-07). When set, FillQuestionnaire emits the patient-reported-required=true
	// trigger item, matching the substrate's AutoFill.
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
type QuestionnaireFetchRequest struct {
	Canonical string `json:"canonical"`
}

// BuildQuestionnaireFetch builds the DTR questionnaire-fetch request bytes for a
// canonical. Reimplements the substrate's json.Marshal(dtr.QuestionnaireFetchRequest{...})
// standalone (byte-identical; test/sdkparity asserts it).
func BuildQuestionnaireFetch(canonical string) ([]byte, error) {
	return json.Marshal(QuestionnaireFetchRequest{Canonical: canonical})
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

// FillQuestionnaire fills the sandbox UC-03 DTR questionnaire into a conformant
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
		// Trigger flag (UC-04): present ONLY when positive; a negative finding is OMITTED.
		if !cc.PriorSurgery {
			return fhir.QuestionnaireResponseItemAnswer{}, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true
	case "high-disability":
		// Trigger flag (UC-06): present ONLY when positive.
		if !cc.HighDisability {
			return fhir.QuestionnaireResponseItemAnswer{}, false
		}
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: boolPtr(true)}, true
	case "patient-reported-required":
		// Trigger flag (UC-07): present ONLY when positive.
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

func intPtr(i int) *int    { return &i }
func boolPtr(b bool) *bool { return &b }
