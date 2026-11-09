package shnsdk

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	decisionDetailRA   = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction"
	decisionDetailCode = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"
)

// TestParseClaimResponse_DecisionDetail: the parsed result carries the
// payer's own process notes with their types (on approvals as on denials),
// the review action that decided the outcome with its X12 886 reasons, and
// any claim adjustment or remark codes the payer put on the adjudication, all
// exactly as sent.
func TestParseClaimResponse_DecisionDetail(t *testing.T) {
	denied := `{"resourceType":"ClaimResponse","outcome":"complete","disposition":"Not medically necessary",` +
		`"processNote":[{"number":1,"type":"print","text":"Appeal within 45 days."},{"number":2,"text":"Call us."},{"number":3,"type":"display"}],` +
		`"item":[{"itemSequence":1,"adjudication":[{"extension":[{"url":"` + decisionDetailRA + `","extension":[` +
		`{"url":"` + decisionDetailCode + `","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A3","display":"Not Certified"}]}},` +
		`{"url":"reasonCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/external/886","code":"0V","display":"Requested information not received"}]}},` +
		`{"url":"reasonCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/external/886","code":"12"}]}}]}],` +
		`"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]},` +
		`"reason":{"coding":[{"system":"https://x12.org/codes/claim-adjustment-reason-codes","code":"197"},{"system":"http://example.org/local","code":"x"},` +
		`{"system":"https://x12.org/codes/remittance-advice-remark-codes","code":"N54","display":"Claim information is inconsistent"}]}}]}]}`
	res, err := ParseClaimResponse([]byte(denied))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "denied" || res.Denial == nil || res.Denial.ReasonCode != "A3" {
		t.Fatalf("result %+v", res)
	}
	if want := []PASProcessNote{{Number: 1, Type: "print", Text: "Appeal within 45 days."}, {Number: 2, Text: "Call us."}}; !reflect.DeepEqual(res.ProcessNotes, want) {
		t.Fatalf("ProcessNotes = %+v, want %+v", res.ProcessNotes, want)
	}
	wantRA := &PASReviewAction{
		Code: PASCoding{System: X12ReviewDecisionSystem, Code: "A3", Display: "Not Certified"},
		Reasons: []PASCoding{
			{System: X12ReviewDecisionReasonSystem, Code: "0V", Display: "Requested information not received"},
			{System: X12ReviewDecisionReasonSystem, Code: "12"},
		},
	}
	if !reflect.DeepEqual(res.ReviewAction, wantRA) {
		t.Fatalf("ReviewAction = %+v, want %+v", res.ReviewAction, wantRA)
	}
	wantReasons := []PASCoding{{System: CARCSystem, Code: "197"}, {System: RARCSystem, Code: "N54", Display: "Claim information is inconsistent"}}
	if !reflect.DeepEqual(res.DenialReasons, wantReasons) {
		t.Fatalf("DenialReasons = %+v, want %+v", res.DenialReasons, wantReasons)
	}
	if want := []PASCoding{{System: "http://example.org/local", Code: "x"}}; !reflect.DeepEqual(res.OtherReasons, want) {
		t.Fatalf("OtherReasons = %+v, want %+v", res.OtherReasons, want)
	}

	approved := `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-1",` +
		`"processNote":[{"number":1,"type":"display","text":"Approved for 6 visits."}],` +
		`"item":[{"itemSequence":1,"adjudication":[{"extension":[{"url":"` + decisionDetailRA + `","extension":[` +
		`{"url":"` + decisionDetailCode + `","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A1"}]}}]}],` +
		`"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]}}]}]}`
	res, err = ParseClaimResponse([]byte(approved))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "approved" || !reflect.DeepEqual(res.ProcessNotes, []PASProcessNote{{Number: 1, Type: "display", Text: "Approved for 6 visits."}}) ||
		!reflect.DeepEqual(res.ReviewAction, &PASReviewAction{Code: PASCoding{System: X12ReviewDecisionSystem, Code: "A1"}}) ||
		res.DenialReasons != nil {
		t.Fatalf("approved result %+v (review action %+v)", res, res.ReviewAction)
	}

	// A bare approval states none of these.
	res, err = ParseClaimResponse([]byte(`{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-2"}`))
	if err != nil || res.ProcessNotes != nil || res.ReviewAction != nil || res.DenialReasons != nil || res.OtherReasons != nil {
		t.Fatalf("bare approval %+v %v", res, err)
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

// decisionItem is one item whose adjudication carries a review action coded
// code and, when reasons is set, a reason element with those codings.
func decisionItem(seq int, code, reasons string) string {
	return decisionItemNumbered(seq, code, "", reasons)
}

// decisionItemNumbered is decisionItem whose review action also carries an
// authorization number, when number is set.
func decisionItemNumbered(seq int, code, number, reasons string) string {
	num := ""
	if number != "" {
		num = `,{"url":"number","valueString":"` + number + `"}`
	}
	item := `{"itemSequence":` + fmt.Sprint(seq) + `,"adjudication":[{"extension":[{"url":"` + decisionDetailRA + `","extension":[` +
		`{"url":"` + decisionDetailCode + `","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"` + code + `"}]}}` + num + `]}],` +
		`"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]}`
	if reasons != "" {
		item += `,"reason":{"coding":[` + reasons + `]}`
	}
	return item + `}]}`
}

// TestParseClaimResponse_ModifiedApprovalStatesItsAction: an
// approval's ReviewAction is the approving action the payer stated, A1 (certified
// in total) else A6 (modified). The outcome rules do not move: an A6 with no
// authorization number is still ambiguous.
func TestParseClaimResponse_ModifiedApprovalStatesItsAction(t *testing.T) {
	approval := func(items ...string) string {
		return `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-6","item":[` + strings.Join(items, ",") + `]}`
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"A6 alone", approval(decisionItem(1, "A6", "")), "A6"},
		{"A1 alone", approval(decisionItem(1, "A1", "")), "A1"},
		{"A6 then A1", approval(decisionItem(1, "A6", ""), decisionItem(2, "A1", "")), "A1"},
	} {
		res, err := ParseClaimResponse([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want := &PASReviewAction{Code: PASCoding{System: X12ReviewDecisionSystem, Code: tc.want}}
		if res.Outcome != "approved" || res.PreAuthRef != "AUTH-6" || !reflect.DeepEqual(res.ReviewAction, want) {
			t.Fatalf("%s: outcome %q ref %q review action %+v, want approved AUTH-6 %s", tc.name, res.Outcome, res.PreAuthRef, res.ReviewAction, tc.want)
		}
	}
	noNumber := `{"resourceType":"ClaimResponse","outcome":"complete","item":[` + decisionItem(1, "A6", "") + `]}`
	if res, err := ParseClaimResponse([]byte(noNumber)); err == nil {
		t.Fatalf("A6 with no authorization number parsed as %+v, want the ambiguous-outcome error", res)
	}
}

// TestParseClaimResponse_OtherReasonsKeepTheirSystem: a reason a
// payer codes in its own system is kept in OtherReasons, in the payer's order and
// with its own system, on a denial as on an approval; CARC and RARC codes stay in
// DenialReasons, whose meaning does not change.
func TestParseClaimResponse_OtherReasonsKeepTheirSystem(t *testing.T) {
	const (
		local1 = `{"system":"http://payer.example/reasons","code":"P-17","display":"Plan exclusion"}`
		local2 = `{"system":"urn:oid:2.16.840.1.113883.3.9999","code":"9"}`
		carc   = `{"system":"https://x12.org/codes/claim-adjustment-reason-codes","code":"197"}`
	)
	wantOther := []PASCoding{{System: "http://payer.example/reasons", Code: "P-17", Display: "Plan exclusion"}, {System: "urn:oid:2.16.840.1.113883.3.9999", Code: "9"}}

	denied := `{"resourceType":"ClaimResponse","outcome":"complete","item":[` + decisionItem(1, "A3", local1+","+carc+","+local2) + `]}`
	res, err := ParseClaimResponse([]byte(denied))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "denied" || !reflect.DeepEqual(res.OtherReasons, wantOther) ||
		!reflect.DeepEqual(res.DenialReasons, []PASCoding{{System: CARCSystem, Code: "197"}}) {
		t.Fatalf("denial: outcome %q other %+v denial %+v", res.Outcome, res.OtherReasons, res.DenialReasons)
	}

	onlyLocal := `{"resourceType":"ClaimResponse","outcome":"complete","item":[` + decisionItem(1, "A3", local1+","+local2) + `]}`
	if res, err = ParseClaimResponse([]byte(onlyLocal)); err != nil || !reflect.DeepEqual(res.OtherReasons, wantOther) || res.DenialReasons != nil {
		t.Fatalf("proprietary reasons only: other %+v denial %+v err %v", res.OtherReasons, res.DenialReasons, err)
	}

	// Every terminal outcome carries them: each denial shape and each approval shape.
	reasons := local1 + "," + local2
	for _, tc := range []struct{ name, body, outcome string }{
		{"A2 denial", `{"resourceType":"ClaimResponse","outcome":"complete","item":[` + decisionItem(1, "A2", reasons) + `]}`, "denied"},
		{"A2 partial approval", `{"resourceType":"ClaimResponse","outcome":"complete","item":[` + decisionItemNumbered(1, "A2", "AUTH-P", reasons) + `]}`, "approved"},
		{"A1 approval", `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-7","item":[` + decisionItem(1, "A1", reasons) + `]}`, "approved"},
		{"A6 approval", `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-8","item":[` + decisionItem(1, "A6", reasons) + `]}`, "approved"},
	} {
		res, err := ParseClaimResponse([]byte(tc.body))
		if err != nil || res.Outcome != tc.outcome || !reflect.DeepEqual(res.OtherReasons, wantOther) {
			t.Fatalf("%s: outcome %q other %+v err %v, want %s with the payer's own codes", tc.name, res.Outcome, res.OtherReasons, err, tc.outcome)
		}
	}

	// A coding with no system is not a CARC or RARC code: it is kept, as sent.
	noSystem := `{"resourceType":"ClaimResponse","outcome":"complete","item":[` + decisionItem(1, "A3", `{"code":"R1","display":"See letter"}`) + `]}`
	if res, err = ParseClaimResponse([]byte(noSystem)); err != nil || !reflect.DeepEqual(res.OtherReasons, []PASCoding{{Code: "R1", Display: "See letter"}}) {
		t.Fatalf("no system: other %+v err %v", res.OtherReasons, err)
	}
}
