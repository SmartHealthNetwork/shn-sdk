package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testAuthor = "Practitioner/1999999999"

// sharedQC returns a QRContext for use in FillQuestionnaireFromAnswers tests.
func sharedQC() QRContext {
	return QRContext{
		PatientRef:  "Patient/TEST-001",
		CoverageRef: "Coverage/TEST-001",
		OrderRef:    "ServiceRequest/sr-TEST-001",
		Authored:    time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
	}
}

// l8000Questionnaire is a minimal br-payer L8000 (PriorAuthRequired) questionnaire:
// group 1 → display 1.1 + boolean required 1.2.
const l8000Questionnaire = `{
  "resourceType": "Questionnaire",
  "id": "l8000-prior-auth",
  "url": "http://example.payer.org/fhir/Questionnaire/l8000-prior-auth",
  "version": "2.0.0",
  "status": "active",
  "item": [
    {
      "linkId": "1",
      "type": "group",
      "text": "Prior Auth Required Assessment",
      "item": [
        {
          "linkId": "1.1",
          "type": "display",
          "text": "Complete the following fields"
        },
        {
          "linkId": "1.2",
          "type": "boolean",
          "text": "Is prior authorization required?",
          "required": true
        }
      ]
    }
  ]
}`

// g0151Questionnaire is a minimal br-payer G0151 (HomeHealthAssessment) questionnaire:
// group 1 → choice required 1.1.
const g0151Questionnaire = `{
  "resourceType": "Questionnaire",
  "id": "g0151-home-health-assessment",
  "url": "http://example.payer.org/fhir/Questionnaire/g0151-home-health-assessment",
  "version": "1.0.0",
  "status": "active",
  "item": [
    {
      "linkId": "1",
      "type": "group",
      "text": "Home Health Assessment",
      "item": [
        {
          "linkId": "1.1",
          "type": "choice",
          "text": "Assessment type performed",
          "required": true
        }
      ]
    }
  ]
}`

// optionalLeafQuestionnaire has one optional boolean leaf (no required flag).
const optionalLeafQuestionnaire = `{
  "resourceType": "Questionnaire",
  "id": "optional-leaf",
  "url": "http://example.payer.org/fhir/Questionnaire/optional-leaf",
  "status": "active",
  "item": [
    {
      "linkId": "opt-1",
      "type": "boolean",
      "text": "Optional flag (no required)"
    }
  ]
}`

// requiredLeafQuestionnaire has one required boolean leaf with no group nesting.
const requiredLeafQuestionnaire = `{
  "resourceType": "Questionnaire",
  "id": "req-leaf",
  "url": "http://example.payer.org/fhir/Questionnaire/req-leaf",
  "status": "active",
  "item": [
    {
      "linkId": "req-1",
      "type": "boolean",
      "text": "Required flag",
      "required": true
    }
  ]
}`

// integerLeafQuestionnaire has one required integer leaf.
const integerLeafQuestionnaire = `{
  "resourceType": "Questionnaire",
  "id": "integer-leaf",
  "url": "http://example.payer.org/fhir/Questionnaire/integer-leaf",
  "status": "active",
  "item": [
    {
      "linkId": "int-1",
      "type": "integer",
      "text": "Number of sessions",
      "required": true
    }
  ]
}`

// stringLeafQuestionnaire has one required string leaf.
const stringLeafQuestionnaire = `{
  "resourceType": "Questionnaire",
  "id": "string-leaf",
  "url": "http://example.payer.org/fhir/Questionnaire/string-leaf",
  "status": "active",
  "item": [
    {
      "linkId": "str-1",
      "type": "string",
      "text": "Clinical note",
      "required": true
    }
  ]
}`

// answerExtShape is the JSON shape for an answer's Extension slice as produced by
// clinicianOriginExtension: top-level extension with nested source/author sub-extensions,
// where author itself carries a nested reference sub-extension.
type answerExtShape struct {
	Url       string `json:"url"`
	Extension []struct {
		Url         string `json:"url"`
		ValueCode   string `json:"valueCode"`
		ValueString string `json:"valueString"`
		Extension   []struct {
			Url         string `json:"url"`
			ValueString string `json:"valueString"`
		} `json:"extension"`
	} `json:"extension"`
}

// checkManualOriginExtension is a test helper that verifies an answer's Extension slice
// carries exactly the DTR information-origin extension with source="manual" AND an
// author sub-extension whose nested reference == wantAuthor (dtrx-1).
func checkManualOriginExtension(t *testing.T, label string, exts []answerExtShape, wantAuthor string) {
	t.Helper()
	foundSource := false
	foundAuthor := false
	for _, ext := range exts {
		if ext.Url != informationOriginExt {
			continue
		}
		for _, sub := range ext.Extension {
			if sub.Url == "source" && sub.ValueCode == "manual" {
				foundSource = true
			}
			if sub.Url == "source" && sub.ValueCode == "auto" {
				t.Errorf("%s: information-origin extension has source=auto; manual answers must stamp source=manual", label)
			}
			if sub.Url == "author" {
				// The author sub-extension is a complex extension with nested url="reference".
				for _, nested := range sub.Extension {
					if nested.Url == "reference" && nested.ValueString == wantAuthor {
						foundAuthor = true
					}
				}
			}
		}
	}
	if !foundSource {
		t.Errorf("%s: answer missing information-origin extension with source=manual", label)
	}
	if !foundAuthor {
		t.Errorf("%s: answer missing dtrx-1 author sub-extension with reference=%q", label, wantAuthor)
	}
}

// TestFillQuestionnaireFromAnswers_Boolean is the L8000-shaped test:
// group 1 → {display 1.1, boolean required 1.2}; answers {"1.2": {Boolean: true}}.
// Asserts: QR mirrors the group nesting, item 1.2 has valueBoolean:true + source="manual"
// + author extension (dtrx-1), display 1.1 is omitted, subject == qc.PatientRef,
// questionnaire == exact versioned canonical, authored is set, and a qr-context extension
// carries the CoverageRef.
func TestFillQuestionnaireFromAnswers_Boolean(t *testing.T) {
	bTrue := true
	answers := map[string]Answer{
		"1.2": {Boolean: &bTrue},
	}
	qc := sharedQC()
	raw, err := FillQuestionnaireFromAnswers([]byte(l8000Questionnaire), answers, testAuthor, qc)
	if err != nil {
		t.Fatalf("FillQuestionnaireFromAnswers: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("FillQuestionnaireFromAnswers returned empty bytes")
	}

	// Unmarshal into a shape we can inspect.
	var qr struct {
		ResourceType  string `json:"resourceType"`
		Status        string `json:"status"`
		Questionnaire string `json:"questionnaire"`
		Authored      string `json:"authored"`
		Subject       struct {
			Reference string `json:"reference"`
		} `json:"subject"`
		Extension []struct {
			Url            string `json:"url"`
			ValueReference *struct {
				Reference string `json:"reference"`
			} `json:"valueReference"`
		} `json:"extension"`
		Item []struct {
			LinkId string `json:"linkId"`
			Item   []struct {
				LinkId string `json:"linkId"`
				Answer []struct {
					ValueBoolean *bool            `json:"valueBoolean"`
					Extension    []answerExtShape `json:"extension"`
				} `json:"answer"`
			} `json:"item"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("QR does not unmarshal: %v", err)
	}

	// Top-level assertions.
	if qr.ResourceType != "QuestionnaireResponse" {
		t.Errorf("resourceType = %q, want QuestionnaireResponse", qr.ResourceType)
	}
	if qr.Status != "completed" {
		t.Errorf("status = %q, want completed", qr.Status)
	}
	if qr.Subject.Reference != qc.PatientRef {
		t.Errorf("subject.reference = %q, want %q", qr.Subject.Reference, qc.PatientRef)
	}
	// authored must be set (non-empty).
	if qr.Authored == "" {
		t.Errorf("authored is empty; must be set from qc.Authored")
	}
	// questionnaire canonical must be the exact versioned canonical (url|version).
	const wantCanonical = "http://example.payer.org/fhir/Questionnaire/l8000-prior-auth|2.0.0"
	if qr.Questionnaire != wantCanonical {
		t.Errorf("questionnaire = %q, want %q", qr.Questionnaire, wantCanonical)
	}
	// At least one qr-context extension must carry qc.CoverageRef (via valueReference).
	foundCoverage := false
	for _, ext := range qr.Extension {
		if ext.ValueReference != nil && ext.ValueReference.Reference == qc.CoverageRef {
			foundCoverage = true
		}
	}
	if !foundCoverage {
		t.Errorf("QR extensions do not carry CoverageRef %q via valueReference; got extensions: %+v", qc.CoverageRef, qr.Extension)
	}

	// Group structure: item[0] == group "1" containing nested items.
	if len(qr.Item) != 1 {
		t.Fatalf("expected 1 top-level item (group 1), got %d", len(qr.Item))
	}
	group1 := qr.Item[0]
	if group1.LinkId != "1" {
		t.Errorf("group linkId = %q, want 1", group1.LinkId)
	}

	// Display item 1.1 must be omitted; only item 1.2 present.
	if len(group1.Item) != 1 {
		t.Fatalf("expected 1 child item (1.2 only, display 1.1 omitted), got %d: %+v", len(group1.Item), group1.Item)
	}
	item12 := group1.Item[0]
	if item12.LinkId != "1.2" {
		t.Errorf("child item linkId = %q, want 1.2", item12.LinkId)
	}
	if len(item12.Answer) != 1 {
		t.Fatalf("expected 1 answer for item 1.2, got %d", len(item12.Answer))
	}
	ans := item12.Answer[0]
	if ans.ValueBoolean == nil || !*ans.ValueBoolean {
		t.Errorf("valueBoolean = %v, want true", ans.ValueBoolean)
	}

	// Extension must carry source="manual" + author (dtrx-1).
	checkManualOriginExtension(t, "item 1.2", ans.Extension, testAuthor)
}

// TestFillQuestionnaireFromAnswers_Coding is the G0151-shaped test:
// group 1 → {choice required 1.1}; answers {"1.1": {Coding: ...}}.
// Asserts: item 1.1 has the valueCoding with system/code/display.
func TestFillQuestionnaireFromAnswers_Coding(t *testing.T) {
	answers := map[string]Answer{
		"1.1": {Coding: &AnswerCoding{
			System:  "http://snomed.info/sct",
			Code:    "91251008",
			Display: "Physical therapy procedure",
		}},
	}
	raw, err := FillQuestionnaireFromAnswers([]byte(g0151Questionnaire), answers, testAuthor, sharedQC())
	if err != nil {
		t.Fatalf("FillQuestionnaireFromAnswers (Coding): %v", err)
	}

	var qr struct {
		Item []struct {
			LinkId string `json:"linkId"`
			Item   []struct {
				LinkId string `json:"linkId"`
				Answer []struct {
					ValueCoding *struct {
						System  string `json:"system"`
						Code    string `json:"code"`
						Display string `json:"display"`
					} `json:"valueCoding"`
					Extension []answerExtShape `json:"extension"`
				} `json:"answer"`
			} `json:"item"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("QR does not unmarshal: %v", err)
	}

	if len(qr.Item) != 1 {
		t.Fatalf("expected 1 top-level item (group 1), got %d", len(qr.Item))
	}
	group1 := qr.Item[0]
	if len(group1.Item) != 1 {
		t.Fatalf("expected 1 child item (1.1), got %d", len(group1.Item))
	}
	item11 := group1.Item[0]
	if item11.LinkId != "1.1" {
		t.Errorf("child item linkId = %q, want 1.1", item11.LinkId)
	}
	if len(item11.Answer) != 1 {
		t.Fatalf("expected 1 answer for item 1.1, got %d", len(item11.Answer))
	}
	ans := item11.Answer[0]
	if ans.ValueCoding == nil {
		t.Fatal("valueCoding is nil; expected a coding answer")
	}
	if ans.ValueCoding.System != "http://snomed.info/sct" {
		t.Errorf("valueCoding.system = %q, want http://snomed.info/sct", ans.ValueCoding.System)
	}
	if ans.ValueCoding.Code != "91251008" {
		t.Errorf("valueCoding.code = %q, want 91251008", ans.ValueCoding.Code)
	}
	if ans.ValueCoding.Display != "Physical therapy procedure" {
		t.Errorf("valueCoding.display = %q, want Physical therapy procedure", ans.ValueCoding.Display)
	}

	// Extension must carry source="manual" + author (dtrx-1).
	checkManualOriginExtension(t, "item 1.1", ans.Extension, testAuthor)
}

// TestFillQuestionnaireFromAnswers_Integer tests an integer-kind answer (valueInteger).
// Asserts: item int-1 has valueInteger == 12.
func TestFillQuestionnaireFromAnswers_Integer(t *testing.T) {
	n := 12
	answers := map[string]Answer{
		"int-1": {Integer: &n},
	}
	raw, err := FillQuestionnaireFromAnswers([]byte(integerLeafQuestionnaire), answers, testAuthor, sharedQC())
	if err != nil {
		t.Fatalf("FillQuestionnaireFromAnswers (Integer): %v", err)
	}

	var qr struct {
		Item []struct {
			LinkId string `json:"linkId"`
			Answer []struct {
				ValueInteger *int             `json:"valueInteger"`
				Extension    []answerExtShape `json:"extension"`
			} `json:"answer"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("QR does not unmarshal: %v", err)
	}
	if len(qr.Item) != 1 {
		t.Fatalf("expected 1 top-level item (int-1), got %d", len(qr.Item))
	}
	item := qr.Item[0]
	if item.LinkId != "int-1" {
		t.Errorf("linkId = %q, want int-1", item.LinkId)
	}
	if len(item.Answer) != 1 {
		t.Fatalf("expected 1 answer for int-1, got %d", len(item.Answer))
	}
	ans := item.Answer[0]
	if ans.ValueInteger == nil || *ans.ValueInteger != 12 {
		t.Errorf("valueInteger = %v, want 12", ans.ValueInteger)
	}
	// Extension must carry source="manual" + author (dtrx-1).
	checkManualOriginExtension(t, "item int-1", ans.Extension, testAuthor)
}

// TestFillQuestionnaireFromAnswers_String tests a string-kind answer (valueString).
// Asserts: item str-1 has valueString == "Patient improving well".
func TestFillQuestionnaireFromAnswers_String(t *testing.T) {
	note := "Patient improving well"
	answers := map[string]Answer{
		"str-1": {String: &note},
	}
	raw, err := FillQuestionnaireFromAnswers([]byte(stringLeafQuestionnaire), answers, testAuthor, sharedQC())
	if err != nil {
		t.Fatalf("FillQuestionnaireFromAnswers (String): %v", err)
	}

	var qr struct {
		Item []struct {
			LinkId string `json:"linkId"`
			Answer []struct {
				ValueString *string          `json:"valueString"`
				Extension   []answerExtShape `json:"extension"`
			} `json:"answer"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("QR does not unmarshal: %v", err)
	}
	if len(qr.Item) != 1 {
		t.Fatalf("expected 1 top-level item (str-1), got %d", len(qr.Item))
	}
	item := qr.Item[0]
	if item.LinkId != "str-1" {
		t.Errorf("linkId = %q, want str-1", item.LinkId)
	}
	if len(item.Answer) != 1 {
		t.Fatalf("expected 1 answer for str-1, got %d", len(item.Answer))
	}
	ans := item.Answer[0]
	if ans.ValueString == nil || *ans.ValueString != note {
		t.Errorf("valueString = %v, want %q", ans.ValueString, note)
	}
	// Extension must carry source="manual" + author (dtrx-1).
	checkManualOriginExtension(t, "item str-1", ans.Extension, testAuthor)
}

// TestFillQuestionnaireFromAnswers_HonestyRejection is the guard's rejection test:
// a questionnaire with a required leaf and an EMPTY answers map returns an ERROR.
// The QR must NOT be built — the honesty invariant: required items need real recorded answers.
func TestFillQuestionnaireFromAnswers_HonestyRejection(t *testing.T) {
	// Case 1: empty answers map.
	qr, err := FillQuestionnaireFromAnswers([]byte(requiredLeafQuestionnaire), map[string]Answer{}, testAuthor, sharedQC())
	if err == nil {
		t.Errorf("HonestyRejection (empty map): want an error for required leaf with no answer, got nil")
	}
	if qr != nil {
		t.Errorf("HonestyRejection (empty map): want nil QR, got non-nil %q", qr)
	}

	// Case 2: an Answer struct with no kind set (zero value) — also treated as "no answer".
	qr, err = FillQuestionnaireFromAnswers([]byte(requiredLeafQuestionnaire), map[string]Answer{
		"req-1": {}, // no Boolean, Integer, String, or Coding
	}, testAuthor, sharedQC())
	if err == nil {
		t.Errorf("HonestyRejection (zero Answer): want an error for required leaf with empty Answer, got nil")
	}
	if qr != nil {
		t.Errorf("HonestyRejection (zero Answer): want nil QR, got non-nil %q", qr)
	}
}

// TestFillQuestionnaireFromAnswers_EmptyAuthorRejection asserts that passing an empty
// author string is a hard error (dtrx-1: source="manual" requires an author). The QR
// must NOT be built without an author.
func TestFillQuestionnaireFromAnswers_EmptyAuthorRejection(t *testing.T) {
	bTrue := true
	answers := map[string]Answer{
		"1.2": {Boolean: &bTrue},
	}
	qr, err := FillQuestionnaireFromAnswers([]byte(l8000Questionnaire), answers, "", sharedQC())
	if err == nil {
		t.Errorf("EmptyAuthorRejection: want error for empty author (dtrx-1), got nil")
	}
	if qr != nil {
		t.Errorf("EmptyAuthorRejection: want nil QR on empty-author error, got non-nil")
	}
	if err != nil && !strings.Contains(err.Error(), "author is required") {
		t.Errorf("EmptyAuthorRejection: error %q does not mention 'author is required'", err.Error())
	}
}

// TestFillQuestionnaireFromAnswers_OptionalItemOmitted: a non-required leaf with no
// supplied answer is silently omitted — no error, and the item does not appear in the QR.
func TestFillQuestionnaireFromAnswers_OptionalItemOmitted(t *testing.T) {
	qr, err := FillQuestionnaireFromAnswers([]byte(optionalLeafQuestionnaire), map[string]Answer{}, testAuthor, sharedQC())
	if err != nil {
		t.Fatalf("OptionalItemOmitted: unexpected error: %v", err)
	}

	var probe struct {
		ResourceType string `json:"resourceType"`
		Item         []any  `json:"item"`
	}
	if err := json.Unmarshal(qr, &probe); err != nil {
		t.Fatalf("OptionalItemOmitted: QR does not unmarshal: %v", err)
	}
	if probe.ResourceType != "QuestionnaireResponse" {
		t.Errorf("OptionalItemOmitted: resourceType = %q, want QuestionnaireResponse", probe.ResourceType)
	}
	// The optional item has no answer; the QR item list should be empty (nothing to emit).
	if len(probe.Item) != 0 {
		t.Errorf("OptionalItemOmitted: expected 0 items in QR (optional, no answer), got %d", len(probe.Item))
	}
}

// TestFillQuestionnaireFromAnswers_ManualNotAuto asserts that filled answers carry
// source="manual" and NOT source="auto". This is the honesty distinction:
// FillQuestionnaire stamps "auto" (CQL-computed); FillQuestionnaireFromAnswers stamps
// "manual" (recorded human entry).
func TestFillQuestionnaireFromAnswers_ManualNotAuto(t *testing.T) {
	bTrue := true
	raw, err := FillQuestionnaireFromAnswers([]byte(l8000Questionnaire), map[string]Answer{
		"1.2": {Boolean: &bTrue},
	}, testAuthor, sharedQC())
	if err != nil {
		t.Fatalf("ManualNotAuto: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"manual"`) {
		t.Errorf("ManualNotAuto: QR does not contain source=manual: %s", s)
	}
	if strings.Contains(s, `"auto"`) {
		t.Errorf("ManualNotAuto: QR contains source=auto; must NOT stamp auto for manual-entry answers: %s", s)
	}
}
