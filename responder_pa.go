package shnsdk

import (
	"net/http"
	"strings"
	"time"
)

// responder_pa.go — the Responder's PA-chain inbound handlers (crd-order-select / pas-claim /
// pas-claim-update), in their CONFORMANT form. The Originator (RunPriorAuth) sends the conformant
// request shapes — a CDS Hooks order-select request (context.draftOrders a FHIR collection Bundle)
// and a conformant Da Vinci Claim Bundle — and these handlers parse them via the conformant parsers
// (conformant_parse.go), mirroring the substrate engine's crd_native.go / pas_native.go binds. The
// Responder is the published partner payer surface: a partner running RunPriorAuth against an
// shnsdk.Responder payer exercises this full CRD -> DTR -> PAS round-trip.
//
// Two documented divergences from the substrate gateway carry over from the eligibility handler
// (see handleEligibility's NOTEs): no patient-registry PCI binding (the SDK Responder has no
// registry; the inbound token is authz-signed AND ciphertext-hash-bound, and the originator
// re-verifies the response token against its own expected subject) and no per-request FHIR
// $validate (runtime conformance is a property of operated edges; a partner-run responder is the
// partner's edge — the response SHAPE is still parity-pinned against the substrate builders).

// handleCRD implements the conformant crd-order-select handler. It extracts the ServiceRequest
// from the conformant CDS Hooks request, runs the three-way member fence (SR subject + Coverage
// beneficiary + context.patientId — mirrors engine.conformantCRDBind), reads the CPT, and asks
// the Adjudicator whether PA is required + which questionnaire applies.
func (r *Responder) handleCRD(w http.ResponseWriter, plaintext []byte) handlerResult {
	srJSON, ok := parseConformantOrderSelectSR(plaintext)
	if !ok {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}

	// Three-way member consistency — SR subject, Coverage beneficiary, context.patientId all
	// reference the same patient (bare member, "Patient/" stripped). Mirrors conformantCRDBind.
	srSubjectRef, err := ParseServiceRequestSubject(srJSON)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}
	covJSON, ctxMember, ok := conformantOrderSelectCoverageAndPatient(plaintext)
	if !ok {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}
	covBeneRef, err := ParseCoverageBeneficiary(covJSON)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}
	srMember := strings.TrimPrefix(srSubjectRef, "Patient/")
	covMember := strings.TrimPrefix(covBeneRef, "Patient/")
	if srMember != covMember || srMember != strings.TrimPrefix(ctxMember, "Patient/") {
		respondErr(w, http.StatusBadRequest, "inconsistent patient in order-select")
		return handlerResult{}
	}

	// SCOPE BOUNDARY (deferral D-PCB-1): the SDK Responder is CPT-only by design. HCPCS personas are
	// handled by the gateway/sandbox path, not here. Do not route a HCPCS persona
	// through this Responder without generalizing this parse to
	// ParseServiceRequestProductCoding — it would 400 here.
	cpt, err := ParseServiceRequestCPT(srJSON)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse CPT failed")
		return handlerResult{}
	}

	paRequired, canonical := r.cfg.Adjudicator.OrderSelect(cpt)
	cov := CardCoverage{Covered: CoveredCovered}
	if paRequired {
		cov.PANeeded, cov.Questionnaires = PANeededAuthNeeded, []string{canonical}
	} else {
		cov.PANeeded = PANeededNoAuth
	}
	cardsJSON, err := BuildCards(cov)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "build cards failed")
		return handlerResult{}
	}
	return handlerResult{payload: cardsJSON}
}

// handlePASSubmit implements the conformant pas-claim handler (FR-21). It parses the conformant
// Claim Bundle, adjudicates from the QR + DR-presence, and builds the approve/pend/deny response.
// On pend the ledger record is deferred to commit (runs only after seal+authorize succeed),
// the same commit-after-seal ordering the gateway uses across the handler/pipeline split.
func (r *Responder) handlePASSubmit(w http.ResponseWriter, plaintext []byte, tok Token, corr string, now time.Time) handlerResult {
	cs, ok := parseConformantClaimSubmit(plaintext)
	if !ok {
		respondErr(w, http.StatusBadRequest, "parse bundle failed")
		return handlerResult{}
	}
	if status, msg := bindConformantClaimSubject(cs); status != 0 {
		respondErr(w, status, msg)
		return handlerResult{}
	}

	dec, err := r.cfg.Adjudicator.PriorAuth(cs.qrJSON, cs.hasDR)
	if err != nil {
		respondErr(w, http.StatusUnprocessableEntity, err.Error())
		return handlerResult{}
	}

	switch dec.Outcome {
	case PASPended:
		pendedJSON, err := BuildPendedResponse(cs.claimPatient, corr, dec.NeededItems, now)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, "build pended response failed")
			return handlerResult{}
		}
		// Ledger ordering — commit records the pend AFTER seal+authorize succeed:
		// a response-leg failure leaves no orphan pended entry. No rollback needed (record is
		// the acquiring step). The provider retries and gets a fresh pended response (record is
		// idempotent on the same subject+corr key).
		subject := tok.Subject
		return handlerResult{
			payload: pendedJSON,
			commit:  func() { r.ledger.record(subject, corr) },
		}

	case PASApproved:
		crJSON, err := BuildClaimResponse(dec.PreAuthRef, dec.ValidUntil, cs.claimPatient, corr, now)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, "build claim response failed")
			return handlerResult{}
		}
		return handlerResult{payload: crJSON}

	default: // PASDenied
		rationale := dec.DenyReason
		if rationale == "" {
			rationale = "Conservative therapy of at least 6 weeks is not documented (4 weeks on record); request does not meet the payer's medical-necessity policy for advanced lumbar imaging."
		}
		denJSON, err := BuildDeniedResponse(cs.claimPatient, corr, rationale, now)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, "build denied response failed")
			return handlerResult{}
		}
		return handlerResult{payload: denJSON}
	}
}

// handlePASUpdate implements the conformant pas-claim-update handler (FR-21/FR-32). It mirrors
// engine.conformantPASUpdateBind's FR-32 enforcement (Provenance present + agent + targets the
// supplemental resource) and the deleted minimized handler's ledger discipline (begin/release/
// finalize the pended claim atomically), porting both to the conformant Claim Bundle shape.
//
// Ledger ordering: finalize runs in commit (after seal+authorize succeed); a response-leg failure
// runs rollback (release) — no stranded-approved claim (the gateway's commit-after-seal discipline).
func (r *Responder) handlePASUpdate(w http.ResponseWriter, plaintext []byte, tok Token, corr string, now time.Time) handlerResult {
	cs, ok := parseConformantClaimSubmit(plaintext)
	if !ok {
		respondErr(w, http.StatusBadRequest, "parse bundle failed")
		return handlerResult{}
	}
	// Bind subject across the WHOLE bundle BEFORE the pend lock (mirrors the gateway: a
	// wrong-subject bundle is rejected 403 before the atomic ledger is touched).
	if status, msg := bindConformantClaimSubject(cs); status != 0 {
		respondErr(w, status, msg)
		return handlerResult{}
	}
	facts, ok := parseConformantUpdateFacts(plaintext)
	if !ok {
		respondErr(w, http.StatusBadRequest, "parse bundle failed")
		return handlerResult{}
	}

	// FR-21: RelatedClaim (Claim.related) is required for a ClaimUpdate.
	if facts.relatedClaim == "" {
		respondErr(w, http.StatusForbidden, "ClaimUpdate missing original-claim reference (Claim.related)")
		return handlerResult{}
	}

	// ATOMIC test-and-set: only one update can be in flight for a given pended claim. RelatedClaim
	// is the original submit's correlation id — the invisible coupling that lets the update find
	// the pend the submit recorded.
	if !r.ledger.begin(tok.Subject, facts.relatedClaim) {
		respondErr(w, http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
		return handlerResult{}
	}

	// claimed: release the just-begun claim on any guard-failure return below.
	fail := func(status int, msg string) handlerResult {
		r.ledger.release(tok.Subject, facts.relatedClaim)
		respondErr(w, status, msg)
		return handlerResult{}
	}

	// FR-32: a ClaimUpdate MUST carry Provenance attributing the supplemental data, with an agent
	// targeting the EXACT supplemental resource in this bundle (mirrors conformantPASUpdateBind).
	if facts.provenanceJSON == nil {
		return fail(http.StatusForbidden, "ClaimUpdate missing Provenance")
	}
	if len(facts.provenanceAgents) == 0 {
		return fail(http.StatusForbidden, "ClaimUpdate Provenance missing agent")
	}
	var wantTarget string
	if facts.hasDR {
		if facts.diagnosticReportID == "" {
			return fail(http.StatusForbidden, "supplemental DiagnosticReport missing id")
		}
		wantTarget = "DiagnosticReport/" + facts.diagnosticReportID
	} else {
		if facts.qrID == "" {
			return fail(http.StatusForbidden, "supplemental QuestionnaireResponse missing id")
		}
		wantTarget = "QuestionnaireResponse/" + facts.qrID
	}
	targeted := false
	for _, ref := range facts.provenanceTargets {
		if ref == wantTarget {
			targeted = true
			break
		}
	}
	if !targeted {
		return fail(http.StatusForbidden, "ClaimUpdate Provenance does not target the supplemental data")
	}

	dec, err := r.cfg.Adjudicator.PriorAuth(cs.qrJSON, cs.hasDR)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, err.Error())
	}
	if dec.Outcome != PASApproved {
		// Still insufficient: release returns the claim to pended so a later, complete amendment
		// can still transition it.
		return fail(http.StatusUnprocessableEntity, "amendment still insufficient")
	}

	crJSON, err := BuildClaimResponse(dec.PreAuthRef, dec.ValidUntil, cs.claimPatient, corr, now)
	if err != nil {
		return fail(http.StatusInternalServerError, "build claim response failed")
	}

	// Return commit+rollback so the pipeline controls the finalize/release timing:
	//   commit:   finalize runs AFTER seal+authorize succeed.
	//   rollback: a response-leg failure releases the claim back to pended (no stranded-approved).
	subject := tok.Subject
	relatedClaim := facts.relatedClaim
	return handlerResult{
		payload:  crJSON,
		commit:   func() { r.ledger.finalize(subject, relatedClaim) },
		rollback: func() { r.ledger.release(subject, relatedClaim) },
	}
}
