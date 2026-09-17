package shnsdk

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func eobNotesParams(dec PADecision, notes []PASProcessNote) PADecisionEOBParams {
	return PADecisionEOBParams{
		ID: "e1", PatientRef: "Patient/p", CoverageRef: "Coverage/p",
		CPTCode: "99348", CPTDisplay: "Home visit, established patient",
		Decision: dec, Created: time.Unix(0, 0).UTC(), ProcessNotes: notes,
	}
}

func eobProcessNotes(t *testing.T, raw []byte) ([]map[string]any, bool) {
	t.Helper()
	var eob map[string]json.RawMessage
	if err := json.Unmarshal(raw, &eob); err != nil {
		t.Fatal(err)
	}
	v, ok := eob["processNote"]
	if !ok {
		return nil, false
	}
	var notes []map[string]any
	if err := json.Unmarshal(v, &notes); err != nil {
		t.Fatal(err)
	}
	return notes, true
}

// TestBuildPADecisionEOB_SuppliedNotesOnly: a denied EOB built with the payer's
// own notes carries exactly those notes, in order and numbered from 1, and no
// fixed appeal text; an empty (non-nil) set carries no processNote at all.
func TestBuildPADecisionEOB_SuppliedNotesOnly(t *testing.T) {
	notes := []PASProcessNote{{Type: "print", Text: "Appeal within 45 days — see ¶ 3."}, {Text: "**Call** us."}}
	raw, err := BuildPADecisionEOB(eobNotesParams(PADecisionDenied, notes))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := eobProcessNotes(t, raw)
	want := []map[string]any{
		{"number": float64(1), "type": "print", "text": notes[0].Text},
		{"number": float64(2), "text": notes[1].Text},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("processNote = %v, want %v", got, want)
	}
	if bytes.Contains(raw, []byte(EOBAppealNote)) {
		t.Fatalf("EOB carries the fixed appeal note alongside the payer's own: %s", raw)
	}

	raw, err = BuildPADecisionEOB(eobNotesParams(PADecisionDenied, []PASProcessNote{}))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := eobProcessNotes(t, raw); ok {
		t.Fatalf("empty notes: processNote = %v, want absent", v)
	}

	// Supplied notes ride on an approval too; nothing is added when none are.
	raw, err = BuildPADecisionEOB(eobNotesParams(PADecisionApproved, notes[:1]))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := eobProcessNotes(t, raw); len(got) != 1 || got[0]["text"] != notes[0].Text {
		t.Fatalf("approved processNote = %v, want the supplied note", got)
	}
}

// TestBuildPADecisionEOB_NilNotesKeepsDeprecatedDefault: with ProcessNotes nil
// a denied EOB keeps the deprecated fixed EOBAppealNote (unchanged output for
// existing callers) and an approved EOB carries none.
func TestBuildPADecisionEOB_NilNotesKeepsDeprecatedDefault(t *testing.T) {
	raw, err := BuildPADecisionEOB(eobNotesParams(PADecisionDenied, nil))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := eobProcessNotes(t, raw)
	want := []map[string]any{{"number": float64(1), "type": "print", "text": EOBAppealNote}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("processNote = %v, want the deprecated default %v", got, want)
	}
	raw, err = BuildPADecisionEOB(eobNotesParams(PADecisionApproved, nil))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := eobProcessNotes(t, raw); ok {
		t.Fatalf("approved: processNote = %v, want absent", v)
	}
}

// TestBuildPADecisionEOB_NoteGuards: a supplied note without text or with a
// type outside the FHIR NoteType codes is refused.
func TestBuildPADecisionEOB_NoteGuards(t *testing.T) {
	for name, n := range map[string]PASProcessNote{"empty text": {Type: "print"}, "unknown type": {Type: "email", Text: "x"}} {
		if _, err := BuildPADecisionEOB(eobNotesParams(PADecisionDenied, []PASProcessNote{{Text: "ok"}, n})); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
