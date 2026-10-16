package shnsdk

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// eobBaseParams states every element a decision EOB requires, as a request
// Claim and a payer's ClaimResponse would supply them.
func eobBaseParams() PADecisionEOBExactParams {
	focal := true
	return PADecisionEOBExactParams{
		ID: "e1", PatientRef: "Patient/p",
		ClaimType:    json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"professional"}]}`),
		Created:      "2026-06-03T00:00:00Z",
		Insurer:      PADecisionEOBReference{Reference: "Organization/ins-1"},
		Provider:     PADecisionEOBReference{Reference: "Practitioner/pr-1"},
		Insurance:    []PADecisionEOBInsurance{{Focal: &focal, Coverage: PADecisionEOBReference{Reference: "Coverage/cov-1"}}},
		Outcome:      "complete",
		ItemSequence: 1,
		CPTCode:      "99348", CPTDisplay: "Home visit, established patient",
	}
}

func eobNotesParams(notes []PASProcessNote) PADecisionEOBExactParams {
	p := eobBaseParams()
	p.ProcessNotes = notes
	return p
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
// own notes carries exactly those notes, in order, numbered only as the payer
// numbered them, and no fixed appeal text; an empty (non-nil) set carries no
// processNote at all.
func TestBuildPADecisionEOB_SuppliedNotesOnly(t *testing.T) {
	notes := []PASProcessNote{{Number: 1, Type: "print", Text: "Appeal within 45 days — see ¶ 3."}, {Text: "**Call** us."}}
	raw, err := BuildPADecisionEOBExact(eobNotesParams(notes))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := eobProcessNotes(t, raw)
	want := []map[string]any{
		{"number": float64(1), "type": "print", "text": notes[0].Text},
		{"text": notes[1].Text},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("processNote = %v, want %v", got, want)
	}
	if bytes.Contains(raw, []byte("Appeal window")) {
		t.Fatalf("EOB carries the fixed appeal note alongside the payer's own: %s", raw)
	}

	raw, err = BuildPADecisionEOBExact(eobNotesParams([]PASProcessNote{}))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := eobProcessNotes(t, raw); ok {
		t.Fatalf("empty notes: processNote = %v, want absent", v)
	}

	// Supplied notes ride on an approval too; nothing is added when none are.
	raw, err = BuildPADecisionEOBExact(eobNotesParams(notes[:1]))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := eobProcessNotes(t, raw); len(got) != 1 || got[0]["text"] != notes[0].Text {
		t.Fatalf("approved processNote = %v, want the supplied note", got)
	}
}

// TestBuildPADecisionEOB_NilNotesWriteNone: with ProcessNotes nil no EOB
// carries a processNote: the fixed appeal text the payer never sent is gone
// .
func TestBuildPADecisionEOB_NilNotesWriteNone(t *testing.T) {
	raw, err := BuildPADecisionEOBExact(eobNotesParams(nil))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := eobProcessNotes(t, raw); ok || bytes.Contains(raw, []byte("Appeal window")) {
		t.Fatalf("processNote = %v, want absent: %s", v, raw)
	}
}

// TestBuildPADecisionEOB_NoteGuards: a supplied note without text or with a
// type outside the FHIR NoteType codes is refused.
func TestBuildPADecisionEOB_NoteGuards(t *testing.T) {
	for name, n := range map[string]PASProcessNote{"empty text": {Type: "print"}, "unknown type": {Type: "email", Text: "x"}} {
		if _, err := BuildPADecisionEOBExact(eobNotesParams([]PASProcessNote{{Text: "ok"}, n})); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
