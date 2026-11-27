package shnsdk

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// responder_pa.go — the Responder's PA-chain inbound handlers (crd-order-select / pas-claim /
// pas-claim-update), in their CONFORMANT form. The Originator (RunPriorAuth) sends the conformant
// request shapes — a CDS Hooks order-select request (context.draftOrders a FHIR collection Bundle)
// and a conformant Da Vinci Claim Bundle — and these handlers parse them via the conformant parsers
// (conformant_parse.go), mirroring the Smart Gateway's own native CRD / PAS binds. The
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
// beneficiary + context.patientId), reads the procedure code,
// asks the Adjudicator whether PA is required and which questionnaire applies, and answers with
// BuildCRDResponse: the requested order returned in an update system action carrying the
// coverage information, and no card. The prefetch coverage may be a Coverage or a Bundle of
// Coverages for the patient; one coverage-information is written per Coverage.
//
// claimedContract is the inbound request-frame claim (handleInbound step 7, "" when the
// request arrived bare or unclaimed) — currently unread; see handleInbound's RECEIVER
// OBLIGATION comment for why.
func (r *Responder) handleCRD(plaintext []byte, now time.Time, claimedContract string) (out handlerResult) {
	srJSON, ok := parseConformantOrderSelectSR(plaintext)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse order-select failed"}
	}

	// Three-way member consistency — SR subject, Coverage beneficiary, context.patientId all
	// reference the same patient. Mirrors conformantCRDBind: a Patient reference is read by
	// FHIR's reference grammar (any base, versioned or not), and context.patientId as a
	// Patient's id (crdContextPatientID).
	srSubjectRef, err := ParseServiceRequestSubject(srJSON)
	if err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse order-select failed"}
	}
	covJSON, ctxMember, ok := conformantOrderSelectCoverageAndPatient(plaintext)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse order-select failed"}
	}
	covBeneRef, err := ParseCoverageBeneficiary(covJSON)
	if err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse order-select failed"}
	}
	member, noPatient := crdContextPatientID(ctxMember)
	if noPatient {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: msgRequestNamesNoOne}
	}
	var hook struct {
		FHIRServer string `json:"fhirServer"`
	}
	_ = json.Unmarshal(plaintext, &hook)
	base := strings.TrimRight(hook.FHIRServer, "/")
	// same reports whether a reference names the request's patient, as a payer's Smart Gateway
	// reads it. An absolute reference names the EHR's patient only on the EHR's own server
	// (fhirServer); on any other it names a resource there. A relative Patient reference is read
	// by FHIR's reference grammar, versioned or not. One that only looks like a reference to a
	// patient ("urn:Patient/<id>") names a patient this cannot know: it is not refused, as the
	// gateway below strict carries it. OrderSelect decides by the procedure code alone, never by
	// the patient, so there is nothing to hand it.
	same := func(resource []byte, ref string) bool {
		// A blank one ("Patient/", "Patient/ ", relative or on the EHR's own server) is the
		// blank member, which names no one.
		blank := ref
		if rest, ok := strings.CutPrefix(ref, base+"/"); ok && strings.Contains(base, "://") {
			blank = rest
		}
		if rest, ok := strings.CutPrefix(blank, "Patient/"); ok && memberNamesNoOne(rest) {
			return rest == member
		}
		// A request whose context.patientId names no one names its patient by nothing this
		// can read: an unreadable subject is not the request's patient then.
		if pasUnreadablePatientRef(ref) {
			return !memberNamesNoOne(member)
		}
		// A "#<id>" naming the one contained Patient of the resource names its member identifier.
		if strings.HasPrefix(ref, "#") {
			var r struct {
				Contained []any `json:"contained"`
			}
			if json.Unmarshal(resource, &r) == nil {
				if m, ok := patientRefMember(ref, containedObjects(r.Contained)); ok {
					return m == member
				}
			}
			return false
		}
		relative := ref
		if strings.Contains(ref, "://") {
			rest, ok := strings.CutPrefix(ref, base+"/")
			if base == "" || !strings.Contains(base, "://") || !ok {
				return false
			}
			relative = rest
		}
		if id, _, ok := readPatientReference(relative); ok && strings.HasPrefix(relative, "Patient/") {
			return id == member
		}
		return relative == member
	}
	if !same(srJSON, srSubjectRef) || !same(covJSON, covBeneRef) {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "inconsistent patient in order-select"}
	}
	// A request that names its patient nowhere (context.patientId blank, and its subjects blank
	// too) is refused as a payer's Smart Gateway refuses it at every level.
	if memberNamesNoOne(member) {
		if ctxMember == "" {
			return handlerResult{appStatus: http.StatusBadRequest, errMsg: "missing context.patientId"}
		}
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: msgRequestNamesNoOne}
	}

	// Procedure coding is system-agnostic here (CPT or HCPCS): ParseServiceRequestProductCoding
	// accepts either allowlisted system (FR-36), so a HCPCS-ordering partner (e.g. an
	// Originator built with ProcedureSystem set to HCPCS) round-trips through this Responder
	// the same as a CPT order. Adjudicator.OrderSelect's parameter is an opaque procedure code —
	// it was never actually CPT-typed, only ever CPT-fed; closing this parse-side gap needed no
	// interface change (Adjudicator growth stays additive-only; see its doc comment).
	_, code, _, err := ParseServiceRequestProductCoding(srJSON)
	if err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse order procedure coding failed: " + err.Error()}
	}
	_, orderID := extractResourceTypeAndID(srJSON)
	if orderID == "" {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "draft order has no id"}
	}
	coverageRefs, ok := coverageReferences(covJSON)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "coverage has no id"}
	}

	paRequired, canonical := r.cfg.Adjudicator.OrderSelect(code)
	// The Adjudicator has answered: every return from here on is its answer, or
	// this Responder refusing it (answeredBySystem).
	defer answeredBySystem(&out)
	assertion := CoverageAssertion{
		ID:            r.newAssertionID(),
		Order:         "ServiceRequest/" + orderID,
		Coverage:      coverageRefs[0],
		ProcedureCode: code,
		PARequired:    paRequired,
	}
	var infos []CoverageInformationInput
	for _, ref := range coverageRefs {
		ci := CoverageInformationInput{
			Coverage:            ref,
			Covered:             CoveredCovered,
			PANeeded:            PANeededNoAuth,
			Date:                now.UTC().Format("2006-01-02"),
			CoverageAssertionID: assertion.ID,
		}
		if paRequired {
			ci.PANeeded = PANeededAuthNeeded
			if canonical != "" {
				ci.DocNeeded = []string{"clinical"}
				ci.Questionnaires = []string{canonical}
				assertion.Questionnaire = canonical
			}
		}
		infos = append(infos, ci)
	}
	answer, err := BuildCRDResponse("2.0", CRDResponseInputs{Orders: []CRDOrderCoverage{{
		Order:       srJSON,
		Description: "Add coverage information to ServiceRequest",
		Coverage:    infos,
	}}})
	if err != nil {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build CRD response failed"}
	}
	// A CDS Hooks response is JSON, not a FHIR resource.
	res := handlerResult{payload: answer, contentType: "application/json"}
	if rec, ok := r.cfg.Adjudicator.(CoverageAssertionRecorder); ok {
		res.commit = func() { rec.RecordCoverageAssertion(assertion) }
	}
	return res
}

// coverageReferences returns "Coverage/<id>" for the prefetch Coverage, or for each Coverage in
// a prefetch Bundle. ok is false when a Coverage has no id or there is none.
func coverageReferences(covJSON []byte) ([]string, bool) {
	entries, isBundle, err := coverageBundleEntries(covJSON)
	if err != nil {
		return nil, false
	}
	if !isBundle {
		_, id := extractResourceTypeAndID(covJSON)
		if id == "" {
			return nil, false
		}
		return []string{"Coverage/" + id}, true
	}
	var refs []string
	for _, e := range entries {
		if e.head.ResourceType != "Coverage" {
			continue
		}
		if e.head.ID == "" {
			return nil, false
		}
		refs = append(refs, "Coverage/"+e.head.ID)
	}
	return refs, len(refs) > 0
}

// crdContextPatientID reads a CDS Hooks context.patientId, which is a Patient's id, not a
// reference, as a payer's Smart Gateway reads it: the one relative "Patient/<id>", with no
// version, is read as its id; a value that holds no "/" is the id as written; any other value
// names no Patient by its structure ("Patient/1/x", "Patient/1/_history/2", "Group/1"), so the
// request names no one. A blank one is the blank member.
func crdContextPatientID(patientID string) (member string, noPatient bool) {
	rest, relative := strings.CutPrefix(patientID, "Patient/")
	if memberNamesNoOne(rest) {
		return rest, false
	}
	if id, _, ok := readPatientReference(patientID); ok && relative && strings.Count(patientID, "/") == 1 {
		return id, false
	}
	if relative || strings.Contains(patientID, "/") {
		return "", true
	}
	return patientID, false
}

// answerGraphRefusal is the refusal of a PAS request whose graph this Responder's answer
// cannot restate. Every answer carries the request's resources as sent, and is assembled only
// when every reference among them resolves in the request and every subject is the Claim's
// patient (buildPASOperationResponse): a request carrying a reference that resolves to nothing,
// such as one that only looks like a reference to a patient ("urn:Patient/<id>"), cannot be
// answered. It is checked before the Adjudicator is asked, so the Adjudicator never decides a
// request this Responder cannot answer, and the request is refused 400 on its structure.
func answerGraphRefusal(request []byte, now time.Time) (handlerResult, bool) {
	// What the answer restates first, with its own refusal (answerRequest).
	if _, refused, ok := answerRequest(request); !ok {
		return refused, true
	}
	if _, err := buildPASOperationResponse(request, []byte(`{"resourceType":"ClaimResponse","id":"graph-check"}`), now); err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "incomplete PAS request graph"}, true
	}
	return handlerResult{}, false
}

// priorAuth adjudicates a PAS submission or ClaimUpdate: with PriorAuthSubmission
// when the Adjudicator implements PASSubmissionAdjudicator, so it sees the request's
// unreadable patient references, else with PriorAuth.
func (r *Responder) priorAuth(cs conformantClaimSubmit, unreadable []string) (PASDecision, error) {
	if s, ok := r.cfg.Adjudicator.(PASSubmissionAdjudicator); ok {
		return s.PriorAuthSubmission(PASSubmission{
			QuestionnaireResponse:       cs.qrJSON,
			HasDiagnosticReport:         cs.hasDR,
			Patient:                     cs.claimPatient,
			UnreadablePatientReferences: unreadable,
		})
	}
	return r.cfg.Adjudicator.PriorAuth(cs.qrJSON, cs.hasDR)
}

// handlePASSubmit implements the conformant pas-claim handler (FR-21). It parses the conformant
// Claim Bundle, adjudicates from the QR + DR-presence, and builds the approve/pend/deny response.
// On pend the ledger record is deferred to commit (runs only after seal+authorize succeed),
// the same commit-after-seal ordering the gateway uses across the handler/pipeline split.
//
// claimedContract is the inbound request-frame claim (handleInbound step 7, "" when the
// request arrived bare or unclaimed) — currently unread; see handleInbound's RECEIVER
// OBLIGATION comment for why.
func (r *Responder) handlePASSubmit(plaintext []byte, tok Token, corr string, now time.Time, claimedContract string) (out handlerResult) {
	cs, ok := parseConformantClaimSubmit(plaintext)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse bundle failed"}
	}
	status, msg, unreadable := bindConformantClaimSubject(cs)
	if status != 0 {
		return handlerResult{appStatus: status, errMsg: msg}
	}

	if refusal, refused := answerGraphRefusal(plaintext, now); refused {
		return refusal
	}
	dec, err := r.priorAuth(cs, unreadable)
	// The Adjudicator has answered: every return from here on is its answer, or
	// this Responder refusing it (answeredBySystem).
	defer answeredBySystem(&out)
	if err != nil {
		return adjudicatorError(err)
	}

	if msg := decisionNotesMisplaced(dec); msg != "" {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: msg}
	}
	switch dec.Outcome {
	case PASPended:
		pendedJSON, res := r.pendedAnswer(plaintext, cs.claimPatient, corr, dec, now)
		if pendedJSON == nil {
			return res
		}
		// Ledger ordering — commit records the pend AFTER seal+authorize succeed:
		// a response-leg failure leaves no orphan pended entry. No rollback needed (record is
		// the acquiring step). The provider retries and gets a fresh pended response (record is
		// idempotent on the same subject+corr key).
		subject := tok.Subject
		return handlerResult{
			payload:     pendedJSON,
			contentType: fhirJSON,
			commit:      func() { r.ledger.record(subject, corr) },
		}

	case PASApproved:
		return r.approvedAnswer(plaintext, cs.claimPatient, corr, dec, now)

	default: // PASDenied
		return r.deniedAnswer(plaintext, cs.claimPatient, corr, dec, now)
	}
}

// fhirJSON is the FHIR JSON media type.
const fhirJSON = "application/fhir+json"

// answerRequest reads what an answer restates from the request it answers: the
// request Claim's type and insurer, as written. A request that lacks either is
// refused (400): the answer would otherwise state a value no one supplied.
func answerRequest(request []byte) (PASAnswerRequest, handlerResult, bool) {
	req, err := PASAnswerRequestOf(request)
	if err == nil {
		err = req.check()
	}
	if err != nil {
		return PASAnswerRequest{}, handlerResult{appStatus: http.StatusBadRequest, errMsg: "the request Claim states no type or insurer for the answer to restate"}, false
	}
	return req, handlerResult{}, true
}

// approvedAnswer is the approved PAS response for the request.
func (r *Responder) approvedAnswer(request []byte, patient, corr string, dec PASDecision, now time.Time) handlerResult {
	req, refused, ok := answerRequest(request)
	if !ok {
		return refused
	}
	crJSON, err := BuildClaimResponseExact(dec.PreAuthRef, dec.ValidUntil, patient, corr, req, now)
	if err != nil {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build claim response failed"}
	}
	crJSON, err = buildPASOperationResponse(request, crJSON, now)
	if err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "incomplete PAS request graph"}
	}
	return handlerResult{payload: crJSON, contentType: fhirJSON}
}

// deniedAnswer is the denied PAS response for the request: the adjudicator's
// own reason and notes, or none.
func (r *Responder) deniedAnswer(request []byte, patient, corr string, dec PASDecision, now time.Time) handlerResult {
	req, refused, ok := answerRequest(request)
	if !ok {
		return refused
	}
	denJSON, err := BuildDeniedResponseWithNotesAtLineExact("2.0", patient, corr, dec.DenyReason, dec.ProcessNotes, req, now)
	if err != nil {
		// The builder's error names only a note's position and type code.
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build denied response failed: " + err.Error()}
	}
	denJSON, err = buildPASOperationResponse(request, denJSON, now)
	if err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "incomplete PAS request graph"}
	}
	return handlerResult{payload: denJSON, contentType: fhirJSON}
}

// pendedAnswer builds the pended PAS response for the request at PAS 2.0 from
// the decision's Task facts. It returns nil and the refusal when the decision
// lacks a fact the Task requires; the Responder never supplies one.
func (r *Responder) pendedAnswer(request []byte, patient, corr string, dec PASDecision, now time.Time) ([]byte, handlerResult) {
	if len(dec.PendedItems) == 0 {
		msg := "a pended decision names no PendedItems"
		if len(dec.NeededItems) > 0 {
			msg = "a pended decision names its PendedItems; the deprecated NeededItems does not say whether an item is an attachment or a questionnaire"
		}
		return nil, handlerResult{appStatus: http.StatusInternalServerError, errMsg: msg}
	}
	claimURL, ok := firstClaimFullURL(request)
	if !ok {
		return nil, handlerResult{appStatus: http.StatusBadRequest, errMsg: "incomplete PAS request graph"}
	}
	req, refused, ok := answerRequest(request)
	if !ok {
		return nil, refused
	}
	payerURL := dec.PayerURL
	if payerURL == "" {
		payerURL = r.cfg.PublicBaseURL
	}
	pendedJSON, err := BuildPendedClaimResponseAtLineExact("2.0", PendedResponseInputs{
		Correlation: corr,
		Created:     now,
		Task: PendedTaskInputs{
			Identifier: dec.TaskIdentifier,
			Status:     dec.TaskStatus,
			Patient:    patient,
			Claim:      claimURL,
			Requester:  dec.TaskRequester,
			Owner:      dec.TaskOwner,
			PayerURL:   payerURL,
			AuthoredOn: now,
			Items:      dec.PendedItems,
		},
		Request: req,
	})
	if err != nil {
		return nil, handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build pended response failed: " + err.Error()}
	}
	pendedJSON, err = buildPASOperationResponse(request, pendedJSON, now)
	if err != nil {
		return nil, handlerResult{appStatus: http.StatusBadRequest, errMsg: "incomplete PAS request graph"}
	}
	return pendedJSON, handlerResult{}
}

// firstClaimFullURL returns the fullUrl of the request Bundle's first Claim
// entry.
func firstClaimFullURL(bundle []byte) (string, bool) {
	var b struct {
		Entry []struct {
			FullURL  string `json:"fullUrl"`
			Resource struct {
				ResourceType string `json:"resourceType"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal(bundle, &b) != nil {
		return "", false
	}
	for _, e := range b.Entry {
		if e.Resource.ResourceType == "Claim" {
			return e.FullURL, e.FullURL != ""
		}
	}
	return "", false
}

// handlePASUpdate implements the conformant pas-claim-update handler (FR-21/FR-32). It enforces
// FR-32 (Provenance present + agent + targets the supplemental resource) and the pended-claim
// ledger discipline (begin/release/finalize the pended claim atomically) on the conformant Claim
// Bundle shape.
//
// Ledger ordering: the decision's ledger step (finalize on approval or denial, release on a
// re-pend) runs in commit, after seal+authorize succeed; a response-leg failure runs rollback
// (release) — no stranded-decided claim (the gateway's commit-after-seal discipline).
//
// claimedContract is the inbound request-frame claim (handleInbound step 7, "" when the
// request arrived bare or unclaimed) — currently unread; see handleInbound's RECEIVER
// OBLIGATION comment for why.
func (r *Responder) handlePASUpdate(plaintext []byte, tok Token, corr string, now time.Time, claimedContract string) (out handlerResult) {
	cs, ok := parseConformantClaimSubmit(plaintext)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse bundle failed"}
	}
	// Bind subject across the WHOLE bundle BEFORE the pend lock (mirrors the gateway: a
	// wrong-subject bundle is rejected 403 before the atomic ledger is touched).
	status, msg, unreadable := bindConformantClaimSubject(cs)
	if status != 0 {
		return handlerResult{appStatus: status, errMsg: msg}
	}
	facts, ok := parseConformantUpdateFacts(plaintext)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse bundle failed"}
	}

	// FR-21: RelatedClaim (Claim.related) is required for a ClaimUpdate.
	if facts.relatedClaim == "" {
		return handlerResult{appStatus: http.StatusForbidden, errMsg: "ClaimUpdate missing original-claim reference (Claim.related)"}
	}

	// ATOMIC test-and-set: only one update can be in flight for a given pended claim. RelatedClaim
	// is the original submit's correlation id — the invisible coupling that lets the update find
	// the pend the submit recorded.
	if !r.ledger.begin(tok.Subject, facts.relatedClaim) {
		return handlerResult{appStatus: http.StatusConflict, errMsg: "ClaimUpdate references no pending claim available for this patient"}
	}

	// claimed: release the just-begun claim on any guard-failure return below, BEFORE
	// returning the app-error result (commit/rollback stay nil on an app error — the
	// pipeline never runs them, so the release must happen here).
	fail := func(status int, msg string) handlerResult {
		r.ledger.release(tok.Subject, facts.relatedClaim)
		return handlerResult{appStatus: status, errMsg: msg}
	}

	// FR-32: a ClaimUpdate MUST carry Provenance attributing the supplemental data, with an agent
	// targeting the EXACT supplemental resource in this bundle (mirrors conformantPASUpdateBind).
	if facts.provenanceJSON == nil {
		return fail(http.StatusForbidden, "ClaimUpdate missing Provenance")
	}
	if len(facts.provenanceAgents) == 0 {
		return fail(http.StatusForbidden, "ClaimUpdate Provenance missing agent")
	}
	wantType, wantID, wantVersion, wantURL := "QuestionnaireResponse", facts.qrID, facts.qrVersion, facts.qrFullURL
	if facts.hasDR {
		if facts.diagnosticReportID == "" {
			return fail(http.StatusForbidden, "supplemental DiagnosticReport missing id")
		}
		wantType, wantID, wantVersion, wantURL = "DiagnosticReport", facts.diagnosticReportID, facts.diagnosticReportVersion, facts.diagnosticReportFullURL
	} else if facts.qrID == "" {
		return fail(http.StatusForbidden, "supplemental QuestionnaireResponse missing id")
	}
	targeted := false
	for _, ref := range facts.provenanceTargets {
		// ProvenanceTargetNames is the rule: the relative "<type>/<id>" or exactly the
		// supplemental entry's own fullUrl (in whatever form that fullUrl takes, the absolute
		// one a requester that writes references in absolute form gives it included), either
		// one version-specific (".../_history/<version>") only at the resource's own
		// meta.versionId. A reference under another base names another server's resource,
		// whatever its path ends in, so it does not attribute this one.
		//
		// The twin of the Smart Gateway's conformantPASUpdateBind (its native PAS bind), whose
		// update Provenance check reads a target by the same rule from shn-gateway v1.8.0. It is
		// a content check there: not run at none, recorded at observe and structural, refused
		// only at strict. Earlier gateways read a target by its spelling, matching exactly
		// "<type>/<id>" or any reference ending in "/<type>/<id>", and no version-specific one,
		// so they differ from this Responder at strict on a target under another base that ends
		// in "/<type>/<id>", a version-specific one at the resource's own meta.versionId, and one
		// equal to the entry's own fullUrl that does not end in "/<type>/<id>" (a urn:uuid: one,
		// say).
		if ProvenanceTargetNames(ref, wantURL, wantType, wantID, wantVersion) {
			targeted = true
			break
		}
	}
	if !targeted {
		return fail(http.StatusForbidden, "ClaimUpdate Provenance does not target the supplemental data")
	}

	if refusal, refused := answerGraphRefusal(plaintext, now); refused {
		return fail(refusal.appStatus, refusal.errMsg)
	}
	dec, err := r.priorAuth(cs, unreadable)
	// The Adjudicator has answered: every return from here on is its answer, or
	// this Responder refusing it (answeredBySystem).
	defer answeredBySystem(&out)
	if err != nil {
		r.ledger.release(tok.Subject, facts.relatedClaim)
		return adjudicatorError(err)
	}
	if msg := decisionNotesMisplaced(dec); msg != "" {
		return fail(http.StatusInternalServerError, msg)
	}

	// The amendment is answered with the adjudicator's own decision, whatever
	// it is. The commit runs after seal+authorize succeed; a response-leg
	// failure releases the claim back to pended.
	//   approved: finalize — the claim is decided and a replayed update finds
	//             nothing.
	//   denied:   finalize — a denied claim is never re-pended.
	//   pended:   release — the claim stays pended for a later amendment.
	var res handlerResult
	subject := tok.Subject
	relatedClaim := facts.relatedClaim
	finalize := func() { r.ledger.finalize(subject, relatedClaim) }
	release := func() { r.ledger.release(subject, relatedClaim) }
	switch dec.Outcome {
	case PASApproved:
		res = r.approvedAnswer(plaintext, cs.claimPatient, corr, dec, now)
		res.commit = finalize
	case PASPended:
		pendedJSON, refusal := r.pendedAnswer(plaintext, cs.claimPatient, corr, dec, now)
		if pendedJSON == nil {
			res = refusal
		} else {
			res = handlerResult{payload: pendedJSON, contentType: fhirJSON, commit: release}
		}
	default: // PASDenied
		res = r.deniedAnswer(plaintext, cs.claimPatient, corr, dec, now)
		res.commit = finalize
	}
	if res.appStatus != 0 {
		release()
		return handlerResult{appStatus: res.appStatus, errMsg: res.errMsg}
	}
	res.rollback = release
	return res
}

// decisionNotesMisplaced reports a decision whose process notes the Responder
// cannot carry: notes ride on denials only, and a decision that sets them with
// another outcome is refused rather than having them dropped.
func decisionNotesMisplaced(dec PASDecision) string {
	if len(dec.ProcessNotes) != 0 && dec.Outcome != PASDenied {
		return "process notes are carried on denials only"
	}
	return ""
}

// handlePASInquire serves a PAS inquiry (pas-claim-inquire). The request is a
// collection Bundle whose first Claim (use preauthorization) names a Patient
// entry of the same Bundle; a Coverage entry, when present, must be for that
// Patient. The Adjudicator's Inquire decision is answered like a submit's.
func (r *Responder) handlePASInquire(plaintext []byte, corr string, now time.Time) (out handlerResult) {
	inq, status, msg := parsePASInquiry(plaintext)
	if status != 0 {
		return handlerResult{appStatus: status, errMsg: msg}
	}
	inquirer, ok := r.cfg.Adjudicator.(InquiryAdjudicator)
	if !ok {
		return handlerResult{appStatus: http.StatusNotImplemented, errMsg: "prior-authorization inquiries are not served by this payer"}
	}
	if refusal, refused := answerGraphRefusal(plaintext, now); refused {
		return refusal
	}
	dec, err := inquirer.Inquire(inq)
	// The Adjudicator has answered: every return from here on is its answer, or
	// this Responder refusing it (answeredBySystem).
	defer answeredBySystem(&out)
	if err != nil {
		return adjudicatorError(err)
	}
	if msg := decisionNotesMisplaced(dec); msg != "" {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: msg}
	}
	switch dec.Outcome {
	case PASApproved:
		return r.approvedAnswer(plaintext, inq.Patient, corr, dec, now)
	case PASPended:
		pendedJSON, refusal := r.pendedAnswer(plaintext, inq.Patient, corr, dec, now)
		if pendedJSON == nil {
			return refusal
		}
		return handlerResult{payload: pendedJSON, contentType: fhirJSON}
	default:
		return r.deniedAnswer(plaintext, inq.Patient, corr, dec, now)
	}
}

// parsePASInquiry reads an inquiry request; a non-zero status is the refusal.
func parsePASInquiry(body []byte) (PASInquiry, int, string) {
	bad := func(msg string) (PASInquiry, int, string) { return PASInquiry{}, http.StatusBadRequest, msg }
	var b struct {
		ResourceType string `json:"resourceType"`
		Type         string `json:"type"`
		Entry        []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
			Request  json.RawMessage `json:"request"`
		} `json:"entry"`
	}
	if json.Unmarshal(body, &b) != nil || b.ResourceType != "Bundle" || b.Type != "collection" {
		return bad("parse inquiry bundle failed")
	}
	inq := PASInquiry{Bundle: body}
	type head struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	var claim json.RawMessage
	byURL := map[string]json.RawMessage{}
	for _, e := range b.Entry {
		var h head
		if json.Unmarshal(e.Resource, &h) != nil {
			return bad("parse inquiry bundle failed")
		}
		if len(e.Request) > 0 {
			return bad("inquiry bundle entries carry no request")
		}
		byURL[e.FullURL] = e.Resource
		if h.ResourceType == "Claim" && claim == nil {
			claim = e.Resource
		}
	}
	if claim == nil {
		return bad("inquiry bundle has no Claim")
	}
	var cl struct {
		Use        string                       `json:"use"`
		Identifier []PASIdentifier              `json:"identifier"`
		Extension  []map[string]json.RawMessage `json:"extension"`
		Patient    struct {
			Reference string `json:"reference"`
		} `json:"patient"`
		Item []struct {
			Sequence         int                          `json:"sequence"`
			Extension        []map[string]json.RawMessage `json:"extension"`
			ProductOrService struct {
				Coding []PASCoding `json:"coding"`
			} `json:"productOrService"`
			ServicedDate string `json:"servicedDate"`
		} `json:"item"`
	}
	if json.Unmarshal(claim, &cl) != nil || cl.Use != "preauthorization" {
		return bad("inquiry Claim is not a preauthorization")
	}
	inq.Patient, inq.ClaimIdentifiers = cl.Patient.Reference, cl.Identifier
	// Claim.patient is read as a payer's Smart Gateway reads it, by FHIR's
	// reference grammar (inquiryClaimPatient), and refused with its answers,
	// before the Adjudicator is asked: no reference; a reference that names no
	// Patient, or names one by an id that is only whitespace, names no member.
	if cl.Patient.Reference == "" {
		return bad("PAS inquiry Claim missing patient")
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	member, containedPatient, noMember := inquiryClaimPatient(doc, cl.Patient.Reference)
	if noMember {
		return bad(msgPatientCarriesNoMember)
	}
	if memberNamesNoOne(member) {
		return bad(msgRequestNamesNoOne)
	}
	patientKeyRef := "Patient/" + member
	var patient json.RawMessage
	if containedPatient != nil {
		patient, _ = json.Marshal(containedPatient)
	}
	for url, res := range byURL {
		var h head
		_ = json.Unmarshal(res, &h)
		if h.ResourceType == "Patient" && (url == cl.Patient.Reference || inquiryReferenceKey(doc, url) == patientKeyRef) {
			patient = res
		}
	}
	if patient == nil {
		return PASInquiry{}, http.StatusForbidden, "inquiry Claim patient is not in the bundle"
	}
	var p struct {
		Identifier []PASIdentifier `json:"identifier"`
	}
	_ = json.Unmarshal(patient, &p)
	// A blank identifier names no member, so it is no member the inquiry
	// names: it is left out, never handed on as one (the Bundle carries it as
	// sent).
	for _, id := range p.Identifier {
		if !memberNamesNoOne(id.Value) {
			inq.MemberIdentifiers = append(inq.MemberIdentifiers, id)
		}
	}
	seen := map[string]bool{}
	for _, res := range byURL {
		var cov struct {
			ResourceType string `json:"resourceType"`
			Beneficiary  struct {
				Reference string `json:"reference"`
			} `json:"beneficiary"`
		}
		if json.Unmarshal(res, &cov) != nil || cov.ResourceType != "Coverage" || inquiryReferenceKey(doc, cov.Beneficiary.Reference) == patientKeyRef {
			continue
		}
		// A beneficiary this cannot read names a patient it cannot know:
		// recorded for the Adjudicator, not refused. One that names another
		// patient by structure is, as bindConformantClaimSubject refuses one.
		if pasUnreadablePatientRef(cov.Beneficiary.Reference) {
			seen[cov.Beneficiary.Reference] = true
			continue
		}
		return PASInquiry{}, http.StatusForbidden, "inquiry Coverage is for another patient"
	}
	// And no resource it carries, contained or nested, is or names another
	// patient (pasOtherPatients), as the gateway reads the inquiry.
	another, others := pasOtherPatients(body, member)
	if another {
		return PASInquiry{}, http.StatusForbidden, "inconsistent patient in PAS inquiry"
	}
	inq.UnreadablePatientReferences = mergeUnreadable(seen, others)
	stringExt := func(exts []map[string]json.RawMessage, want string) string {
		for _, e := range exts {
			var url, v string
			_ = json.Unmarshal(e["url"], &url)
			if url == want && json.Unmarshal(e["valueString"], &v) == nil {
				return v
			}
		}
		return ""
	}
	inq.AuthorizationNumber = stringExt(cl.Extension, pasExtAuthorizationNumber)
	inq.AdministrationReferenceNumber = stringExt(cl.Extension, pasExtAdministrationReferenceNumber)
	for _, it := range cl.Item {
		item := PASInquiryItem{
			Sequence:                      it.Sequence,
			ServiceDate:                   it.ServicedDate,
			AuthorizationNumber:           stringExt(it.Extension, pasExtAuthorizationNumber),
			AdministrationReferenceNumber: stringExt(it.Extension, pasExtAdministrationReferenceNumber),
		}
		if len(it.ProductOrService.Coding) > 0 {
			item.ProductOrService = it.ProductOrService.Coding[0]
		}
		for _, e := range it.Extension {
			var url string
			_ = json.Unmarshal(e["url"], &url)
			if url == pasExtItemTraceNumber {
				_ = json.Unmarshal(e["valueIdentifier"], &item.TraceNumber)
			}
		}
		inq.Items = append(inq.Items, item)
	}
	return inq, 0, ""
}

// extractResourceTypeAndID reads resourceType + id from a FHIR resource JSON.
func extractResourceTypeAndID(resourceJSON []byte) (resourceType, id string) {
	var m struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(resourceJSON, &m); err != nil {
		return "", ""
	}
	return m.ResourceType, m.ID
}
