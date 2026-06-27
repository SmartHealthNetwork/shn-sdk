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
// FillQuestionnaire it is NOT restricted to one canonical.
func FillQuestionnaireFromAnswers(questionnaireJSON []byte, answers map[string]Answer, author string, qc QRContext) ([]byte, error) {
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
		Extension:     dtrQRContextExtensions(qc),
		Item:          items,
	}
	raw, err := json.Marshal(qr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswers: marshal questionnaire response: %w", err)
	}
	return raw, nil
}

// fillItems recursively walks a slice of QuestionnaireItem and returns the
// corresponding QuestionnaireResponseItems. It is the core of the structure-driven
// walk: groups recurse; display items are skipped; leaf items are filled from
// `answers` with source="manual" + author (dtrx-1); required leaves without an answer
// produce an error.
func fillItems(qItems []fhir.QuestionnaireItem, answers map[string]Answer, author string) ([]fhir.QuestionnaireResponseItem, error) {
	var result []fhir.QuestionnaireResponseItem
	for _, qi := range qItems {
		switch qi.Type {
		case fhir.QuestionnaireItemTypeGroup:
			// Group: recurse into children and mirror the nesting in the QR.
			children, err := fillItems(qi.Item, answers, author)
			if err != nil {
				return nil, err
			}
			// Only emit the group item when it has at least one child QR item
			// (mirroring FillQuestionnaire's omit-when-no-answer behaviour for the
			// group level — no child content means no group item in the QR).
			if len(children) > 0 {
				result = append(result, fhir.QuestionnaireResponseItem{
					LinkId: qi.LinkId,
					Item:   children,
				})
			}

		case fhir.QuestionnaireItemTypeDisplay:
			// Display items carry no answer; skip them entirely.
			continue

		default:
			// Leaf item: look up the caller-supplied answer.
			a, ok := answers[qi.LinkId]
			if !ok || !answerHasValue(a) {
				// No answer supplied. Required items are a hard error (honesty guard).
				if isRequired(qi) {
					return nil, fmt.Errorf("required item %q has no supplied answer (honesty guard: a required QR item cannot be fabricated)", qi.LinkId)
				}
				// Optional: silently omit.
				continue
			}
			qrAnswer, err := answerToQRAnswer(a)
			if err != nil {
				return nil, fmt.Errorf("item %q: %w", qi.LinkId, err)
			}
			// Stamp source="manual" + author (dtrx-1) — recorded human entry, not
			// CQL-computed "auto". Reuses clinicianOriginExtension which already
			// builds the conformant source + nested author sub-extension.
			qrAnswer.Extension = []fhir.Extension{clinicianOriginExtension(author)}
			result = append(result, fhir.QuestionnaireResponseItem{
				LinkId: qi.LinkId,
				Answer: []fhir.QuestionnaireResponseItemAnswer{qrAnswer},
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
