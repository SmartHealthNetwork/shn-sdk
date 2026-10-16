package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	pasRA  = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	pasRAC = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"
	pdxRA  = "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/extension-reviewAction"
	pdxRAC = "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/extension-reviewActionCode"
)

// payerReviewAction is a payer's PAS review action as a ClaimResponse states
// it, with the given sub-extensions after its code; pdexReviewAction is the
// same with the EOB's own URLs and every value unchanged.
func payerReviewAction(code, display string, more ...string) string {
	return reviewActionWith(pasRA, pasRAC, code, display, more...)
}

func pdexReviewAction(code, display string, more ...string) string {
	return reviewActionWith(pdxRA, pdxRAC, code, display, more...)
}

func reviewActionWith(outer, codeURL, code, display string, more ...string) string {
	subs := []string{`{"url":"` + codeURL + `","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"` + code + `","display":"` + display + `"}]}}`}
	subs = append(subs, more...)
	return `{"url":"` + outer + `","extension":[` + strings.Join(subs, ",") + `]}`
}

const (
	submittedCat = `"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted","display":"Submitted Amount"}]}`
	reasonEntry  = `{"category":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pdex/CodeSystem/PDexAdjudicationDiscriminator","code":"denialreason"}]},"reason":{"coding":[{"system":"https://x12.org/codes/claim-adjustment-reason-codes","code":"197","display":"Precertification/authorization absent"}]}}`
	numberSub    = `{"url":"number","valueString":"AUTH-77"}`
	reasonSub    = `{"url":"reasonCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/external/886","code":"0V"}]}}`
	opinionSub   = `{"url":"secondSurgicalOpinionFlag","valueBoolean":true}`
	// rowNet is the request item's net a row supplies.
	rowNet = `{"value":250.75,"currency":"USD"}`
)

func eobAdjudicationsRaw(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	var eob struct {
		Item []struct {
			Adjudication []json.RawMessage `json:"adjudication"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &eob); err != nil {
		t.Fatal(err)
	}
	if len(eob.Item) != 1 {
		t.Fatalf("items = %d", len(eob.Item))
	}
	return eob.Item[0].Adjudication
}

func rawOrNil(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// TestBuildPADecisionEOB_CarriesThePayersAdjudication: the payer's own
// item.adjudication entries are carried with their category, amount (only
// where stated) and reason byte for byte, and their PAS review action
// re-expressed in the EOB's own extension with every value unchanged — the
// reference payer's amountless submitted entry with its A3, an approval's A1,
// an A6, a number, review reasons, the second-opinion flag, a denial's
// reason codes. No amount, review action or reason code is ever added. The
// request's net is added as "submitted" only when the payer stated no
// submitted entry.
func TestBuildPADecisionEOB_CarriesThePayersAdjudication(t *testing.T) {
	submittedNet := `{"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted","display":"Submitted Amount"}]},"amount":` + rowNet + `}`
	entry := func(ra string, more string) string { return `{"extension":[` + ra + `],` + submittedCat + more + `}` }
	for _, r := range []struct {
		name  string
		payer []string
		net   string
		want  []string
	}{
		{"real payer denial: amountless submitted with A3",
			[]string{entry(payerReviewAction("A3", "Not Required"), "")}, "",
			[]string{entry(pdexReviewAction("A3", "Not Required"), "")}},
		{"the payer's submitted entry stands alone beside the request's net",
			[]string{entry(payerReviewAction("A3", "Not Certified"), "")}, rowNet,
			[]string{entry(pdexReviewAction("A3", "Not Certified"), "")}},
		{"approval with A1 and its number",
			[]string{entry(payerReviewAction("A1", "Certified in total", numberSub), "")}, "",
			[]string{entry(pdexReviewAction("A1", "Certified in total", numberSub), "")}},
		{"modified approval A6 with reasons and the second-opinion flag, on the payer's amount",
			[]string{entry(payerReviewAction("A6", "Modified", reasonSub, opinionSub), `,"amount":{"value":10.50,"currency":"USD"}`)}, "",
			[]string{entry(pdexReviewAction("A6", "Modified", reasonSub, opinionSub), `,"amount":{"value":10.50,"currency":"USD"}`)}},
		{"denial with the payer's reasons",
			[]string{entry(payerReviewAction("A3", "Not Certified"), ""), reasonEntry}, "",
			[]string{entry(pdexReviewAction("A3", "Not Certified"), ""), reasonEntry}},
		{"reasons only: the request's net added as submitted",
			[]string{reasonEntry}, rowNet, []string{submittedNet, reasonEntry}},
		{"nothing stated: no adjudication", nil, "", nil},
	} {
		t.Run(r.name, func(t *testing.T) {
			p := eobNotesParams(nil)
			for _, a := range r.payer {
				p.Adjudications = append(p.Adjudications, json.RawMessage(a))
			}
			p.SubmittedAmount = rawOrNil(r.net)
			raw, err := BuildPADecisionEOBExact(p)
			if err != nil {
				t.Fatal(err)
			}
			var gotS []string
			for _, g := range eobAdjudicationsRaw(t, raw) {
				gotS = append(gotS, string(g))
			}
			if !reflect.DeepEqual(gotS, r.want) {
				t.Fatalf("adjudication:\n got %q\nwant %q", gotS, r.want)
			}
			for _, minted := range []string{`"value":0`, pasRA, pasRAC, `"meta"`, "pdex-priorauthorization"} {
				if bytes.Contains(raw, []byte(minted)) {
					t.Fatalf("EOB carries %s: %s", minted, raw)
				}
			}
		})
	}
	p := eobNotesParams(nil)
	p.Adjudications = []json.RawMessage{json.RawMessage(`["not an object"]`)}
	if _, err := BuildPADecisionEOBExact(p); err == nil {
		t.Fatal("an adjudication that is not a JSON object must be refused, never rewritten")
	}
}

// TestBuildPADecisionEOB_UnmappedReviewActionSubExtension: a payer review
// action sub-extension the EOB's extension has no counterpart for writes no
// EOB, naming it; it is never dropped.
func TestBuildPADecisionEOB_UnmappedReviewActionSubExtension(t *testing.T) {
	p := eobNotesParams(nil)
	p.Adjudications = []json.RawMessage{json.RawMessage(`{"extension":[` + payerReviewAction("A3", "Not Certified", `{"url":"http://payer.example/x","valueString":"y"}`) + `],` + submittedCat + `}`)}
	out, err := BuildPADecisionEOBExact(p)
	var unmapped *PADecisionEOBUnmappedError
	if !errors.As(err, &unmapped) || unmapped.URL != "http://payer.example/x" || unmapped.Position != 2 || out != nil {
		t.Fatalf("got %s, %v; want PADecisionEOBUnmappedError{http://payer.example/x}", out, err)
	}
}

// TestReviewActionTranscriptionTable is the hermetic tripwire over the
// registered PAS → PDex review action table: every PAS sub-extension it maps
// and its PDex counterpart (PAS 2.0.1/2.1.0/2.2.1 and PDex 2.1.0 define the
// same four). Changing an entry is a change to what the EOB states.
func TestReviewActionTranscriptionTable(t *testing.T) {
	want := map[string]string{
		pasRAC:                      pdxRAC,
		"number":                    "number",
		"reasonCode":                "reasonCode",
		"secondSurgicalOpinionFlag": "secondSurgicalOpinionFlag",
	}
	if !reflect.DeepEqual(reviewActionSubExtensionPDex, want) {
		t.Fatalf("review action table = %v, want %v", reviewActionSubExtensionPDex, want)
	}
	if pasReviewActionURL != pasRA || pdexReviewActionURL != pdxRA {
		t.Fatalf("review action extensions = %s -> %s", pasReviewActionURL, pdexReviewActionURL)
	}
}

// TestBuildPADecisionEOB_PreAuthRefsAsStated: the EOB carries the payer's own
// authorization numbers exactly as stated, in order, each once, and none
// when the payer stated none.
func TestBuildPADecisionEOB_PreAuthRefsAsStated(t *testing.T) {
	notes := []PASProcessNote{{Number: 1, Type: "display", Text: "Approved for 6 visits."}, {Number: 2, Type: "printoper", Text: "Ref 12"}}
	p := eobNotesParams(notes)
	p.PreAuthRefs = []string{"AUTH-9", "AUTH-77", "AUTH-9", ""}
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	gotNotes, _ := eobProcessNotes(t, raw)
	wantNotes := []map[string]any{
		{"number": float64(1), "type": "display", "text": notes[0].Text},
		{"number": float64(2), "type": "printoper", "text": notes[1].Text},
	}
	if !reflect.DeepEqual(gotNotes, wantNotes) || !bytes.Contains(raw, []byte(`"preAuthRef":["AUTH-9","AUTH-77"]`)) {
		t.Fatalf("notes %v / %s", gotNotes, raw)
	}
	p.PreAuthRefs = nil
	if raw, _ = BuildPADecisionEOBExact(p); bytes.Contains(raw, []byte("preAuthRef")) {
		t.Fatalf("an EOB carries an authorization number the payer did not state: %s", raw)
	}
}

// TestBuildPADecisionEOB_CarriesSourcesAsWritten: type, created, insurer,
// provider, insurance, outcome and item.sequence are written exactly as the
// caller's sources state them; a reference with an identifier is stated by
// it, else by its literal reference.
func TestBuildPADecisionEOB_CarriesSourcesAsWritten(t *testing.T) {
	p := eobBaseParams()
	focal, other := true, false
	p.ClaimType = json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"institutional"}]}`)
	p.Created = "2026-09-30T08:15:00-05:00"
	p.Insurer = PADecisionEOBReference{Identifier: json.RawMessage(`{"system":"http://hl7.org/fhir/sid/us-npi","value":"1972532007"}`), Reference: "Organization/ignored", Display: "Payer"}
	p.Provider = PADecisionEOBReference{Reference: "PractitionerRole/role-1", Display: "Dr. Lee"}
	p.Insurance = []PADecisionEOBInsurance{
		{Focal: &focal, Coverage: PADecisionEOBReference{Identifier: json.RawMessage(`{"system":"http://payer.test/member","value":"M-1"}`)}},
		{Focal: &other, Coverage: PADecisionEOBReference{Reference: "Coverage/second"}},
	}
	p.Outcome, p.ItemSequence = "partial", 4
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/claim-type","code":"institutional"}]}`,
		`"created":"2026-09-30T08:15:00-05:00"`,
		`"insurer":{"identifier":{"system":"http://hl7.org/fhir/sid/us-npi","value":"1972532007"},"display":"Payer"}`,
		`"provider":{"reference":"PractitionerRole/role-1","display":"Dr. Lee"}`,
		`"insurance":[{"focal":true,"coverage":{"identifier":{"system":"http://payer.test/member","value":"M-1"}}},{"focal":false,"coverage":{"reference":"Coverage/second"}}]`,
		`"outcome":"partial"`, `"sequence":4`,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("EOB does not carry %s:\n%s", want, raw)
		}
	}
	if bytes.Contains(raw, []byte("Organization/ignored")) {
		t.Errorf("a reference stated by its identifier also carries the literal: %s", raw)
	}
}

// TestBuildPADecisionEOB_RequiredElementsAbsent: each element the EOB
// requires, absent, refuses the build with PADecisionEOBAbsentError naming
// it; nothing is filled in.
func TestBuildPADecisionEOB_RequiredElementsAbsent(t *testing.T) {
	for element, mutate := range map[string]func(*PADecisionEOBExactParams){
		"type":    func(p *PADecisionEOBExactParams) { p.ClaimType = nil },
		"created": func(p *PADecisionEOBExactParams) { p.Created = "" },
		"insurer": func(p *PADecisionEOBExactParams) { p.Insurer = PADecisionEOBReference{Display: "only a display"} },
		"provider": func(p *PADecisionEOBExactParams) {
			p.Provider = PADecisionEOBReference{Identifier: json.RawMessage("null")}
		},
		"insurance":          func(p *PADecisionEOBExactParams) { p.Insurance = nil },
		"insurance.focal":    func(p *PADecisionEOBExactParams) { p.Insurance[0].Focal = nil },
		"insurance.coverage": func(p *PADecisionEOBExactParams) { p.Insurance[0].Coverage = PADecisionEOBReference{} },
		"outcome":            func(p *PADecisionEOBExactParams) { p.Outcome = "" },
		"item.sequence":      func(p *PADecisionEOBExactParams) { p.ItemSequence = 0 },
	} {
		p := eobBaseParams()
		p.Insurance = append([]PADecisionEOBInsurance(nil), p.Insurance...)
		mutate(&p)
		out, err := BuildPADecisionEOBExact(p)
		var absent *PADecisionEOBAbsentError
		if !errors.As(err, &absent) || absent.Element != element || out != nil {
			t.Errorf("%s absent: got %s, %v; want PADecisionEOBAbsentError{%s}", element, out, err, element)
		}
	}
}
