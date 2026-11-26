package shnsdk

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// responder_recipient_key_test.go — the Responder's answer to a requester
// whose registered encryption key cannot be sealed to (a key of small order,
// or none: Seal's ErrLowOrderKey). The answer is refused on authentication
// before any response-leg token is asked for: 502, application/json, exactly
// the Smart Gateway's body ({"error": "the recipient's registered encryption
// key is not usable", "rule": "authentication"}), naming no cause. Once the
// Adjudicator has answered (it may have acted) the refusal is attested
// delivered (X-SHN-Delivered: yes), as a Smart Gateway attests one once its
// system has answered; the Responder's own refusals before any Adjudicator
// call carry no attestation. A ledger step the answer held is rolled back.

// wantKeyNotUsableBody is the refusal's body, byte for byte: the Smart
// Gateway's, and its json.Encoder framing.
const wantKeyNotUsableBody = `{"error":"the recipient's registered encryption key is not usable","rule":"authentication"}` + "\n"

// unusableRecipientKey is one registered requester key the seal refuses.
type unusableRecipientKey struct {
	name string
	key  *[32]byte
}

// unusableRecipientKeys are the X25519 keys of small order libsodium's
// blocklist names, each with its top bit clear and set, in a fixed order, and
// a nil key.
func unusableRecipientKeys(t *testing.T) []unusableRecipientKey {
	t.Helper()
	enc := lowOrderEncodings(t)
	if len(enc) != 14 {
		t.Fatalf("%d small-order encodings, want the 7 blocklisted points with the top bit clear and set", len(enc))
	}
	names := make([]string, 0, len(enc))
	for n := range enc {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]unusableRecipientKey, 0, len(names)+1)
	for _, n := range names {
		out = append(out, unusableRecipientKey{n, enc[n]})
	}
	return append(out, unusableRecipientKey{"nil key", nil})
}

// countingAdaptiveAdjudicator counts adaptive rounds as Adjudicator calls.
type countingAdaptiveAdjudicator struct {
	*countingAdjudicator
	next NextQuestionAdjudicator
}

func (a countingAdaptiveAdjudicator) NextQuestion(qr []byte) ([]byte, error) {
	a.calls.Add(1)
	return a.next.NextQuestion(qr)
}

// countingInquiryAdjudicator counts inquiries as Adjudicator calls.
type countingInquiryAdjudicator struct {
	*countingAdjudicator
	inq InquiryAdjudicator
}

func (a countingInquiryAdjudicator) Inquire(inq PASInquiry) (PASDecision, error) {
	a.calls.Add(1)
	return a.inq.Inquire(inq)
}

// countAuthz puts a counting relay in front of the harness's Authorization
// Framework; a Responder built after it asks for its tokens through it.
func (h *paTestHarness) countAuthz(t *testing.T) *atomic.Int32 {
	t.Helper()
	inner := h.authzSrv
	n := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h.authzSrv = srv
	return n
}

// keyRowAdjudicator builds a row's Adjudicator and the count of the calls that
// reached it.
type keyRowAdjudicator func(h *paTestHarness) (Adjudicator, *countingAdjudicator)

func plainCounting(h *paTestHarness) (Adjudicator, *countingAdjudicator) {
	c := &countingAdjudicator{inner: &paTestAdjudicator{now: h.now}}
	return c, c
}

func adaptiveCounting(h *paTestHarness) (Adjudicator, *countingAdjudicator) {
	a := &adaptiveAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, answer: []byte(`{"resourceType":"QuestionnaireResponse","status":"completed"}`)}
	c := &countingAdjudicator{inner: a}
	return countingAdaptiveAdjudicator{c, a}, c
}

func inquiryCounting(h *paTestHarness) (Adjudicator, *countingAdjudicator) {
	a := &inquiringAdjudicator{paTestAdjudicator: paTestAdjudicator{now: h.now}, decisions: []PASDecision{{Outcome: PASApproved, PreAuthRef: "A"}}}
	c := &countingAdjudicator{inner: a}
	return countingInquiryAdjudicator{c, a}, c
}

func errPriorAuthCounting(*paTestHarness) (Adjudicator, *countingAdjudicator) {
	c := &countingAdjudicator{inner: errPriorAuthAdjudicator{}}
	return c, c
}

// recipientKeyRow is one request on one leg, and the answer a requester whose
// key is usable gets for it.
type recipientKeyRow struct {
	name, txType, op string
	adj              keyRowAdjudicator
	payload          func(t *testing.T, h *paTestHarness) []byte
	// attested: the request reaches the Adjudicator, so a refusal to send its
	// answer is attested delivered.
	attested bool
	// status is the answer frame's status to a requester whose key is usable;
	// msg, for a refusal, its message.
	status int
	msg    func(t *testing.T, payload []byte) string
}

func eligibilityRequest(t *testing.T, h *paTestHarness) []byte {
	t.Helper()
	cer, err := BuildEligibilityRequestExact("MBR-001", "9999999999", CMSPayerIdentity, h.now)
	if err != nil {
		t.Fatal(err)
	}
	return cer
}

// eligibilityRequestNoInsurer is an eligibility request naming no insurer,
// which this Responder refuses before asking its Adjudicator.
func eligibilityRequestNoInsurer(t *testing.T, h *paTestHarness) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(eligibilityRequest(t, h), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["insurer"]; !ok {
		t.Fatal("fixture: the eligibility request names no insurer to remove")
	}
	delete(m, "insurer")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func approvingClaim(t *testing.T, h *paTestHarness) []byte {
	t.Helper()
	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	return buildConformantClaim(t, "MBR-001", "key-claim", qr, h.now)
}

func pendingQR(t *testing.T, h *paTestHarness) []byte {
	t.Helper()
	return answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8, PriorSurgery: true}, h.now)
}

func inquiryRequest(t *testing.T, _ *paTestHarness) []byte {
	t.Helper()
	b, err := BuildPASInquiryBundle("2.0", testInquiryInputs("2.0"))
	if err != nil {
		t.Fatal(err)
	}
	return b.Body
}

func packageFrame(canonical string) func(*testing.T, *paTestHarness) []byte {
	return func(t *testing.T, _ *paTestHarness) []byte {
		return frameDTROp(t, FrameOperationQuestionnairePackage, packageRequest(t, QuestionnairePackageInputs{
			Coverages:      [][]byte{pkgCoverage("c1", "Patient/p1")},
			Orders:         [][]byte{pkgOrder("o1", "Patient/p1")},
			Questionnaires: []string{canonical},
			Context:        "assert-1",
		}))
	}
}

// packageFrameNoCoverage is a questionnaire-package request naming no
// coverage, which this Responder refuses before asking its Adjudicator.
func packageFrameNoCoverage(t *testing.T, _ *paTestHarness) []byte {
	return frameDTROp(t, FrameOperationQuestionnairePackage,
		[]byte(`{"resourceType":"Parameters","parameter":[{"name":"questionnaire","valueCanonical":"`+SupportedQuestionnaireCanonical+`"}]}`))
}

func nextQuestionFrame(t *testing.T, _ *paTestHarness) []byte {
	return frameDTROp(t, FrameOperationNextQuestion, []byte(nqQR))
}

// attestedKeyRows are requests the Adjudicator answers: an answer on each leg,
// and a handler's refusal given after the Adjudicator answered.
func attestedKeyRows() []recipientKeyRow {
	return []recipientKeyRow{
		{"coverage-eligibility answer", "coverage-eligibility", "eligibility-inquiry", plainCounting, eligibilityRequest, true, 200, nil},
		{"crd-order-select answer", "crd-order-select", "crd-order-select", plainCounting,
			func(t *testing.T, _ *paTestHarness) []byte { return buildConformantCRD(t, "MBR-001", "72148") }, true, 200, nil},
		{"dtr questionnaire-package answer", "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", plainCounting,
			packageFrame(SupportedQuestionnaireCanonical), true, 200, nil},
		{"dtr next-question answer", "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", adaptiveCounting, nextQuestionFrame, true, 200, nil},
		{"pas-claim answer", "pas-claim", "pas-submit", plainCounting, approvingClaim, true, 200, nil},
		{"pas-claim pended answer", "pas-claim", "pas-submit", plainCounting,
			func(t *testing.T, h *paTestHarness) []byte {
				return buildConformantClaim(t, "MBR-001", "key-pend", pendingQR(t, h), h.now)
			}, true, 200, nil},
		{"pas-claim-inquire answer", "pas-claim-inquire", "pas-inquire", inquiryCounting, inquiryRequest, true, 200, nil},
		{"dtr questionnaire-package unknown to the Adjudicator", "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", plainCounting,
			packageFrame(SupportedQuestionnaireCanonical + "|9"), true, 400, fixed("unknown questionnaire canonical")},
		{"pas-claim Adjudicator error", "pas-claim", "pas-submit", errPriorAuthCounting, approvingClaim, true, 422, fixed("adjudication unavailable")},
	}
}

// unattestedKeyRows are the Responder's own refusals before any Adjudicator
// call: every refusal handleInbound writes itself after opening the payload,
// and a handler's own refusal on each leg.
func unattestedKeyRows() []recipientKeyRow {
	var rows []recipientKeyRow
	for _, r := range postDecryptionRefusals() {
		rows = append(rows, recipientKeyRow{r.name, r.txType, r.op, plainCounting, r.payload, false, r.status, r.msg})
	}
	return append(rows,
		recipientKeyRow{"coverage-eligibility naming no insurer", "coverage-eligibility", "eligibility-inquiry", plainCounting,
			eligibilityRequestNoInsurer, false, 400, fixed("the request names no insurer")},
		recipientKeyRow{"crd-order-select that does not parse", "crd-order-select", "crd-order-select", plainCounting,
			func(*testing.T, *paTestHarness) []byte { return []byte(`{"hook":"order-select"}`) }, false, 400, fixed("parse order-select failed")},
		recipientKeyRow{"dtr questionnaire-package without coverage", "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", plainCounting,
			packageFrameNoCoverage, false, 400, fixed("questionnaire-package request has no coverage")},
		recipientKeyRow{"dtr next-question to a payer without adaptive questionnaires", "dtr-questionnaire-fetch", "dtr-questionnaire-fetch", plainCounting,
			nextQuestionFrame, false, 422, fixed("adaptive questionnaires are not served by this payer")},
		recipientKeyRow{"pas-claim that does not parse", "pas-claim", "pas-submit", plainCounting,
			func(*testing.T, *paTestHarness) []byte {
				return []byte(`{"resourceType":"Bundle","type":"collection"}`)
			}, false, 400, fixed("parse bundle failed")},
		recipientKeyRow{"pas-claim-update naming no pending claim", "pas-claim-update", "pas-update-submit", plainCounting,
			func(t *testing.T, h *paTestHarness) []byte {
				return buildConformantUpdate(t, "MBR-001", "key-upd", "key-never-pended", pendingQR(t, h), h.now)
			}, false, 409, fixed("ClaimUpdate references no pending claim available for this patient")},
		recipientKeyRow{"pas-claim-inquire that is not a bundle", "pas-claim-inquire", "pas-inquire", inquiryCounting,
			func(*testing.T, *paTestHarness) []byte { return []byte(`{"resourceType":"Claim"}`) }, false, 400, fixed("parse inquiry bundle failed")},
		recipientKeyRow{"pas-claim-inquire to a payer that serves no inquiries", "pas-claim-inquire", "pas-inquire", plainCounting,
			inquiryRequest, false, 501, fixed("prior-authorization inquiries are not served by this payer")},
	)
}

// keyRowRun is one request of a row sent to a frame-capable requester whose
// registered key is key (usable: the requester's own).
type keyRowRun struct {
	h       *paTestHarness
	payload []byte
	resp    *http.Response
	raw     []byte
	calls   int32
	authz   int32
}

func runKeyRow(t *testing.T, row recipientKeyRow, key *[32]byte, usable bool) keyRowRun {
	t.Helper()
	h, ident, _ := newPAHarness(t)
	adj, counter := row.adj(h)
	payload := row.payload(t, h)
	envBytes, hubHdr := h.buildForwardEnv(t, row.txType, row.op, "key-"+strings.ReplaceAll(row.name, " ", "-"), payload)
	// Every request to the Authorization Framework from here on is the
	// Responder asking for its response leg's token.
	authz := h.countAuthz(t)
	resolve := func(holderID string) (*[32]byte, bool) {
		if holderID != h.senderID {
			return nil, false
		}
		if usable {
			return h.senderEncPub, true
		}
		return key, true
	}
	srv := h.refusalResponder(t, ident, adj, framesV1, resolve)
	resp := postInbound(t, srv, envBytes, hubHdr)
	raw := readBody(t, resp)
	return keyRowRun{h: h, payload: payload, resp: resp, raw: raw, calls: counter.calls.Load(), authz: authz.Load()}
}

// assertKeyNotUsable holds an answer to the exact refusal, attested or not.
func assertKeyNotUsable(t *testing.T, resp *http.Response, raw []byte, attested bool) {
	t.Helper()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP %d (%s), want 502 application/json; body: %s", resp.StatusCode, resp.Header.Get("Content-Type"), raw)
	}
	if string(raw) != wantKeyNotUsableBody {
		t.Fatalf("body = %q\nwant exactly %q", raw, wantKeyNotUsableBody)
	}
	lower := strings.ToLower(string(raw))
	for _, cause := range []string{"x25519", "order"} {
		if strings.Contains(lower, cause) {
			t.Fatalf("the refusal names its cause (%q): %s", cause, raw)
		}
	}
	got := resp.Header.Values(deliveredHeaderForTest)
	if attested && (len(got) != 1 || got[0] != "yes") {
		t.Fatalf("%s = %q, want exactly yes: the Adjudicator answered", deliveredHeaderForTest, got)
	}
	if !attested && len(got) != 0 {
		t.Fatalf("%s = %q, want none: the Adjudicator never answered", deliveredHeaderForTest, got)
	}
}

// deliveredHeaderForTest is the delivery attestation's header, spelled out.
const deliveredHeaderForTest = "X-SHN-Delivered"

// assertControlAnswer holds a usable key's answer to a 200 response leg the
// requester opens, carrying the row's frame status (and, for a refusal, its
// exact body), with no delivery attestation and one response-leg token asked.
func assertControlAnswer(t *testing.T, row recipientKeyRow, run keyRowRun) {
	t.Helper()
	if run.resp.StatusCode != http.StatusOK || run.resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP %d (%s), want the answer sealed into a 200 response leg; body: %s", run.resp.StatusCode, run.resp.Header.Get("Content-Type"), run.raw)
	}
	if got := run.resp.Header.Values(deliveredHeaderForTest); len(got) != 0 {
		t.Fatalf("a sent answer carries %s = %q", deliveredHeaderForTest, got)
	}
	if run.authz != 1 {
		t.Fatalf("%d response-leg authorization(s) asked, want 1", run.authz)
	}
	hdr, body, err := DecodeHTTPFrame(run.h.openResponse(t, run.raw))
	if err != nil {
		t.Fatalf("the answer is not a frame: %v", err)
	}
	if hdr.Status != row.status {
		t.Fatalf("frame status %d, want %d; body: %s", hdr.Status, row.status, body)
	}
	if row.msg != nil {
		want, ct := wantRefusal(t, row.txType, row.status, row.msg(t, run.payload))
		if !bytes.Equal(body, want) || hdr.Headers["Content-Type"] != ct {
			t.Fatalf("frame body %s (%s)\nwant exactly %s (%s)", body, hdr.Headers["Content-Type"], want, ct)
		}
	}
}

// REJECTION: an answer the Adjudicator gave, on every leg that reaches it, to
// a requester whose registered key the seal refuses, is refused 502 on
// authentication with the exact body, attested delivered; the Adjudicator was
// asked, and no response-leg token was.
func TestSDKResponder_RecipientKeyNotUsable_AttestedOnEveryLeg(t *testing.T) {
	for _, row := range attestedKeyRows() {
		for _, k := range unusableRecipientKeys(t) {
			t.Run(row.name+"/"+k.name, func(t *testing.T) {
				run := runKeyRow(t, row, k.key, false)
				assertKeyNotUsable(t, run.resp, run.raw, true)
				if run.calls == 0 {
					t.Fatal("the Adjudicator was never asked: the row does not reach it")
				}
				if run.authz != 0 {
					t.Fatalf("%d response-leg authorization(s) asked for an answer that cannot be sealed", run.authz)
				}
			})
		}
	}
}

// REJECTION: the Responder's own refusals before any Adjudicator call, to a
// requester whose registered key the seal refuses, are refused 502 on
// authentication with the exact body and carry no delivery attestation; the
// Adjudicator was never asked, and no response-leg token was.
func TestSDKResponder_RecipientKeyNotUsable_UnattestedBeforeTheAdjudicator(t *testing.T) {
	for _, row := range unattestedKeyRows() {
		for _, k := range unusableRecipientKeys(t) {
			t.Run(row.name+"/"+k.name, func(t *testing.T) {
				run := runKeyRow(t, row, k.key, false)
				assertKeyNotUsable(t, run.resp, run.raw, false)
				if run.calls != 0 {
					t.Fatalf("the refused request reached the Adjudicator %d time(s)", run.calls)
				}
				if run.authz != 0 {
					t.Fatalf("%d response-leg authorization(s) asked for an answer that cannot be sealed", run.authz)
				}
			})
		}
	}
}

// TestSDKResponder_RecipientKeyNotUsable_Control: every row above, to a
// requester whose key is usable, is answered in a 200 response leg the
// requester opens, with the row's answer or exact refusal, unattested; the
// attested rows reach the Adjudicator and the unattested ones do not.
func TestSDKResponder_RecipientKeyNotUsable_Control(t *testing.T) {
	for _, row := range append(attestedKeyRows(), unattestedKeyRows()...) {
		t.Run(row.name, func(t *testing.T) {
			run := runKeyRow(t, row, nil, true)
			assertControlAnswer(t, row, run)
			if row.attested && run.calls == 0 {
				t.Fatal("the Adjudicator was never asked")
			}
			if !row.attested && run.calls != 0 {
				t.Fatalf("the request reached the Adjudicator %d time(s)", run.calls)
			}
		})
	}
}

// keySwitchRig is a Responder whose requester's registered key can be changed
// between requests.
type keySwitchRig struct {
	h     *paTestHarness
	srv   *httptest.Server
	adj   *countingAdjudicator
	authz *atomic.Int32
	key   atomic.Pointer[[32]byte]
}

func newKeySwitchRig(t *testing.T) *keySwitchRig {
	t.Helper()
	h, ident, _ := newPAHarness(t)
	rig := &keySwitchRig{h: h, adj: &countingAdjudicator{inner: &paTestAdjudicator{now: h.now}}}
	rig.key.Store(h.senderEncPub)
	rig.authz = h.countAuthz(t)
	rig.srv = h.refusalResponder(t, ident, rig.adj, framesV1, func(holderID string) (*[32]byte, bool) {
		if holderID != h.senderID {
			return nil, false
		}
		return rig.key.Load(), true
	})
	return rig
}

// send posts one request on correlation corr (each its own: the Hub's
// assertion is single-use).
func (rig *keySwitchRig) send(t *testing.T, txType, op, corr string, payload []byte) (*http.Response, []byte) {
	t.Helper()
	envBytes, hubHdr := rig.h.buildForwardEnv(t, txType, op, corr, payload)
	resp := postInbound(t, rig.srv, envBytes, hubHdr)
	return resp, readBody(t, resp)
}

// answer opens a usable key's answer: its frame status and body.
func (rig *keySwitchRig) answer(t *testing.T, resp *http.Response, raw []byte) (int, []byte) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d, want a 200 response leg; body: %s", resp.StatusCode, raw)
	}
	hdr, body, err := DecodeHTTPFrame(rig.h.openResponse(t, raw))
	if err != nil {
		t.Fatalf("the answer is not a frame: %v", err)
	}
	return hdr.Status, body
}

// REJECTION: a pas-claim-update the Adjudicator answered, to a requester whose
// registered key the seal refuses, is refused 502 attested delivered, no
// response-leg token is asked for, and the claim it held is released: the
// same amendment, sent again once the requester's key is usable, is
// adjudicated and approved (a claim left mid-update would answer 409).
func TestSDKResponder_RecipientKeyNotUsable_UpdateReleasesTheClaim(t *testing.T) {
	for _, k := range unusableRecipientKeys(t) {
		t.Run(k.name, func(t *testing.T) {
			rig := newKeySwitchRig(t)
			h := rig.h
			qr := pendingQR(t, h)
			origCorr := "key-upd-orig-1"
			resp, raw := rig.send(t, "pas-claim", "pas-submit", origCorr, buildConformantClaim(t, "MBR-001", origCorr, qr, h.now))
			status, body := rig.answer(t, resp, raw)
			if res, err := parsePASOutcome(body); status != 200 || err != nil || res.Outcome != "pended" {
				t.Fatalf("setup submit: frame %d, outcome %+v, %v", status, res, err)
			}

			rig.key.Store(k.key)
			calls, authz := rig.adj.calls.Load(), rig.authz.Load()
			resp, raw = rig.send(t, "pas-claim-update", "pas-update-submit", "key-upd-amend-1",
				buildConformantUpdate(t, "MBR-001", "key-upd-amend-1", origCorr, qr, h.now))
			assertKeyNotUsable(t, resp, raw, true)
			if rig.adj.calls.Load() != calls+1 {
				t.Fatalf("the update reached the Adjudicator %d time(s), want once", rig.adj.calls.Load()-calls)
			}
			if rig.authz.Load() != authz {
				t.Fatalf("%d response-leg authorization(s) asked for an answer that cannot be sealed", rig.authz.Load()-authz)
			}

			rig.key.Store(h.senderEncPub)
			resp, raw = rig.send(t, "pas-claim-update", "pas-update-submit", "key-upd-amend-2",
				buildConformantUpdate(t, "MBR-001", "key-upd-amend-2", origCorr, qr, h.now))
			status, body = rig.answer(t, resp, raw)
			if res, err := parsePASOutcome(body); status != 200 || err != nil || res.Outcome != "approved" {
				t.Fatalf("the amendment sent again: frame %d %s, outcome %+v, %v; want approved, the claim released", status, body, res, err)
			}
		})
	}
}

// REJECTION: a pended pas-claim answer refused for the requester's key
// records no pend: an amendment of it, once the key is usable, names no
// pending claim.
func TestSDKResponder_RecipientKeyNotUsable_RefusedPendRecordsNothing(t *testing.T) {
	for _, k := range unusableRecipientKeys(t) {
		t.Run(k.name, func(t *testing.T) {
			rig := newKeySwitchRig(t)
			h := rig.h
			qr := pendingQR(t, h)
			origCorr := "key-pend-orig-1"
			rig.key.Store(k.key)
			resp, raw := rig.send(t, "pas-claim", "pas-submit", origCorr, buildConformantClaim(t, "MBR-001", origCorr, qr, h.now))
			assertKeyNotUsable(t, resp, raw, true)

			rig.key.Store(h.senderEncPub)
			resp, raw = rig.send(t, "pas-claim-update", "pas-update-submit", "key-pend-amend", buildConformantUpdate(t, "MBR-001", "key-pend-amend", origCorr, qr, h.now))
			status, body := rig.answer(t, resp, raw)
			want, _ := wantRefusal(t, "pas-claim-update", http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
			if status != http.StatusConflict || !bytes.Equal(body, want) {
				t.Fatalf("amendment of the refused pend: frame %d %s, want 409 %s", status, body, want)
			}
		})
	}
}
