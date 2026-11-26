package shnsdk

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// submissionAdjudicator implements PASSubmissionAdjudicator: it records each
// submission it is asked about, and counts any PriorAuth call, which a
// Responder must not make to it.
type submissionAdjudicator struct {
	paTestAdjudicator
	mu        sync.Mutex
	got       []PASSubmission
	priorAuth int
}

func (a *submissionAdjudicator) PriorAuthSubmission(s PASSubmission) (PASDecision, error) {
	a.mu.Lock()
	a.got = append(a.got, s)
	a.mu.Unlock()
	return testPriorAuthDecision(s.QuestionnaireResponse, s.HasDiagnosticReport, a.now)
}

func (a *submissionAdjudicator) PriorAuth(qr []byte, hasDR bool) (PASDecision, error) {
	a.mu.Lock()
	a.priorAuth++
	a.mu.Unlock()
	return a.paTestAdjudicator.PriorAuth(qr, hasDR)
}

func (a *submissionAdjudicator) submissions() ([]PASSubmission, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]PASSubmission(nil), a.got...), a.priorAuth
}

// plainAdjudicator is a plain Adjudicator that counts its PriorAuth calls.
type plainAdjudicator struct {
	paTestAdjudicator
	mu    sync.Mutex
	calls int
}

func (a *plainAdjudicator) PriorAuth(qr []byte, hasDR bool) (PASDecision, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return a.paTestAdjudicator.PriorAuth(qr, hasDR)
}

func (a *plainAdjudicator) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// unreadablePatientRefs are references that carry a "Patient" segment but name
// no Patient by FHIR's reference grammar.
var unreadablePatientRefs = []string{
	"urn:Patient/MBR-001",
	"#Patient/MBR-001",
	" Patient/MBR-001",
	"Basic/1/_history/Patient/MBR-001",
	"Patient/MBR%2F001",
}

// resolvableUnreadable adds to a PAS request Bundle an entry whose own fullUrl
// is a reference that only looks like a patient's, and has the operative
// Claim name it in a non-subject element: a reference the request's own
// Bundle resolves, so the Responder's answer can restate it.
func resolvableUnreadable(t *testing.T, body []byte, ref string) []byte {
	t.Helper()
	entry := `{"fullUrl":"` + jsonEscape(ref) + `","resource":{"resourceType":"Basic","id":"unread-1","code":{"text":"a resource named by a URN"}}},`
	out := bytes.Replace(body, []byte(`"entry":[`), []byte(`"entry":[`+entry), 1)
	out = bytes.Replace(out, []byte(`"resourceType":"Claim",`), []byte(`"resourceType":"Claim","referral":{"reference":"`+jsonEscape(ref)+`"},`), 1)
	if bytes.Count(out, []byte(jsonEscape(ref))) != 2 {
		t.Fatalf("fixture: the entry and the Claim's reference were not both added: %s", out)
	}
	return out
}

// A PAS submission whose order and QuestionnaireResponse subjects only look
// like references to its patient names a patient the Responder cannot know.
// Its answer restates the request's resources, and cannot when a reference
// among them resolves to nothing: the request is refused 400 on its structure
// before either kind of Adjudicator is asked, never 403 for the reference's
// text. A reference the request's own Bundle resolves (an entry's own fullUrl)
// is answered:
//   - an Adjudicator that implements PASSubmissionAdjudicator decides it,
//     seeing the reference, as written, in UnreadablePatientReferences;
//   - a plain Adjudicator is asked PriorAuth as before, and the request is
//     answered.
//
// A subject that names another patient by structure is still refused 403,
// before either is asked.
func TestResponderPASSubmit_UnreadablePatientReferences(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	sub := &submissionAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}}
	_, subSrv := h.makeResponderSrv(t, responderIdent, sub)
	plain := &plainAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}}
	_, plainSrv := h.makeResponderSrv(t, responderIdent, plain)

	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	const subject = `"subject":{"reference":"Patient/MBR-001"}`
	n := 0
	submit := func(t *testing.T, srv *httptest.Server, body []byte) (*http.Response, []byte) {
		t.Helper()
		n++
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "unread-"+strconv.Itoa(n), body)
		resp := postInbound(t, srv, envBytes, hubHdr)
		return resp, readBody(t, resp)
	}
	asked := func() (int, int) {
		got, priorAuth := sub.submissions()
		return len(got) + priorAuth, plain.count()
	}

	// Control: a request whose references all name its patient reaches the
	// extension with no unreadable reference, and never PriorAuth.
	good := buildConformantClaim(t, "MBR-001", "unread-control", qr, h.now)
	if n := bytes.Count(good, []byte(subject)); n < 2 {
		t.Fatalf("fixture: %d subjects name the patient relatively, want the order's and the QuestionnaireResponse's", n)
	}
	if resp, body := submit(t, subSrv, good); resp.StatusCode != http.StatusOK {
		t.Fatalf("control: %d %s", resp.StatusCode, body)
	}
	got, priorAuth := sub.submissions()
	if len(got) != 1 || priorAuth != 0 {
		t.Fatalf("control: %d submissions and %d PriorAuth calls, want 1 and 0", len(got), priorAuth)
	}
	if len(got[0].UnreadablePatientReferences) != 0 || got[0].Patient == "" || len(got[0].QuestionnaireResponse) == 0 {
		t.Fatalf("control submission = %+v, want the request's patient and QuestionnaireResponse and no unreadable reference", got[0])
	}

	for _, ref := range unreadablePatientRefs {
		t.Run("unresolvable "+strconv.Quote(ref), func(t *testing.T) {
			body := bytes.ReplaceAll(buildConformantClaim(t, "MBR-001", "unread-u", qr, h.now), []byte(subject), []byte(`"subject":{"reference":"`+jsonEscape(ref)+`"}`))
			subBefore, plainBefore := asked()
			for _, srv := range []*httptest.Server{subSrv, plainSrv} {
				resp, out := submit(t, srv, body)
				assertError(t, resp, out, http.StatusBadRequest, "incomplete PAS request graph")
			}
			if s, p := asked(); s != subBefore || p != plainBefore {
				t.Fatal("an Adjudicator was asked about a request whose answer cannot restate it")
			}
		})
	}

	const resolvable = "urn:Patient/MBR-001"
	t.Run("resolvable "+strconv.Quote(resolvable), func(t *testing.T) {
		body := resolvableUnreadable(t, buildConformantClaim(t, "MBR-001", "unread-r", qr, h.now), resolvable)
		before, _ := sub.submissions()
		if resp, out := submit(t, subSrv, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("with the extension: %d %s, want the request answered", resp.StatusCode, out)
		}
		got, priorAuth := sub.submissions()
		if len(got) != len(before)+1 || priorAuth != 0 {
			t.Fatalf("with the extension: %d new submissions and %d PriorAuth calls, want 1 and 0", len(got)-len(before), priorAuth)
		}
		if want := []string{resolvable}; !reflect.DeepEqual(got[len(got)-1].UnreadablePatientReferences, want) {
			t.Fatalf("UnreadablePatientReferences = %q, want %q", got[len(got)-1].UnreadablePatientReferences, want)
		}
		calls := plain.count()
		if resp, out := submit(t, plainSrv, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("with a plain Adjudicator: %d %s, want the request answered", resp.StatusCode, out)
		}
		if plain.count() != calls+1 {
			t.Fatal("a plain Adjudicator was not asked PriorAuth")
		}
	})

	// Another patient by structure: refused 403, before either is asked.
	t.Run("another patient by structure", func(t *testing.T) {
		body := bytes.ReplaceAll(buildConformantClaim(t, "MBR-001", "unread-o", qr, h.now), []byte(subject), []byte(`"subject":{"reference":"Patient/MBR-OTHER"}`))
		subBefore, plainBefore := asked()
		for _, srv := range []*httptest.Server{subSrv, plainSrv} {
			resp, out := submit(t, srv, body)
			assertError(t, resp, out, http.StatusForbidden, "inconsistent patient in PAS bundle")
		}
		if s, p := asked(); s != subBefore || p != plainBefore {
			t.Fatal("an Adjudicator was asked about a request naming another patient")
		}
	})
}

// A PAS inquiry whose Coverage beneficiary only looks like a reference to its
// patient cannot be restated by the answer: refused 400 before the Adjudicator
// is asked. One its own Bundle resolves is answered, and the Adjudicator sees
// it in UnreadablePatientReferences. A beneficiary that names another patient
// by structure is still refused 403.
func TestResponderInquire_UnreadablePatientReferences(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &inquiringAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, decisions: []PASDecision{{Outcome: PASApproved, PreAuthRef: "A"}}}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	good, err := BuildPASInquiryBundle("2.0", testInquiryInputs("2.0"))
	if err != nil {
		t.Fatal(err)
	}
	beneficiary := regexp.MustCompile(`"beneficiary":\{"reference":"[^"]*"\}`)
	if n := len(beneficiary.FindAll(good.Body, -1)); n != 1 {
		t.Fatalf("fixture: %d Coverage beneficiaries, want 1", n)
	}
	n := 0
	inquire := func(t *testing.T, body []byte) (*http.Response, []byte) {
		t.Helper()
		n++
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-unread-"+strconv.Itoa(n), body)
		resp := postInbound(t, srv, envBytes, hubHdr)
		return resp, readBody(t, resp)
	}

	// Control: no unreadable reference.
	if resp, body := inquire(t, good.Body); resp.StatusCode != http.StatusOK {
		t.Fatalf("control: %d %s", resp.StatusCode, body)
	}
	if got := adj.inquiries(); len(got) != 1 || len(got[0].UnreadablePatientReferences) != 0 {
		t.Fatalf("control: %d inquiries; want 1 with no unreadable reference", len(got))
	}

	for _, ref := range []string{"urn:Patient/pat-1", "#Patient/pat-1", " Patient/pat-1", "Basic/1/_history/Patient/pat-1", "Patient/pat%2F1"} {
		t.Run("unresolvable "+strconv.Quote(ref), func(t *testing.T) {
			body := beneficiary.ReplaceAll(good.Body, []byte(`"beneficiary":{"reference":"`+jsonEscape(ref)+`"}`))
			before := len(adj.inquiries())
			resp, out := inquire(t, body)
			assertError(t, resp, out, http.StatusBadRequest, "incomplete PAS request graph")
			if len(adj.inquiries()) != before {
				t.Fatal("the Adjudicator was asked about an inquiry whose answer cannot restate it")
			}
		})
	}

	const resolvable = "urn:Patient/pat-1"
	t.Run("resolvable "+strconv.Quote(resolvable), func(t *testing.T) {
		body := resolvableUnreadable(t, good.Body, resolvable)
		before := len(adj.inquiries())
		if resp, out := inquire(t, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s, want the inquiry answered", resp.StatusCode, out)
		}
		got := adj.inquiries()
		if len(got) != before+1 {
			t.Fatal("the Adjudicator was not asked")
		}
		if want := []string{resolvable}; !reflect.DeepEqual(got[len(got)-1].UnreadablePatientReferences, want) {
			t.Fatalf("UnreadablePatientReferences = %q, want %q", got[len(got)-1].UnreadablePatientReferences, want)
		}
	})

	t.Run("another patient by structure", func(t *testing.T) {
		body := beneficiary.ReplaceAll(good.Body, []byte(`"beneficiary":{"reference":"Patient/pat-OTHER"}`))
		before := len(adj.inquiries())
		resp, out := inquire(t, body)
		assertError(t, resp, out, http.StatusForbidden, "inquiry Coverage is for another patient")
		if len(adj.inquiries()) != before {
			t.Fatal("the Adjudicator was asked about an inquiry naming another patient")
		}
	})
}

// pasOtherPatients reads the two answers pasCarriesAnotherPatient joins apart:
// a reference that only looks like a patient's is unreadable, never another
// patient, and each is listed once, in byte order.
func TestPASOtherPatients_UnreadableIsNotAnother(t *testing.T) {
	rel := func(ref string) string {
		return `{"resource":{"resourceType":"RelatedPerson","id":"rp","patient":{"reference":"` + jsonEscape(ref) + `"}}}`
	}
	for _, row := range []struct {
		name       string
		bundle     string
		another    bool
		unreadable []string
	}{
		{"the member only", pasGuardBundle("", ""), false, nil},
		{"urn:Patient", pasGuardBundle("", rel("urn:Patient/MBR-COVERED")), false, []string{"urn:Patient/MBR-COVERED"}},
		{"two, one twice", pasGuardBundle("", rel("urn:Patient/B")+","+rel("#Patient/A")+","+rel("urn:Patient/B")), false, []string{"#Patient/A", "urn:Patient/B"}},
		{"another patient beside one", pasGuardBundle("", rel("Patient/MBR-OTHER")+","+rel(" Patient/A")), true, nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			another, unreadable := pasOtherPatients([]byte(row.bundle), "MBR-COVERED")
			if another != row.another {
				t.Fatalf("another = %v, want %v", another, row.another)
			}
			if row.another {
				return // the read may stop at the first other patient
			}
			if !reflect.DeepEqual(unreadable, row.unreadable) {
				t.Fatalf("unreadable = %q, want %q", unreadable, row.unreadable)
			}
			if carries := pasCarriesAnotherPatient([]byte(row.bundle), "MBR-COVERED"); carries != (len(row.unreadable) > 0) {
				t.Fatalf("pasCarriesAnotherPatient = %v, want the two answers joined", carries)
			}
		})
	}
}

// The Responder's order-select reads the order's subject as a payer's Smart
// Gateway reads it. A reference that names the request's patient versioned, or
// absolute on the EHR's own server (fhirServer), is that patient: answered,
// where earlier releases refused it 400 inconsistent. One that only looks like
// a reference to a patient names a patient this cannot know: answered, not
// refused (OrderSelect decides by the procedure code alone, so there is nothing
// to mark). An absolute reference on another server names another patient,
// and is refused as before.
func TestResponderCRD_OrderSubjectsAreReadByTheGrammar(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
	good := buildConformantCRD(t, "MBR-001", "72148")
	subject := regexp.MustCompile(`"subject":\{"reference":"Patient/MBR-001"\}`)
	if n := len(subject.FindAll(good, -1)); n != 1 {
		t.Fatalf("fixture: %d order subjects name the patient, want 1", n)
	}
	withServer := func(body []byte) []byte {
		out := bytes.Replace(body, []byte(`{`), []byte(`{"fhirServer":"https://ehr.example/fhir",`), 1)
		if bytes.Equal(out, body) {
			t.Fatal("fixture: fhirServer not added")
		}
		return out
	}
	n := 0
	post := func(t *testing.T, body []byte) (*http.Response, []byte) {
		t.Helper()
		n++
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-grammar-"+strconv.Itoa(n), body)
		resp := postInbound(t, srv, envBytes, hubHdr)
		return resp, readBody(t, resp)
	}
	with := func(ref string) []byte {
		return subject.ReplaceAll(good, []byte(`"subject":{"reference":"`+jsonEscape(ref)+`"}`))
	}
	for name, body := range map[string][]byte{
		"versioned":                    with("Patient/MBR-001/_history/2"),
		"absolute on the EHR's server": withServer(with("https://ehr.example/fhir/Patient/MBR-001")),
		"urn:Patient":                  with("urn:Patient/MBR-001"),
		"#Patient":                     with("#Patient/MBR-001"),
		"behind white space":           with(" Patient/MBR-001"),
	} {
		t.Run(name, func(t *testing.T) {
			if resp, out := post(t, body); resp.StatusCode != http.StatusOK {
				t.Fatalf("%d %s, want the order-select answered", resp.StatusCode, out)
			}
		})
	}
	contained := bytes.Replace(with("#p1"), []byte(`"subject":{"reference":"#p1"}`),
		[]byte(`"contained":[{"resourceType":"Patient","id":"p1","identifier":[{"system":"`+MemberSystem+`","value":"MBR-001"}]}],"subject":{"reference":"#p1"}`), 1)
	t.Run("the order's contained Patient", func(t *testing.T) {
		if resp, out := post(t, contained); resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s, want the order-select answered", resp.StatusCode, out)
		}
	})
	patientID := regexp.MustCompile(`"patientId":"[^"]*"`)
	if n := len(patientID.FindAll(good, -1)); n != 1 {
		t.Fatalf("fixture: %d patientIds, want 1", n)
	}
	// A context.patientId other than an id or the one relative Patient/<id> names no one.
	for _, id := range []string{"Patient/MBR-001/_history/2", "Group/1", "Patient/MBR-001/x"} {
		t.Run("patientId "+id, func(t *testing.T) {
			resp, out := post(t, patientID.ReplaceAll(good, []byte(`"patientId":"`+id+`"`)))
			assertError(t, resp, out, http.StatusBadRequest, msgRequestNamesNoOne)
		})
	}
	beneficiary := regexp.MustCompile(`"beneficiary":\{"reference":"Patient/MBR-001"\}`)
	if n := len(beneficiary.FindAll(good, -1)); n != 1 {
		t.Fatalf("fixture: %d beneficiaries name the patient, want 1", n)
	}
	// A blank context.patientId beside subjects that only look like a patient's names the
	// patient by nothing this can read.
	unreadableOnly := func(id string) []byte {
		body := beneficiary.ReplaceAll(with("urn:Patient/MBR-001"), []byte(`"beneficiary":{"reference":"urn:Patient/MBR-001"}`))
		return patientID.ReplaceAll(body, []byte(`"patientId":"`+id+`"`))
	}
	for name, body := range map[string][]byte{
		"absolute on another server":          withServer(with("https://elsewhere.example/fhir/Patient/MBR-001")),
		"another patient":                     with("Patient/MBR-OTHER"),
		"a blank subject":                     with("Patient/"),
		"a blank subject on the EHR's server": withServer(with("https://ehr.example/fhir/Patient/")),
		"a #id naming no contained Patient":   with("#p9"),
		"no patientId, unreadable subjects":   unreadableOnly(""),
		"blank Patient/, unreadable subjects": unreadableOnly("Patient/"),
	} {
		t.Run(name, func(t *testing.T) {
			resp, out := post(t, body)
			assertError(t, resp, out, http.StatusBadRequest, "inconsistent patient in order-select")
		})
	}
	// A request naming its patient nowhere: context.patientId blank, and its subjects blank too.
	blankOnly := func(id, subject string, server bool) []byte {
		body := beneficiary.ReplaceAll(with(subject), []byte(`"beneficiary":{"reference":"`+jsonEscape(subject)+`"}`))
		body = patientID.ReplaceAll(body, []byte(`"patientId":"`+id+`"`))
		if server {
			body = withServer(body)
		}
		return body
	}
	for name, row := range map[string]struct {
		body []byte
		msg  string
	}{
		"no patientId, blank subjects":                        {blankOnly("", "Patient/", false), "missing context.patientId"},
		"no patientId, blank subjects on the EHR's server":    {blankOnly("", "https://ehr.example/fhir/Patient/", true), "missing context.patientId"},
		"blank Patient/ patientId, blank subjects":            {blankOnly("Patient/", "Patient/", false), msgRequestNamesNoOne},
		"blank Patient/ patientId, blank subjects on the EHR": {blankOnly("Patient/", "https://ehr.example/fhir/Patient/", true), msgRequestNamesNoOne},
	} {
		t.Run(name, func(t *testing.T) {
			resp, out := post(t, row.body)
			assertError(t, resp, out, http.StatusBadRequest, row.msg)
		})
	}
}

// A package whose only beneficiary only looks like a reference to a patient, and whose patient
// another parameter names (a Patient resource, another resource's patient reference, in a part,
// a Bundle or a nested Parameters), names its patient: it is answered, as a payer's Smart
// Gateway at none or observe reads every parameter and carries it.
func TestResponderDTR_PatientNamedByAnotherParameter(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
	cov := `{"name":"coverage","resource":` + string(bytes.TrimSpace(pkgCoverage("c1", "Group/1/Patient/p1"))) + `}`
	patient := `{"resourceType":"Patient","id":"p1"}`
	q := `{"name":"questionnaire","valueCanonical":"` + SupportedQuestionnaireCanonical + `"}`
	for i, other := range []string{
		`{"name":"referenced","resource":` + patient + `}`,
		`{"name":"order","resource":` + string(pkgOrder("o1", "Patient/p1")) + `}`,
		`{"name":"context","part":[{"name":"patient","resource":` + patient + `}]}`,
		`{"name":"referenced","resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":` + patient + `}]}}`,
		`{"name":"referenced","resource":{"resourceType":"Parameters","parameter":[{"name":"p","resource":` + patient + `}]}}`,
		`{"name":"referenced","resource":{"resourceType":"Observation","status":"final","code":{"text":"x"},"subject":{"reference":"Patient/p1"}}}`,
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			req := []byte(`{"resourceType":"Parameters","parameter":[` + cov + `,` + other + `,` + q + `]}`)
			envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-elsewhere-"+strconv.Itoa(i), frameDTROp(t, FrameOperationQuestionnairePackage, req))
			resp := postInbound(t, srv, envBytes, hubHdr)
			body := readBody(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s, want the package answered", resp.StatusCode, body)
			}
			if _, err := ExtractQuestionnaireFromPackage(h.openResponse(t, body)); err != nil {
				t.Fatalf("answer is not a package: %v", err)
			}
		})
	}
}

// The Responder's DTR questionnaire package reads its beneficiaries and order subjects by
// FHIR's reference grammar, as a payer's Smart Gateway reads them. One that only looks like a
// reference to a patient ("Patient/" inside another reference, a trailing path) names a patient
// this cannot know: beside a reference that names the patient it is answered, not refused, as
// the gateway at none or observe carries it. Earlier releases read the text after "Patient/".
func TestResponderDTR_UnreadablePatientReferencesAreNotRefused(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
	for i, order := range []string{"Group/1/Patient/p9", "Patient/p9/x", "Group/1/Patient/p1"} {
		t.Run(order, func(t *testing.T) {
			// Written by hand: the SDK's builder refuses an order it reads as another patient's.
			req := []byte(`{"resourceType":"Parameters","parameter":[{"name":"coverage","resource":` + string(bytes.TrimSpace(pkgCoverage("c1", "Patient/p1"))) +
				`},{"name":"order","resource":` + string(pkgOrder("o1", order)) + `},{"name":"questionnaire","valueCanonical":"` + SupportedQuestionnaireCanonical + `"}]}`)
			envBytes, hubHdr := h.buildForwardEnv(t, "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", "dtr-unreadable-"+strconv.Itoa(i), frameDTROp(t, FrameOperationQuestionnairePackage, req))
			resp := postInbound(t, srv, envBytes, hubHdr)
			body := readBody(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s, want the package answered", resp.StatusCode, body)
			}
			if _, err := ExtractQuestionnaireFromPackage(h.openResponse(t, body)); err != nil {
				t.Fatalf("answer is not a package: %v", err)
			}
		})
	}
}

// A request in a Bundle whose patient reference names no Patient by FHIR's
// reference grammar, and that no entry answers, names no one: refused as a
// request sent alone is (errNoMember), never read as a member.
func TestParseEligibilitySubmission_ABundledReferenceNamingNoPatientNamesNoOne(t *testing.T) {
	const req = `{"resourceType":"CoverageEligibilityRequest","status":"active","purpose":["validation"],"patient":{"reference":"REF"},"created":"2026-09-25","insurer":{"reference":"Organization/o"}}`
	for _, ref := range []string{"Patient/MBR-001/$everything", "urn:Patient/MBR-001", "Group/1", "MBR-001"} {
		t.Run(ref, func(t *testing.T) {
			body := `{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"urn:uuid:r","resource":` + strings.Replace(req, "REF", ref, 1) + `}]}`
			_, err := ParseEligibilitySubmission([]byte(body))
			var refusal *EligibilityRequestRefusal
			if !errors.As(err, &refusal) || !errors.Is(err, ErrBlankMember) || err.Error() != errNoMember.Error() {
				t.Fatalf("got %v, want the refusal of a reference that names no one", err)
			}
		})
	}
}

// The ClaimUpdate is read as the submission is. One whose graph the answer cannot restate is
// refused 400 before the Adjudicator is asked, and the pended claim it amends is released: a
// later update of that claim is decided, through PriorAuthSubmission. One whose unreadable
// reference the Bundle resolves is decided, with the reference in UnreadablePatientReferences.
func TestResponderPASUpdate_UnreadablePatientReferences(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	sub := &submissionAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}}
	_, srv := h.makeResponderSrv(t, responderIdent, sub)
	// The QR that pends on submit (PriorSurgery, no DR) and approves on update (DR added).
	pendQR := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8, PriorSurgery: true}, h.now)
	const subject = `"subject":{"reference":"Patient/MBR-001"}`
	post := func(t *testing.T, leg, op, corr string, body []byte) (*http.Response, []byte) {
		t.Helper()
		envBytes, hubHdr := h.buildForwardEnv(t, leg, op, corr, body)
		resp := postInbound(t, srv, envBytes, hubHdr)
		return resp, readBody(t, resp)
	}
	pend := func(t *testing.T, corr string) {
		t.Helper()
		resp, body := post(t, "pas-claim", "pas-submit", corr, buildConformantClaim(t, "MBR-001", corr, pendQR, h.now))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit: %d %s", resp.StatusCode, body)
		}
		if res, _ := parsePASOutcome(h.openResponse(t, body)); res.Outcome != "pended" {
			t.Fatalf("submit outcome %q, want pended", res.Outcome)
		}
	}
	approved := func(t *testing.T, resp *http.Response, body []byte) {
		t.Helper()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update: %d %s", resp.StatusCode, body)
		}
		if res, err := parsePASOutcome(h.openResponse(t, body)); err != nil || res.Outcome != "approved" {
			t.Fatalf("update outcome %q (%v), want approved", res.Outcome, err)
		}
	}

	t.Run("unresolvable, then the claim is updated", func(t *testing.T) {
		pend(t, "upd-unread-orig")
		good := buildConformantUpdate(t, "MBR-001", "upd-unread-1", "upd-unread-orig", pendQR, h.now)
		if bytes.Count(good, []byte(subject)) == 0 {
			t.Fatal("fixture: no subject names the patient relatively")
		}
		before, _ := sub.submissions()
		resp, body := post(t, "pas-claim-update", "pas-update-submit", "upd-unread-1", bytes.ReplaceAll(good, []byte(subject), []byte(`"subject":{"reference":"urn:Patient/MBR-001"}`)))
		assertError(t, resp, body, http.StatusBadRequest, "incomplete PAS request graph")
		if got, _ := sub.submissions(); len(got) != len(before) {
			t.Fatal("the Adjudicator was asked about an update whose answer cannot restate it")
		}
		// The pend was released: the same claim's update is decided.
		resp, body = post(t, "pas-claim-update", "pas-update-submit", "upd-unread-2", buildConformantUpdate(t, "MBR-001", "upd-unread-2", "upd-unread-orig", pendQR, h.now))
		approved(t, resp, body)
		got, priorAuth := sub.submissions()
		if len(got) != len(before)+1 || priorAuth != 0 || !got[len(got)-1].HasDiagnosticReport || len(got[len(got)-1].UnreadablePatientReferences) != 0 {
			t.Fatalf("after the refused update: %d new submissions, %d PriorAuth calls; want the update decided by PriorAuthSubmission", len(got)-len(before), priorAuth)
		}
	})

	t.Run("resolvable", func(t *testing.T) {
		const ref = "urn:Patient/MBR-001"
		pend(t, "upd-res-orig")
		body := resolvableUnreadable(t, buildConformantUpdate(t, "MBR-001", "upd-res-1", "upd-res-orig", pendQR, h.now), ref)
		before, _ := sub.submissions()
		resp, out := post(t, "pas-claim-update", "pas-update-submit", "upd-res-1", body)
		approved(t, resp, out)
		got, _ := sub.submissions()
		if len(got) != len(before)+1 || !reflect.DeepEqual(got[len(got)-1].UnreadablePatientReferences, []string{ref}) {
			t.Fatalf("update submissions %d, unreadable %q; want one with %q", len(got)-len(before), got[len(got)-1].UnreadablePatientReferences, ref)
		}
	})
}
