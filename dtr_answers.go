package shnsdk

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// Answer is one typed QR answer the caller supplies for a manual questionnaire item.
// Exactly one kind is set. Public and FHIR-type-agnostic so consumers do not need to
// import the internal fhir package. An Answer with no kind set is treated as "no answer"
// (so a required item with an empty Answer still errors — the honesty guard).
type Answer struct {
	Boolean *bool
	Integer *int
	String  *string
	Coding  *AnswerCoding
}

// AnswerCoding holds the three fields of a FHIR Coding value, flattened for callers
// who should not have to construct a samply fhir.Coding directly.
type AnswerCoding struct{ System, Code, Display string }

// FillQuestionnaireFromAnswers builds a conformant QuestionnaireResponse for ANY
// questionnaire by walking its item tree and filling each leaf from `answers` (keyed
// by linkId). It is the generic, structure-driven analog of FillQuestionnaire for
// MANUAL questionnaires (no CQL):
//
//   - group items: recurse, mirroring the questionnaire's nesting in the QR;
//   - display items: skipped (no answer);
//   - leaf items WITH a supplied answer: emitted with the typed value[x] + a
//     source="manual" + author information-origin extension (dtrx-1: source="manual"
//     requires an author; reuses clinicianOriginExtension(author));
//   - REQUIRED leaf items WITHOUT a supplied answer: ERROR (the honesty guard — a
//     required answer must trace to real recorded data, never be fabricated). Optional
//     leaves without an answer are omitted.
//
// author is the Practitioner reference that recorded the manual answers (e.g.
// "Practitioner/1234567890"). It is required: dtrx-1 mandates an author sub-extension
// when source="manual". An empty author returns an error.
//
// The QR carries subject=qc.PatientRef, the versioned questionnaire canonical,
// authored, and the DTR qr-context extensions (reuses dtrQRContextExtensions). Unlike
// FillQuestionnaire it is NOT restricted to one canonical. It is
// FillQuestionnaireFromAnswersAtLine("2.0", …), byte-identical (regression-fenced by
// sdk/dtr_answers_test.go). Use FillQuestionnaireFromAnswersAtLine to target 2.1/2.2
// (DTR package differential: the qr-coverage extension at 2.2 — the answer-level
// source="manual" code itself never changes by line).
func FillQuestionnaireFromAnswers(questionnaireJSON []byte, answers map[string]Answer, author string, qc QRContext) ([]byte, error) {
	return FillQuestionnaireFromAnswersAtLine("2.0", questionnaireJSON, answers, author, qc)
}

// FillQuestionnaireFromAnswersAtLine is FillQuestionnaireFromAnswers parameterized by
// DTR line ("2.0", "2.1", "2.2"). Unknown line -> error (fail-closed, never a silent
// 2.0 fallback).
func FillQuestionnaireFromAnswersAtLine(line string, questionnaireJSON []byte, answers map[string]Answer, author string, qc QRContext) ([]byte, error) {
	def, ok := DTRLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswersAtLine: unknown DTR line %q", line)
	}
	return fillQuestionnaireFromAnswers(def, questionnaireJSON, answers, author, qc)
}

func fillQuestionnaireFromAnswers(def DTRDef, questionnaireJSON []byte, answers map[string]Answer, author string, qc QRContext) ([]byte, error) {
	if author == "" {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswers: author is required (dtrx-1: source=\"manual\" answers must name an author)")
	}
	var q fhir.Questionnaire
	if err := json.Unmarshal(questionnaireJSON, &q); err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswers: parse questionnaire: %w", err)
	}

	// Walk the item tree and collect QR items. Error on any required leaf without an answer.
	items, err := fillItems(q.Item, answers, author)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswers: %w", err)
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
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswers: marshal questionnaire response: %w", err)
	}
	return raw, nil
}

// fillItems walks the questionnaire's item tree with the ONE shared walker and
// fills each leaf from `answers` with source="manual" + author (dtrx-1); required
// leaves without an answer produce an error (the honesty guard); optional ones are
// omitted.
func fillItems(qItems []fhir.QuestionnaireItem, answers map[string]Answer, author string) ([]fhir.QuestionnaireResponseItem, error) {
	return walkQuestionnaireItems(qItems, func(qi fhir.QuestionnaireItem) (fhir.QuestionnaireResponseItemAnswer, bool, error) {
		a, ok := answers[qi.LinkId]
		if !ok || !answerHasValue(a) {
			// No answer supplied. Required items are a hard error (honesty guard).
			if isRequired(qi) {
				return fhir.QuestionnaireResponseItemAnswer{}, false, fmt.Errorf("required item %q has no supplied answer (honesty guard: a required QR item cannot be fabricated)", qi.LinkId)
			}
			// Optional: omit.
			return fhir.QuestionnaireResponseItemAnswer{}, false, nil
		}
		qrAnswer, err := answerToQRAnswer(a)
		if err != nil {
			return fhir.QuestionnaireResponseItemAnswer{}, false, fmt.Errorf("item %q: %w", qi.LinkId, err)
		}
		// Stamp source="manual" + author (dtrx-1) — recorded human entry, not
		// CQL-computed "auto". Reuses clinicianOriginExtension which already
		// builds the conformant source + nested author sub-extension.
		qrAnswer.Extension = []fhir.Extension{clinicianOriginExtension(author)}
		return qrAnswer, true, nil
	})
}

// leafFiller decides the QR answer for ONE question item (never a group or a
// display item — the walker handles those). answered=false omits the leaf; a
// non-nil error aborts the whole fill, so a builder can refuse rather than emit a
// quietly incomplete QR.
type leafFiller func(qi fhir.QuestionnaireItem) (answer fhir.QuestionnaireResponseItemAnswer, answered bool, err error)

// walkQuestionnaireItems is the ONE structure-driven walk every QR builder in this
// module shares (FillQuestionnaire's sandbox fill and FillQuestionnaireFromAnswers'
// manual fill differ only in their leafFiller). It mirrors the questionnaire's
// nesting into the QR on BOTH axes FHIR nests on:
//
//   - group items recurse; the group is emitted only when at least one descendant
//     answered (no empty shells — a QR group with nothing under it asserts nothing);
//   - display items carry no answer and are skipped;
//   - question items are filled by `fill`; a question's own child items (SDC's
//     "questions within an answer") recurse too and ride under answer.item — and
//     if such a child answers while its parent does not, the walk ERRORS rather
//     than drop the child or fabricate a parent answer to hang it from.
//
// Two walkers that each got half of this right is how nested items went unread
// for a whole release line; this is the one walk. Not depth-capped: a cap would
// silently drop content below it.
func walkQuestionnaireItems(qItems []fhir.QuestionnaireItem, fill leafFiller) ([]fhir.QuestionnaireResponseItem, error) {
	var result []fhir.QuestionnaireResponseItem
	for _, qi := range qItems {
		switch qi.Type {
		case fhir.QuestionnaireItemTypeGroup:
			children, err := walkQuestionnaireItems(qi.Item, fill)
			if err != nil {
				return nil, err
			}
			if len(children) > 0 {
				result = append(result, fhir.QuestionnaireResponseItem{LinkId: qi.LinkId, Item: children})
			}

		case fhir.QuestionnaireItemTypeDisplay:
			continue

		default:
			answer, answered, err := fill(qi)
			if err != nil {
				return nil, err
			}
			children, err := walkQuestionnaireItems(qi.Item, fill)
			if err != nil {
				return nil, err
			}
			if !answered {
				if len(children) > 0 {
					return nil, fmt.Errorf("item %q has answered child items but no answer of its own (answer.item needs an answer to hang from; refusing to drop the children or fabricate the parent)", qi.LinkId)
				}
				continue
			}
			answer.Item = children
			result = append(result, fhir.QuestionnaireResponseItem{
				LinkId: qi.LinkId,
				Answer: []fhir.QuestionnaireResponseItemAnswer{answer},
			})
		}
	}
	return result, nil
}

// answerHasValue reports whether an Answer has at least one kind set. An Answer with
// no kind set is the caller's way of saying "I have no recorded value for this item",
// which is equivalent to not providing an entry in the answers map.
func answerHasValue(a Answer) bool {
	return a.Boolean != nil || a.Integer != nil || a.String != nil || a.Coding != nil
}

// isRequired returns true only when the questionnaire item's Required field is
// explicitly set to true (Required is *bool — absent/false are both non-required).
func isRequired(qi fhir.QuestionnaireItem) bool {
	return qi.Required != nil && *qi.Required
}

// answerToQRAnswer maps an Answer to a fhir.QuestionnaireResponseItemAnswer by kind.
// Exactly one kind is expected; the first non-nil kind wins (caller contract: set only one).
func answerToQRAnswer(a Answer) (fhir.QuestionnaireResponseItemAnswer, error) {
	switch {
	case a.Boolean != nil:
		return fhir.QuestionnaireResponseItemAnswer{ValueBoolean: a.Boolean}, nil
	case a.Integer != nil:
		return fhir.QuestionnaireResponseItemAnswer{ValueInteger: a.Integer}, nil
	case a.String != nil:
		return fhir.QuestionnaireResponseItemAnswer{ValueString: a.String}, nil
	case a.Coding != nil:
		return fhir.QuestionnaireResponseItemAnswer{ValueCoding: &fhir.Coding{
			System:  strPtr(a.Coding.System),
			Code:    strPtr(a.Coding.Code),
			Display: strPtr(a.Coding.Display),
		}}, nil
	default:
		// Should not be reached because fillItems checks answerHasValue first.
		return fhir.QuestionnaireResponseItemAnswer{}, fmt.Errorf("answer has no kind set (all fields nil)")
	}
}
