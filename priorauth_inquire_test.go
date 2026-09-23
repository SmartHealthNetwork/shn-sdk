package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// inquiringAdjudicator is the test policy plus an Inquire that answers with
// its decisions in order (the last one repeats) and records each inquiry.
type inquiringAdjudicator struct {
	paTestAdjudicator
	mu        sync.Mutex
	decisions []PASDecision
	got       []PASInquiry
	onInquire func()
}

func (a *inquiringAdjudicator) Inquire(inq PASInquiry) (PASDecision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, inq)
	if a.onInquire != nil {
		a.onInquire()
	}
	i := min(len(a.got)-1, len(a.decisions)-1)
	return a.decisions[i], nil
}

func (a *inquiringAdjudicator) inquiries() []PASInquiry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]PASInquiry(nil), a.got...)
}

// pairInquiryRecords are the requester's own records for member MBR-001.
func pairInquiryRecords() PASInquiryRecords {
	return PASInquiryRecords{
		// Typed MB, as a real participant's record is and as the submit path
		// requires: a payer matches an inquiry on that identifier.
		Patient:  testMemberPatient("MBR-001"),
		Coverage: []byte(`{"resourceType":"Coverage","id":"cov-001","status":"active","beneficiary":{"reference":"Patient/MBR-001"},"payor":[{"reference":"Organization/payer-1"}]}`),
		Provider: inquiryProvider,
		Insurer:  inquiryInsurer,
	}
}

// inquiryPair is a published client and Responder paired through a Hub
// stand-in, the Responder's Adjudicator answering inquiries.
type inquiryPair struct {
	h      *paTestHarness
	sender Identity
	hub    *pairHub
	hubSrv *httptest.Server
	ep     Endpoints
	payer  Payer
	adj    *inquiringAdjudicator
	req    PriorAuthRequest
}

func newInquiryPair(t *testing.T, decisions ...PASDecision) *inquiryPair {
	t.Helper()
	h, responderIdent, senderIdent := newPAHarness(t)
	adj := &inquiringAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, decisions: decisions}
	_, responderSrv := h.makeResponderSrv(t, responderIdent, adj)
	hub := newPairHub(t, h, responderSrv, responderIdent)
	mux := http.NewServeMux()
	mux.HandleFunc("/route", hub.route)
	hubSrv := httptest.NewServer(mux)
	t.Cleanup(hubSrv.Close)
	return &inquiryPair{
		h: h, sender: senderIdent, hub: hub, hubSrv: hubSrv, adj: adj,
		ep:    Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL},
		payer: Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub},
		req: PriorAuthRequest{
			Member: "MBR-001", DOB: "1975-04-02", Family: "Johansson", NPI: "9999999999",
			Provider:       testRequestingProvider(),
			Patient:        testMemberPatient("MBR-001"),
			Coverage:       testMemberCoverageSearch("MBR-001"),
			MemberIDSystem: MemberSystem,
			Clinical:       DemoLumbarContextPriorSurgery(), ProcedureSystem: systemHCPCS, ProcedureCPT: "G0151",
			ProcedureDisplay: "Home health physical therapy, each 15 minutes", DiagnosisICD10: "M54.16",
		},
	}
}

// pend runs the prior authorization to its pended submit answer.
func (p *inquiryPair) pend(t *testing.T) PriorAuthResult {
	t.Helper()
	res, err := p.sender.RunPriorAuth(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req)
	if err != nil || res.Outcome != "pended" || res.Resume == nil || res.Resume.Continuation == nil {
		t.Fatalf("submit: %+v %v", res, err)
	}
	return res
}

// TestClientInquire_UsesHandleFacts: Inquire builds its inquiry from the
// handle's continuation facts and the requester's records, sends it on the
// inquiry leg to the payer the handle names, and returns the payer's
// decision.
func TestClientInquire_UsesHandleFacts(t *testing.T) {
	p := newInquiryPair(t, PASDecision{Outcome: PASApproved, PreAuthRef: "AUTH-7", ValidUntil: "2026-12-31"})
	res := p.pend(t)
	cont := res.Resume.Continuation
	// ProviderNPI is the NPI of the party the SUBMISSION named — the requesting
	// provider record that rode the claim — not the ordering practitioner's NPI
	// the request also carries ("9999999999"). A payer matches an inquiry on the
	// identifier of the party the claim named, so recording any other one would
	// send an inquiry about an authorization nobody stored.
	if cont.Line != "2.0" || cont.PayerHolder != p.payer.ID || cont.MemberID != "MBR-001" || cont.ProviderNPI != "1417947384" ||
		len(cont.Items) != 1 || cont.Items[0].ProductOrService.Code != "G0151" || cont.Items[0].TraceNumber.System != PASItemTraceSystem ||
		len(cont.ClaimIdentifiers) == 0 || len(cont.ClaimResponseIdentifiers) == 0 {
		t.Fatalf("continuation = %+v", cont)
	}
	// The handle round-trips as JSON and carries no clinical content.
	raw, err := json.Marshal(cont)
	if err != nil {
		t.Fatal(err)
	}
	for _, clinical := range []string{"QuestionnaireResponse", "ServiceRequest", "M54.16", "Johansson", "DiagnosticReport"} {
		if bytes.Contains(raw, []byte(clinical)) {
			t.Errorf("continuation carries %q: %s", clinical, raw)
		}
	}
	var handle PriorAuthResume
	if b, err := json.Marshal(res.Resume); err != nil || json.Unmarshal(b, &handle) != nil {
		t.Fatalf("resume handle does not round-trip: %v", err)
	}

	records := pairInquiryRecords()
	got, err := p.sender.Inquire(context.Background(), p.hubSrv.Client(), p.ep, p.payer, handle, records)
	if err != nil {
		t.Fatalf("Inquire: %v", err)
	}
	if got.Outcome != "approved" || got.PreAuthRef != "AUTH-7" {
		t.Fatalf("Inquire result = %+v", got)
	}

	sent := p.hub.leg(t, "pas-claim-inquire")
	if !bytes.Contains(sent, bytes.TrimSpace(records.Patient)) || !bytes.Contains(sent, records.Coverage) {
		t.Error("the inquiry does not embed the requester's records exactly")
	}
	inqs := p.adj.inquiries()
	if len(inqs) != 1 {
		t.Fatalf("payer received %d inquiries", len(inqs))
	}
	inq := inqs[0]
	if len(inq.Items) != 1 || inq.Items[0].TraceNumber != cont.Items[0].TraceNumber || inq.Items[0].ProductOrService != cont.Items[0].ProductOrService {
		t.Errorf("inquiry items %+v, want the handle's %+v", inq.Items, cont.Items)
	}
	if len(inq.MemberIdentifiers) != 1 || inq.MemberIdentifiers[0].Value != "MBR-001" || !strings.HasSuffix(inq.Patient, "/Patient/MBR-001") {
		t.Errorf("inquiry member %+v patient %q", inq.MemberIdentifiers, inq.Patient)
	}
	if len(inq.ClaimIdentifiers) != 1 || inq.ClaimIdentifiers[0].System != PASInquiryIdentifierSystem {
		t.Errorf("inquiry claim identifiers %+v", inq.ClaimIdentifiers)
	}

	t.Run("still pended keeps a usable handle", func(t *testing.T) {
		p := newInquiryPair(t, testPendedDecision())
		res := p.pend(t)
		got, err := p.sender.Inquire(context.Background(), p.hubSrv.Client(), p.ep, p.payer, *res.Resume, pairInquiryRecords())
		if err != nil || got.Outcome != "pended" || got.Resume == nil || got.Resume.Continuation == nil {
			t.Fatalf("pended inquiry: %+v %v", got, err)
		}
		if len(got.NeededItems) != 1 || got.Resume.OriginalCorrelationID != res.Resume.OriginalCorrelationID ||
			len(got.Resume.Continuation.ClaimResponseIdentifiers) < len(res.Resume.Continuation.ClaimResponseIdentifiers) {
			t.Errorf("pended inquiry handle = %+v", got.Resume)
		}
		// The caller's handle is not modified.
		if len(res.Resume.Continuation.ClaimResponseIdentifiers) != len(cont.ClaimResponseIdentifiers) {
			t.Error("Inquire changed the caller's continuation")
		}
	})

	t.Run("refusals", func(t *testing.T) {
		old := handle
		old.Continuation = nil
		if _, err := p.sender.Inquire(context.Background(), p.hubSrv.Client(), p.ep, p.payer, old, records); !errors.Is(err, ErrNoContinuation) {
			t.Errorf("handle without continuation: %v", err)
		}
		other := p.payer
		other.ID = "other-payer"
		if _, err := p.sender.Inquire(context.Background(), p.hubSrv.Client(), p.ep, other, handle, records); err == nil || !strings.Contains(err.Error(), "is for payer") {
			t.Errorf("another payer: %v", err)
		}
		noMember := records
		noMember.Patient = []byte(`{"resourceType":"Patient","id":"MBR-001"}`)
		if _, err := p.sender.Inquire(context.Background(), p.hubSrv.Client(), p.ep, p.payer, handle, noMember); err == nil || !strings.Contains(err.Error(), "member id") {
			t.Errorf("records without the member identifier: %v", err)
		}
	})

	t.Run("a payer that does not answer inquiries", func(t *testing.T) {
		h, responderIdent, senderIdent := newPAHarness(t)
		_, responderSrv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})
		hub := newPairHub(t, h, responderSrv, responderIdent)
		mux := http.NewServeMux()
		mux.HandleFunc("/route", hub.route)
		hubSrv := httptest.NewServer(mux)
		t.Cleanup(hubSrv.Close)
		ep := Endpoints{HubURL: hubSrv.URL, AuthzURL: h.authzSrv.URL}
		payer := Payer{ID: h.responderID, EncPub: h.responderEnc, AuthzPub: h.authzPub}
		req := p.req
		res, err := senderIdent.RunPriorAuth(context.Background(), hubSrv.Client(), ep, payer, req)
		if err != nil || res.Resume == nil {
			t.Fatalf("submit: %+v %v", res, err)
		}
		_, err = senderIdent.Inquire(context.Background(), hubSrv.Client(), ep, payer, *res.Resume, pairInquiryRecords())
		if err == nil || !strings.Contains(err.Error(), "501") {
			t.Errorf("Inquire against a non-inquiring payer: %v", err)
		}
	})
}

// TestInquiryDecision_Matching: the answer's decision is chosen by the
// request's trace numbers or the payer's identifiers; none or several is an
// error; a PAS 2.2 Parameters answer is read too.
func TestInquiryDecision_Matching(t *testing.T) {
	cont := PriorAuthContinuation{
		Line: "2.2", PayerHolder: "payer", MemberID: "m",
		ClaimIdentifiers:         []PASIdentifier{{System: "urn:c", Value: "claim-1"}},
		Items:                    []PASInquiryItem{{Sequence: 1, TraceNumber: PASIdentifier{System: PASItemTraceSystem, Value: "c.1"}}},
		ClaimResponseIdentifiers: []PASIdentifier{{System: "urn:p", Value: "cr-1"}},
	}
	const approvalAdjudication = `[{"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A1"}]}}]}]}]`
	approved := func(extra string) string {
		if strings.Contains(extra, `"adjudication":[]`) {
			extra = strings.Replace(extra, `"adjudication":[]`, `"adjudication":`+approvalAdjudication, 1)
		} else {
			extra += `,"item":[{"itemSequence":1,"adjudication":` + approvalAdjudication + `}]`
		}
		return `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-1"` + extra + `}`
	}
	byTrace := approved(`,"item":[{"itemSequence":1,"extension":[{"url":"` + pasExtItemTraceNumber + `","valueIdentifier":{"system":"` + PASItemTraceSystem + `","value":"c.1"}}],"adjudication":[]}]`)
	byID := approved(`,"identifier":[{"system":"urn:p","value":"cr-1"}]`)
	byRequest := approved(`,"request":{"identifier":{"system":"urn:c","value":"claim-1"}}`)
	unrelated := `{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-OTHER","identifier":[{"system":"urn:p","value":"cr-9"}]}`
	for name, row := range map[string]struct {
		answer string
		want   error
	}{
		"trace number":         {string(contentBundle(unrelated, byTrace)), nil},
		"payer identifier":     {string(contentBundle(byID)), nil},
		"request identifier":   {string(contentBundle(unrelated, byRequest)), nil},
		"no match":             {string(contentBundle(unrelated)), ErrInquiryNoMatch},
		"empty answer":         {string(contentBundle()), ErrInquiryNoMatch},
		"two matches":          {string(contentBundle(byTrace, byID)), ErrInquiryAmbiguous},
		"2.2 parameters":       {`{"resourceType":"Parameters","parameter":[{"name":"return","resource":` + string(contentBundle(unrelated)) + `},{"name":"return","resource":` + string(contentBundle(byTrace)) + `}]}`, nil},
		"2.2 parameters twice": {`{"resourceType":"Parameters","parameter":[{"name":"return","resource":` + string(contentBundle(byID)) + `},{"name":"return","resource":` + string(contentBundle(byTrace)) + `}]}`, ErrInquiryAmbiguous},
	} {
		res, err := inquiryDecision([]byte(row.answer), cont)
		if row.want != nil {
			if !errors.Is(err, row.want) {
				t.Errorf("%s: err = %v, want %v", name, err, row.want)
			}
			continue
		}
		if err != nil || res.Outcome != "approved" || res.PreAuthRef != "AUTH-1" {
			t.Errorf("%s: %+v %v", name, res, err)
		}
		selected, selectErr := SelectPASInquiryAnswer([]byte(row.answer), cont)
		if selectErr != nil || selected.Result.Outcome != res.Outcome || !bytes.Contains(selected.Response, []byte(`"preAuthRef":"AUTH-1"`)) || !bytes.Contains(selected.Bundle, selected.Response) {
			t.Errorf("%s: selection lost exact response/Bundle scope: %v", name, selectErr)
		}

	}
	if _, err := inquiryDecision([]byte(`{"resourceType":"OperationOutcome"}`), cont); err == nil {
		t.Error("an OperationOutcome answer was read as a decision")
	}
}

// fakeWaitTimer is a simulated clock: Sleep advances it and records the
// delay; nothing waits on the wall clock.
type fakeWaitTimer struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	cancel context.CancelFunc
	cutAt  int
}

func (f *fakeWaitTimer) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeWaitTimer) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.sleeps = append(f.sleeps, d)
	f.now = f.now.Add(d)
	n := len(f.sleeps)
	f.mu.Unlock()
	if f.cancel != nil && n == f.cutAt {
		f.cancel()
	}
	return ctx.Err()
}

func (f *fakeWaitTimer) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// TestRunPriorAuth_DefaultNoWait: without WithWait a pended decision is
// returned as it came, and no inquiry is sent.
func TestRunPriorAuth_DefaultNoWait(t *testing.T) {
	p := newInquiryPair(t, PASDecision{Outcome: PASApproved, PreAuthRef: "AUTH-1"})
	for name, run := range map[string]func() (PriorAuthResult, error){
		"RunPriorAuth": func() (PriorAuthResult, error) {
			return p.sender.RunPriorAuth(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req)
		},
		"RunPriorAuthWith no options": func() (PriorAuthResult, error) {
			return p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req)
		},
		"WithWait(0)": func() (PriorAuthResult, error) {
			return p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req, WithWait(0))
		},
	} {
		res, err := run()
		if err != nil || res.Outcome != "pended" {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if n := len(p.adj.inquiries()); n != 0 {
		t.Fatalf("%d inquiries sent without a wait", n)
	}
	for name, opts := range map[string][]PriorAuthOption{
		"negative wait":        {WithWait(-time.Second)},
		"wait without records": {WithWait(time.Second)},
	} {
		if _, err := p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req, opts...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if legs := len(p.hub.legsInOrder); legs != 3 {
		t.Errorf("legs sent = %v (a refused option must send nothing)", p.hub.legsInOrder)
	}
}

// TestPriorAuthWait_TheBoundIsReachable holds the longest wait a caller may ask
// for and the inquiry schedule to each other.
//
// They used to be two independent numbers: the schedule hands out 2, 4, 5, 5, 5,
// 5 seconds, so its sixth and last inquiry falls due at 26 s — while a caller
// could ask to wait 120. Every second past 26 held the call open with no inquiry
// left to make, and no row could tell, because the rows that pass the maximum
// drive a fake clock where everything fits.
//
// So this row asserts the relationship rather than the numbers. It goes red if
// the maximum is raised past what the schedule delivers, or the schedule slowed
// until its last inquiry falls outside the maximum.
func TestPriorAuthWait_TheBoundIsReachable(t *testing.T) {
	reach := priorAuthScheduleReach()
	if reach != 26*time.Second {
		t.Fatalf("the schedule's last inquiry falls due at %v, want 26s (2+4+5+5+5+5)", reach)
	}
	if reach > MaxPriorAuthWait {
		t.Fatalf("the schedule's last inquiry falls due at %v, past the %v maximum — a caller asking for the maximum can never make the last inquiry the cap allows",
			reach, MaxPriorAuthWait)
	}
	if MaxPriorAuthWait > reach+priorAuthInquiryBackoff {
		t.Fatalf("the maximum wait is %v and the schedule stops asking at %v — the %v beyond it makes no inquiry, it only holds the caller's call open",
			MaxPriorAuthWait, reach, MaxPriorAuthWait-reach)
	}

	// Drive it: a caller asking for the maximum, on a clock that moves only by the
	// schedule's own delays, makes every inquiry the cap allows.
	p := newInquiryPair(t, testPendedDecision())
	timer := &fakeWaitTimer{now: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	res, err := p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req,
		WithWait(MaxPriorAuthWait), WithInquiryRecords(pairInquiryRecords()), withWaitTimer(timer))
	if err != nil || res.Outcome != "pended" {
		t.Fatalf("result %+v %v", res, err)
	}
	if n := len(p.adj.inquiries()); n != MaxPriorAuthInquiries {
		t.Fatalf("a wait of %v made %d inquiries, want all %d — the schedule must fit inside the bound a caller may ask for",
			MaxPriorAuthWait, n, MaxPriorAuthInquiries)
	}
}

// TestRunPriorAuth_WaitCapsInquiries: a wait inquires at 2 s, then backs off
// (4 s, then 5 s steps), sends at most MaxPriorAuthInquiries, never runs past
// the wait (itself capped at MaxPriorAuthWait), stops at the first decision,
// follows context cancellation, and ends pended without an error.
func TestRunPriorAuth_WaitCapsInquiries(t *testing.T) {
	start := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	records := WithInquiryRecords(pairInquiryRecords())
	secs := func(ds []time.Duration) []int {
		var out []int
		for _, d := range ds {
			out = append(out, int(d/time.Second))
		}
		return out
	}
	equal := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	t.Run("count cap", func(t *testing.T) {
		p := newInquiryPair(t, testPendedDecision())
		timer := &fakeWaitTimer{now: start}
		res, err := p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req,
			WithWait(10*time.Minute), records, withWaitTimer(timer))
		if err != nil || res.Outcome != "pended" || res.Resume == nil {
			t.Fatalf("result %+v %v", res, err)
		}
		if n := len(p.adj.inquiries()); n != MaxPriorAuthInquiries {
			t.Errorf("%d inquiries, want %d", n, MaxPriorAuthInquiries)
		}
		if got := secs(timer.sleeps); !equal(got, []int{2, 4, 5, 5, 5, 5}) {
			t.Errorf("schedule %v", got)
		}
	})

	t.Run("wait bound", func(t *testing.T) {
		p := newInquiryPair(t, testPendedDecision())
		timer := &fakeWaitTimer{now: start}
		p.adj.onInquire = func() { timer.advance(8 * time.Second) } // each inquiry takes 8 s
		res, err := p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req,
			WithWait(10*time.Minute), records, withWaitTimer(timer))
		if err != nil || res.Outcome != "pended" {
			t.Fatalf("result %+v %v", res, err)
		}
		// 2+8, +4+8, +5+8 = 35 s, and the third inquiry started at 24 s — inside the
		// 30 s cap. The fourth would start at 35+5 = 40 s, past it.
		if n := len(p.adj.inquiries()); n != 3 {
			t.Errorf("%d inquiries, want 3 within the %s cap", n, MaxPriorAuthWait)
		}
		if elapsed := timer.Now().Sub(start); elapsed > MaxPriorAuthWait+8*time.Second {
			t.Errorf("waited %s", elapsed)
		}

		p2 := newInquiryPair(t, testPendedDecision())
		timer2 := &fakeWaitTimer{now: start}
		if _, err := p2.sender.RunPriorAuthWith(context.Background(), p2.hubSrv.Client(), p2.ep, p2.payer, p2.req,
			WithWait(5*time.Second), records, withWaitTimer(timer2)); err != nil {
			t.Fatal(err)
		}
		if n := len(p2.adj.inquiries()); n != 1 || !equal(secs(timer2.sleeps), []int{2}) {
			t.Errorf("5 s wait: %d inquiries, sleeps %v", n, secs(timer2.sleeps))
		}
		p3 := newInquiryPair(t, testPendedDecision())
		timer3 := &fakeWaitTimer{now: start}
		if _, err := p3.sender.RunPriorAuthWith(context.Background(), p3.hubSrv.Client(), p3.ep, p3.payer, p3.req,
			WithWait(time.Second), records, withWaitTimer(timer3)); err != nil {
			t.Fatal(err)
		}
		if n := len(p3.adj.inquiries()); n != 0 || len(timer3.sleeps) != 0 {
			t.Errorf("1 s wait: %d inquiries, sleeps %v", n, timer3.sleeps)
		}
	})

	t.Run("stops at the decision", func(t *testing.T) {
		p := newInquiryPair(t, testPendedDecision(), PASDecision{Outcome: PASDenied, DenyReason: "not covered"})
		timer := &fakeWaitTimer{now: start}
		res, err := p.sender.RunPriorAuthWith(context.Background(), p.hubSrv.Client(), p.ep, p.payer, p.req,
			WithWait(time.Minute), records, withWaitTimer(timer))
		if err != nil || res.Outcome != "denied" || res.Denial == nil || res.Denial.Rationale != "not covered" {
			t.Fatalf("result %+v %v", res, err)
		}
		if n := len(p.adj.inquiries()); n != 2 {
			t.Errorf("%d inquiries, want 2", n)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		p := newInquiryPair(t, testPendedDecision())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := &fakeWaitTimer{now: start, cancel: cancel, cutAt: 2}
		_, err := p.sender.RunPriorAuthWith(ctx, p.hubSrv.Client(), p.ep, p.payer, p.req,
			WithWait(time.Minute), records, withWaitTimer(timer))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want cancellation", err)
		}
		if n := len(p.adj.inquiries()); n != 1 {
			t.Errorf("%d inquiries before cancellation, want 1", n)
		}
	})

	t.Run("real timer honors the context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := (realWaitTimer{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Fatalf("Sleep = %v", err)
		}
	})
}

// TestResponderInquire_Refusals: the Responder refuses a malformed or
// wrong-subject inquiry before the Adjudicator sees it.
func TestResponderInquire_Refusals(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	adj := &inquiringAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, decisions: []PASDecision{{Outcome: PASApproved, PreAuthRef: "A"}}}
	_, srv := h.makeResponderSrv(t, responderIdent, adj)
	good, err := BuildPASInquiryBundle("2.0", testInquiryInputs("2.0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name   string
		body   []byte
		status int
		msg    string
	}{
		{"not a bundle", []byte(`{"resourceType":"Claim"}`), 400, "parse inquiry bundle failed"},
		{"no claim", []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"urn:x","resource":{"resourceType":"Patient","id":"p"}}]}`), 400, "inquiry bundle has no Claim"},
		{"entry request", bytes.Replace(good.Body, []byte(`"resource":{"resourceType":"Patient"`), []byte(`"request":{"method":"POST","url":"Patient"},"resource":{"resourceType":"Patient"`), 1), 400, "inquiry bundle entries carry no request"},
		{"not preauthorization", bytes.Replace(good.Body, []byte(`"use":"preauthorization"`), []byte(`"use":"claim"`), 1), 400, "inquiry Claim is not a preauthorization"},
		{"group patient", bytes.Replace(good.Body, []byte(`"patient":{"reference":"https://shn.example/fhir/Patient/pat-1"}`), []byte(`"patient":{"reference":"Group/pat-1"}`), 1), 403, "inquiry Claim patient is not a Patient reference"},
		{"patient not in bundle", bytes.Replace(good.Body, []byte(`"patient":{"reference":"https://shn.example/fhir/Patient/pat-1"}`), []byte(`"patient":{"reference":"Patient/pat-9"}`), 1), 403, "inquiry Claim patient is not in the bundle"},
		{"coverage for another patient", bytes.Replace(good.Body, []byte(`"beneficiary":{"reference":"Patient/pat-1"}`), []byte(`"beneficiary":{"reference":"Patient/pat-2"}`), 1), 403, "inquiry Coverage is for another patient"},
	} {
		t.Run(row.name, func(t *testing.T) {
			envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-"+strings.ReplaceAll(row.name, " ", "-"), row.body)
			resp := postInbound(t, srv, envBytes, hubHdr)
			assertError(t, resp, readBody(t, resp), row.status, row.msg)
		})
	}
	if n := len(adj.inquiries()); n != 0 {
		t.Fatalf("the Adjudicator saw %d refused inquiries", n)
	}
	// The inquiry leg's operation is pinned: a token for another operation is refused.
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-inquire", "pas-submit", "inq-wrong-op", good.Body)
	resp := postInbound(t, srv, envBytes, hubHdr)
	assertError(t, resp, readBody(t, resp), http.StatusForbidden, "authz verification failed")
	// A good inquiry is answered with the decision; its answer is FHIR JSON.
	envBytes, hubHdr = h.buildForwardEnv(t, "pas-claim-inquire", "pas-inquire", "inq-good", good.Body)
	resp = postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good inquiry: %d %s", resp.StatusCode, body)
	}
	if res, err := parsePASOutcome(h.openResponse(t, body)); err != nil || res.Outcome != "approved" {
		t.Fatalf("good inquiry answer: %+v %v", res, err)
	}
}
