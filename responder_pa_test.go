package shnsdk

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// errAdjudicationUnavailable is the error errPriorAuthAdjudicator returns to exercise the
// handler's PriorAuth-error → 422 path.
var errAdjudicationUnavailable = errors.New("adjudication unavailable")

// responder_pa_test.go — hermetic Responder PA-chain tests for the CONFORMANT PA cases
// (crd-order-select / pas-claim / pas-claim-update). These drive the conformant requests the
// Originator (RunPriorAuth) builds through the real Responder.Handler() pipeline, asserting the
// CRD cards / approve / pend / deny responses + the request-parse rejection rows.
// TestResponder_FullPAChain pairs the Originator's conformant request shapes with the Responder
// end to end (a full provider->payer PA round-trip in-process).

// ---- conformant CRD request builders (member-parameterized) ----

// buildConformantCRD builds a conformant CRD order-select request for the given member/CPT, with
// the SR subject / Coverage beneficiary / context.patientId all bound to member (the happy path).
func buildConformantCRD(t *testing.T, member, cpt string) []byte {
	t.Helper()
	patientRef := "Patient/" + member
	coverageRef := "Coverage/" + member
	srJSON, err := BuildServiceRequest(cpt, "MRI lumbar spine without contrast", "M54.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	covJSON, err := BuildCoverageWithPayer(patientRef, coverageRef, CMSPayerIdentity)
	if err != nil {
		t.Fatalf("BuildCoverageWithPayer: %v", err)
	}
	req, err := BuildConformantOrderSelectRequest(srJSON, covJSON, patientRef)
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest: %v", err)
	}
	return req
}

// answeredQR builds an answered sandbox lumbar QR for the demo persona with the given clinical
// context (so a test can drive approve / pend by choosing the inputs).
func answeredQR(t *testing.T, member string, cc ClinicalContext, now time.Time) []byte {
	t.Helper()
	patientRef := "Patient/" + member
	coverageRef := "Coverage/" + member
	qrJSON, err := FillQuestionnaire(SandboxLumbarQuestionnaire(), cc, QRContext{
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		OrderRef:    "ServiceRequest/sr-" + member,
		Authored:    now,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	return qrJSON
}

// buildConformantClaim builds a conformant PAS $submit Claim Bundle for member with the given QR.
func buildConformantClaim(t *testing.T, member, corr string, qrJSON []byte, now time.Time) []byte {
	t.Helper()
	patientRef := "Patient/" + member
	coverageRef := "Coverage/" + member
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	bundle, err := BuildConformantClaimBundle(ConformantClaimInputs{
		QR:          qrJSON,
		SR:          srJSON,
		PatientRef:  patientRef,
		CoverageRef: coverageRef,
		Corr:        corr,
		Created:     now,
		Payer:       CMSPayerIdentity,
	})
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	return bundle
}

// ---- TestResponder_CRD ----

// TestResponder_CRD proves the conformant crd-order-select dispatch: PA-required happy path +
// the rejection rows the deleted minimized test covered.
func TestResponder_CRD(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	t.Run("pa-required", func(t *testing.T) {
		req := buildConformantCRD(t, "MBR-001", "72148")
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-happy-1", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		cov, err := ParseCards(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("ParseCards: %v", err)
		}
		if !cov.PARequired() {
			t.Errorf("PARequired = false, want true; cov=%+v", cov)
		}
		if !cov.NeedsDTR() || cov.Questionnaires[0] != QuestionnaireCanonicalLumbarMRI {
			t.Errorf("questionnaire = %v, want %q", cov.Questionnaires, QuestionnaireCanonicalLumbarMRI)
		}
	})

	t.Run("no-pa-required", func(t *testing.T) {
		req := buildConformantCRD(t, "MBR-001", "99999") // a CPT the sandbox does not gate
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-nopa-1", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		cov, err := ParseCards(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("ParseCards: %v", err)
		}
		if cov.PARequired() {
			t.Errorf("PARequired = true, want false; cov=%+v", cov)
		}
	})

	t.Run("malformed-no-SR", func(t *testing.T) {
		// A CDS Hooks request with an empty draftOrders Bundle → no ServiceRequest → 400.
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-garbage-1", []byte(`{"context":{"draftOrders":{"resourceType":"Bundle","type":"collection","entry":[]}}}`))
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "parse order-select failed")
	})

	t.Run("inconsistent-patient", func(t *testing.T) {
		// SR subject MBR-001, Coverage beneficiary MBR-OTHER → three-way fence rejects.
		srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", "Patient/MBR-001")
		covJSON, _ := BuildCoverageWithPayer("Patient/MBR-OTHER", "Coverage/MBR-OTHER", CMSPayerIdentity)
		req, _ := BuildConformantOrderSelectRequest(srJSON, covJSON, "Patient/MBR-001")
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-inconsist-1", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "inconsistent patient in order-select")
	})
}

// ---- TestResponder_PASSubmit ----

// TestResponder_PASSubmit proves the conformant pas-claim dispatch: approve / pend / deny +
// the rejection rows (malformed bundle → 400; adjudicator error → 422).
func TestResponder_PASSubmit(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	t.Run("approve", func(t *testing.T) {
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "pas-approve-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-approve-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		res, err := parsePASOutcome(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("parsePASOutcome: %v", err)
		}
		if res.Outcome != "approved" {
			t.Errorf("outcome = %q, want approved", res.Outcome)
		}
		if res.PreAuthRef == "" {
			t.Error("PreAuthRef empty, want a pre-auth reference")
		}
	})

	t.Run("pend", func(t *testing.T) {
		// PriorSurgery without a DiagnosticReport → sandbox pends (FR-20).
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8, PriorSurgery: true}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "pas-pend-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-pend-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		res, err := parsePASOutcome(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("parsePASOutcome: %v", err)
		}
		if res.Outcome != "pended" {
			t.Errorf("outcome = %q, want pended", res.Outcome)
		}
	})

	t.Run("deny", func(t *testing.T) {
		// Conservative therapy < 6 weeks → sandbox denies.
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 4}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "pas-deny-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-deny-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		res, err := parsePASOutcome(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("parsePASOutcome: %v", err)
		}
		if res.Outcome != "denied" {
			t.Errorf("outcome = %q, want denied", res.Outcome)
		}
	})

	t.Run("malformed-bundle", func(t *testing.T) {
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-garbage-1", []byte("not json"))
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "parse bundle failed")
	})

	t.Run("subject-mismatch-403", func(t *testing.T) {
		// valid exchange − one mutation → reject: Claim.patient = MBR-001 but ServiceRequest +
		// QuestionnaireResponse subject = MBR-OTHER → the intra-bundle bind rejects 403 (a QR for
		// a different patient must not approve THIS Claim). A hand-built bundle since the builder
		// owns consistent refs.
		mismatch := []byte(`{"resourceType":"Bundle","type":"collection","entry":[
			{"resource":{"resourceType":"Claim","patient":{"reference":"Patient/MBR-001"}}},
			{"resource":{"resourceType":"ServiceRequest","subject":{"reference":"Patient/MBR-OTHER"}}},
			{"resource":{"resourceType":"QuestionnaireResponse","subject":{"reference":"Patient/MBR-OTHER"}}}
		]}`)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-mismatch-1", mismatch)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusForbidden, "inconsistent patient in PAS bundle")
	})

	t.Run("qr-missing-subject-403", func(t *testing.T) {
		// A QR present but subjectless → reject (a subjectless QR could approve a Claim for a
		// different patient). Mirrors the deleted bindBundleSubject's REQUIRED-QR-subject arm.
		noSubjQR := []byte(`{"resourceType":"Bundle","type":"collection","entry":[
			{"resource":{"resourceType":"Claim","patient":{"reference":"Patient/MBR-001"}}},
			{"resource":{"resourceType":"ServiceRequest","subject":{"reference":"Patient/MBR-001"}}},
			{"resource":{"resourceType":"QuestionnaireResponse","status":"completed"}}
		]}`)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-noqrsubj-1", noSubjQR)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusForbidden, "PAS bundle QuestionnaireResponse missing subject")
	})

	t.Run("adjudicator-error-422", func(t *testing.T) {
		// An Adjudicator whose PriorAuth returns an error → the handler maps it to 422 with the
		// error text (mirrors the substrate gateway). Uses a dedicated error-returning adjudicator
		// (the sandbox only errors on un-buildable QR JSON, which the builder rejects first).
		_, errSrv := h.makeResponderSrv(t, responderIdent, &errPriorAuthAdjudicator{})
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "pas-err-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-err-1", bundle)
		resp := postInbound(t, errSrv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusUnprocessableEntity, "adjudication unavailable")
	})
}

// errPriorAuthAdjudicator returns an error from PriorAuth (the 422 path); the other methods
// mirror the sandbox so non-PAS legs still work if ever invoked.
type errPriorAuthAdjudicator struct{}

func (errPriorAuthAdjudicator) Eligibility(_ string) (bool, string) { return true, "" }
func (errPriorAuthAdjudicator) OrderSelect(cpt string) (bool, string) {
	return cpt == "72148", QuestionnaireCanonicalLumbarMRI
}
func (errPriorAuthAdjudicator) Questionnaire(_ string) ([]byte, bool) {
	return SandboxLumbarQuestionnaire(), true
}
func (errPriorAuthAdjudicator) PriorAuth(_ []byte, _ bool) (PASDecision, error) {
	return PASDecision{}, errAdjudicationUnavailable
}

// ---- TestResponder_PASUpdate ----

// buildConformantUpdate builds a conformant amended-re-POST bundle adding a DiagnosticReport +
// Provenance(target=DR) for member, related to origCorr (the original submit correlation).
func buildConformantUpdate(t *testing.T, member, updateCorr, origCorr string, qrJSON []byte, now time.Time) []byte {
	t.Helper()
	patientRef := "Patient/" + member
	coverageRef := "Coverage/" + member
	srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", patientRef)
	drJSON, err := BuildDiagnosticReport("dr-"+member, patientRef, "72148", "operative report")
	if err != nil {
		t.Fatalf("BuildDiagnosticReport: %v", err)
	}
	// Provenance target is rewritten by the builder to the bundle-local DR id; pass a placeholder.
	provJSON, err := BuildProvenance("DiagnosticReport/dr-"+member, "Organization/provider", now)
	if err != nil {
		t.Fatalf("BuildProvenance: %v", err)
	}
	bundle, err := BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{
		QR:               qrJSON,
		SR:               srJSON,
		PatientRef:       patientRef,
		CoverageRef:      coverageRef,
		Provenance:       provJSON,
		DiagnosticReport: drJSON,
		Corr:             updateCorr,
		OriginalCorr:     origCorr,
		Created:          now,
		Payer:            CMSPayerIdentity,
	})
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle: %v", err)
	}
	return bundle
}

// TestResponder_PASUpdate proves the conformant pas-claim-update dispatch end-to-end: a submit
// pends (recording the ledger), then a conformant amended re-POST (adding the operative DR +
// Provenance) approves — and the key FR-21/FR-32 rejection rows.
func TestResponder_PASUpdate(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	// The QR that pends on submit (PriorSurgery, no DR) and approves on update (DR added).
	pendQR := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8, PriorSurgery: true}, h.now)

	t.Run("happy-pend-then-approve", func(t *testing.T) {
		origCorr := "upd-orig-1"
		// 1. Submit → pend (records the ledger under subject|origCorr).
		submit := buildConformantClaim(t, "MBR-001", origCorr, pendQR, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", origCorr, submit)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		res, _ := parsePASOutcome(h.openResponse(t, body))
		if res.Outcome != "pended" {
			t.Fatalf("submit outcome = %q, want pended", res.Outcome)
		}

		// 2. Update → approve (related to origCorr; adds DR + Provenance(target=DR)).
		update := buildConformantUpdate(t, "MBR-001", "upd-amend-1", origCorr, pendQR, h.now)
		envBytes2, hubHdr2 := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "upd-amend-1", update)
		resp2 := postInbound(t, srv, envBytes2, hubHdr2)
		body2 := readBody(t, resp2)
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("update status = %d, want 200; body: %s", resp2.StatusCode, body2)
		}
		res2, err := parsePASOutcome(h.openResponse(t, body2))
		if err != nil {
			t.Fatalf("parsePASOutcome (update): %v", err)
		}
		if res2.Outcome != "approved" {
			t.Errorf("update outcome = %q, want approved", res2.Outcome)
		}
	})

	t.Run("no-pending-claim-409", func(t *testing.T) {
		// An update referencing a corr that was never pended → 409 (ledger.begin fails).
		update := buildConformantUpdate(t, "MBR-001", "upd-amend-2", "upd-never-pended", pendQR, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "upd-amend-2", update)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
	})

	t.Run("missing-related-403", func(t *testing.T) {
		// A conformant submit bundle (no Claim.related) sent on the update leg → 403 (FR-21).
		submit := buildConformantClaim(t, "MBR-001", "upd-norel-1", pendQR, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", "upd-norel-1", submit)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusForbidden, "ClaimUpdate missing original-claim reference (Claim.related)")
	})
}

// ---- TestResponder_FullPAChain ----

// TestResponder_FullPAChain drives a conformant CRD request AND a conformant PAS-submit (approve)
// through the SAME Responder server, asserting a PA-required CRD cards response and an approved
// ClaimResponse — the hermetic, in-process analog of a full provider->payer PA round-trip against
// this Responder (the integration that exercises the conformant Responder PA cases end to end).
func TestResponder_FullPAChain(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	// LEG 1 — CRD order-select → PA-required cards.
	crdReq := buildConformantCRD(t, "MBR-COVERED", "72148")
	crdEnv, crdHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "feed-crd-1", crdReq)
	crdResp := postInbound(t, srv, crdEnv, crdHdr)
	crdBody := readBody(t, crdResp)
	if crdResp.StatusCode != http.StatusOK {
		t.Fatalf("CRD status = %d, want 200; body: %s", crdResp.StatusCode, crdBody)
	}
	cov, err := ParseCards(h.openResponse(t, crdBody))
	if err != nil {
		t.Fatalf("ParseCards: %v", err)
	}
	if !cov.PARequired() || !cov.NeedsDTR() {
		t.Fatalf("CRD cards: PARequired=%v NeedsDTR=%v, want both true", cov.PARequired(), cov.NeedsDTR())
	}

	// LEG 3 — PAS submit (approve). (LEG 2 DTR is a separate dispatch already covered by
	// TestResponder_DTR; the PA chain's core legs are crd-order-select + pas-submit.)
	qr := answeredQR(t, "MBR-COVERED", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	pasBundle := buildConformantClaim(t, "MBR-COVERED", "feed-pas-1", qr, h.now)
	pasEnv, pasHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "feed-pas-1", pasBundle)
	pasResp := postInbound(t, srv, pasEnv, pasHdr)
	pasBody := readBody(t, pasResp)
	if pasResp.StatusCode != http.StatusOK {
		t.Fatalf("PAS status = %d, want 200; body: %s", pasResp.StatusCode, pasBody)
	}
	res, err := parsePASOutcome(h.openResponse(t, pasBody))
	if err != nil {
		t.Fatalf("parsePASOutcome: %v", err)
	}
	if res.Outcome != "approved" {
		t.Errorf("PAS outcome = %q, want approved", res.Outcome)
	}
}
