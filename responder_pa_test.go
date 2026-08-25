package shnsdk

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	srJSON, err := BuildServiceRequest(cpt, "MRI lumbar spine without contrast", "M54.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	covJSON, err := BuildCoverageWithPayer(patientRef, member, CMSPayerIdentity)
	if err != nil {
		t.Fatalf("BuildCoverageWithPayer: %v", err)
	}
	req, err := BuildConformantOrderSelectRequest(srJSON, covJSON, patientRef)
	if err != nil {
		t.Fatalf("BuildConformantOrderSelectRequest: %v", err)
	}
	return req
}

// answeredQR builds an answered demo lumbar QR for the demo persona with the given clinical
// context (so a test can drive approve / pend by choosing the inputs).
func answeredQR(t *testing.T, member string, cc ClinicalContext, now time.Time) []byte {
	t.Helper()
	patientRef := "Patient/" + member
	coverageRef := "Coverage/" + member
	qrJSON, err := FillQuestionnaire(demoLumbarQuestionnaire(), cc, QRContext{
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
		MemberID:    member,
		Corr:        corr,
		Created:     now,
		Payer:       CMSPayerIdentity,
	})
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	return bundle
}

// buildConformantClaimAbsolute is buildConformantClaim in the REFERENCE-PAYER-CONFORMANT form —
// the shape RunPriorAuth now emits (PayerOrgEntry + AbsoluteRefs). Its point here is the
// reference spellings, not the payer Organization: Claim.patient comes out absolute while
// ServiceRequest.subject stays relative, because the latter is a patient-compartment anchor.
func buildConformantClaimAbsolute(t *testing.T, member, corr string, qrJSON []byte, now time.Time) []byte {
	t.Helper()
	patientRef := "Patient/" + member
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	bundle, err := BuildConformantClaimBundle(ConformantClaimInputs{
		QR:            qrJSON,
		SR:            srJSON,
		PatientRef:    patientRef,
		CoverageRef:   "Coverage/" + member,
		MemberID:      member,
		Corr:          corr,
		Created:       now,
		PayerOrgEntry: true,
		AbsoluteRefs:  true,
		Payer:         CMSPayerIdentity,
	})
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle(absolute): %v", err)
	}
	return bundle
}

// ---- TestResponder_CRD ----

// TestResponder_CRD proves the conformant crd-order-select dispatch: PA-required happy path +
// the rejection rows the deleted minimized test covered.
func TestResponder_CRD(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})

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
		if !cov.NeedsDTR() || cov.Questionnaires[0] != SupportedQuestionnaireCanonical {
			t.Errorf("questionnaire = %v, want %q", cov.Questionnaires, SupportedQuestionnaireCanonical)
		}
	})

	t.Run("no-pa-required", func(t *testing.T) {
		req := buildConformantCRD(t, "MBR-001", "99999") // a CPT the test policy does not gate
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

	t.Run("hcpcs-order-pa-required", func(t *testing.T) {
		// A HCPCS-system draft order round-trips through the Responder the same as CPT:
		// ParseServiceRequestProductCoding is system-agnostic (CPT or HCPCS), so the
		// procedure code reaches Adjudicator.OrderSelect regardless of which allowlisted
		// system it arrived on — the mirror image of the Originator's ProcedureSystem field.
		member, patientRef := "MBR-001", "Patient/MBR-001"
		srJSON, err := BuildServiceRequestCoded(systemHCPCS, "G0151", "Home health PT, each 15 minutes", "M54.16", patientRef)
		if err != nil {
			t.Fatalf("BuildServiceRequestCoded: %v", err)
		}
		covJSON, err := BuildCoverageWithPayer(patientRef, member, CMSPayerIdentity)
		if err != nil {
			t.Fatalf("BuildCoverageWithPayer: %v", err)
		}
		req, err := BuildConformantOrderSelectRequest(srJSON, covJSON, patientRef)
		if err != nil {
			t.Fatalf("BuildConformantOrderSelectRequest: %v", err)
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-hcpcs-1", req)
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
			t.Errorf("PARequired = false, want true (HCPCS G0151); cov=%+v", cov)
		}
		if !cov.NeedsDTR() || cov.Questionnaires[0] != SupportedQuestionnaireCanonical {
			t.Errorf("questionnaire = %v, want %q", cov.Questionnaires, SupportedQuestionnaireCanonical)
		}
	})

	t.Run("malformed-no-SR", func(t *testing.T) {
		// A CDS Hooks request with an empty draftOrders Bundle → no ServiceRequest → 400.
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-garbage-1", []byte(`{"context":{"draftOrders":{"resourceType":"Bundle","type":"collection","entry":[]}}}`))
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "parse order-select failed")
	})

	t.Run("unrecognized-code-system", func(t *testing.T) {
		// A code.coding present but on neither allowlisted system (CPT/HCPCS) is an honest
		// no-coding, not a wrong adjudication (FR-36 allowlist) — legible 400 naming the cause.
		srJSON, _ := BuildServiceRequestCoded("http://loinc.org", "12345-6", "not a procedure system", "M54.16", "Patient/MBR-001")
		covJSON, _ := BuildCoverageWithPayer("Patient/MBR-001", "MBR-001", CMSPayerIdentity)
		req, _ := BuildConformantOrderSelectRequest(srJSON, covJSON, "Patient/MBR-001")
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "crd-badsystem-1", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "parse order procedure coding failed: shnsdk: ServiceRequest has no {CPT,HCPCS} procedure coding")
	})

	t.Run("inconsistent-patient", func(t *testing.T) {
		// SR subject MBR-001, Coverage beneficiary MBR-OTHER → three-way fence rejects.
		srJSON, _ := BuildServiceRequest("72148", "MRI lumbar spine without contrast", "M54.16", "Patient/MBR-001")
		covJSON, _ := BuildCoverageWithPayer("Patient/MBR-OTHER", "MBR-OTHER", CMSPayerIdentity)
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
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})

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
		// PriorSurgery without a DiagnosticReport → the test policy pends (FR-20).
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
		// Conservative therapy < 6 weeks → the test policy denies.
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

	// ---- the fence compares MEMBER IDENTITY, not reference SPELLING ----
	//
	// A conformant PAS bundle legally carries one patient in two spellings: bundle-internal
	// references are absolutized so a real payer can resolve them, while the patient-compartment
	// anchors (ServiceRequest.subject, Coverage.beneficiary) deliberately stay relative so the
	// payer's `context Patient` retrieves still match. Claim.patient is absolute; SR.subject is
	// not. A fence that compared raw strings read that as two patients and refused a valid
	// bundle — which is exactly what took the separated-stack SDK-responder lane down.
	//
	// These rows pin BOTH halves: the valid mixed-spelling bundle is accepted, AND a genuinely
	// different member is still refused in every spelling combination. The second half is the
	// one that matters: tolerance of spelling must not become tolerance of a different patient.

	t.Run("mixed-spelling-same-patient-accepted", func(t *testing.T) {
		// The exact shape RunPriorAuth now puts on the wire: absolute Claim.patient +
		// QuestionnaireResponse.subject, relative ServiceRequest.subject, one member.
		qr := answeredQR(t, "MBR-001", DemoLumbarContext(), h.now)
		bundle := buildConformantClaimAbsolute(t, "MBR-001", "pas-abs-1", qr, h.now)
		if !strings.Contains(string(bundle), `"reference":"https://shn.example/fhir/Patient/MBR-001"`) {
			t.Fatalf("fixture is not exercising the absolute spelling — the row would pass for the wrong reason")
		}
		if !strings.Contains(string(bundle), `"reference":"Patient/MBR-001"`) {
			t.Fatalf("fixture carries no relative anchor — the row would pass for the wrong reason")
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-abs-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (one patient in two legal spellings is one patient); body: %s", resp.StatusCode, body)
		}
		res, err := parsePASOutcome(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("parsePASOutcome: %v", err)
		}
		if res.Outcome != "approved" {
			t.Errorf("outcome = %q, want approved", res.Outcome)
		}
	})

	// One row per spelling combination, each a DIFFERENT member on the ServiceRequest. None may
	// be accepted: if any is, the fence has stopped fencing.
	for _, tc := range []struct{ name, claimRef, srRef string }{
		{"absolute-claim-relative-sr", "https://shn.example/fhir/Patient/MBR-001", "Patient/MBR-OTHER"},
		{"relative-claim-absolute-sr", "Patient/MBR-001", "https://shn.example/fhir/Patient/MBR-OTHER"},
		{"both-absolute", "https://shn.example/fhir/Patient/MBR-001", "https://other.example/fhir/Patient/MBR-OTHER"},
		{"different-bases-same-mismatch", "https://a.example/fhir/Patient/MBR-001", "https://b.example/fhir/Patient/MBR-OTHER"},
	} {
		t.Run("mismatch-"+tc.name+"-403", func(t *testing.T) {
			mismatch := []byte(`{"resourceType":"Bundle","type":"collection","entry":[
				{"resource":{"resourceType":"Claim","patient":{"reference":"` + tc.claimRef + `"}}},
				{"resource":{"resourceType":"ServiceRequest","subject":{"reference":"` + tc.srRef + `"}}}
			]}`)
			envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-mismatch-"+tc.name, mismatch)
			resp := postInbound(t, srv, envBytes, hubHdr)
			assertError(t, resp, readBody(t, resp), http.StatusForbidden, "inconsistent patient in PAS bundle")
		})
	}

	t.Run("non-patient-reference-cannot-collide-403", func(t *testing.T) {
		// The identity read returns a reference with no "Patient/" segment verbatim, so a
		// Group/<member> subject can never be mistaken for Patient/<member> even though the ids
		// are identical. Without this row, a future "just take the last path segment" rewrite of
		// the extraction would look equivalent and silently open exactly that hole.
		mismatch := []byte(`{"resourceType":"Bundle","type":"collection","entry":[
			{"resource":{"resourceType":"Claim","patient":{"reference":"Patient/MBR-001"}}},
			{"resource":{"resourceType":"ServiceRequest","subject":{"reference":"Group/MBR-001"}}}
		]}`)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-mismatch-group", mismatch)
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
		// (the test policy only errors on un-buildable QR JSON, which the builder rejects first).
		_, errSrv := h.makeResponderSrv(t, responderIdent, &errPriorAuthAdjudicator{})
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "pas-err-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "pas-err-1", bundle)
		resp := postInbound(t, errSrv, envBytes, hubHdr)
		assertError(t, resp, readBody(t, resp), http.StatusUnprocessableEntity, "adjudication unavailable")
	})
}

// errPriorAuthAdjudicator returns an error from PriorAuth (the 422 path); the other methods
// mirror the PA lane so non-PAS legs still work if ever invoked.
type errPriorAuthAdjudicator struct{}

func (errPriorAuthAdjudicator) Eligibility(_ string) (bool, string) { return true, "" }
func (errPriorAuthAdjudicator) OrderSelect(cpt string) (bool, string) {
	return cpt == "72148", SupportedQuestionnaireCanonical
}
func (errPriorAuthAdjudicator) Questionnaire(_ string) ([]byte, bool) {
	return demoLumbarQuestionnaire(), true
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
		MemberID:         member,
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
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})

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

	// ---- the FR-32 supplemental-data fence matches the TARGET, not its SPELLING ----
	//
	// The reference-payer-conformant lane absolutizes bundle references, which rewrites
	// Provenance.target to its absolute fullUrl while the fence assembles the wanted target
	// from the bare resource id. Tolerating that is required (the acceptance half is proven
	// end-to-end by TestPriorAuthClientIntoResponder_PendedThenResumed, which drives the real
	// client's own amendment through this fence). These rows are the other half: a Provenance
	// that attributes something ELSE is still refused, in either spelling. Without them, the
	// tolerance could widen into "any target will do" and nothing would notice.

	for _, tc := range []struct{ name, target string }{
		{"relative-wrong-id", "DiagnosticReport/dr-SOMEONE-ELSE"},
		{"absolute-wrong-id", "https://shn.example/fhir/DiagnosticReport/dr-SOMEONE-ELSE"},
		{"right-id-wrong-type", "QuestionnaireResponse/dr-MBR-001"},
		{"suffix-lookalike-type", "https://shn.example/fhir/SupplementalDiagnosticReport/dr-MBR-001"},
	} {
		t.Run("provenance-targets-"+tc.name+"-403", func(t *testing.T) {
			// Each row pends its OWN claim first, so the refusal below is attributable to the
			// Provenance target and not to a missing pend (which answers 409, a different
			// guard) — and so the rows cannot interfere with each other through the ledger.
			origCorr := "upd-orig-prov-" + tc.name
			submit := buildConformantClaim(t, "MBR-001", origCorr, pendQR, h.now)
			sEnv, sHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", origCorr, submit)
			sResp := postInbound(t, srv, sEnv, sHdr)
			sBody := readBody(t, sResp)
			if sResp.StatusCode != http.StatusOK {
				t.Fatalf("setup submit status = %d, want 200; body: %s", sResp.StatusCode, sBody)
			}
			if res, _ := parsePASOutcome(h.openResponse(t, sBody)); res.Outcome != "pended" {
				t.Fatalf("setup submit outcome = %q, want pended", res.Outcome)
			}

			corr := "upd-prov-" + tc.name
			update := buildConformantUpdate(t, "MBR-001", corr, origCorr, pendQR, h.now)
			update = retargetProvenance(t, update, tc.target)
			envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim-update", "pas-update-submit", corr, update)
			resp := postInbound(t, srv, envBytes, hubHdr)
			assertError(t, resp, readBody(t, resp), http.StatusForbidden, "ClaimUpdate Provenance does not target the supplemental data")
		})
	}
}

// retargetProvenance rewrites the Provenance entry's target[0].reference — one mutation on top
// of a real builder's output, never a hand-typed bundle.
func retargetProvenance(t *testing.T, bundleJSON []byte, target string) []byte {
	t.Helper()
	var bundle map[string]any
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		t.Fatalf("retargetProvenance: unmarshal: %v", err)
	}
	entries, _ := bundle["entry"].([]any)
	done := false
	for _, e := range entries {
		em, _ := e.(map[string]any)
		res, _ := em["resource"].(map[string]any)
		if res == nil || res["resourceType"] != "Provenance" {
			continue
		}
		targets, _ := res["target"].([]any)
		if len(targets) == 0 {
			t.Fatal("retargetProvenance: Provenance carries no target[]")
		}
		t0, _ := targets[0].(map[string]any)
		t0["reference"] = target
		done = true
	}
	if !done {
		t.Fatal("retargetProvenance: bundle carries no Provenance entry")
	}
	out, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("retargetProvenance: marshal: %v", err)
	}
	return out
}

// ---- TestResponder_FullPAChain ----

// TestResponder_FullPAChain drives a conformant CRD request AND a conformant PAS-submit (approve)
// through the SAME Responder server, asserting a PA-required CRD cards response and an approved
// ClaimResponse — the hermetic, in-process analog of a full provider->payer PA round-trip against
// this Responder (the integration that exercises the conformant Responder PA cases end to end).
func TestResponder_FullPAChain(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &paTestAdjudicator{now: h.now})

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
