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
// MANUAL questionnaires (no CQL) — use it ONLY when `answers` values are genuinely
// entered by a human operator (a clinician or a patient), never for values a system
// derived from a clinical record (use FillQuestionnaireFromAutoAnswers for that — the
// stamp must name who/what actually produced the value, per FR-16/FR-17; register
// §15(a)):
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
// when source="manual". An empty author returns an error. NOTE: source="manual" alone
// is not FR-16-conformant for a clinician/patient author — the fence
// (gateway/engine/attestfence.go) additionally requires a COMPLETE attestation
// extension (shnsdk.BuildManualAttestedItem / BuildPatientAttestedItem) whenever
// author names a Practitioner/Patient; this generic filler does not add one, so a
// genuinely clinician/patient-authored fill needs its own attestation step layered on
// top (the pattern gateway/engine/originate_uc03_oxygen.go's 6.1 item uses).
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
	raw, err := buildQRFromAnswers(def, questionnaireJSON, answers, qc, func() fhir.Extension {
		return clinicianOriginExtension(author)
	})
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAnswers: %w", err)
	}
	return raw, nil
}

// FillQuestionnaireFromAutoAnswers is FillQuestionnaireFromAnswers's auto-populated
// twin (register §15(a)): the SAME structure-driven walker, filling each leaf from
// `answers` keyed by linkId, but stamping source="auto"/"auto-client" (FR-17's auto
// class, def.AutoOriginSourceCode — the same code FillQuestionnaireAtLine's CQL fill
// stamps) instead of source="manual". No author sub-extension: DTR's auto/auto-client
// source carries only the "source" sub-extension (dtrx-1's author mandate applies only
// to source="manual"). Use this — never FillQuestionnaireFromAnswers — when `answers`
// values are derived from a clinical record or other system data the deriving system
// already possesses (e.g. a value read off a seeded FHIR order, or a fact the
// requesting/responding system already knows to be true, such as "no PA exists yet"
// at $questionnaire-package time); a genuinely human-operator-entered value belongs on
// FillQuestionnaireFromAnswers instead, with its own FR-16/FR-27 attestation layered
// on top. Same honesty guard as FillQuestionnaireFromAnswers: a required leaf with no
// supplied answer is a hard error, never fabricated.
func FillQuestionnaireFromAutoAnswers(questionnaireJSON []byte, answers map[string]Answer, qc QRContext) ([]byte, error) {
	return FillQuestionnaireFromAutoAnswersAtLine("2.0", questionnaireJSON, answers, qc)
}

// FillQuestionnaireFromAutoAnswersAtLine is FillQuestionnaireFromAutoAnswers
// parameterized by DTR line ("2.0", "2.1", "2.2"). Unknown line -> error (fail-closed,
// never a silent 2.0 fallback). The auto/auto-client source code migration is per-line
// (def.AutoOriginSourceCode); the qr-context/qr-coverage differential is shared with
// FillQuestionnaireFromAnswersAtLine (dtrQRContextExtensions).
func FillQuestionnaireFromAutoAnswersAtLine(line string, questionnaireJSON []byte, answers map[string]Answer, qc QRContext) ([]byte, error) {
	def, ok := DTRLineDef(line)
	if !ok {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAutoAnswersAtLine: unknown DTR line %q", line)
	}
	return fillQuestionnaireFromAutoAnswers(def, questionnaireJSON, answers, qc)
}

func fillQuestionnaireFromAutoAnswers(def DTRDef, questionnaireJSON []byte, answers map[string]Answer, qc QRContext) ([]byte, error) {
	raw, err := buildQRFromAnswers(def, questionnaireJSON, answers, qc, func() fhir.Extension {
		return originExtension(def)
	})
	if err != nil {
		return nil, fmt.Errorf("shnsdk: FillQuestionnaireFromAutoAnswers: %w", err)
	}
	return raw, nil
}

// buildQRFromAnswers is the shared body of fillQuestionnaireFromAnswers and
// fillQuestionnaireFromAutoAnswers: parse the questionnaire, walk its item tree
// filling each leaf from `answers`, stamp every filled answer with originExt() (the
// ONLY thing the manual and auto variants differ on), and marshal the QR. Errors are
// returned UNWRAPPED — each caller wraps with its own function name for a legible
// trace.
func buildQRFromAnswers(def DTRDef, questionnaireJSON []byte, answers map[string]Answer, qc QRContext, originExt func() fhir.Extension) ([]byte, error) {
	var q fhir.Questionnaire
	if err := json.Unmarshal(questionnaireJSON, &q); err != nil {
		return nil, fmt.Errorf("parse questionnaire: %w", err)
	}

	// Walk the item tree and collect QR items. Error on any required leaf without an answer.
	items, err := fillItems(q.Item, answers, originExt)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("marshal questionnaire response: %w", err)
	}
	return raw, nil
}

// fillItems walks the questionnaire's item tree with the ONE shared walker and fills
// each leaf from `answers`, stamping the answer with originExt() (source="manual"+
// author for a human-entered fill, source="auto"/"auto-client" for a system-derived
// one — the caller decides which); required leaves without an answer produce an error
// (the honesty guard); optional ones are omitted.
func fillItems(qItems []fhir.QuestionnaireItem, answers map[string]Answer, originExt func() fhir.Extension) ([]fhir.QuestionnaireResponseItem, error) {
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
		qrAnswer.Extension = []fhir.Extension{originExt()}
		return qrAnswer, true, nil
	})
}

// leafFiller decides the QR answer for ONE question item (never a group or a
// display item — the walker handles those). answered=false omits the leaf; a
// non-nil error aborts the whole fill, so a builder can refuse rather than emit a
// quietly incomplete QR.
type leafFiller func(qi fhir.QuestionnaireItem) (answer fhir.QuestionnaireResponseItemAnswer, answered bool, err error)

// walkQuestionnaireItems is the ONE structure-driven walk every QR builder in this
// module shares (FillQuestionnaire's demo fill and FillQuestionnaireFromAnswers'
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
