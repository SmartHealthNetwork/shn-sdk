package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// pastaskInputs is a complete Task input for line with the given needs.
func pastaskInputs(line string, items ...PendedItem) PendedTaskInputs {
	if len(items) == 0 {
		items = []PendedItem{{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}}}
	}
	return PendedTaskInputs{
		ID:         "pended-req",
		Identifier: PASIdentifier{System: "urn:test:payer:pa-request", Value: "req-1"},
		Status:     "requested",
		Patient:    "Patient/MBR-1",
		Claim:      "https://provider.test/fhir/Claim/claim-1",
		Requester:  testPayerIdentifier,
		Owner:      testPayerIdentifier,
		PayerURL:   "https://payer.test/fhir",
		AuthoredOn: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Items:      items,
	}
}

// questionnaireNeed is a questionnaire need in the form line defines.
func questionnaireNeed(line string, seq int) PendedItem {
	if line == "2.0" {
		return PendedItem{Sequence: seq, QuestionnaireIDs: []PASIdentifier{{System: "urn:test:payer:questionnaire", Value: "home-oxygen"}}}
	}
	return PendedItem{Sequence: seq, QuestionnaireContexts: []string{"ctx-" + line}}
}

type builtTask struct {
	ID   string `json:"id"`
	Meta struct {
		Profile []string `json:"profile"`
	} `json:"meta"`
	Identifier []PASIdentifier `json:"identifier"`
	Status     string          `json:"status"`
	Intent     string          `json:"intent"`
	Code       struct {
		Coding []PASCoding `json:"coding"`
	} `json:"code"`
	For struct {
		Reference string `json:"reference"`
	} `json:"for"`
	Requester *struct {
		Identifier PASIdentifier `json:"identifier"`
	} `json:"requester"`
	Owner *struct {
		Identifier PASIdentifier `json:"identifier"`
	} `json:"owner"`
	ReasonCode struct {
		Coding []PASCoding `json:"coding"`
	} `json:"reasonCode"`
	ReasonReference struct {
		Reference string `json:"reference"`
	} `json:"reasonReference"`
	Input []map[string]json.RawMessage `json:"input"`
}

func decodeTask(t *testing.T, raw []byte) builtTask {
	t.Helper()
	var b builtTask
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode Task: %v\n%s", err, raw)
	}
	return b
}

func inputTypeCode(t *testing.T, in map[string]json.RawMessage) string {
	t.Helper()
	var ty struct {
		Coding []PASCoding `json:"coding"`
	}
	if err := json.Unmarshal(in["type"], &ty); err != nil || len(ty.Coding) != 1 || ty.Coding[0].System != PASTempCodesSystem {
		t.Fatalf("input type is not one PASTempCodes coding: %s", in["type"])
	}
	return ty.Coding[0].Code
}

// TestPendedTask_ReasonReferenceTargetsClaim: the Task's reasonReference is
// exactly the request Claim it was given; a reference to anything but a
// Claim is refused.
func TestPendedTask_ReasonReferenceTargetsClaim(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		for _, claim := range []string{"https://provider.test/fhir/Claim/claim-1", "Claim/claim-1", "urn:uuid:5f9a3c0e-1b2d-4e5f-8a9b-0c1d2e3f4a5b", "https://provider.test/fhir/Claim/claim-1/_history/2"} {
			in := pastaskInputs(line)
			in.Claim = claim
			tasks, err := BuildPendedTasks(line, in)
			if err != nil {
				t.Fatalf("%s %s: %v", line, claim, err)
			}
			if got := decodeTask(t, tasks[0]).ReasonReference.Reference; got != claim {
				t.Errorf("%s: reasonReference = %q, want %q", line, got, claim)
			}
		}
		for _, bad := range []string{"https://provider.test/fhir/ClaimResponse/cr-1", "Patient/MBR-1", "ServiceRequest/sr-1", "Claim/", "claim-1", "https://provider.test/fhir/PreClaim/x"} {
			in := pastaskInputs(line)
			in.Claim = bad
			if _, err := BuildPendedTasks(line, in); err == nil || !strings.Contains(err.Error(), "not a Claim reference") {
				t.Errorf("%s: reasonReference %q accepted (err=%v)", line, bad, err)
			}
		}
	}
}

// TestPendedTask_RequiresIdentifierAndPayerURL: every fact only the payer
// has is required; the builder never invents one.
func TestPendedTask_RequiresIdentifierAndPayerURL(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		for _, row := range []struct {
			name   string
			mutate func(*PendedTaskInputs)
			want   string
		}{
			{"no identifier", func(in *PendedTaskInputs) { in.Identifier = PASIdentifier{} }, "Task identifier"},
			{"identifier without system", func(in *PendedTaskInputs) { in.Identifier.System = "" }, "Task identifier"},
			{"identifier without value", func(in *PendedTaskInputs) { in.Identifier.Value = " " }, "Task identifier"},
			{"no payer URL", func(in *PendedTaskInputs) { in.PayerURL = "" }, "payer URL"},
			{"relative payer URL", func(in *PendedTaskInputs) { in.PayerURL = "/fhir" }, "not an absolute"},
			{"non-http payer URL", func(in *PendedTaskInputs) { in.PayerURL = "ftp://payer.test/fhir" }, "not an absolute"},
			{"no status", func(in *PendedTaskInputs) { in.Status = "" }, "HRex task status"},
			{"draft status", func(in *PendedTaskInputs) { in.Status = "draft" }, "HRex task status"},
			{"no patient", func(in *PendedTaskInputs) { in.Patient = "" }, "not a Patient reference"},
			{"group patient", func(in *PendedTaskInputs) { in.Patient = "Group/g1" }, "not a Patient reference"},
			{"no claim", func(in *PendedTaskInputs) { in.Claim = "" }, "request Claim reference"},
			{"bad id", func(in *PendedTaskInputs) { in.ID = "task 1" }, "not a FHIR id"},
			{"no items", func(in *PendedTaskInputs) { in.Items = nil }, "at least one needed item"},
			{"item without a need", func(in *PendedTaskInputs) { in.Items = []PendedItem{{Sequence: 1}} }, "names no needed"},
			{"sequence zero", func(in *PendedTaskInputs) { in.Items[0].Sequence = 0 }, "below 1"},
			{"repeated sequence", func(in *PendedTaskInputs) { in.Items = append(in.Items, in.Items[0]) }, "listed twice"},
			{"attachment without system", func(in *PendedTaskInputs) { in.Items[0].AttachmentCodes[0].System = "" }, "system and a code"},
			{"attachment from another system", func(in *PendedTaskInputs) { in.Items[0].AttachmentCodes[0].System = "http://snomed.info/sct" }, "not bound"},
			{"requester half set", func(in *PendedTaskInputs) { in.Requester = PASIdentifier{Value: "x"} }, "requester identifier"},
			{"owner half set", func(in *PendedTaskInputs) { in.Owner = PASIdentifier{System: "urn:x"} }, "owner identifier"},
		} {
			in := pastaskInputs(line)
			in.Items = []PendedItem{{Sequence: 1, AttachmentCodes: []PASCoding{testOperativeNote}}}
			row.mutate(&in)
			if _, err := BuildPendedTasks(line, in); err == nil || !strings.Contains(err.Error(), row.want) {
				t.Errorf("%s %s: err = %v, want it to mention %q", line, row.name, err, row.want)
			}
		}
		// Requester and owner: required at 2.0, optional later.
		in := pastaskInputs(line)
		in.Requester, in.Owner = PASIdentifier{}, PASIdentifier{}
		tasks, err := BuildPendedTasks(line, in)
		if line == "2.0" {
			if err == nil || !strings.Contains(err.Error(), "required at PAS 2.0") {
				t.Errorf("2.0 without requester/owner: err = %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s without requester/owner: %v", line, err)
		}
		if b := decodeTask(t, tasks[0]); b.Requester != nil || b.Owner != nil {
			t.Errorf("%s: requester/owner written though not supplied: %s", line, tasks[0])
		}
	}
	if _, err := BuildPendedTasks("3.0", pastaskInputs("2.0")); err == nil {
		t.Error("unknown line accepted")
	}
}

// assertTaskShape checks the fixed and participant-supplied elements.
func assertTaskShape(t *testing.T, line string, in PendedTaskInputs, b builtTask, wantCode string) {
	t.Helper()
	def, _ := PASLineDef(line)
	if len(b.Meta.Profile) != 1 || b.Meta.Profile[0] != PASTaskProfile+"|"+def.PackageVersion {
		t.Errorf("%s: meta.profile = %v", line, b.Meta.Profile)
	}
	if len(b.Identifier) != 1 || b.Identifier[0] != in.Identifier {
		t.Errorf("%s: identifier = %v", line, b.Identifier)
	}
	if b.Status != in.Status || b.Intent != "order" || b.For.Reference != in.Patient {
		t.Errorf("%s: status/intent/for = %q/%q/%q", line, b.Status, b.Intent, b.For.Reference)
	}
	if len(b.Code.Coding) != 1 || b.Code.Coding[0] != (PASCoding{System: PASTempCodesSystem, Code: wantCode}) {
		t.Errorf("%s: code = %v, want %s", line, b.Code.Coding, wantCode)
	}
	if len(b.ReasonCode.Coding) != 1 || b.ReasonCode.Coding[0] != (PASCoding{System: PASTempCodesSystem, Code: "priorAuthorization"}) {
		t.Errorf("%s: reasonCode = %v", line, b.ReasonCode.Coding)
	}
	if b.Requester == nil || b.Requester.Identifier != in.Requester || b.Owner == nil || b.Owner.Identifier != in.Owner {
		t.Errorf("%s: requester/owner = %+v/%+v", line, b.Requester, b.Owner)
	}
	if len(b.Input) < 2 {
		t.Fatalf("%s: input has %d entries, profile requires 2..*", line, len(b.Input))
	}
	if code := inputTypeCode(t, b.Input[0]); code != "payer-url" || string(b.Input[0]["valueUrl"]) != `"`+in.PayerURL+`"` {
		t.Errorf("%s: first input = %s", line, mustJSON(t, b.Input[0]))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lineNumberOf returns the input's line-number extension URL and value.
func lineNumberOf(t *testing.T, in map[string]json.RawMessage) (string, string) {
	t.Helper()
	var ext []map[string]json.RawMessage
	if err := json.Unmarshal(in["extension"], &ext); err != nil || len(ext) != 1 {
		t.Fatalf("input extension = %s, want one line-number extension", in["extension"])
	}
	var url string
	_ = json.Unmarshal(ext[0]["url"], &url)
	for k, v := range ext[0] {
		if k != "url" {
			return url, k + "=" + string(v)
		}
	}
	return url, ""
}

// TestPendedTask_InvariantAttachments: an attachment-request-code Task has an
// attachments-needed input for each code (profile invariant AttachmentNeeded),
// each with the line's line-number extension, and nothing of the other kind.
func TestPendedTask_InvariantAttachments(t *testing.T) {
	wantExt := map[string][2]string{
		"2.0": {"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-paLineNumber", "valueInteger=3"},
		"2.1": {"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-paLineNumber", "valueInteger=3"},
		"2.2": {"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-serviceLineNumber", "valuePositiveInt=3"},
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		codes := []PASCoding{testOperativeNote, {System: "http://loinc.org", Code: "28570-0"}}
		in := pastaskInputs(line, PendedItem{Sequence: 3, AttachmentCodes: codes})
		tasks, err := BuildPendedTasks(line, in)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("%s: tasks=%d err=%v", line, len(tasks), err)
		}
		b := decodeTask(t, tasks[0])
		assertTaskShape(t, line, in, b, PASTaskCodeAttachments)
		needs := b.Input[1:]
		if len(needs) != len(codes) {
			t.Fatalf("%s: %d need inputs, want %d", line, len(needs), len(codes))
		}
		for i, n := range needs {
			if code := inputTypeCode(t, n); code != "attachments-needed" {
				t.Errorf("%s: need %d type %q", line, i, code)
			}
			var cc struct {
				Coding []PASCoding `json:"coding"`
			}
			if err := json.Unmarshal(n["valueCodeableConcept"], &cc); err != nil || len(cc.Coding) != 1 || cc.Coding[0] != codes[i] {
				t.Errorf("%s: need %d value = %s", line, i, n["valueCodeableConcept"])
			}
			if url, v := lineNumberOf(t, n); url != wantExt[line][0] || v != wantExt[line][1] {
				t.Errorf("%s: line number %s %s, want %v", line, url, v, wantExt[line])
			}
		}
	}
	// X12 755 report types are bound at 2.0 only.
	x12 := PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{{System: "https://codesystem.x12.org/005010/755", Code: "OZ"}}}
	if _, err := BuildPendedTasks("2.0", pastaskInputs("2.0", x12)); err != nil {
		t.Errorf("2.0 X12 755 code refused: %v", err)
	}
	for _, line := range []string{"2.1", "2.2"} {
		if _, err := BuildPendedTasks(line, pastaskInputs(line, x12)); err == nil {
			t.Errorf("%s: X12 755 code accepted", line)
		}
	}
}

// TestPendedTask_InvariantQuestionnaire: an attachment-request-questionnaire
// Task carries questionnaires-needed identifiers at 2.0 (invariant
// QuestionnaireNeeded) and questionnaire-context strings at 2.1 and 2.2
// (invariant QuestionnaireContext); the other line's form is refused.
func TestPendedTask_InvariantQuestionnaire(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		item := questionnaireNeed(line, 2)
		in := pastaskInputs(line, item)
		tasks, err := BuildPendedTasks(line, in)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("%s: tasks=%d err=%v", line, len(tasks), err)
		}
		b := decodeTask(t, tasks[0])
		assertTaskShape(t, line, in, b, PASTaskCodeQuestionnaires)
		if len(b.Input) != 2 {
			t.Fatalf("%s: inputs = %d, want payer-url and one need", line, len(b.Input))
		}
		n := b.Input[1]
		switch line {
		case "2.0":
			var id PASIdentifier
			if inputTypeCode(t, n) != "questionnaires-needed" || json.Unmarshal(n["valueIdentifier"], &id) != nil || id != item.QuestionnaireIDs[0] {
				t.Errorf("2.0 need = %s", mustJSON(t, n))
			}
		default:
			if inputTypeCode(t, n) != "questionnaire-context" || string(n["valueString"]) != `"ctx-`+line+`"` {
				t.Errorf("%s need = %s", line, mustJSON(t, n))
			}
		}
		if len(n) != 3 { // extension, type, one value
			t.Errorf("%s: need has members %v", line, rawKeys(n))
		}
	}
	ctx := PendedItem{Sequence: 1, QuestionnaireContexts: []string{"c"}}
	if _, err := BuildPendedTasks("2.0", pastaskInputs("2.0", ctx)); err == nil || !strings.Contains(err.Error(), "not defined at PAS 2.0.1") {
		t.Errorf("2.0 accepted a questionnaire context: %v", err)
	}
	ids := PendedItem{Sequence: 1, QuestionnaireIDs: []PASIdentifier{{System: "urn:q", Value: "q"}}}
	for _, line := range []string{"2.1", "2.2"} {
		if _, err := BuildPendedTasks(line, pastaskInputs(line, ids)); err == nil || !strings.Contains(err.Error(), "not defined at PAS") {
			t.Errorf("%s accepted a questionnaire identifier: %v", line, err)
		}
	}
	if _, err := BuildPendedTasks("2.1", pastaskInputs("2.1", PendedItem{Sequence: 1, QuestionnaireContexts: []string{" "}})); err == nil {
		t.Error("an empty questionnaire context was accepted")
	}
	if _, err := BuildPendedTasks("2.0", pastaskInputs("2.0", PendedItem{Sequence: 1, QuestionnaireIDs: []PASIdentifier{{Value: "q"}}})); err == nil {
		t.Error("a questionnaire identifier without a system was accepted")
	}
}

func rawKeys(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPendedTask_TwoTasksWhenBothNeeded: needs of both kinds yield two Tasks,
// attachments first, each with only its own kind, its own code and id, and
// the same payer facts; items are written in line order.
func TestPendedTask_TwoTasksWhenBothNeeded(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		q := questionnaireNeed(line, 2)
		both := PendedItem{Sequence: 2, AttachmentCodes: []PASCoding{testOperativeNote}, QuestionnaireIDs: q.QuestionnaireIDs, QuestionnaireContexts: q.QuestionnaireContexts}
		first := PendedItem{Sequence: 1, AttachmentCodes: []PASCoding{{System: "http://loinc.org", Code: "28570-0"}}}
		in := pastaskInputs(line, both, first)
		tasks, err := BuildPendedTasks(line, in)
		if err != nil || len(tasks) != 2 {
			t.Fatalf("%s: tasks=%d err=%v", line, len(tasks), err)
		}
		att, qt := decodeTask(t, tasks[0]), decodeTask(t, tasks[1])
		assertTaskShape(t, line, in, att, PASTaskCodeAttachments)
		assertTaskShape(t, line, in, qt, PASTaskCodeQuestionnaires)
		if att.ID != "pended-req" || qt.ID != "pended-req-questionnaire" {
			t.Errorf("%s: ids %q, %q", line, att.ID, qt.ID)
		}
		var seqs []string
		for _, n := range att.Input[1:] {
			if inputTypeCode(t, n) != "attachments-needed" {
				t.Errorf("%s: attachment Task carries %s", line, mustJSON(t, n))
			}
			_, v := lineNumberOf(t, n)
			seqs = append(seqs, v)
		}
		if len(seqs) != 2 || !strings.HasSuffix(seqs[0], "=1") || !strings.HasSuffix(seqs[1], "=2") {
			t.Errorf("%s: attachment line order %v", line, seqs)
		}
		if len(qt.Input) != 2 || inputTypeCode(t, qt.Input[1]) == "attachments-needed" {
			t.Errorf("%s: questionnaire Task inputs %s", line, mustJSON(t, qt.Input))
		}
	}
}

// TestPendedTask_NoTaskFieldsInvented: the Task holds exactly the members the
// builder documents, and nothing the inputs did not supply.
func TestPendedTask_NoTaskFieldsInvented(t *testing.T) {
	in := pastaskInputs("2.2")
	in.AuthoredOn = time.Time{}
	in.Requester, in.Owner = PASIdentifier{}, PASIdentifier{}
	tasks, err := BuildPendedTasks("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(tasks[0], &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"resourceType", "id", "meta", "identifier", "status", "intent", "code", "for", "reasonCode", "reasonReference", "input"}
	if len(m) != len(want) {
		t.Fatalf("members %v, want %v", rawKeys(m), want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("member %s missing", k)
		}
	}
}
