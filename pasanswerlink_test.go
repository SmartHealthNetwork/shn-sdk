package shnsdk

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// Rows for linking an amendment's attached answer to the item the payer asked
// for it about: the answer is linked through Claim.item.
// informationSequence to a new Claim.supportingInfo entry, and the item it is
// linked to carries the PAS infoChanged marker "added", only when the payer's
// own pend names that item with a need the answer meets. Nothing else is
// linked or marked.

// answerLinkPend is a pended PAS response at line whose Task asks for items.
func answerLinkPend(t *testing.T, line string, items ...PendedItem) []byte {
	t.Helper()
	b, err := BuildPendedClaimResponseAtLineExact(line, PendedResponseInputs{
		Correlation: "convergence-pas-submit-0001",
		Created:     time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC),
		Task:        pastaskInputs(line, items...),
		Request:     testAnswerRequest,
	})
	if err != nil {
		t.Fatalf("build the pend: %v", err)
	}
	return b
}

// answerLinkInputs are the golden amendment inputs at line, carrying a
// supplemental report coded as the operative note the pend asks for.
func answerLinkInputs(t *testing.T, line string) ConformantClaimUpdateInputs {
	t.Helper()
	in := conformantUpdateInputsFromGolden(t)
	in.PriorClaim = testPriorClaimAt(t, line)
	var dr map[string]any
	if err := json.Unmarshal(in.DiagnosticReport, &dr); err != nil {
		t.Fatal(err)
	}
	dr["code"] = map[string]any{"coding": []any{map[string]any{"system": testOperativeNote.System, "code": testOperativeNote.Code, "display": testOperativeNote.Display}}}
	in.DiagnosticReport, _ = json.Marshal(dr)
	return in
}

type answerLinkView struct {
	supportingInfo []json.RawMessage
	infoSeq        map[int][]int  // item sequence → informationSequence
	marks          map[int]string // item sequence → infoChanged code
}

func answerLinkRead(t *testing.T, bundle []byte) answerLinkView {
	t.Helper()
	claim := bundleClaims(t, bundle)[0]
	v := answerLinkView{infoSeq: map[int][]int{}, marks: map[int]string{}}
	if raw, ok := claim["supportingInfo"]; ok {
		_ = json.Unmarshal(raw, &v.supportingInfo)
	}
	var items []struct {
		Sequence            int   `json:"sequence"`
		InformationSequence []int `json:"informationSequence"`
		Extension           []struct {
			URL       string `json:"url"`
			ValueCode string `json:"valueCode"`
		} `json:"extension"`
	}
	if err := json.Unmarshal(claim["item"], &items); err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.InformationSequence != nil {
			v.infoSeq[it.Sequence] = it.InformationSequence
		}
		for _, e := range it.Extension {
			if e.URL == pasInfoChangedExtensionURL {
				if _, dup := v.marks[it.Sequence]; dup {
					t.Fatalf("item %d carries infoChanged twice", it.Sequence)
				}
				v.marks[it.Sequence] = e.ValueCode
			}
		}
	}
	return v
}

func (v answerLinkView) none(t *testing.T, name string) {
	t.Helper()
	if len(v.supportingInfo) != 0 || len(v.infoSeq) != 0 || len(v.marks) != 0 {
		t.Errorf("%s: linked or marked: supportingInfo=%s informationSequence=%v infoChanged=%v", name, v.supportingInfo, v.infoSeq, v.marks)
	}
}

// TestPASUpdate_AnswerLinkedToTheItemThePayerAskedAbout: the payer's pend
// asks for the operative note on item 1, and the amendment attaches it. At
// PAS 2.0 and 2.1 the Claim gains one supportingInfo entry (PASTempCodes
// additionalInformation, referencing the attached report), item 1 points at it
// and carries infoChanged "added". At PAS 2.2 the AdditionalInformation slice
// requires a documentInformation extension whose X12 PWK01 report type no
// source states, so nothing is linked or marked.
func TestPASUpdate_AnswerLinkedToTheItemThePayerAskedAbout(t *testing.T) {
	for _, line := range pasLines {
		in := answerLinkInputs(t, line)
		in.Pend = answerLinkPend(t, line, PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		v := answerLinkRead(t, out)
		if line == "2.2" {
			v.none(t, line)
			continue
		}
		wantInfo := `{"category":{"coding":[{"code":"additionalInformation","system":"http://hl7.org/fhir/us/davinci-pas/CodeSystem/PASTempCodes"}]},"sequence":1,"valueReference":{"reference":"DiagnosticReport/convergence-dr-operative"}}`
		if len(v.supportingInfo) != 1 || canonJSON(v.supportingInfo[0]) != wantInfo {
			t.Errorf("%s: supportingInfo %s, want [%s]", line, v.supportingInfo, wantInfo)
		}
		if !slices.Equal(v.infoSeq[1], []int{1}) || len(v.infoSeq) != 1 {
			t.Errorf("%s: informationSequence %v, want item 1 → [1]", line, v.infoSeq)
		}
		if v.marks[1] != pasInfoChangedAdded || len(v.marks) != 1 {
			t.Errorf("%s: infoChanged %v, want item 1 added", line, v.marks)
		}
	}
}

// TestPASUpdate_QuestionnaireAnswerLinkedAt20: at PAS 2.0 the pend names the
// questionnaire by identifier; an amended QuestionnaireResponse answering
// that questionnaire (no report attached) is linked to the item.
func TestPASUpdate_QuestionnaireAnswerLinkedAt20(t *testing.T) {
	in := answerLinkInputs(t, "2.0")
	in.DiagnosticReport = nil
	var qr struct {
		Questionnaire string `json:"questionnaire"`
	}
	if err := json.Unmarshal(in.QR, &qr); err != nil || qr.Questionnaire == "" {
		t.Fatalf("the golden QR names no questionnaire: %v", err)
	}
	in.Pend = answerLinkPend(t, "2.0", PendedItem{Sequence: 1, QuestionnaireIDs: []PASIdentifier{{System: "urn:test:payer:questionnaire", Value: qr.Questionnaire}}})
	out, err := BuildConformantClaimUpdateBundleAtLineExact("2.0", in)
	if err != nil {
		t.Fatal(err)
	}
	v := answerLinkRead(t, out)
	if len(v.supportingInfo) != 1 || !slices.Equal(v.infoSeq[1], []int{1}) || v.marks[1] != pasInfoChangedAdded {
		t.Fatalf("supportingInfo=%s informationSequence=%v infoChanged=%v, want the amended QR linked to item 1 and item 1 added", v.supportingInfo, v.infoSeq, v.marks)
	}
	var si struct {
		ValueReference struct {
			Reference string `json:"reference"`
		} `json:"valueReference"`
	}
	_ = json.Unmarshal(v.supportingInfo[0], &si)
	if si.ValueReference.Reference != "QuestionnaireResponse/"+conformantPASUpdateQRID {
		t.Fatalf("supportingInfo references %q, want the amended QR", si.ValueReference.Reference)
	}
}

// TestPASUpdate_AnswerMatchingNoPendedItemLinksNothing: an answer the payer's
// pend does not ask for, a pend about an item the amendment does not carry, a
// pend with no Task about any item, and no pend at all link and mark nothing.
func TestPASUpdate_AnswerMatchingNoPendedItemLinksNothing(t *testing.T) {
	other := PASCoding{System: "http://loinc.org", Code: "28570-0", Display: "Procedure note"}
	for _, line := range pasLines {
		for name, items := range map[string][]PendedItem{
			"another attachment":      {{Sequence: 1, AttachmentCodes: []PASCoding{other}}},
			"the note, on item 2":     {{Sequence: 2, AttachmentCodes: []PASCoding{testOperativeNote}}},
			"a questionnaire need":    {questionnaireNeed(line, 1)},
			"the note, the same code": {{Sequence: 1, AttachmentCodes: []PASCoding{{System: pasSystemX12AttachmentReports, Code: testOperativeNote.Code}}}},
			"no pend":                 nil,
		} {
			if line != "2.0" && name == "the note, the same code" {
				continue // an X12 755 attachment code is a PAS 2.0 form only
			}
			var pend []byte
			if items != nil {
				pend = answerLinkPend(t, line, items...)
			}
			in := answerLinkInputs(t, line)
			in.Pend = pend
			out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
			if err != nil {
				t.Fatalf("%s %s: %v", line, name, err)
			}
			answerLinkRead(t, out).none(t, line+" "+name)
		}
	}
}

// TestPASAnswerLinks_TwoItemClaim: the link step over a two-item Claim. The
// payer's pend asks for the note on item 1 only, so only item 1 is linked to
// the new supportingInfo entry and only item 1 is marked; item 2 is sent as
// before. A pend about item 2's need the answer does not meet links neither.
func TestPASAnswerLinks_TwoItemClaim(t *testing.T) {
	def, _ := PASLineDef("2.0")
	dr := []byte(`{"resourceType":"DiagnosticReport","id":"convergence-dr-operative","code":{"coding":[{"system":"http://loinc.org","code":"11504-8"}]}}`)
	claim := []byte(`{"resourceType":"Claim","item":[{"sequence":1,"productOrService":{"text":"a"}},{"sequence":2,"productOrService":{"text":"b"}}]}`)
	var prior struct {
		Item []json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(claim, &prior); err != nil {
		t.Fatal(err)
	}
	pend := answerLinkPend(t, "2.0",
		PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}},
		PendedItem{Sequence: 2, AttachmentCodes: []PASCoding{{System: "http://loinc.org", Code: "28570-0"}}})
	linked, err := linkPASAnswer(def, claim, pend, dr)
	if err != nil {
		t.Fatal(err)
	}
	bundle := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(linked) + `}]}`)
	marks, err := pasInfoChangedMarks(bundle, prior.Item)
	if err != nil {
		t.Fatal(err)
	}
	marked, err := applyInfoChangedMarks(linked, marks)
	if err != nil {
		t.Fatal(err)
	}
	v := answerLinkRead(t, []byte(`{"resourceType":"Bundle","entry":[{"resource":`+string(marked)+`}]}`))
	if len(v.supportingInfo) != 1 || !slices.Equal(v.infoSeq[1], []int{1}) || len(v.infoSeq) != 1 {
		t.Fatalf("supportingInfo=%s informationSequence=%v, want item 1 only → [1]", v.supportingInfo, v.infoSeq)
	}
	if v.marks[1] != pasInfoChangedAdded || len(v.marks) != 1 {
		t.Fatalf("infoChanged %v, want item 1 added only", v.marks)
	}

	// The pend asks item 1 for something else: nothing links.
	pend = answerLinkPend(t, "2.0", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{{System: "http://loinc.org", Code: "28570-0"}}})
	linked, err = linkPASAnswer(def, claim, pend, dr)
	if err != nil {
		t.Fatal(err)
	}
	if canonJSON(linked) != canonJSON(claim) {
		t.Fatalf("an answer the pend did not ask for changed the Claim: %s", linked)
	}
}

// TestPASAnswerLinks_ReferencePayerCanonicalForm: the reference payer names
// the questionnaire as a valueCanonical on its questionnaires-needed input (a
// form PAS 2.0.1 does not define, reported as nonconformant, never coerced).
// It is still the payer's own statement of what item 1 needs, so the
// QuestionnaireResponse answering that canonical is linked to item 1; one
// answering another questionnaire is not.
func TestPASAnswerLinks_ReferencePayerCanonicalForm(t *testing.T) {
	def, _ := PASLineDef("2.0")
	claim := []byte(`{"resourceType":"Claim","item":[{"sequence":1}]}`)
	// The pend as the reference payer sends it: the conformant Task, with the
	// questionnaire named by a valueCanonical in place of the identifier.
	pend := answerLinkPend(t, "2.0", PendedItem{Sequence: 1, QuestionnaireIDs: []PASIdentifier{{System: "urn:test", Value: "placeholder"}}})
	var b map[string]any
	if err := json.Unmarshal(pend, &b); err != nil {
		t.Fatal(err)
	}
	for _, e := range b["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		if r["resourceType"] != "Task" {
			continue
		}
		for _, in := range r["input"].([]any) {
			im := in.(map[string]any)
			if _, ok := im["valueIdentifier"]; ok {
				delete(im, "valueIdentifier")
				im["valueCanonical"] = "http://payer.example/Questionnaire/HomeOxygen"
			}
		}
	}
	pend, _ = json.Marshal(b)
	qr := func(canonical string) []byte {
		return []byte(`{"resourceType":"QuestionnaireResponse","id":"convergence-qr-amended","questionnaire":"` + canonical + `"}`)
	}
	pended, _, err := ParsePendedResponseDetail(pend)
	if err != nil || !pended {
		t.Fatalf("the reference payer's pend does not parse as pended: %v", err)
	}
	linked, err := linkPASAnswer(def, claim, pend, qr("http://payer.example/Questionnaire/HomeOxygen|1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	v := answerLinkRead(t, []byte(`{"resourceType":"Bundle","entry":[{"resource":`+string(linked)+`}]}`))
	if !slices.Equal(v.infoSeq[1], []int{1}) {
		t.Fatalf("informationSequence %v, want item 1 linked to the QR answering the payer's canonical", v.infoSeq)
	}
	linked, err = linkPASAnswer(def, claim, pend, qr("http://payer.example/Questionnaire/Other"))
	if err != nil {
		t.Fatal(err)
	}
	if canonJSON(linked) != canonJSON(claim) {
		t.Fatalf("a QR answering another questionnaire was linked: %s", linked)
	}
}
