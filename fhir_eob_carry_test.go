package shnsdk

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// eobBuilt decodes a built EOB for assertions on its output, never on the
// builder's source.
func eobBuilt(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var eob map[string]json.RawMessage
	if err := json.Unmarshal(raw, &eob); err != nil {
		t.Fatalf("decode EOB: %v\n%s", err, raw)
	}
	return eob
}

func eobItemProduct(t *testing.T, raw []byte) string {
	t.Helper()
	var items []struct {
		ProductOrService json.RawMessage `json:"productOrService"`
	}
	if err := json.Unmarshal(eobBuilt(t, raw)["item"], &items); err != nil || len(items) != 1 {
		t.Fatalf("items: %v %s", err, raw)
	}
	return string(items[0].ProductOrService)
}

// TestBuildPADecisionEOB_NoMintedProductSystem: the builder never
// defaults a code system. A coding stated with no system carries only its
// code (R4 Coding.system is 0..1), and the request item's productOrService,
// when supplied, is carried exactly as written.
func TestBuildPADecisionEOB_NoMintedProductSystem(t *testing.T) {
	p := eobBaseParams()
	p.ProcedureSystem = ""
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := eobItemProduct(t, raw); got != `{"coding":[{"code":"99348","display":"Home visit, established patient"}]}` {
		t.Fatalf("productOrService = %s, want the code with no system", got)
	}

	p.ProductOrService = json.RawMessage(`{"coding":[{"code":"72148"},{"system":"http://payer.example/local","code":"L-1","version":"3"}],"text":"MRI L-spine"}`)
	raw, err = BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := eobItemProduct(t, raw); got != string(p.ProductOrService) {
		t.Fatalf("productOrService = %s, want the request item's own %s", got, p.ProductOrService)
	}
}

// TestBuildPADecisionEOB_NoteNumberAsStated: a payer note's number is
// carried when the payer stated one and omitted when it did not; the builder
// never numbers a note itself.
func TestBuildPADecisionEOB_NoteNumberAsStated(t *testing.T) {
	raw, err := BuildPADecisionEOBExact(eobNotesParams([]PASProcessNote{{Number: 7, Type: "display", Text: "a"}, {Text: "b"}}))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := eobProcessNotes(t, raw)
	want := []map[string]any{{"number": float64(7), "type": "display", "text": "a"}, {"text": "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("processNote = %v, want %v", got, want)
	}
}

// TestParseClaimResponse_KeepsNoteNumber: the decision parser keeps
// the payer's own note number (additive), and states none it did not send.
func TestParseClaimResponse_KeepsNoteNumber(t *testing.T) {
	cr := `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"P-1",` +
		`"processNote":[{"number":3,"type":"display","text":"three"},{"text":"none"}]}`
	got, err := ParseClaimResponse([]byte(cr))
	if err != nil {
		t.Fatal(err)
	}
	want := []PASProcessNote{{Number: 3, Type: "display", Text: "three"}, {Text: "none"}}
	if !reflect.DeepEqual(got.ProcessNotes, want) {
		t.Fatalf("notes = %+v, want %+v", got.ProcessNotes, want)
	}
}

// TestBuildPADecisionEOB_ErrorsEchoNoPayload: a refusal names the
// element and its position only, never a value the payer stated, so a
// finding or log line built from it carries no payload.
func TestBuildPADecisionEOB_ErrorsEchoNoPayload(t *testing.T) {
	const marker = "PLANTED-MARKER-2178"
	p := eobNotesParams([]PASProcessNote{{Text: "ok"}, {Type: marker, Text: "x"}})
	_, err := BuildPADecisionEOBExact(p)
	if err == nil || strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), "process note 2 type is not display, print or printoper") {
		t.Fatalf("note type refusal = %v, want the position and no payload", err)
	}

	p = eobNotesParams(nil)
	p.Adjudications = []json.RawMessage{json.RawMessage(`{"extension":[` + payerReviewAction("A3", "Not Certified", `{"url":"http://payer.example/`+marker+`","valueString":"`+marker+`"}`) + `],` + submittedCat + `}`)}
	_, err = BuildPADecisionEOBExact(p)
	var unmapped *PADecisionEOBUnmappedError
	if !errors.As(err, &unmapped) || unmapped.Position != 2 || strings.Contains(err.Error(), marker) ||
		!strings.Contains(err.Error(), "review action sub-extension 2 has no counterpart") {
		t.Fatalf("unmapped refusal = %v (%+v), want sub-extension 2 and no payload", err, unmapped)
	}
}

// TestBuildPADecisionEOB_SubmittedWithoutSystem: a payer submitted
// adjudication coded with no system is still the payer's submitted entry, so
// the request's net is not added beside it.
func TestBuildPADecisionEOB_SubmittedWithoutSystem(t *testing.T) {
	p := eobBaseParams()
	p.Adjudications = []json.RawMessage{json.RawMessage(`{"category":{"coding":[{"code":"submitted"}]}}`)}
	p.SubmittedAmount = json.RawMessage(rowNet)
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := eobAdjudicationsRaw(t, raw); len(got) != 1 || string(got[0]) != string(p.Adjudications[0]) {
		t.Fatalf("adjudication = %s, want only the payer's own", got)
	}
}

// TestBuildPADecisionEOB_HeaderAndAddItemAdjudication: PAS lets a
// payer state its review action on the ClaimResponse's header adjudication
// and on an addItem's adjudication. Both are carried onto the EOB's header
// adjudication and addItem with the same transcription as the item's; an
// unmapped review action in either writes no EOB.
func TestBuildPADecisionEOB_HeaderAndAddItemAdjudication(t *testing.T) {
	header := `{"extension":[` + payerReviewAction("A1", "Certified in total", numberSub) + `],` + submittedCat + `}`
	addItem := `{"itemSequence":[1],"productOrService":{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0431"}]},` +
		`"adjudication":[{"extension":[` + payerReviewAction("A1", "Certified in total") + `],` + submittedCat + `}]}`
	p := eobBaseParams()
	p.HeaderAdjudications = []json.RawMessage{json.RawMessage(header)}
	p.AddItems = []json.RawMessage{json.RawMessage(addItem)}
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	eob := eobBuilt(t, raw)
	wantHeader := `[{"extension":[` + pdexReviewAction("A1", "Certified in total", numberSub) + `],` + submittedCat + `}]`
	if string(eob["adjudication"]) != wantHeader {
		t.Fatalf("header adjudication = %s, want %s", eob["adjudication"], wantHeader)
	}
	wantAdd := `[{"itemSequence":[1],"productOrService":{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0431"}]},` +
		`"adjudication":[{"extension":[` + pdexReviewAction("A1", "Certified in total") + `],` + submittedCat + `}]}]`
	if string(eob["addItem"]) != wantAdd {
		t.Fatalf("addItem = %s, want %s", eob["addItem"], wantAdd)
	}

	for name, mutate := range map[string]func(*PADecisionEOBExactParams){
		"header": func(p *PADecisionEOBExactParams) {
			p.HeaderAdjudications = []json.RawMessage{json.RawMessage(`{"extension":[` + payerReviewAction("A1", "x", `{"url":"http://payer.example/x"}`) + `]}`)}
		},
		"addItem": func(p *PADecisionEOBExactParams) {
			p.AddItems = []json.RawMessage{json.RawMessage(`{"adjudication":[{"extension":[` + payerReviewAction("A1", "x", `{"url":"http://payer.example/x"}`) + `]}]}`)}
		},
		"addItem not an object": func(p *PADecisionEOBExactParams) { p.AddItems = []json.RawMessage{json.RawMessage(`[1]`)} },
	} {
		p := eobBaseParams()
		mutate(&p)
		if out, err := BuildPADecisionEOBExact(p); err == nil || out != nil {
			t.Errorf("%s: got %s, want no EOB", name, out)
		}
	}
}

// TestBuildPADecisionEOB_AddItemSubDetailSequenceTranscribed: R4 names the
// member ClaimResponse.addItem.subdetailSequence and
// ExplanationOfBenefit.addItem.subDetailSequence. The payer's addItem is
// carried with that one member renamed and its value as written (bridging the
// resource, not changing what it states); every other member is as written.
func TestBuildPADecisionEOB_AddItemSubDetailSequenceTranscribed(t *testing.T) {
	p := eobBaseParams()
	p.AddItems = []json.RawMessage{json.RawMessage(`{"itemSequence":[1],"detailSequence":[2],"subdetailSequence":[3,4],"productOrService":{"coding":[{"code":"E0431"}]}}`)}
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"itemSequence":[1],"detailSequence":[2],"subDetailSequence":[3,4],"productOrService":{"coding":[{"code":"E0431"}]}}]`
	if got := string(eobBuilt(t, raw)["addItem"]); got != want {
		t.Fatalf("addItem = %s, want %s", got, want)
	}
}
