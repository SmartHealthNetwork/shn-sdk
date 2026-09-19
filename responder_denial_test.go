package shnsdk

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// responder_denial_test.go — a denial carries only what the participant's
// adjudicator decided: its own reason (exactly, or none at all) and its own
// notes (exactly, or none at all). The Responder adds no rationale, appeal
// window or review instruction of its own, whatever service was denied.

// decisionAdjudicator answers every PriorAuth with one fixed decision.
type decisionAdjudicator struct {
	errPriorAuthAdjudicator
	dec PASDecision
}

func (a decisionAdjudicator) PriorAuth(_ []byte, _ bool) (PASDecision, error) { return a.dec, nil }

// deniedClaimResponse drives one $submit for an order (code, display) through
// the real Responder pipeline with adj, and returns the whole response Bundle
// plus its ClaimResponse resource.
func deniedClaimResponse(t *testing.T, dec PASDecision, code, display, corr string) (bundle []byte, claimResponse map[string]json.RawMessage) {
	t.Helper()
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, decisionAdjudicator{dec: dec})
	member := "MBR-001"
	patientRef := "Patient/" + member
	qr := answeredQR(t, member, DemoLumbarContext(), h.now)
	sr, err := BuildServiceRequest(code, display, "Z74.09", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	claim, err := BuildConformantClaimBundle(ConformantClaimInputs{Insurer: testPayerOrganization(CMSPayerIdentity), Coverage: testMemberCoverage(member),
		Provider:       testRequestingProvider(),
		MemberIDSystem: MemberSystem,
		QR:             qr, SR: sr, PatientRef: patientRef, CoverageRef: "Coverage/" + member,
		MemberID: member, Corr: corr, Created: h.now, Payer: CMSPayerIdentity, PayerOrgEntry: true,
	})
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", corr, claim)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	bundle = h.openResponse(t, body)
	raw, _, err := selectPASClaimResponse(bundle)
	if err != nil {
		t.Fatalf("selectPASClaimResponse: %v", err)
	}
	if err := json.Unmarshal(raw, &claimResponse); err != nil {
		t.Fatalf("decode ClaimResponse: %v", err)
	}
	res, err := ParseClaimResponse(bundle)
	if err != nil || res.Outcome != "denied" {
		t.Fatalf("ParseClaimResponse = %+v, %v; want a denial", res, err)
	}
	return bundle, claimResponse
}

// TestResponderDenial_NoReasonOmitsDisposition: a denial whose adjudicator gave
// no reason carries no disposition at all — not an empty one, not a default.
func TestResponderDenial_NoReasonOmitsDisposition(t *testing.T) {
	bundle, cr := deniedClaimResponse(t, PASDecision{Outcome: PASDenied}, "72148", "MRI lumbar spine without contrast", "deny-noreason-1")
	if v, ok := cr["disposition"]; ok {
		t.Fatalf("ClaimResponse.disposition = %s, want the element absent", v)
	}
	if bytes.Contains(bundle, []byte(`"disposition"`)) {
		t.Fatalf("response carries a disposition member: %s", bundle)
	}
}

// TestResponderDenial_ReasonPreservedExactly: the adjudicator's reason reaches
// the requester as exactly that string — non-ASCII, markdown and markup
// characters included.
func TestResponderDenial_ReasonPreservedExactly(t *testing.T) {
	for _, reason := range []string{
		"Excluded service under the member's plan.",
		"**Nicht erfüllt** — _siehe_ [Richtlinie §4](https://payer.example/policy?a=1&b=2) <b>überprüft</b>",
		"保険適用外のサービスです。\n\n- 理由: 1\n- 参照: `R-12`",
		"  leading and trailing space  ",
	} {
		_, cr := deniedClaimResponse(t, PASDecision{Outcome: PASDenied, DenyReason: reason}, "72148", "MRI lumbar spine without contrast", "deny-exact-1")
		var got string
		if err := json.Unmarshal(cr["disposition"], &got); err != nil {
			t.Fatalf("decode disposition %s: %v", cr["disposition"], err)
		}
		if got != reason {
			t.Errorf("disposition = %q, want exactly %q", got, reason)
		}
	}
}

// TestResponderDenial_NoInventedProcessNote: a denial carries no processNote
// unless the adjudicator supplies one; supplied notes arrive exactly, in order,
// numbered from 1.
func TestResponderDenial_NoInventedProcessNote(t *testing.T) {
	t.Run("none supplied", func(t *testing.T) {
		bundle, cr := deniedClaimResponse(t, PASDecision{Outcome: PASDenied, DenyReason: "Excluded service."}, "72148", "MRI lumbar spine without contrast", "deny-nonote-1")
		if v, ok := cr["processNote"]; ok {
			t.Fatalf("ClaimResponse.processNote = %s, want the element absent", v)
		}
		for _, invented := range []string{"Appeal window", "peer-to-peer", "medical director"} {
			if bytes.Contains(bundle, []byte(invented)) {
				t.Errorf("response carries %q the participant never supplied: %s", invented, bundle)
			}
		}
	})
	t.Run("supplied notes carried exactly", func(t *testing.T) {
		notes := []PASProcessNote{
			{Type: "print", Text: "Appeal within 45 days — see ¶ 3."},
			{Text: "Call **1-800-555-0100** for review."},
			{Type: "display", Text: "Référence: D-7"},
		}
		_, cr := deniedClaimResponse(t, PASDecision{Outcome: PASDenied, DenyReason: "Excluded service.", ProcessNotes: notes}, "72148", "MRI lumbar spine without contrast", "deny-notes-1")
		var got []map[string]any
		if err := json.Unmarshal(cr["processNote"], &got); err != nil {
			t.Fatalf("decode processNote %s: %v", cr["processNote"], err)
		}
		want := []map[string]any{
			{"number": float64(1), "type": "print", "text": notes[0].Text},
			{"number": float64(2), "text": notes[1].Text},
			{"number": float64(3), "type": "display", "text": notes[2].Text},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("processNote = %v, want %v", got, want)
		}
	})
}

// TestResponderDenial_UnrelatedServiceNoLumbarText: denying a service that has
// nothing to do with imaging, with no reason, yields no imaging rationale.
func TestResponderDenial_UnrelatedServiceNoLumbarText(t *testing.T) {
	_, cr := deniedClaimResponse(t, PASDecision{Outcome: PASDenied}, "99348", "Home visit, established patient", "deny-homevisit-1")
	raw, err := json.Marshal(cr)
	if err != nil {
		t.Fatal(err)
	}
	for _, invented := range []string{"lumbar", "imaging", "Conservative therapy", "medical-necessity", "Appeal window"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(invented)) {
			t.Errorf("home-visit denial carries %q the participant never supplied: %s", invented, raw)
		}
	}
}

// TestResponderDecision_NotesOnNonDenialRefused: notes are carried on denials;
// a decision that supplies notes with any other outcome is refused rather than
// having its notes silently dropped.
func TestResponderDecision_NotesOnNonDenialRefused(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	notes := []PASProcessNote{{Type: "print", Text: "a note"}}
	for name, dec := range map[string]PASDecision{
		"approved": {Outcome: PASApproved, PreAuthRef: "PA-1", ValidUntil: "2026-09-10", ProcessNotes: notes},
		"pended":   {Outcome: PASPended, NeededItems: []string{"operative-diagnostic-report"}, ProcessNotes: notes},
	} {
		t.Run(name, func(t *testing.T) {
			_, srv := h.makeResponderSrv(t, responderIdent, decisionAdjudicator{dec: dec})
			qr := answeredQR(t, "MBR-001", DemoLumbarContext(), h.now)
			corr := "notes-" + name
			bundle := buildConformantClaim(t, "MBR-001", corr, qr, h.now)
			envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", corr, bundle)
			resp := postInbound(t, srv, envBytes, hubHdr)
			assertError(t, resp, readBody(t, resp), http.StatusInternalServerError, "process notes are carried on denials only")
		})
	}
}

// TestResponderDenial_BadNoteNamedIn500: a denial whose note the builder
// refuses is answered 500 naming the note's position and type, not the note text.
func TestResponderDenial_BadNoteNamedIn500(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	dec := PASDecision{Outcome: PASDenied, ProcessNotes: []PASProcessNote{{Text: "fine"}, {Type: "email", Text: "private words"}}}
	_, srv := h.makeResponderSrv(t, responderIdent, decisionAdjudicator{dec: dec})
	qr := answeredQR(t, "MBR-001", DemoLumbarContext(), h.now)
	bundle := buildConformantClaim(t, "MBR-001", "deny-badnote-1", qr, h.now)
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "deny-badnote-1", bundle)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	assertError(t, resp, body, http.StatusInternalServerError,
		`build denied response failed: shnsdk: process note 2 type "email" is not display, print or printoper`)
	if bytes.Contains(body, []byte("private words")) {
		t.Fatalf("500 body carries note text: %s", body)
	}
}

// TestDecisionNotesMisplaced covers the guard both PAS handlers (submit and
// update) apply before building a response.
func TestDecisionNotesMisplaced(t *testing.T) {
	notes := []PASProcessNote{{Text: "a note"}}
	for _, tc := range []struct {
		dec  PASDecision
		want bool
	}{
		{PASDecision{Outcome: PASDenied, ProcessNotes: notes}, false},
		{PASDecision{Outcome: PASDenied}, false},
		{PASDecision{Outcome: PASApproved}, false},
		{PASDecision{Outcome: PASApproved, ProcessNotes: notes}, true},
		{PASDecision{Outcome: PASPended, ProcessNotes: notes}, true},
	} {
		if got := decisionNotesMisplaced(tc.dec) != ""; got != tc.want {
			t.Errorf("decisionNotesMisplaced(%+v) refused=%v, want %v", tc.dec, got, tc.want)
		}
	}
}

// TestBuildDeniedResponseWithNotes_Guards: a note without text, or with a type
// outside the FHIR note-type codes, is refused; an unknown line is refused.
func TestBuildDeniedResponseWithNotes_Guards(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	ok := []PASProcessNote{{Type: "printoper", Text: "fine"}}
	if _, err := BuildDeniedResponseWithNotesAtLine("2.2", "Patient/p", "c", "", ok, now); err != nil {
		t.Fatalf("valid notes refused: %v", err)
	}
	for name, notes := range map[string][]PASProcessNote{
		"empty text":   {{Type: "print", Text: ""}},
		"unknown type": {{Type: "email", Text: "x"}},
		"second bad":   {{Text: "x"}, {Type: "Print", Text: "y"}},
	} {
		if _, err := BuildDeniedResponseWithNotesAtLine("2.0", "Patient/p", "c", "r", notes, now); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := BuildDeniedResponseWithNotesAtLine("9.9", "Patient/p", "c", "r", ok, now); err == nil || !strings.Contains(err.Error(), "BuildDeniedResponseWithNotesAtLine: unknown PAS line") {
		t.Errorf("unknown line: err = %v, want it to name BuildDeniedResponseWithNotesAtLine", err)
	}
}
