package shnsdk

import (
	"reflect"
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
	if want := []PASProcessNote{{Type: "print", Text: "Appeal within 45 days."}, {Text: "Call us."}}; !reflect.DeepEqual(res.ProcessNotes, want) {
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

	approved := `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-1",` +
		`"processNote":[{"number":1,"type":"display","text":"Approved for 6 visits."}],` +
		`"item":[{"itemSequence":1,"adjudication":[{"extension":[{"url":"` + decisionDetailRA + `","extension":[` +
		`{"url":"` + decisionDetailCode + `","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A1"}]}}]}],` +
		`"category":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/adjudication","code":"submitted"}]}}]}]}`
	res, err = ParseClaimResponse([]byte(approved))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "approved" || !reflect.DeepEqual(res.ProcessNotes, []PASProcessNote{{Type: "display", Text: "Approved for 6 visits."}}) ||
		!reflect.DeepEqual(res.ReviewAction, &PASReviewAction{Code: PASCoding{System: X12ReviewDecisionSystem, Code: "A1"}}) ||
		res.DenialReasons != nil {
		t.Fatalf("approved result %+v (review action %+v)", res, res.ReviewAction)
	}

	// A bare approval states none of these.
	res, err = ParseClaimResponse([]byte(`{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-2"}`))
	if err != nil || res.ProcessNotes != nil || res.ReviewAction != nil || res.DenialReasons != nil {
		t.Fatalf("bare approval %+v %v", res, err)
	}
}
