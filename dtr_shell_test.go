package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// shellQuestionnaire is a payer's OWN questionnaire — one this package ships no prefill
// logic for. That is the whole case BuildQuestionnaireResponseShell exists to answer.
const shellQuestionnaire = `{"resourceType":"Questionnaire","id":"PayerOwn","status":"active",` +
	`"url":"http://payer.example/fhir/Questionnaire/PayerOwn",` +
	`"item":[{"linkId":"1","type":"boolean","text":"something only the payer knows"}]}`

// TestBuildQuestionnaireResponseShell_Shape is the shape pin: in-progress, ZERO answers,
// naming the questionnaire the payer advertised and the patient it is about.
//
// The zero-answer part is the load-bearing half. A shell with any answer in it would be
// this SDK asserting a clinical fact it did not derive from anything — the precise fiction
// the retired in-process payer told. "I have no answers yet" is the honest response to a
// questionnaire whose prepopulation logic lives on the payer's side.
func TestBuildQuestionnaireResponseShell_Shape(t *testing.T) {
	authored := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	raw, err := BuildQuestionnaireResponseShell([]byte(shellQuestionnaire), QRContext{
		PatientRef:  "Patient/MBR-D-UC03",
		CoverageRef: "Coverage/MBR-D-UC03",
		OrderRef:    "ServiceRequest/sr-1",
		Authored:    authored,
	})
	if err != nil {
		t.Fatalf("BuildQuestionnaireResponseShell: %v", err)
	}

	var qr struct {
		ResourceType  string                     `json:"resourceType"`
		Status        string                     `json:"status"`
		Questionnaire string                     `json:"questionnaire"`
		Subject       struct{ Reference string } `json:"subject"`
		Authored      string                     `json:"authored"`
		Item          []json.RawMessage          `json:"item"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("shell is not valid json: %v (%s)", err, raw)
	}
	if qr.ResourceType != "QuestionnaireResponse" {
		t.Errorf("resourceType = %q, want QuestionnaireResponse", qr.ResourceType)
	}
	if qr.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress — an unanswered shell is not completed work", qr.Status)
	}
	if qr.Questionnaire != "http://payer.example/fhir/Questionnaire/PayerOwn" {
		t.Errorf("questionnaire = %q, want the canonical the payer advertised", qr.Questionnaire)
	}
	if qr.Subject.Reference != "Patient/MBR-D-UC03" {
		t.Errorf("subject.reference = %q, want Patient/MBR-D-UC03", qr.Subject.Reference)
	}
	if len(qr.Item) != 0 {
		t.Errorf("shell carries %d item(s), want 0 — it must assert no answers it did not derive: %s", len(qr.Item), raw)
	}
	if qr.Authored != authored.Format(time.RFC3339) {
		t.Errorf("authored = %q, want %q", qr.Authored, authored.Format(time.RFC3339))
	}
}

// TestBuildQuestionnaireResponseShell_RequiresPatientRef is the rejection row for the one
// guard: a shell with no subject names no patient, and a QuestionnaireResponse whose
// subject the payer must infer is exactly what the substrate's member fences exist to
// refuse. Fail at construction, not on the wire.
func TestBuildQuestionnaireResponseShell_RequiresPatientRef(t *testing.T) {
	_, err := BuildQuestionnaireResponseShell([]byte(shellQuestionnaire), QRContext{
		CoverageRef: "Coverage/MBR-D-UC03",
		Authored:    time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("a shell with no PatientRef must be refused, got nil error")
	}
	if !strings.Contains(err.Error(), "PatientRef") {
		t.Errorf("refusal must name the missing field, got %v", err)
	}
}

// TestBuildQuestionnaireResponseShell_RequiresQuestionnaireURL: a questionnaire with no
// url cannot be named, so the shell cannot say what it is a response TO. Refuse rather
// than emit a QuestionnaireResponse pointing at nothing.
func TestBuildQuestionnaireResponseShell_RequiresQuestionnaireURL(t *testing.T) {
	for _, tc := range []struct{ name, questionnaire string }{
		{"no url", `{"resourceType":"Questionnaire","id":"x","status":"active"}`},
		{"not json", `{not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildQuestionnaireResponseShell([]byte(tc.questionnaire), QRContext{PatientRef: "Patient/x"}); err == nil {
				t.Fatal("want a refusal, got nil error")
			}
		})
	}
}
