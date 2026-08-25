package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
)

// nestedDemoQuestionnaireJSON is the demo lumbar-MRI questionnaire with its
// items grouped the way real Da Vinci payer questionnaires group them: two
// top-level `group` items, one of which holds a further group (depth 3 on the
// item.item axis), plus a `display` item that carries no answer. Same canonical,
// same leaf linkIds, same leaf order as the flat shape — only the structure
// differs. (A flat corpus hid one-level item walkers for a whole release
// line; this is the shape the builder must mirror.)
const nestedDemoQuestionnaireJSON = `{
  "resourceType": "Questionnaire",
  "id": "pa-lumbar-mri",
  "url": "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri",
  "version": "1.0.0",
  "status": "active",
  "item": [
    {"linkId": "clinical-history", "type": "group", "text": "Clinical history", "item": [
      {"linkId": "clinical-history-note", "type": "display", "text": "Answer from the record, not recollection."},
      {"linkId": "conservative-therapy-weeks", "type": "integer", "text": "Weeks of conservative therapy completed"},
      {"linkId": "neuro-deficit", "type": "boolean", "text": "Progressive neurological deficit present?"},
      {"linkId": "prior-treatment", "type": "group", "text": "Prior treatment", "item": [
        {"linkId": "prior-imaging", "type": "boolean", "text": "Prior imaging performed?"},
        {"linkId": "prior-surgery", "type": "boolean", "text": "Prior lumbar surgery?"}
      ]}
    ]},
    {"linkId": "functional-status", "type": "group", "text": "Functional status", "item": [
      {"linkId": "high-disability", "type": "boolean", "text": "High disability index flag?"},
      {"linkId": "patient-reported-required", "type": "boolean", "text": "Patient-reported functional status required?"},
      {"linkId": "functional-status-oswestry", "type": "text", "text": "Oswestry disability index (clinician-attested)"}
    ]}
  ]
}`

// qrItemProbe is the recursive QR item shape the nesting tests read.
type qrItemProbe struct {
	LinkId string `json:"linkId"`
	Answer []struct {
		ValueInteger *int  `json:"valueInteger"`
		ValueBoolean *bool `json:"valueBoolean"`
		Extension    []struct {
			Url       string `json:"url"`
			Extension []struct {
				Url       string  `json:"url"`
				ValueCode *string `json:"valueCode"`
			} `json:"extension"`
		} `json:"extension"`
		Item []qrItemProbe `json:"item"`
	} `json:"answer"`
	Item []qrItemProbe `json:"item"`
}

func decodeQRItems(t *testing.T, qr []byte) []qrItemProbe {
	t.Helper()
	var doc struct {
		Item []qrItemProbe `json:"item"`
	}
	if err := json.Unmarshal(qr, &doc); err != nil {
		t.Fatalf("decode QR: %v\n%s", err, qr)
	}
	return doc.Item
}

func linkIDs(items []qrItemProbe) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.LinkId)
	}
	return out
}

func originSource(t *testing.T, it qrItemProbe) string {
	t.Helper()
	if len(it.Answer) != 1 {
		t.Fatalf("item %q: answer count = %d, want 1", it.LinkId, len(it.Answer))
	}
	for _, e := range it.Answer[0].Extension {
		if e.Url != informationOriginExt {
			continue
		}
		for _, sub := range e.Extension {
			if sub.Url == "source" && sub.ValueCode != nil {
				return *sub.ValueCode
			}
		}
	}
	t.Fatalf("item %q: answer carries no information-origin source", it.LinkId)
	return ""
}

// TestFillQuestionnaire_MirrorsNestedGroups: the demo fill must reproduce the
// questionnaire's group structure in the QR — groups recurse (depth 3 on the
// item.item axis), display items vanish, a group with no answered leaf is
// omitted (never an empty shell), and leaf order within each group is kept.
// The demo fill used to iterate q.Item once: a group linkId had no answer
// mapping, so every nested leaf was silently dropped and the QR came back with
// zero items.
func TestFillQuestionnaire_MirrorsNestedGroups(t *testing.T) {
	t.Run("covered persona: only the clinical-history branch answers", func(t *testing.T) {
		qr, err := FillQuestionnaire([]byte(nestedDemoQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
		if err != nil {
			t.Fatalf("FillQuestionnaire: %v", err)
		}
		top := decodeQRItems(t, qr)
		if got, want := linkIDs(top), []string{"clinical-history"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("top-level items = %v, want %v (functional-status has no answered leaf for this persona and must be omitted, not emitted empty)\n%s", got, want, qr)
		}
		ch := top[0]
		if len(ch.Answer) != 0 {
			t.Errorf("group clinical-history must carry no answer, got %d", len(ch.Answer))
		}
		// display item skipped; prior-surgery=false omitted; order preserved.
		if got, want := linkIDs(ch.Item), []string{"conservative-therapy-weeks", "neuro-deficit", "prior-treatment"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("clinical-history.item = %v, want %v\n%s", got, want, qr)
		}
		if ch.Item[0].Answer[0].ValueInteger == nil || *ch.Item[0].Answer[0].ValueInteger != 6 {
			t.Errorf("conservative-therapy-weeks at depth 2 = %+v, want valueInteger 6", ch.Item[0].Answer)
		}
		pt := ch.Item[2]
		if got, want := linkIDs(pt.Item), []string{"prior-imaging"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("prior-treatment.item = %v, want %v (depth 3; prior-surgery=false is omitted)\n%s", got, want, qr)
		}
		if pt.Item[0].Answer[0].ValueBoolean == nil || !*pt.Item[0].Answer[0].ValueBoolean {
			t.Errorf("prior-imaging at depth 3 = %+v, want valueBoolean true", pt.Item[0].Answer)
		}
		// FR-17 attribution rides on every nested answer, not just top-level ones.
		for _, leaf := range []qrItemProbe{ch.Item[0], ch.Item[1], pt.Item[0]} {
			if got := originSource(t, leaf); got != "auto" {
				t.Errorf("%s: information-origin source = %q, want auto", leaf.LinkId, got)
			}
		}
	})

	t.Run("high-disability persona: the functional-status group appears with only its answered leaf", func(t *testing.T) {
		cc := mbrCoveredCC()
		cc.HighDisability = true
		cc.HighDisabilityRef = "Observation/obs-odi"
		qr, err := FillQuestionnaire([]byte(nestedDemoQuestionnaireJSON), cc, mbrCoveredQC())
		if err != nil {
			t.Fatalf("FillQuestionnaire: %v", err)
		}
		top := decodeQRItems(t, qr)
		if got, want := linkIDs(top), []string{"clinical-history", "functional-status"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("top-level items = %v, want %v\n%s", got, want, qr)
		}
		fs := top[1]
		// patient-reported-required=false and functional-status-oswestry (no local
		// source) stay omitted — the group carries exactly the answered leaf.
		if got, want := linkIDs(fs.Item), []string{"high-disability"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("functional-status.item = %v, want %v\n%s", got, want, qr)
		}
	})

	t.Run("2.2 line: the auto-client origin code reaches nested answers", func(t *testing.T) {
		qr, err := FillQuestionnaireAtLine("2.2", []byte(nestedDemoQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
		if err != nil {
			t.Fatalf("FillQuestionnaireAtLine(2.2): %v", err)
		}
		top := decodeQRItems(t, qr)
		if len(top) != 1 || len(top[0].Item) != 3 || len(top[0].Item[2].Item) != 1 {
			t.Fatalf("unexpected shape at 2.2: %s", qr)
		}
		if got := originSource(t, top[0].Item[2].Item[0]); got != "auto-client" {
			t.Errorf("prior-imaging at depth 3, line 2.2: source = %q, want auto-client", got)
		}
	})
}

// TestFillQuestionnaire_UnknownLinkIdFailsLoud: the demo fill is fenced to ONE
// canonical whose leaves it knows by linkId. A leaf it does not know — at any
// depth — is fixture drift, and the fill must refuse rather than emit a
// quietly incomplete QR (the same silence the nested-item walkers closed on the
// read side; the builder's `continue` used to skip it without a word).
func TestFillQuestionnaire_UnknownLinkIdFailsLoud(t *testing.T) {
	inject := func(t *testing.T, src string, mutate func(items []any) []any) []byte {
		t.Helper()
		var q map[string]any
		if err := json.Unmarshal([]byte(src), &q); err != nil {
			t.Fatal(err)
		}
		q["item"] = mutate(q["item"].([]any))
		out, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	unknown := map[string]any{"linkId": "smoking-status", "type": "string", "text": "Smoking status"}

	cases := []struct {
		name string
		q    []byte
	}{
		{"top level", inject(t, demoQuestionnaireJSON, func(items []any) []any { return append(items, unknown) })},
		{"inside a group", inject(t, nestedDemoQuestionnaireJSON, func(items []any) []any {
			g := items[1].(map[string]any)
			g["item"] = append(g["item"].([]any), unknown)
			return items
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qr, err := FillQuestionnaire(tc.q, mbrCoveredCC(), mbrCoveredQC())
			if err == nil {
				t.Fatalf("FillQuestionnaire accepted an unknown leaf linkId; want a fail-loud error, got QR:\n%s", qr)
			}
			if qr != nil {
				t.Errorf("FillQuestionnaire returned a QR alongside the error; must NEVER emit a half-filled QR")
			}
			if !strings.Contains(err.Error(), "smoking-status") {
				t.Errorf("error %q does not name the unknown linkId", err)
			}
		})
	}

	// Restraint: an unknown GROUP linkId is structure, not an answer — it must
	// recurse, not error. (A payer may regroup leaves without changing them.)
	regrouped := inject(t, demoQuestionnaireJSON, func(items []any) []any {
		return []any{map[string]any{"linkId": "everything", "type": "group", "item": items}}
	})
	qr, err := FillQuestionnaire(regrouped, mbrCoveredCC(), mbrCoveredQC())
	if err != nil {
		t.Fatalf("an unknown group linkId must not be refused: %v", err)
	}
	top := decodeQRItems(t, qr)
	if len(top) != 1 || top[0].LinkId != "everything" || len(top[0].Item) != 3 {
		t.Fatalf("regrouped fill = %s, want one 'everything' group holding the 3 answered leaves", qr)
	}
}

// TestWalkQuestionnaireItems_AnswerAxis: FHIR nests QR items on TWO axes —
// item.item (groups) and item.answer.item (a question's own child questions).
// The shared walker must carry the second axis too: an answered parent's
// answered children ride under answer.item; and a child that answers while its
// parent does not is REFUSED (there is no answer to hang it from), never
// dropped and never given a fabricated parent. Exercised through the generic
// manual fill, which accepts any questionnaire.
func TestWalkQuestionnaireItems_AnswerAxis(t *testing.T) {
	const q = `{
	  "resourceType": "Questionnaire",
	  "url": "http://example.org/fhir/Questionnaire/answer-axis",
	  "status": "active",
	  "item": [
	    {"linkId": "smoker", "type": "boolean", "text": "Current smoker?", "item": [
	      {"linkId": "smoker.packs", "type": "integer", "text": "Packs per day"}
	    ]}
	  ]
	}`
	qc := QRContext{PatientRef: "Patient/p1", CoverageRef: "Coverage/c1", OrderRef: "ServiceRequest/sr1", Authored: mbrCoveredQC().Authored}
	yes, two := true, 2

	t.Run("answered parent: child rides under answer.item", func(t *testing.T) {
		qr, err := FillQuestionnaireFromAnswers([]byte(q), map[string]Answer{
			"smoker":       {Boolean: &yes},
			"smoker.packs": {Integer: &two},
		}, "Practitioner/1", qc)
		if err != nil {
			t.Fatalf("FillQuestionnaireFromAnswers: %v", err)
		}
		top := decodeQRItems(t, qr)
		if len(top) != 1 || top[0].LinkId != "smoker" || len(top[0].Answer) != 1 {
			t.Fatalf("unexpected top shape: %s", qr)
		}
		if len(top[0].Item) != 0 {
			t.Errorf("child must NOT ride on item.item for a question parent (that axis is for groups): %s", qr)
		}
		kids := top[0].Answer[0].Item
		if len(kids) != 1 || kids[0].LinkId != "smoker.packs" || kids[0].Answer[0].ValueInteger == nil || *kids[0].Answer[0].ValueInteger != 2 {
			t.Fatalf("answer.item = %+v, want the answered smoker.packs child: %s", kids, qr)
		}
	})

	t.Run("unanswered parent, answered child: refused, not dropped", func(t *testing.T) {
		qr, err := FillQuestionnaireFromAnswers([]byte(q), map[string]Answer{
			"smoker.packs": {Integer: &two},
		}, "Practitioner/1", qc)
		if err == nil {
			t.Fatalf("want an error (child answered under an unanswered parent); got QR:\n%s", qr)
		}
		if qr != nil {
			t.Errorf("must not emit a QR alongside the refusal")
		}
		if !strings.Contains(err.Error(), "smoker") {
			t.Errorf("error %q does not name the parent item", err)
		}
	})

	t.Run("unanswered parent, unanswered child: both omitted", func(t *testing.T) {
		qr, err := FillQuestionnaireFromAnswers([]byte(q), map[string]Answer{}, "Practitioner/1", qc)
		if err != nil {
			t.Fatalf("FillQuestionnaireFromAnswers: %v", err)
		}
		if top := decodeQRItems(t, qr); len(top) != 0 {
			t.Fatalf("want no items, got %v", linkIDs(top))
		}
	})
}
