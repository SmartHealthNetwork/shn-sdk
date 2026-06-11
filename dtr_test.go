package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sandboxQuestionnaireJSON is a minimal-but-faithful copy of the sandbox UC-03
// lumbar-MRI questionnaire (the substrate's dtr.QuestionnaireFor output): the
// supported canonical url + the known items FillQuestionnaire fills. The SDK can't
// import the substrate fixture, so the recognized shape lives here; the parity test
// (test/sdkparity/dtr_parity_test.go) proves the fill matches the substrate on the
// REAL substrate-built questionnaire.
const sandboxQuestionnaireJSON = `{
  "resourceType": "Questionnaire",
  "id": "pa-lumbar-mri",
  "url": "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri",
  "version": "1.0.0",
  "status": "active",
  "item": [
    {"linkId": "conservative-therapy-weeks", "type": "integer", "text": "Weeks of conservative therapy completed"},
    {"linkId": "neuro-deficit", "type": "boolean", "text": "Progressive neurological deficit present?"},
    {"linkId": "prior-imaging", "type": "boolean", "text": "Prior imaging performed?"},
    {"linkId": "prior-surgery", "type": "boolean", "text": "Prior lumbar surgery?"},
    {"linkId": "high-disability", "type": "boolean", "text": "High disability index flag?"},
    {"linkId": "patient-reported-required", "type": "boolean", "text": "Patient-reported functional status required?"},
    {"linkId": "functional-status-oswestry", "type": "text", "text": "Oswestry disability index (clinician-attested)"}
  ]
}`

func mbrCoveredCC() ClinicalContext {
	return ClinicalContext{
		ConditionCode:            "M51.16",
		ConditionRef:             "Condition/cond-m5116",
		ConservativeTherapyWeeks: 6,
		ConservativeTherapyRef:   "Observation/obs-pt-weeks",
		ConservativeDate:         "2026-05-20",
		NeuroDeficit:             false,
		NeuroDeficitRef:          "Observation/obs-neuro",
		PriorImaging:             true,
		PriorImagingRef:          "DiagnosticReport/dr-xray",
	}
}

func mbrCoveredQC() QRContext {
	return QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC),
	}
}

// TestFillQuestionnaireFailsLoudOnUnknownCanonical: FillQuestionnaire must FAIL
// LOUDLY (naming the supported canonical) on a questionnaire whose url it does not
// recognize, and emit NO QR — never a half-filled one.
func TestFillQuestionnaireFailsLoudOnUnknownCanonical(t *testing.T) {
	bogus := `{
      "resourceType": "Questionnaire",
      "url": "http://example.org/fhir/Questionnaire/some-other-form",
      "status": "active",
      "item": [{"linkId": "conservative-therapy-weeks", "type": "integer"}]
    }`

	qr, err := FillQuestionnaire([]byte(bogus), mbrCoveredCC(), mbrCoveredQC())
	if err == nil {
		t.Fatalf("FillQuestionnaire accepted an unsupported questionnaire; want a fail-loud error")
	}
	if qr != nil {
		t.Errorf("FillQuestionnaire returned a non-nil QR (%q) on an unsupported questionnaire; must NEVER emit a half-filled QR", qr)
	}
	// The error must name BOTH the rejected canonical and the supported one.
	if !strings.Contains(err.Error(), SupportedQuestionnaireCanonical) {
		t.Errorf("error %q does not name the supported canonical %q", err, SupportedQuestionnaireCanonical)
	}
	if !strings.Contains(err.Error(), "some-other-form") {
		t.Errorf("error %q does not name the rejected canonical", err)
	}
}

// TestFillQuestionnaireHappyPath: the sandbox questionnaire fills into a non-empty
// QR that unmarshals as a completed QuestionnaireResponse.
func TestFillQuestionnaireHappyPath(t *testing.T) {
	qr, err := FillQuestionnaire([]byte(sandboxQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	if len(qr) == 0 {
		t.Fatal("FillQuestionnaire returned an empty QR")
	}
	var probe struct {
		ResourceType string `json:"resourceType"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(qr, &probe); err != nil {
		t.Fatalf("QR does not unmarshal: %v", err)
	}
	if probe.ResourceType != "QuestionnaireResponse" {
		t.Errorf("resourceType = %q, want QuestionnaireResponse", probe.ResourceType)
	}
	if probe.Status != "completed" {
		t.Errorf("status = %q, want completed", probe.Status)
	}
}

// TestBuildQuestionnaireFetchAndParseURL: the fetch request round-trips its canonical,
// and ParseQuestionnaireURL reads the url out of the sandbox questionnaire.
func TestBuildQuestionnaireFetchAndParseURL(t *testing.T) {
	fetch, err := BuildQuestionnaireFetch(SupportedQuestionnaireCanonical)
	if err != nil {
		t.Fatalf("BuildQuestionnaireFetch: %v", err)
	}
	var req QuestionnaireFetchRequest
	if err := json.Unmarshal(fetch, &req); err != nil {
		t.Fatalf("unmarshal fetch: %v", err)
	}
	if req.Canonical != SupportedQuestionnaireCanonical {
		t.Errorf("fetch canonical = %q, want %q", req.Canonical, SupportedQuestionnaireCanonical)
	}

	url, err := ParseQuestionnaireURL([]byte(sandboxQuestionnaireJSON))
	if err != nil {
		t.Fatalf("ParseQuestionnaireURL: %v", err)
	}
	if url != SupportedQuestionnaireCanonical {
		t.Errorf("parsed url = %q, want %q", url, SupportedQuestionnaireCanonical)
	}
}
