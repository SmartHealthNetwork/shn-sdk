package shnsdk

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// eobAdjudications decodes item[0].adjudication of a decision EOB.
func eobAdjudications(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var eob struct {
		Item []struct {
			Adjudication []map[string]any `json:"adjudication"`
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

func coding(system, code, display string) map[string]any {
	c := map[string]any{"system": system, "code": code}
	if display != "" {
		c["display"] = display
	}
	return c
}

// amountSlice is the adjudication an EOB carries the payer's decision on:
// the amount-type slice with the 0 USD placeholder, and the reviewAction
// extension when rv is non-nil.
func amountSlice(rv map[string]any) map[string]any {
	a := map[string]any{
		"category": map[string]any{"coding": []any{coding("http://terminology.hl7.org/CodeSystem/adjudication", "submitted", "Submitted Amount")}},
		"amount":   map[string]any{"value": float64(0), "currency": "USD"},
	}
	if rv != nil {
		a["extension"] = []any{rv}
	}
	return a
}

func reviewActionExt(code map[string]any, reasons ...map[string]any) map[string]any {
	subs := []any{map[string]any{
		"url":                  "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/extension-reviewActionCode",
		"valueCodeableConcept": map[string]any{"coding": []any{code}},
	}}
	for _, r := range reasons {
		subs = append(subs, map[string]any{"url": "reasonCode", "valueCodeableConcept": map[string]any{"coding": []any{r}}})
	}
	return map[string]any{"extension": subs, "url": "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/extension-reviewAction"}
}

func denialReasonSlice(reason map[string]any) map[string]any {
	return map[string]any{
		"category": map[string]any{"coding": []any{map[string]any{
			"system": "http://hl7.org/fhir/us/davinci-pdex/CodeSystem/PDexAdjudicationDiscriminator", "code": "denialreason"}}},
		"reason": map[string]any{"coding": []any{reason}},
	}
}

// TestBuildPADecisionEOB_DenialReasonOnlyFromThePayer: when the caller states
// the payer's decision detail (ReviewAction or DenialReasons, even empty), a
// denied EOB carries a denialreason adjudication only for each adjustment
// reason code the payer supplied, exactly as supplied, and otherwise carries
// the payer's decision as the reviewAction extension on the amount-type
// adjudication with the 0 USD placeholder approvals use. No reason code is
// added.
func TestBuildPADecisionEOB_DenialReasonOnlyFromThePayer(t *testing.T) {
	notCertified := coding(X12ReviewDecisionSystem, "A3", "Not Certified")
	payerA3 := coding(X12ReviewDecisionSystem, "A3", "Denied per plan terms")
	reason886 := coding(X12ReviewDecisionReasonSystem, "0V", "Requested information not received")
	carc := coding(CARCSystem, "197", "Precertification/authorization absent")
	rarc := coding(RARCSystem, "N54", "")
	rows := []struct {
		name string
		p    func(*PADecisionEOBParams)
		want []any
	}{
		{"payer review action with its reason, no adjustment code",
			func(p *PADecisionEOBParams) {
				p.ReviewAction = &PASReviewAction{
					Code:    PASCoding{System: X12ReviewDecisionSystem, Code: "A3", Display: "Denied per plan terms"},
					Reasons: []PASCoding{{System: X12ReviewDecisionReasonSystem, Code: "0V", Display: "Requested information not received"}},
				}
			},
			[]any{amountSlice(reviewActionExt(payerA3, reason886))}},
		{"no detail stated: the denial as its X12 decision code",
			func(p *PADecisionEOBParams) { p.DenialReasons = []PASCoding{} },
			[]any{amountSlice(reviewActionExt(notCertified))}},
		{"payer adjustment codes, each its own denialreason",
			func(p *PADecisionEOBParams) {
				p.DenialReasons = []PASCoding{
					{System: CARCSystem, Code: "197", Display: "Precertification/authorization absent"},
					{System: RARCSystem, Code: "N54"},
				}
			},
			[]any{amountSlice(reviewActionExt(notCertified)), denialReasonSlice(carc), denialReasonSlice(rarc)}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			p := eobNotesParams(PADecisionDenied, []PASProcessNote{})
			r.p(&p)
			raw, err := BuildPADecisionEOB(p)
			if err != nil {
				t.Fatal(err)
			}
			got := eobAdjudications(t, raw)
			var gotAny []any
			for _, a := range got {
				gotAny = append(gotAny, a)
			}
			if !reflect.DeepEqual(gotAny, r.want) {
				gj, _ := json.Marshal(gotAny)
				wj, _ := json.Marshal(r.want)
				t.Fatalf("adjudication:\n got %s\nwant %s", gj, wj)
			}
			if bytes.Contains(raw, []byte(`"50"`)) {
				t.Fatalf("EOB carries a reason code the payer never supplied: %s", raw)
			}
			if _, ok := eobProcessNotes(t, raw); ok {
				t.Fatal("no processNote was supplied")
			}
		})
	}
}

// TestBuildPADecisionEOB_ApprovalWithPayerDetail: an approval keeps its
// amount-type adjudication and preAuthRef, carries the payer's review action
// when stated and the payer's typed notes, and adds none of its own.
func TestBuildPADecisionEOB_ApprovalWithPayerDetail(t *testing.T) {
	notes := []PASProcessNote{{Type: "display", Text: "Approved for 6 visits."}, {Type: "printoper", Text: "Ref 12"}}
	p := eobNotesParams(PADecisionApproved, notes)
	p.AuthNumber = "AUTH-9"
	p.ReviewAction = &PASReviewAction{Code: PASCoding{System: X12ReviewDecisionSystem, Code: "A1", Display: "Certified in total"}}
	raw, err := BuildPADecisionEOB(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{amountSlice(reviewActionExt(coding(X12ReviewDecisionSystem, "A1", "Certified in total")))}
	var got []any
	for _, a := range eobAdjudications(t, raw) {
		got = append(got, a)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adjudication = %v", got)
	}
	gotNotes, _ := eobProcessNotes(t, raw)
	wantNotes := []map[string]any{
		{"number": float64(1), "type": "display", "text": notes[0].Text},
		{"number": float64(2), "type": "printoper", "text": notes[1].Text},
	}
	if !reflect.DeepEqual(gotNotes, wantNotes) || !bytes.Contains(raw, []byte(`"preAuthRef":["AUTH-9"]`)) {
		t.Fatalf("notes %v / %s", gotNotes, raw)
	}

	// Without a stated review action the approval is unchanged.
	p.ReviewAction, p.DenialReasons = nil, []PASCoding{}
	stated, err := BuildPADecisionEOB(p)
	if err != nil {
		t.Fatal(err)
	}
	p.DenialReasons = nil
	plain, err := BuildPADecisionEOB(p)
	if err != nil || !bytes.Equal(stated, plain) {
		t.Fatalf("approval differs when no detail is stated:\n%s\n%s (%v)", stated, plain, err)
	}
}

// TestBuildPADecisionEOB_DefaultPathUnchanged: without ReviewAction and
// DenialReasons a denied EOB keeps the deprecated CARC 50 denialreason (and,
// with ProcessNotes nil, the fixed appeal note), byte for byte as before.
func TestBuildPADecisionEOB_DefaultPathUnchanged(t *testing.T) {
	const legacyAdjudication = `"adjudication":[{"category":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-pdex/CodeSystem/PDexAdjudicationDiscriminator","code":"denialreason"}]},` +
		`"reason":{"coding":[{"system":"https://x12.org/codes/claim-adjustment-reason-codes","code":"50","display":"These are non-covered services because this is not deemed a 'medical necessity' by the payer"}]}}]`
	for name, notes := range map[string][]PASProcessNote{"nil notes": nil, "payer notes": {{Text: "Appeal in 45 days."}}} {
		raw, err := BuildPADecisionEOB(eobNotesParams(PADecisionDenied, notes))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), legacyAdjudication) || bytes.Contains(raw, []byte("reviewAction")) {
			t.Fatalf("%s: %s", name, raw)
		}
	}
}

// TestBuildPADecisionEOB_DecisionDetailGuards: a stated detail the EOB
// profile cannot carry is refused, never rewritten.
func TestBuildPADecisionEOB_DecisionDetailGuards(t *testing.T) {
	valid := func() PADecisionEOBParams {
		p := eobNotesParams(PADecisionDenied, []PASProcessNote{})
		p.ReviewAction = &PASReviewAction{
			Code:    PASCoding{System: X12ReviewDecisionSystem, Code: "A3"},
			Reasons: []PASCoding{{System: X12ReviewDecisionReasonSystem, Code: "0V"}},
		}
		p.DenialReasons = []PASCoding{{System: CARCSystem, Code: "197"}}
		return p
	}
	if _, err := BuildPADecisionEOB(valid()); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, mutate := range map[string]func(*PADecisionEOBParams){
		"adjustment reason on an approval":    func(p *PADecisionEOBParams) { p.Decision = PADecisionApproved },
		"adjustment reason in another system": func(p *PADecisionEOBParams) { p.DenialReasons[0].System = "http://example.org/reasons" },
		"adjustment reason without a system":  func(p *PADecisionEOBParams) { p.DenialReasons[0].System = "" },
		"adjustment reason without a code":    func(p *PADecisionEOBParams) { p.DenialReasons[0].Code = "" },
		"review action in another system":     func(p *PADecisionEOBParams) { p.ReviewAction.Code.System = "http://example.org/306" },
		"review action without a code":        func(p *PADecisionEOBParams) { p.ReviewAction.Code.Code = "" },
		"review action is a pend":             func(p *PADecisionEOBParams) { p.ReviewAction.Code.Code = "A4" },
		"review reason in another system":     func(p *PADecisionEOBParams) { p.ReviewAction.Reasons[0].System = CARCSystem },
		"review reason without a code":        func(p *PADecisionEOBParams) { p.ReviewAction.Reasons[0].Code = "" },
		"certified in total on a denial":      func(p *PADecisionEOBParams) { p.ReviewAction.Code.Code = "A1" },
		"certified with changes on a denial":  func(p *PADecisionEOBParams) { p.ReviewAction.Code.Code = "A6" },
		"not certified on an approval": func(p *PADecisionEOBParams) {
			p.Decision, p.DenialReasons = PADecisionApproved, nil
		},
	} {
		p := valid()
		mutate(&p)
		if out, err := BuildPADecisionEOB(p); err == nil {
			t.Errorf("%s: want an error, got %s", name, out)
		}
	}
}

// TestBuildPADecisionEOB_PartialCertificationOnEitherDecision: A2 (certified,
// partial) is carried on an approval (a partial certification) and on a
// denial (the Da Vinci reference payer denies with A2), as the payer sent it.
func TestBuildPADecisionEOB_PartialCertificationOnEitherDecision(t *testing.T) {
	for _, dec := range []PADecision{PADecisionApproved, PADecisionDenied} {
		p := eobNotesParams(dec, []PASProcessNote{})
		p.ReviewAction = &PASReviewAction{Code: PASCoding{System: X12ReviewDecisionSystem, Code: "A2"}}
		if raw, err := BuildPADecisionEOB(p); err != nil || !bytes.Contains(raw, []byte(`"code":"A2"`)) {
			t.Fatalf("decision %d: %s %v", dec, raw, err)
		}
	}
}
