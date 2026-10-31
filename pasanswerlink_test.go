package shnsdk

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
// requires a documentInformation extension whose X12 PWK01 report type only the
// requester states, so with no statement nothing is linked or marked
// (TestPASUpdate_StatedReportTypeLinksAt22).
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
	linked, err := linkPASAnswer(def, claim, pend, dr, nil)
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
	linked, err = linkPASAnswer(def, claim, pend, dr, nil)
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
	linked, err := linkPASAnswer(def, claim, pend, qr("http://payer.example/Questionnaire/HomeOxygen|1.0.0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	v := answerLinkRead(t, []byte(`{"resourceType":"Bundle","entry":[{"resource":`+string(linked)+`}]}`))
	if !slices.Equal(v.infoSeq[1], []int{1}) {
		t.Fatalf("informationSequence %v, want item 1 linked to the QR answering the payer's canonical", v.infoSeq)
	}
	linked, err = linkPASAnswer(def, claim, pend, qr("http://payer.example/Questionnaire/Other"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if canonJSON(linked) != canonJSON(claim) {
		t.Fatalf("a QR answering another questionnaire was linked: %s", linked)
	}
}

// The reference payer's 2.2 pend, as it ran on the network: a CommunicationRequest
// beside the ClaimResponse, naming the line, the questionnaire it asks for and the
// documentation code, and a PWK01 category nothing here reads.
func commRequestPend(t *testing.T, base []byte, line int, questionnaire string) []byte {
	t.Helper()
	var ext []map[string]any
	if line > 0 {
		ext = append(ext, map[string]any{"url": pasExtServiceLineNumber, "valuePositiveInt": line})
	}
	if questionnaire != "" {
		ext = append(ext, map[string]any{"url": pasExtRequestedQuestionnaire, "valueCanonical": questionnaire})
	}
	entry, err := json.Marshal(map[string]any{"resource": map[string]any{
		"resourceType": "CommunicationRequest", "id": "cr1", "status": "active",
		"extension": ext,
		"category":  []any{map[string]any{"coding": []any{map[string]any{"system": pasSystemX12AttachmentReports, "code": "15"}}}},
		"payload":   []any{map[string]any{"contentString": "102089-0"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var b map[string]json.RawMessage
	if err := json.Unmarshal(base, &b); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(b["entry"], &entries); err != nil {
		t.Fatal(err)
	}
	kept := entries[:0:0]
	for _, e := range entries {
		var r struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
			} `json:"resource"`
		}
		_ = json.Unmarshal(e, &r)
		if r.Resource.ResourceType != "Task" {
			kept = append(kept, e)
		}
	}
	b["entry"], _ = json.Marshal(append(kept, entry))
	out, _ := json.Marshal(b)
	return out
}

// A pend read from a CommunicationRequest names the same needs a Task does: the
// line, and the questionnaire or code asked for. It never reads the category.
func TestParsePendedResponseDetail_ReadsACommunicationRequest(t *testing.T) {
	base := answerLinkPend(t, "2.2", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
	pended, detail, err := ParsePendedResponseDetail(commRequestPend(t, base, 2, "http://example.org/Q"))
	if err != nil || !pended {
		t.Fatalf("pended %v err %v", pended, err)
	}
	if len(detail.Tasks) != 0 || len(detail.Requests) != 1 {
		t.Fatalf("tasks %d requests %+v", len(detail.Tasks), detail.Requests)
	}
	r := detail.Requests[0]
	if r.Sequence != 2 || r.Questionnaire != "http://example.org/Q" || !slices.Equal(r.Codes, []string{"102089-0"}) {
		t.Fatalf("request %+v", r)
	}
}

// The requester's statement is what lets an answer link at PAS 2.2. Absent, nothing
// links, whatever the pend asks: reading the pend never supplies the report type.
func TestPASUpdate_StatedReportTypeLinksAt22(t *testing.T) {
	const canonical = "http://example.org/fhir/Questionnaire/HomeHealthAssessment"
	stated := json.RawMessage(`{"system":"` + pasSystemX12AttachmentReports + `","code":"PY"}`)
	base := answerLinkPend(t, "2.2", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})

	// A Task's need, with the statement: linked, marked, and the entry carries it.
	in := answerLinkInputs(t, "2.2")
	in.Pend, in.ReportType = base, stated
	out, err := BuildConformantClaimUpdateBundleAtLineExact("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	v := answerLinkRead(t, out)
	if !slices.Equal(v.infoSeq[1], []int{1}) || v.marks[1] != pasInfoChangedAdded {
		t.Fatalf("informationSequence %v, infoChanged %v", v.infoSeq, v.marks)
	}
	want := `"extension":[{"extension":[{"url":"reportTypeCode","valueCodeableConcept":{"coding":[{"code":"PY","system":"` + pasSystemX12AttachmentReports + `"}]}}],"url":"` + pasExtDocumentInformation + `"}]`
	if len(v.supportingInfo) != 1 || !strings.Contains(canonJSON(v.supportingInfo[0]), want) {
		t.Fatalf("supportingInfo %s, want it to hold %s", v.supportingInfo, want)
	}

	// The same pend, no statement: nothing is linked or marked.
	in = answerLinkInputs(t, "2.2")
	in.Pend = base
	out, err = BuildConformantClaimUpdateBundleAtLineExact("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	answerLinkRead(t, out).none(t, "no statement")

	// The reference payer's CommunicationRequest pend, with a QuestionnaireResponse
	// answering what it asks for: linked by the line it names.
	qrIn := answerLinkInputs(t, "2.2")
	qrIn.DiagnosticReport = nil
	var qr map[string]any
	if err := json.Unmarshal(qrIn.QR, &qr); err != nil {
		t.Fatal(err)
	}
	qr["questionnaire"] = canonical
	qrIn.QR, _ = json.Marshal(qr)
	qrIn.Pend, qrIn.ReportType = commRequestPend(t, base, 1, canonical), stated
	out, err = BuildConformantClaimUpdateBundleAtLineExact("2.2", qrIn)
	if err != nil {
		t.Fatal(err)
	}
	if v := answerLinkRead(t, out); !slices.Equal(v.infoSeq[1], []int{1}) {
		t.Fatalf("a CommunicationRequest pend linked %v", v.infoSeq)
	}
	// Another questionnaire, or a request naming no line, ties nothing.
	for name, pend := range map[string][]byte{
		"another questionnaire": commRequestPend(t, base, 1, "http://example.org/Other"),
		"no line":               commRequestPend(t, base, 0, canonical),
		"a line no item has":    commRequestPend(t, base, 9, canonical),
	} {
		in := qrIn
		in.Pend = pend
		out, err := BuildConformantClaimUpdateBundleAtLineExact("2.2", in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		answerLinkRead(t, out).none(t, name)
	}
}

// A CommunicationRequest adds no way to fail a pend read: a value this reader
// cannot read is one the payer does not state.
func TestParsePendedResponseDetail_ToleratesWhatItCannotRead(t *testing.T) {
	const canonical = "http://example.org/Q"
	base := answerLinkPend(t, "2.2", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
	pend := commRequestPend(t, base, 1, canonical)
	for name, row := range map[string]struct {
		from, to string
		sequence int
	}{
		"a request's line as a string": {`"valuePositiveInt":1`, `"valuePositiveInt":"1"`, 0},
		"a fractional line":            {`"valuePositiveInt":1`, `"valuePositiveInt":1.5`, 0},
		"a payload that is an object":  {`"payload":[{"contentString":"102089-0"}]`, `"payload":{"contentString":"102089-0"}`, 1},
	} {
		got := []byte(strings.Replace(string(pend), row.from, row.to, 1))
		if string(got) == string(pend) {
			t.Fatalf("%s: fixture unchanged", name)
		}
		pended, detail, err := ParsePendedResponseDetail(got)
		if err != nil || !pended {
			t.Fatalf("%s: pended %v, err %v", name, pended, err)
		}
		if len(detail.Requests) != 1 || detail.Requests[0].Sequence != row.sequence {
			t.Fatalf("%s: requests %+v", name, detail.Requests)
		}
	}
	// Another resource's malformed extension is no business of this reader.
	var b map[string]json.RawMessage
	_ = json.Unmarshal(pend, &b)
	var entries []json.RawMessage
	_ = json.Unmarshal(b["entry"], &entries)
	bad, _ := json.Marshal(map[string]any{"resource": map[string]any{"resourceType": "Patient", "extension": map[string]any{"valuePositiveInt": "3"}, "payload": 5}})
	b["entry"], _ = json.Marshal(append(entries, bad))
	mixed, _ := json.Marshal(b)
	if pended, detail, err := ParsePendedResponseDetail(mixed); err != nil || !pended || len(detail.Requests) != 1 {
		t.Fatalf("a Patient's extension: pended %v, err %v, requests %+v", pended, err, detail.Requests)
	}
}

// The report type is the requester's, whatever the pend carries. The pend's PWK01
// category is 15: the entry states the requester's PY, and with no statement
// nothing links, a CommunicationRequest pend included.
func TestPASUpdate_ACommunicationRequestNeverSuppliesTheReportType(t *testing.T) {
	const canonical = "http://example.org/fhir/Questionnaire/HomeHealthAssessment"
	base := answerLinkPend(t, "2.2", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
	in := answerLinkInputs(t, "2.2")
	in.DiagnosticReport = nil
	var qr map[string]any
	if err := json.Unmarshal(in.QR, &qr); err != nil {
		t.Fatal(err)
	}
	qr["questionnaire"] = canonical
	in.QR, _ = json.Marshal(qr)
	in.Pend = commRequestPend(t, base, 1, canonical)

	out, err := BuildConformantClaimUpdateBundleAtLineExact("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	answerLinkRead(t, out).none(t, "no statement")

	for _, null := range []string{`null`, `  null  `} {
		in.ReportType = json.RawMessage(null)
		out, err = BuildConformantClaimUpdateBundleAtLineExact("2.2", in)
		if err != nil {
			t.Fatal(err)
		}
		answerLinkRead(t, out).none(t, "a null statement")
	}

	in.ReportType = json.RawMessage(`{"system":"` + pasSystemX12AttachmentReports + `","code":"PY"}`)
	out, err = BuildConformantClaimUpdateBundleAtLineExact("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	v := answerLinkRead(t, out)
	if len(v.supportingInfo) != 1 || !strings.Contains(canonJSON(v.supportingInfo[0]), `"code":"PY"`) || strings.Contains(canonJSON(v.supportingInfo[0]), `"code":"15"`) {
		t.Fatalf("supportingInfo %s", v.supportingInfo)
	}
}

// What ties an answer to a CommunicationRequest: a LOINC code, by that system; a
// questionnaire, for a QuestionnaireResponse; and a line named on the resource or,
// at PAS 2.1.0, on the payload.
func TestPASPendedItemsAnswered_CommunicationRequest(t *testing.T) {
	base := answerLinkPend(t, "2.2", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
	const canonical = "http://example.org/Q"
	pend := commRequestPend(t, base, 2, canonical)
	code := func(system string) []byte {
		return []byte(`{"resourceType":"DiagnosticReport","code":{"coding":[{"system":"` + system + `","code":"102089-0"}]}}`)
	}
	for name, row := range map[string]struct {
		answer []byte
		want   []int
	}{
		"its LOINC code":        {code(pasSystemLOINC), []int{2}},
		"its code in a system":  {code("http://example.org/other"), nil},
		"its questionnaire":     {[]byte(`{"resourceType":"QuestionnaireResponse","questionnaire":"` + canonical + `"}`), []int{2}},
		"a version of it":       {[]byte(`{"resourceType":"QuestionnaireResponse","questionnaire":"` + canonical + `|1.0.0"}`), []int{2}},
		"another questionnaire": {[]byte(`{"resourceType":"QuestionnaireResponse","questionnaire":"http://example.org/R"}`), nil},
		"not a QR, naming it":   {[]byte(`{"resourceType":"DocumentReference","questionnaire":"` + canonical + `"}`), nil},
	} {
		if got := pasPendedItemsAnswered(pend, row.answer); !slices.Equal(got, row.want) {
			t.Errorf("%s: %v, want %v", name, got, row.want)
		}
	}
	// PAS 2.1.0 names the line on the payload, with that payload's code.
	payload := `"payload":[{"contentString":"102089-0","extension":[{"url":"` + pasExtServiceLineNumber + `","valuePositiveInt":3}]},{"contentString":"11506-3"}]`
	onPayload := []byte(strings.Replace(string(commRequestPend(t, base, 0, "")), `"payload":[{"contentString":"102089-0"}]`, payload, 1))
	if got := pasPendedItemsAnswered(onPayload, code(pasSystemLOINC)); !slices.Equal(got, []int{3}) {
		t.Fatalf("a line on the payload: %v", got)
	}
}

// A request naming a line on the resource and on a payload: the payload's own line
// takes that payload's code, and the rest take the resource's. A junk element beside
// valid ones hides none of them, and only a CommunicationRequest is read.
func TestParsePendedResponseDetail_RequestLines(t *testing.T) {
	base := answerLinkPend(t, "2.2", PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
	const canonical = "http://example.org/Q"
	pend := string(commRequestPend(t, base, 1, canonical))
	line := func(n int) string {
		return `{"url":"` + pasExtServiceLineNumber + `","valuePositiveInt":` + strconv.Itoa(n) + `}`
	}
	payload := `"payload":[7,{"contentString":5},{"contentString":"X","extension":[7,` + line(2) + `]},{"contentString":"Y"}]`
	got := strings.Replace(pend, `"payload":[{"contentString":"102089-0"}]`, payload, 1)
	if got == pend {
		t.Fatal("fixture unchanged")
	}
	got = strings.Replace(got, `"extension":[{"url":"`+pasExtServiceLineNumber, `"extension":[7,{"url":"`+pasExtServiceLineNumber, 1)

	var b map[string]json.RawMessage
	_ = json.Unmarshal([]byte(got), &b)
	var entries []json.RawMessage
	_ = json.Unmarshal(b["entry"], &entries)
	other, _ := json.Marshal(map[string]any{"resource": map[string]any{
		"resourceType": "Communication",
		"extension":    []any{map[string]any{"url": pasExtServiceLineNumber, "valuePositiveInt": 8}},
		"payload":      []any{map[string]any{"contentString": "Z"}},
	}})
	b["entry"], _ = json.Marshal(append(entries, other))
	raw, _ := json.Marshal(b)

	_, detail, err := ParsePendedResponseDetail(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []PendedCommunicationRequest{
		{Sequence: 2, Codes: []string{"X"}, Questionnaire: canonical},
		{Sequence: 1, Codes: []string{"Y"}, Questionnaire: canonical},
	}
	if !reflect.DeepEqual(detail.Requests, want) {
		t.Fatalf("requests %+v, want %+v", detail.Requests, want)
	}
	qr := []byte(`{"resourceType":"QuestionnaireResponse","questionnaire":"` + canonical + `"}`)
	if got := pasPendedItemsAnswered(raw, qr); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("a QR tied %v, want both lines", got)
	}
}

// Below 2.2 the slice needs no report type, so a CommunicationRequest's tie links
// with no statement, and the entry carries no documentInformation.
func TestPASUpdate_ACommunicationRequestLinksBelow22(t *testing.T) {
	for _, line := range []string{"2.0", "2.1"} {
		base := answerLinkPend(t, line, PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
		const canonical = "http://example.org/fhir/Questionnaire/HomeHealthAssessment"
		in := answerLinkInputs(t, line)
		in.DiagnosticReport = nil
		var qr map[string]any
		if err := json.Unmarshal(in.QR, &qr); err != nil {
			t.Fatal(err)
		}
		qr["questionnaire"] = canonical
		in.QR, _ = json.Marshal(qr)
		in.Pend = commRequestPend(t, base, 1, canonical)
		out, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		v := answerLinkRead(t, out)
		if !slices.Equal(v.infoSeq[1], []int{1}) || v.marks[1] != pasInfoChangedAdded {
			t.Fatalf("%s: informationSequence %v, infoChanged %v", line, v.infoSeq, v.marks)
		}
		if strings.Contains(string(out), "documentInformation") {
			t.Fatalf("%s: an entry below 2.2 carries documentInformation", line)
		}
	}
}

// A statement that cannot be carried is refused, at every line, never carried or
// defaulted. Below 2.2 the slice has no such extension: a valid statement builds
// what the line always built.
func TestPASUpdate_ReportTypeStatement(t *testing.T) {
	for _, line := range pasLines {
		for name, raw := range map[string]string{
			"no code":        `{"system":"` + pasSystemX12AttachmentReports + `"}`,
			"no system":      `{"code":"PY"}`,
			"another system": `{"system":"http://loinc.org","code":"PY"}`,
			"not a coding":   `"PY"`,
		} {
			in := answerLinkInputs(t, line)
			in.ReportType = json.RawMessage(raw)
			_, buildErr := BuildConformantClaimUpdateBundleAtLineExact(line, in)
			if !errors.Is(buildErr, ErrPASClaimFactInvalid) {
				t.Errorf("%s %s: err %v, want ErrPASClaimFactInvalid", line, name, buildErr)
			}
			if err := CheckPASReportType(in.ReportType); err == nil || err.Error() != buildErr.Error() {
				t.Errorf("%s %s: the check says %v, the build says %v", line, name, err, buildErr)
			}
		}
	}
	for _, none := range []string{"", "null", "  null  "} {
		if err := CheckPASReportType(json.RawMessage(none)); err != nil {
			t.Errorf("no statement (%q) refused: %v", none, err)
		}
	}
	if err := CheckPASReportType(json.RawMessage(`{"system":"` + pasSystemX12AttachmentReports + `","code":"PY"}`)); err != nil {
		t.Errorf("a valid statement refused: %v", err)
	}
	for _, line := range []string{"2.0", "2.1"} {
		in := answerLinkInputs(t, line)
		in.Pend = answerLinkPend(t, line, PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}})
		plain, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		in.ReportType = json.RawMessage(`{"system":"` + pasSystemX12AttachmentReports + `","code":"PY"}`)
		stated, err := BuildConformantClaimUpdateBundleAtLineExact(line, in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stated), "documentInformation") || len(stated) != len(plain) {
			t.Errorf("%s: a statement changed the build", line)
		}
	}
}
