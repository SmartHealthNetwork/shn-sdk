package shnsdk

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A PAS inquiry whose Claim.patient names no member ("Patient/" then only
// whitespace, in any spelling of the reference) is refused 400 with the text
// a payer's Smart Gateway answers the same inquiry with, before the
// Adjudicator is asked. A blank member names no one: read as a member
// identifier, a blank code matches any member. A well-formed inquiry is
// answered.
func TestResponderInquire_RefusesAClaimPatientNamingNoOne(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &inquiringAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, decisions: []PASDecision{{Outcome: PASApproved, PreAuthRef: "A"}}}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	good, err := BuildPASInquiryBundle("2.0", testInquiryInputs("2.0"))
	if err != nil {
		t.Fatal(err)
	}
	// The inquiry names its patient by id "pat-1": in the Claim's reference, the
	// Patient entry's fullUrl and id, and the Coverage's beneficiary. Each row
	// writes a blank id in every place, so the inquiry is otherwise consistent
	// and, unguarded, reaches the Adjudicator.
	const id = "pat-1"
	if n := bytes.Count(good.Body, []byte(id)); n < 3 {
		t.Fatalf("fixture: the inquiry names its patient %d times, want the Claim's, the Patient entry's and the Coverage's", n)
	}
	for i, blank := range []string{" ", "\t", "\u00a0", "Patient/ "} {
		body := bytes.ReplaceAll(good.Body, []byte(id), []byte(jsonEscape(blank)))
		t.Run(strconv.Quote(blank), func(t *testing.T) {
			envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-blank-"+strconv.Itoa(i), body)
			resp := postInbound(t, srv, envBytes, hubHdr)
			assertError(t, resp, readBody(t, resp), http.StatusBadRequest, msgRequestNamesNoOne)
		})
	}
	// A Claim.patient naming no Patient is refused with the answers a payer's
	// Smart Gateway gives it: no reference has its own text, and a reference
	// that names no Patient by FHIR's reference grammar names no one, however
	// much of it reads "Patient/".
	const ref = "https://shn.example/fhir/Patient/pat-1"
	for i, row := range []struct{ ref, msg string }{
		{"", "PAS inquiry Claim missing patient"},
		{"Patient/", msgRequestNamesNoOne},
		{"Patient/Patient/", msgRequestNamesNoOne},
		{"Group/pat-1", msgRequestNamesNoOne},
		{"XPatient/pat-1", msgRequestNamesNoOne},
		{"urn:Patient/pat-1", msgRequestNamesNoOne},
		{"Basic/1/_history/Patient/pat-1", msgRequestNamesNoOne},
		{"Group/1/Patient/pat-1", msgRequestNamesNoOne},
		{"#Patient/pat-1", msgRequestNamesNoOne},
		{"https://shn.example/Patient/fhir/Practitioner/9", msgRequestNamesNoOne},
	} {
		body := bytes.Replace(good.Body, []byte(`"patient":{"reference":"`+ref+`"}`), []byte(`"patient":{"reference":"`+row.ref+`"}`), 1)
		if bytes.Equal(body, good.Body) {
			t.Fatal("fixture: the Claim's patient reference was not replaced")
		}
		t.Run("no id/"+strconv.Quote(row.ref), func(t *testing.T) {
			envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-noid-"+strconv.Itoa(i), body)
			resp := postInbound(t, srv, envBytes, hubHdr)
			assertError(t, resp, readBody(t, resp), http.StatusBadRequest, row.msg)
		})
	}
	if n := len(adj.inquiries()); n != 0 {
		t.Fatalf("the Adjudicator saw %d inquiries naming no one", n)
	}
	// Control: the same inquiry naming its patient is answered.
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-blank-control", good.Body)
	resp := postInbound(t, srv, envBytes, hubHdr)
	if body := readBody(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("control inquiry: %d %s", resp.StatusCode, body)
	}
	if n := len(adj.inquiries()); n != 1 {
		t.Fatalf("the Adjudicator saw %d inquiries, want the control's", n)
	}
}

// A PAS inquiry whose Patient entry carries a blank identifier (empty, only
// whitespace, or "Patient/" with no id) beside one naming its member is
// answered, as a payer's Smart Gateway carries it: the blank names no member,
// so PASInquiry.MemberIdentifiers leaves it out and the Adjudicator never
// searches for it. The Bundle still carries it as sent.
func TestResponderInquire_MemberIdentifiersLeaveOutABlank(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &inquiringAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, decisions: []PASDecision{{Outcome: PASApproved, PreAuthRef: "A"}}}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	good, err := BuildPASInquiryBundle("2.0", testInquiryInputs("2.0"))
	if err != nil {
		t.Fatal(err)
	}
	const named = `"system":"http://payer.test/member","value":"MBR-1"}]`
	if n := bytes.Count(good.Body, []byte(named)); n != 1 {
		t.Fatalf("fixture: the Patient's member identifier is written %d times, want once", n)
	}
	blanks := `,{"system":"http://payer.test/member","value":""},{"system":"http://payer.test/member","value":" "},{"system":"http://payer.test/member","value":"Patient/"}`
	body := bytes.Replace(good.Body, []byte(named), []byte(strings.TrimSuffix(named, "]")+blanks+"]"), 1)
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-blank-identifiers", body)
	resp := postInbound(t, srv, envBytes, hubHdr)
	if b := readBody(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("inquiry: %d %s", resp.StatusCode, b)
	}
	got := adj.inquiries()
	if len(got) != 1 {
		t.Fatalf("the Adjudicator saw %d inquiries, want 1", len(got))
	}
	if ids := got[0].MemberIdentifiers; len(ids) != 1 || ids[0].Value != "MBR-1" {
		t.Fatalf("MemberIdentifiers %+v, want only the member's MBR-1", ids)
	}
	if !bytes.Equal(got[0].Bundle, body) || !bytes.Contains(got[0].Bundle, []byte(blanks)) {
		t.Fatal("the Adjudicator's Bundle is not the inquiry as sent")
	}
}

// NewPriorAuthContinuation refuses a member id that names no member (empty,
// only whitespace, or "Patient/" with no id) with an error wrapping
// ErrBlankMember: a continuation's inquiries would ask for no one. A
// well-formed member is recorded as before.
func TestNewPriorAuthContinuation_RefusesABlankMember(t *testing.T) {
	// The member ids ResolvePCIStrict refuses, by the same rule (one "Patient/"
	// removed, then blank).
	for _, member := range []string{"", " ", "\t", "\u00a0", " \n ", "Patient/", "Patient/ "} {
		// The member is read before the request, so no request is needed.
		if _, err := NewPriorAuthContinuation("2.0", "payer", member, nil, nil); !errors.Is(err, ErrBlankMember) {
			t.Errorf("member %q: error %v; want one wrapping ErrBlankMember", member, err)
		}
	}
	// Control (needs the repository's goldens): a well-formed member is recorded.
	upd := conformantUpdateInputsFromGolden(t)
	bundle, err := BuildConformantClaimUpdateBundleExact(upd)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundleExact: %v", err)
	}
	cont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-COVERED", bundle, nil)
	if err != nil || cont.MemberID != "MBR-COVERED" {
		t.Fatalf("control: %+v %v", cont.MemberID, err)
	}
	// An empty payer holder is refused as before, and is no blank member.
	if _, err := NewPriorAuthContinuation("2.0", "", "MBR-COVERED", bundle, nil); err == nil || errors.Is(err, ErrBlankMember) {
		t.Fatalf("empty payer holder: error %v; want a refusal that is no blank member", err)
	}
}

// jsonEscape writes s as it sits inside a JSON string.
func jsonEscape(s string) string {
	return strings.NewReplacer("\t", `\t`, "\u00a0", `\u00a0`).Replace(s)
}
