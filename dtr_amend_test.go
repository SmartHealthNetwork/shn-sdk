package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
)

// findQRItem returns every item with linkId anywhere in the tree (both axes) with
// the path of ancestor linkIds it was reached through.
func findQRItem(items []qrItemProbe, linkID string, path []string) (hits [][]string, found []qrItemProbe) {
	for _, it := range items {
		if it.LinkId == linkID {
			hits = append(hits, append(append([]string{}, path...), it.LinkId))
			found = append(found, it)
		}
		h, f := findQRItem(it.Item, linkID, append(append([]string{}, path...), it.LinkId))
		hits, found = append(hits, h...), append(found, f...)
		for _, a := range it.Answer {
			h, f := findQRItem(a.Item, linkID, append(append([]string{}, path...), it.LinkId))
			hits, found = append(hits, h...), append(found, f...)
		}
	}
	return hits, found
}

func mustAmend(t *testing.T, qr, q, item []byte) []qrItemProbe {
	t.Helper()
	got, err := AmendQRWithItemIn(qr, q, item)
	if err != nil {
		t.Fatalf("AmendQRWithItem: %v", err)
	}
	return decodeQRItems(t, got)
}

func attestedOswestry(t *testing.T) []byte {
	t.Helper()
	item, err := BuildManualAttestedItem("functional-status-oswestry", "42",
		Attestation{NPI: "1999999999", Text: "I attest these are my clinical findings.", When: "2026-08-21"})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// TestAmendQRWithItem_PlacesByQuestionnaire: an amendment lands WHERE THE
// QUESTIONNAIRE PUTS THE ITEM, not at the top of the QR. Appending at the top
// level produced a QR whose shape disagreed with its own questionnaire and —
// when the populated QR already held the item's unanswered shell at depth —
// two items with one linkId, which the adjudicator refuses as ambiguous.
func TestAmendQRWithItem_PlacesByQuestionnaire(t *testing.T) {
	q := []byte(nestedSandboxQuestionnaireJSON)

	t.Run("into an existing group, after its answered sibling", func(t *testing.T) {
		cc := mbrCoveredCC()
		cc.HighDisability = true
		qr, err := FillQuestionnaire(q, cc, mbrCoveredQC())
		if err != nil {
			t.Fatal(err)
		}
		top := mustAmend(t, qr, q, attestedOswestry(t))
		hits, _ := findQRItem(top, "functional-status-oswestry", nil)
		if len(hits) != 1 || strings.Join(hits[0], "/") != "functional-status/functional-status-oswestry" {
			t.Fatalf("oswestry placed at %v, want exactly one at functional-status/functional-status-oswestry", hits)
		}
		fs := top[1]
		if got := strings.Join(linkIDs(fs.Item), ","); got != "high-disability,functional-status-oswestry" {
			t.Fatalf("functional-status.item = %s, want high-disability,functional-status-oswestry", got)
		}
	})

	t.Run("creating the group shell when the QR has none", func(t *testing.T) {
		qr, err := FillQuestionnaire(q, mbrCoveredCC(), mbrCoveredQC()) // no functional-status group
		if err != nil {
			t.Fatal(err)
		}
		top := mustAmend(t, qr, q, attestedOswestry(t))
		if got := strings.Join(linkIDs(top), ","); got != "clinical-history,functional-status" {
			t.Fatalf("top-level = %s, want clinical-history,functional-status (shell created in questionnaire order)", got)
		}
		if got := strings.Join(linkIDs(top[1].Item), ","); got != "functional-status-oswestry" {
			t.Fatalf("functional-status.item = %s, want only the amended item", got)
		}
		if len(top[1].Answer) != 0 {
			t.Errorf("a group shell must carry no answer")
		}
	})

	t.Run("supersedes an existing occurrence at depth — exactly one survives", func(t *testing.T) {
		// A populated QR that already holds an unsigned placeholder at depth.
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"clinical-history","item":[{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]}]},` +
			`{"linkId":"functional-status","item":[` +
			`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` +
			`{"linkId":"functional-status-oswestry","answer":[{"valueString":"unsigned-auto"}]}]}]}`)
		top := mustAmend(t, qr, q, attestedOswestry(t))
		hits, found := findQRItem(top, "functional-status-oswestry", nil)
		if len(hits) != 1 || strings.Join(hits[0], "/") != "functional-status/functional-status-oswestry" {
			t.Fatalf("want exactly one oswestry at depth, got %v", hits)
		}
		if len(found[0].Answer) != 1 {
			t.Fatalf("surviving item answer count = %d", len(found[0].Answer))
		}
		// The attested value (42) must have won over the placeholder.
		got, _ := AmendQRWithItemIn(qr, q, attestedOswestry(t))
		if !strings.Contains(string(got), `"valueString":"42"`) || strings.Contains(string(got), "unsigned-auto") {
			t.Fatalf("attested value did not supersede the placeholder: %s", got)
		}
	})

	t.Run("depth 3: inserted in questionnaire order, not last", func(t *testing.T) {
		qr, err := FillQuestionnaire(q, mbrCoveredCC(), mbrCoveredQC()) // prior-treatment holds only prior-imaging
		if err != nil {
			t.Fatal(err)
		}
		// neuro-deficit sits between conservative-therapy-weeks and prior-treatment
		// in the questionnaire; drop it from the QR, then amend it back.
		var m map[string]json.RawMessage
		_ = json.Unmarshal(qr, &m)
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(m["item"], &items)
		var ch []json.RawMessage
		_ = json.Unmarshal(items[0]["item"], &ch)
		ch = append(ch[:1], ch[2:]...) // [weeks, prior-treatment]
		items[0]["item"], _ = json.Marshal(ch)
		m["item"], _ = json.Marshal(items)
		qr, _ = json.Marshal(m)

		neuro := []byte(`{"linkId":"neuro-deficit","answer":[{"valueBoolean":true}]}`)
		top := mustAmend(t, qr, q, neuro)
		if got := strings.Join(linkIDs(top[0].Item), ","); got != "conservative-therapy-weeks,neuro-deficit,prior-treatment" {
			t.Fatalf("clinical-history.item = %s, want questionnaire order with neuro-deficit in the middle", got)
		}
		surgery := []byte(`{"linkId":"prior-surgery","answer":[{"valueBoolean":true}]}`)
		top = mustAmend(t, qr, q, surgery)
		hits, _ := findQRItem(top, "prior-surgery", nil)
		if len(hits) != 1 || strings.Join(hits[0], "/") != "clinical-history/prior-treatment/prior-surgery" {
			t.Fatalf("prior-surgery placed at %v, want clinical-history/prior-treatment/prior-surgery", hits)
		}
	})

	t.Run("flat questionnaire: appended last, as before", func(t *testing.T) {
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]}]}`)
		top := mustAmend(t, qr, []byte(sandboxQuestionnaireJSON), attestedOswestry(t))
		if got := strings.Join(linkIDs(top), ","); got != "conservative-therapy-weeks,functional-status-oswestry" {
			t.Fatalf("flat amend = %s", got)
		}
	})
}

// TestAmendQRWithItem_AnswerAxis: an item that is the child of a QUESTION (not a
// group) lives under that question's answer. The amend hangs it there when the
// parent is answered, and refuses when it is not — it never fabricates a parent
// answer to hang the child from, and never picks one of several answers.
func TestAmendQRWithItem_AnswerAxis(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"http://example.org/q","status":"active","item":[
	  {"linkId":"smoker","type":"boolean","item":[{"linkId":"smoker.packs","type":"integer"}]}]}`)
	packs := []byte(`{"linkId":"smoker.packs","answer":[{"valueInteger":2}]}`)

	t.Run("answered parent", func(t *testing.T) {
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[{"linkId":"smoker","answer":[{"valueBoolean":true}]}]}`)
		top := mustAmend(t, qr, q, packs)
		hits, _ := findQRItem(top, "smoker.packs", nil)
		if len(hits) != 1 || len(top[0].Answer) != 1 || len(top[0].Answer[0].Item) != 1 || len(top[0].Item) != 0 {
			t.Fatalf("smoker.packs must ride under smoker's answer.item: hits=%v top=%+v", hits, top)
		}
	})
	t.Run("parent absent from the QR: refused", func(t *testing.T) {
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[]}`)
		if _, err := AmendQRWithItemIn(qr, q, packs); err == nil || !strings.Contains(err.Error(), "smoker") {
			t.Fatalf("want a refusal naming the unanswered parent, got %v", err)
		}
	})
	t.Run("parent present but unanswered: refused", func(t *testing.T) {
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[{"linkId":"smoker"}]}`)
		got, err := AmendQRWithItemIn(qr, q, packs)
		if err == nil || !strings.Contains(err.Error(), "smoker") {
			t.Fatalf("want a refusal naming the unanswered parent (no fabricated answer), got err=%v qr=%s", err, got)
		}
	})
	t.Run("parent with two answers: refused", func(t *testing.T) {
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[{"linkId":"smoker","answer":[{"valueBoolean":true},{"valueBoolean":false}]}]}`)
		if _, err := AmendQRWithItemIn(qr, q, packs); err == nil {
			t.Fatal("want a refusal for an ambiguous parent")
		}
	})
}

// TestAmendQRWithItem_Refusals: the inputs the amend must not guess about.
func TestAmendQRWithItem_Refusals(t *testing.T) {
	q := []byte(nestedSandboxQuestionnaireJSON)
	qr, err := FillQuestionnaire(q, mbrCoveredCC(), mbrCoveredQC())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		qr, q, it  []byte
		wantInErr  string
		wantNilErr bool
	}{
		{"linkId not in the questionnaire", qr, q, []byte(`{"linkId":"smoking-status","answer":[{"valueString":"never"}]}`), "smoking-status", false},
		{"item without a linkId", qr, q, []byte(`{"answer":[{"valueString":"42"}]}`), "linkId", false},
		{"questionnaire argument is not a Questionnaire (swapped args)", qr, qr, attestedOswestry(t), "Questionnaire", false},
		{"qr argument is not a QuestionnaireResponse (swapped args)", q, q, attestedOswestry(t), "QuestionnaireResponse", false},
		{"two existing occurrences (repeating group): ambiguous", []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"functional-status","item":[{"linkId":"functional-status-oswestry","answer":[{"valueString":"1"}]}]},` +
			`{"linkId":"functional-status","item":[{"linkId":"functional-status-oswestry","answer":[{"valueString":"2"}]}]}]}`), q, attestedOswestry(t), "functional-status-oswestry", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AmendQRWithItemIn(tc.qr, tc.q, tc.it)
			if err == nil {
				t.Fatalf("want an error, got QR:\n%s", got)
			}
			if got != nil {
				t.Errorf("must not return a QR alongside an error")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantInErr)
			}
		})
	}
}

// TestAmendedNestedQRAdjudicates closes the loop the amend exists for: the
// nested, amended QR is what the sandbox adjudicator reads — a UC-06 pend
// (high-disability, unattested) must approve once the attested item is placed
// at depth, and a UC-07 pend (patient-reported-required) must approve once the
// patient-attested item is placed at depth.
func TestAmendedNestedQRAdjudicates(t *testing.T) {
	q := []byte(nestedSandboxQuestionnaireJSON)
	t.Run("clinician (UC-06 shape)", func(t *testing.T) {
		cc := mbrCoveredCC()
		cc.HighDisability = true
		qr, err := FillQuestionnaire(q, cc, mbrCoveredQC())
		if err != nil {
			t.Fatal(err)
		}
		if dec, err := SandboxAdjudicate(qr, false, testNow, nil); err != nil || dec.Outcome != PASPended {
			t.Fatalf("unattested nested QR must pend: out=%v err=%v", dec.Outcome, err)
		}
		amended, err := AmendQRWithItemIn(qr, q, attestedOswestry(t))
		if err != nil {
			t.Fatal(err)
		}
		if dec, err := SandboxAdjudicate(amended, false, testNow, nil); err != nil || dec.Outcome != PASApproved {
			t.Fatalf("attested nested QR must approve: out=%v err=%v", dec.Outcome, err)
		}
	})
	t.Run("patient (UC-07 shape)", func(t *testing.T) {
		cc := mbrCoveredCC()
		cc.PatientReported = true
		qr, err := FillQuestionnaire(q, cc, mbrCoveredQC())
		if err != nil {
			t.Fatal(err)
		}
		if dec, err := SandboxAdjudicate(qr, false, testNow, nil); err != nil || dec.Outcome != PASPended {
			t.Fatalf("unattested nested QR must pend: out=%v err=%v", dec.Outcome, err)
		}
		item, err := BuildPatientAttestedItem("functional-status-oswestry", "42", "Patient/MBR-COVERED", "2026-08-21")
		if err != nil {
			t.Fatal(err)
		}
		amended, err := AmendQRWithItemIn(qr, q, item)
		if err != nil {
			t.Fatal(err)
		}
		if dec, err := SandboxAdjudicate(amended, false, testNow, nil); err != nil || dec.Outcome != PASApproved {
			t.Fatalf("patient-attested nested QR must approve: out=%v err=%v", dec.Outcome, err)
		}
	})
}

// TestAmendQRWithItem_SupersedeAnswerAxis: supersede reaches items under an
// ANSWER too (item.answer.item), not only under groups — exactly one occurrence
// survives, in place, carrying the amended value.
func TestAmendQRWithItem_SupersedeAnswerAxis(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"http://example.org/q","status":"active","item":[
	  {"linkId":"smoker","type":"boolean","item":[{"linkId":"smoker.packs","type":"integer"}]}]}`)
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"smoker","answer":[{"valueBoolean":true,"item":[{"linkId":"smoker.packs","answer":[{"valueInteger":1}]}]}]}]}`)
	top := mustAmend(t, qr, q, []byte(`{"linkId":"smoker.packs","answer":[{"valueInteger":2}]}`))
	hits, found := findQRItem(top, "smoker.packs", nil)
	if len(hits) != 1 || strings.Join(hits[0], "/") != "smoker/smoker.packs" {
		t.Fatalf("want exactly one smoker.packs under smoker's answer, got %v", hits)
	}
	if len(found[0].Answer) != 1 || found[0].Answer[0].ValueInteger == nil || *found[0].Answer[0].ValueInteger != 2 {
		t.Fatalf("superseded value = %+v, want valueInteger 2", found[0].Answer)
	}
}

// TestAmendQRWithItem_LegacySupersedesAtDepth: the deprecated 2-arg form has no
// questionnaire to place by, but it MUST still supersede an existing occurrence
// wherever it sits — otherwise a gateway on this form amending a populated QR
// that already holds the item's shell inside its group would leave two items
// for one linkId (the adjudicator's ambiguity refusal). Append-at-top-level
// applies only when the linkId is absent.
func TestAmendQRWithItem_LegacySupersedesAtDepth(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"functional-status","item":[` +
		`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` +
		`{"linkId":"functional-status-oswestry","answer":[{"valueString":"unsigned-auto"}]}]}]}`)
	got, err := AmendQRWithItem(qr, attestedOswestry(t))
	if err != nil {
		t.Fatalf("AmendQRWithItem: %v", err)
	}
	top := decodeQRItems(t, got)
	hits, _ := findQRItem(top, "functional-status-oswestry", nil)
	if len(hits) != 1 || strings.Join(hits[0], "/") != "functional-status/functional-status-oswestry" {
		t.Fatalf("legacy amend must supersede in place at depth, got occurrences %v", hits)
	}
	if !strings.Contains(string(got), `"valueString":"42"`) || strings.Contains(string(got), "unsigned-auto") {
		t.Fatalf("attested value did not supersede the placeholder: %s", got)
	}
}

// adaptiveHHAJSON is the shape a Da Vinci payer serves for an SDC ADAPTIVE
// questionnaire: the questionnaireAdaptive extension, and only the first
// top-level group delivered (the later, enableWhen-gated groups arrive through
// $next-question as answers accumulate). The item a clinician later attests
// ("3.2", inside group "3") is therefore NOT in the tree the client holds.
const adaptiveHHAJSON = `{
  "resourceType": "Questionnaire",
  "url": "http://example.org/fhir/Questionnaire/HomeHealthAssessment",
  "status": "active",
  "extension": [{"url": "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-questionnaireAdaptive", "valueBoolean": true}],
  "item": [
    {"linkId": "1", "type": "group", "text": "Service Category Selection", "item": [
      {"linkId": "1.1", "type": "choice", "text": "What category of home health service is being requested?", "required": true}
    ]}
  ]
}`

// TestAmendQRWithItemIn_AdaptiveQuestionnaire: on an adaptive questionnaire the
// delivered tree is partial BY DESIGN, so "not an item of the questionnaire" does
// not mean "not an item of the questionnaire" — it means "not delivered yet". An
// absent item whose group has not been delivered is appended at the top level
// (the only placement the client can make honestly; the source questionnaire's
// group for it is unknown client-side). A refusal here returned 500 on the
// clinician-attestation resume against a real Da Vinci payer. Non-adaptive
// questionnaires keep the refusal: there the tree is complete and an unknown
// linkId is a caller error. An adaptive item that IS delivered is still placed
// by the tree, and an existing occurrence is still superseded in place.
func TestAmendQRWithItemIn_AdaptiveQuestionnaire(t *testing.T) {
	base := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"1","item":[{"linkId":"1.1","answer":[{"valueCoding":{"system":"http://snomed.info/sct","code":"91251008"}}]}]}]}`)
	item, err := BuildManualAttestedItem("3.2", "Impaired ambulation", Attestation{NPI: "1999999999", Text: "attest", When: "2026-08-21"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("undelivered group on an adaptive questionnaire: appended at the top level", func(t *testing.T) {
		got, err := AmendQRWithItemIn(base, []byte(adaptiveHHAJSON), item)
		if err != nil {
			t.Fatalf("AmendQRWithItemIn on an adaptive questionnaire refused an undelivered item: %v", err)
		}
		top := decodeQRItems(t, got)
		if got := strings.Join(linkIDs(top), ","); got != "1,3.2" {
			t.Fatalf("top-level items = %s, want 1,3.2", got)
		}
	})

	t.Run("same tree WITHOUT the adaptive extension: still refused", func(t *testing.T) {
		q := strings.Replace(adaptiveHHAJSON, `"extension": [{"url": "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-questionnaireAdaptive", "valueBoolean": true}],`, "", 1)
		if !strings.Contains(q, `"item"`) || strings.Contains(q, "questionnaireAdaptive") {
			t.Fatal("control questionnaire not built")
		}
		if _, err := AmendQRWithItemIn(base, []byte(q), item); err == nil || !strings.Contains(err.Error(), "3.2") {
			t.Fatalf("want the unknown-linkId refusal on a non-adaptive questionnaire, got %v", err)
		}
	})

	t.Run("adaptive, item delivered: placed by the tree, not appended", func(t *testing.T) {
		delivered := strings.Replace(adaptiveHHAJSON, `]}
  ]
}`, `]},
    {"linkId": "3", "type": "group", "text": "Physical Therapy Assessment", "item": [
      {"linkId": "3.1", "type": "string"}, {"linkId": "3.2", "type": "text"}, {"linkId": "3.3", "type": "text"}
    ]}
  ]
}`, 1)
		if !strings.Contains(delivered, `"linkId": "3.2"`) {
			t.Fatal("delivered questionnaire not built")
		}
		got, err := AmendQRWithItemIn(base, []byte(delivered), item)
		if err != nil {
			t.Fatal(err)
		}
		hits, _ := findQRItem(decodeQRItems(t, got), "3.2", nil)
		if len(hits) != 1 || strings.Join(hits[0], "/") != "3/3.2" {
			t.Fatalf("delivered adaptive item placed at %v, want 3/3.2", hits)
		}
	})

	t.Run("adaptive, item already present at depth: superseded in place, not appended", func(t *testing.T) {
		qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
			`{"linkId":"3","item":[{"linkId":"3.2","answer":[{"valueString":"old"}]}]}]}`)
		got, err := AmendQRWithItemIn(qr, []byte(adaptiveHHAJSON), item)
		if err != nil {
			t.Fatal(err)
		}
		hits, _ := findQRItem(decodeQRItems(t, got), "3.2", nil)
		if len(hits) != 1 || strings.Join(hits[0], "/") != "3/3.2" || strings.Contains(string(got), `"old"`) {
			t.Fatalf("supersede at depth on an adaptive questionnaire: hits=%v qr=%s", hits, got)
		}
	})
}
