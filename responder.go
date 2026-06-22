package shnsdk

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Adjudicator is the partner-implemented decision surface behind a Responder.
// Growth is ADDITIVE ONLY: methods are added per operation; existing methods
// never change. B1 added Eligibility; B2 adds the PA chain.
type Adjudicator interface {
	Eligibility(memberID string) (covered bool, reason string)

	// OrderSelect decides whether the CPT on a draft order requires prior
	// authorization, and if so which DTR questionnaire canonical applies.
	OrderSelect(cpt string) (paRequired bool, questionnaireCanonical string)

	// Questionnaire returns the FHIR Questionnaire JSON for a canonical this
	// payer advertises via OrderSelect. ok=false → 400 "unknown questionnaire
	// canonical". (SandboxLumbarQuestionnaire serves the sandbox flow.)
	Questionnaire(canonical string) (questionnaireJSON []byte, ok bool)

	// PriorAuth adjudicates a PAS submission from the QuestionnaireResponse and
	// whether the bundle carried supplemental evidence. Used for BOTH the
	// initial submit and the ClaimUpdate re-adjudication. An error → 422 with
	// the error text (mirrors the substrate gateway).
	PriorAuth(qrJSON []byte, hasDiagnosticReport bool) (PASDecision, error)
}

// ResponderConfig wires a payer responder. Every field is REQUIRED except Clock
// and Client (defaulted) — NewResponder fails closed on anything missing.
type ResponderConfig struct {
	Identity        Identity
	AuthzURL        string
	AuthzPub        ed25519.PublicKey
	HubTransportPub ed25519.PublicKey
	ResolveEnc      func(holderID string) (*[32]byte, bool)
	Adjudicator     Adjudicator
	Clock           func() time.Time
	Client          *http.Client
}

// Responder serves a payer holder's /substrate/inbound with the SAME pipeline
// and error contract as the substrate's own gateway (PARTICIPANT_PROTOCOL.md
// §6.2/§6.2a; pinned by test/sdkparity responder vectors): X-Hub-Assertion
// FIRST (header only, before the body is read), then metadata, recipient,
// operation pin, bound-token verification (incl. ciphertext hash), open,
// adjudicate, seal-then-authorize the response leg (AI-2), respond
// synchronously.
type Responder struct {
	cfg    ResponderConfig
	jti    *ReplayGuard
	ledger *pendedLedger
}

// responderReqOp pins each TransactionType to the request operation the inbound
// token must carry. Unknown types → 400 before token work. Mirrors the gateway's PA
// leg catalog (gateway/engine/workstream_pa.go, paCatalog — the .Op field; only the
// four payer ops — federated-query and patient-dtr are facility/PHG roles, not payer).
// The SDK keeps its own copy because it is the published partner-facing surface and
// does not import the private gateway engine.
var responderReqOp = map[string]string{
	"coverage-eligibility":    "eligibility-inquiry",
	"crd-order-select":        "crd-order-select",
	"dtr-questionnaire-fetch": "dtr-questionnaire-fetch",
	"pas-claim":               "pas-submit",
	"pas-claim-update":        "pas-update-submit",
}

// NewResponder validates cfg, defaults Clock and Client, and returns a ready
// Responder. Every required field is checked with a distinct error so callers
// know exactly what is missing (fail-closed).
func NewResponder(cfg ResponderConfig) (*Responder, error) {
	if cfg.Identity.HolderID == "" {
		return nil, errors.New("shnsdk: ResponderConfig.Identity.HolderID is empty")
	}
	if len(cfg.Identity.SignPriv) == 0 {
		return nil, errors.New("responder: Identity.SignPriv required")
	}
	if cfg.Identity.EncPriv == nil {
		return nil, errors.New("responder: Identity.EncPriv required")
	}
	if cfg.Identity.EncPub == nil {
		return nil, errors.New("responder: Identity.EncPub required")
	}
	if cfg.AuthzURL == "" {
		return nil, errors.New("shnsdk: ResponderConfig.AuthzURL is empty")
	}
	if len(cfg.AuthzPub) == 0 {
		return nil, errors.New("shnsdk: ResponderConfig.AuthzPub is nil or empty")
	}
	if len(cfg.HubTransportPub) == 0 {
		return nil, errors.New("shnsdk: ResponderConfig.HubTransportPub is nil or empty (per-hop transport auth has no off state)")
	}
	if cfg.ResolveEnc == nil {
		return nil, errors.New("shnsdk: ResponderConfig.ResolveEnc is nil")
	}
	if cfg.Adjudicator == nil {
		return nil, errors.New("shnsdk: ResponderConfig.Adjudicator is nil")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Responder{cfg: cfg, jti: NewReplayGuard(MaxAssertionTTL, 1<<16), ledger: newPendedLedger()}, nil
}

// Handler returns a ServeMux with exactly POST /substrate/inbound wired to
// handleInbound.
func (r *Responder) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /substrate/inbound", r.handleInbound)
	return mux
}

// handlerResult is what a per-TransactionType handler returns to handleInbound.
// payload nil → the handler already wrote an error response (pipeline does nothing).
// commit runs AFTER the response leg seals + authorizes successfully (the ledger
// state mutation — pend record / update finalize — must not happen until the
// answer is actually produced: review-fixes-6 #1). rollback runs if the pipeline
// FAILS after the handler claimed ledger state (release a claimed update so the
// provider can retry). Both are nil for handlers that touch no ledger state.
type handlerResult struct {
	payload  []byte
	commit   func()
	rollback func()
}

// respondErr writes a JSON {"error": msg} body with the given HTTP status code.
func respondErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Ignore the write error — nothing useful to do on a broken connection.
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// sha256Hex returns hex(sha256(b)). Used to compute the per-leg payload hash
// (AI-2). Kept package-private; no public helper is needed.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// handleInbound is the EXACT inbound pipeline, matching internal/gateway
// handleInbound's error contract and check order (pinned by sdkparity vectors):
//
//  1. hop auth (header only, before body read)
//  2. body read + decode envelope
//  3. metadata guards (frame, corr)
//  4. recipient check
//  5. transaction-type → op pin
//  6. authz token unmarshal + VerifyBound
//  7. open
//  8. per-TransactionType handler
//  9. resolve sender enc key + seal
//  10. AI-2 authorize response leg + stamp token + encode + 200
func (r *Responder) handleInbound(w http.ResponseWriter, req *http.Request) {
	// Single clock read for hop-auth / VerifyBound / response building (mirrors
	// sampleparticipant's single-instant comment).
	now := r.cfg.Clock()

	// 1. Hop auth: verify X-Hub-Assertion FIRST, header only, before the body is
	//    read or the envelope decoded — an unauthenticated caller never reaches the
	//    decoder (§6.2a). Any failure → 403.
	jti, err := parseAndVerifyHubAssertion(
		req.Header.Get("X-Hub-Assertion"),
		r.cfg.Identity.HolderID,
		r.cfg.HubTransportPub,
		now,
	)
	if err != nil || r.jti.CheckAndRecord(jti, now) {
		respondErr(w, http.StatusForbidden, "missing or invalid hub assertion")
		return
	}

	// 2. Read + decode envelope.
	raw, err := io.ReadAll(io.LimitReader(req.Body, MaxRequestBytes))
	if err != nil {
		respondErr(w, http.StatusBadRequest, "read body failed")
		return
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "decode envelope failed")
		return
	}

	// 3. Metadata guards: require binding-critical fields before trusting them
	//    (empty frame/corr would skip VerifyBound's binding checks and produce
	//    audit records with empty correlation).
	if env.Metadata.AuthorityFrame == "" {
		respondErr(w, http.StatusBadRequest, "missing authority frame")
		return
	}
	if env.Metadata.CorrelationID == "" {
		respondErr(w, http.StatusBadRequest, "missing correlation id")
		return
	}

	// 4. Recipient check (cheap defence-in-depth; the bound authz token is the
	//    authority check — both are required).
	if env.Metadata.Recipient != r.cfg.Identity.HolderID {
		respondErr(w, http.StatusForbidden, "envelope not addressed to this holder")
		return
	}

	// 5. Map TransactionType → expected request op. Unknown types are rejected
	//    with 400 BEFORE token verification.
	op, ok := responderReqOp[env.Metadata.TransactionType]
	if !ok {
		respondErr(w, http.StatusBadRequest, "unknown transaction type")
		return
	}

	corr := env.Metadata.CorrelationID

	// 6. Authz token: unmarshal then VerifyBound (C2/H1).
	var tok Token
	if err := json.Unmarshal([]byte(env.Metadata.AuthzToken), &tok); err != nil {
		respondErr(w, http.StatusForbidden, "invalid authz token")
		return
	}
	if err := VerifyBound(tok, r.cfg.AuthzPub, now,
		"provider-tpo", op, corr, env.Metadata.Sender, "", sha256Hex(env.Ciphertext)); err != nil {
		respondErr(w, http.StatusForbidden, "authz verification failed")
		return
	}

	// 7. Open.
	plaintext, err := Open(env, r.cfg.Identity.EncPub, r.cfg.Identity.EncPriv)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "decryption failed")
		return
	}

	// 8. Dispatch per TransactionType. Each handler returns a handlerResult:
	//    payload nil → the handler already wrote an error response (pipeline does
	//    nothing). payload non-nil (contract: builders always return non-empty slices
	//    on success) → proceed to seal+authorize. commit/rollback manage ledger state
	//    transitions that must not happen until the response leg succeeds (review-fixes-6 #1).
	var res handlerResult

	switch env.Metadata.TransactionType {
	case "coverage-eligibility":
		res = r.handleEligibility(w, plaintext, tok, corr, now)
	case "crd-order-select":
		res = r.handleCRD(w, plaintext, tok, corr)
	case "dtr-questionnaire-fetch":
		res = r.handleDTR(w, plaintext)
	case "pas-claim":
		res = r.handlePASSubmit(w, plaintext, tok, corr, now)
	case "pas-claim-update":
		res = r.handlePASUpdate(w, plaintext, tok, corr, now)
	default:
		// Defensive: step 5 already rejects unknowns via responderReqOp, but
		// this hardens against a future responderReqOp edit.
		respondErr(w, http.StatusBadRequest, "unknown transaction type")
		return
	}
	if res.payload == nil {
		// Handler already wrote an error response.
		return
	}

	// 9. Resolve sender enc key + seal (AI-2: seal FIRST, then authorize).
	senderEncPub, ok := r.cfg.ResolveEnc(env.Metadata.Sender)
	if !ok {
		if res.rollback != nil {
			res.rollback()
		}
		respondErr(w, http.StatusBadGateway, "requester key not resolvable")
		return
	}
	respMeta := Metadata{
		Sender:          r.cfg.Identity.HolderID,
		Recipient:       env.Metadata.Sender,
		TransactionType: env.Metadata.TransactionType,
		AuthorityFrame:  "payer-coverage",
		Timestamp:       now.Format(time.RFC3339),
		CorrelationID:   corr,
	}
	respEnv, err := Seal(respMeta, res.payload, senderEncPub)
	if err != nil {
		if res.rollback != nil {
			res.rollback()
		}
		respondErr(w, http.StatusInternalServerError, "seal failed")
		return
	}

	// 10. AI-2 seal-then-authorize: authorize the response leg bound to
	//     sha256Hex(ciphertext), then stamp the token into the cleartext metadata.
	//     The response op is derived from the TransactionType (mirrors payer.go respondLeg).
	respOp := responseOp(env.Metadata.TransactionType)
	respTok, err := r.cfg.Identity.Authorize(req.Context(), r.cfg.Client, r.cfg.AuthzURL, AuthorizeRequest{
		Frame:         "payer-coverage",
		Operation:     respOp,
		SubjectPCI:    tok.Subject,
		CorrelationID: corr,
		PayloadHash:   sha256Hex(respEnv.Ciphertext),
	})
	if err != nil {
		if res.rollback != nil {
			res.rollback()
		}
		respondErr(w, http.StatusBadGateway, "authorize response leg failed")
		return
	}
	respTokJSON, err := json.Marshal(respTok)
	if err != nil {
		if res.rollback != nil {
			res.rollback()
		}
		respondErr(w, http.StatusInternalServerError, "marshal response token failed")
		return
	}
	respEnv.Metadata.AuthzToken = string(respTokJSON)

	out, err := EncodeEnvelope(respEnv)
	if err != nil {
		if res.rollback != nil {
			res.rollback()
		}
		respondErr(w, http.StatusInternalServerError, "encode failed")
		return
	}

	// Commit ledger state (pend record / finalize) AFTER seal+authorize succeed and
	// BEFORE writing the 200 — mirrors gateway review-fixes-6 #1 ordering across the
	// handler/pipeline split. The residual write-after-commit gap is the same
	// deferred-outbox gap the gateway documents.
	if res.commit != nil {
		res.commit()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// responseOp maps TransactionType → the response-leg operation name (mirrors
// payer.go respondLeg calls). The TransactionType is also used as the response
// envelope TransactionType (same as the request — the Hub echoes it back).
func responseOp(txType string) string {
	switch txType {
	case "coverage-eligibility":
		return "eligibility-response"
	case "crd-order-select":
		return "crd-cards"
	case "dtr-questionnaire-fetch":
		return "dtr-questionnaire"
	case "pas-claim":
		return "pas-response"
	case "pas-claim-update":
		return "pas-update-response"
	default:
		return ""
	}
}

// handleEligibility implements the coverage-eligibility handler. Returns a
// handlerResult with payload on success or writes an error and returns handlerResult{}.
//
// NOTE — divergence from the substrate gateway, by design: the gateway
// additionally resolves the payload's member against its patient registry
// and rejects when the derived PCI != tok.Subject (subject↔payload binding,
// H2a). The SDK responder has no patient registry, so that defense-in-depth
// layer is structurally unavailable here — and not load-bearing: the inbound
// token is authz-signed AND ciphertext-hash-bound, so subject/payload drift
// can only originate from the originator or authz itself, and the originator
// re-verifies the response token against its own expected subject.
//
// NOTE — second divergence, also by design: the substrate gateway runs
// runtime FHIR $validate on both the inbound request and the outbound
// response (and on CRD/DTR/PAS payloads); the SDK responder runs none.
// Runtime conformance is a property of gateways the operator runs
// (payload-blind Hub ⟹ conformance is enforced at operated edges); a
// partner-run responder is the partner's edge. The response SHAPE is still
// parity-pinned byte-for-byte against the substrate builder (test/sdkparity
// fhir parity), so a conformant resource is what this code produces — it is
// just not re-validated per request here.
func (r *Responder) handleEligibility(w http.ResponseWriter, plaintext []byte, tok Token, corr string, now time.Time) handlerResult {
	member, err := ParseEligibilityRequestMember(plaintext)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse member failed")
		return handlerResult{}
	}

	covered, reason := r.cfg.Adjudicator.Eligibility(member)
	crrJSON, err := BuildEligibilityResponse(corr, "Patient/"+member, covered, reason, now)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "build response failed")
		return handlerResult{}
	}
	return handlerResult{payload: crrJSON}
}

// handleCRD implements the crd-order-select handler. Mirrors payer.go
// handleCRDInbound guard order and error strings, minus the two documented
// divergences (no ResolvePatient/PCI binding, no $validate).
func (r *Responder) handleCRD(w http.ResponseWriter, plaintext []byte, tok Token, corr string) handlerResult {
	osReq, err := ParseOrderSelectRequest(plaintext)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}

	// Index 0 is guaranteed by ParseOrderSelectRequest.
	srJSON := []byte(osReq.Context.DraftOrders[0])

	// Parse SR subject; on failure, gate returns "parse order-select failed"
	// (mirrors gateway which wraps both SR and Coverage parse errors the same way).
	srSubjectRef, err := ParseServiceRequestSubject(srJSON)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}
	covBeneRef, err := ParseCoverageBeneficiary([]byte(osReq.Prefetch.Coverage))
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse order-select failed")
		return handlerResult{}
	}

	// H2: 3-way member consistency — SR subject, Coverage beneficiary, context.patientId
	// all must reference the same patient (bare member, "Patient/" stripped).
	srMember := strings.TrimPrefix(srSubjectRef, "Patient/")
	covMember := strings.TrimPrefix(covBeneRef, "Patient/")
	ctxMember := strings.TrimPrefix(osReq.Context.PatientID, "Patient/")
	if srMember != covMember || srMember != ctxMember {
		respondErr(w, http.StatusBadRequest, "inconsistent patient in order-select")
		return handlerResult{}
	}

	// NOTE — divergence from the substrate gateway, by design: the gateway
	// resolves srMember to a PCI via ResolvePatient and rejects when pci != tok.Subject.
	// The SDK responder has no patient registry; that defense-in-depth layer is
	// structurally unavailable here (same rationale as the eligibility NOTE above).

	cpt, err := ParseServiceRequestCPT(srJSON)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse CPT failed")
		return handlerResult{}
	}

	paRequired, canonical := r.cfg.Adjudicator.OrderSelect(cpt)
	cov := CardCoverage{Covered: "covered"}
	if paRequired {
		cov.PANeeded, cov.Questionnaires = "auth-needed", []string{canonical}
	} else {
		cov.PANeeded = "no-auth"
	}
	cardsJSON, err := BuildCards(cov)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "build cards failed")
		return handlerResult{}
	}
	return handlerResult{payload: cardsJSON}
}

// handleDTR implements the dtr-questionnaire-fetch handler. Mirrors payer.go
// handleDTRInbound guard order and error strings, minus the $validate divergence.
func (r *Responder) handleDTR(w http.ResponseWriter, plaintext []byte) handlerResult {
	var fetch QuestionnaireFetchRequest
	if err := json.Unmarshal(plaintext, &fetch); err != nil {
		respondErr(w, http.StatusBadRequest, "parse questionnaire fetch failed")
		return handlerResult{}
	}

	questionnaireJSON, ok := r.cfg.Adjudicator.Questionnaire(fetch.Canonical)
	if !ok {
		respondErr(w, http.StatusBadRequest, "unknown questionnaire canonical")
		return handlerResult{}
	}
	// §6.2: uniform leg shape — wrap the bare Questionnaire into a one-entry
	// $questionnaire-package collection Bundle (byte-identical to the substrate
	// gateway's buildQuestionnairePackage; test/sdkparity asserts parity). The
	// consumer (RunPriorAuth) extracts the bare Questionnaire on the far side.
	pkg, err := BuildQuestionnairePackage(questionnaireJSON)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "build questionnaire package failed")
		return handlerResult{}
	}
	return handlerResult{payload: pkg}
}

// bindBundleSubject enforces PAS bundle-internal patient consistency (H2/H3,
// FR-32 §5): the Claim patient, the SR, the QR, and (when present) the
// DiagnosticReport must all reference the SAME patient. The QR subject is
// REQUIRED — a subjectless QR could approve a Claim for a different patient.
// Returns (0,"") on success or (HTTP status, message) to write.
//
// NOTE — divergence from the substrate gateway: the gateway additionally
// resolves cb.ClaimPatient against the patient registry (ResolvePatient) and
// rejects when pci != tok.Subject. The SDK responder has no patient registry;
// that defense-in-depth layer is structurally unavailable here (same rationale
// as the eligibility/CRD NOTE above). ALL bundle-internal consistency checks
// ARE enforced.
func bindBundleSubject(cb ClaimBundle) (status int, msg string) {
	member := strings.TrimPrefix(cb.ClaimPatient, "Patient/")
	if cb.QRSubject == "" {
		return http.StatusForbidden, "PAS bundle QuestionnaireResponse missing subject"
	}
	if strings.TrimPrefix(cb.SRSubject, "Patient/") != member {
		return http.StatusForbidden, "inconsistent patient in PAS bundle"
	}
	if strings.TrimPrefix(cb.QRSubject, "Patient/") != member {
		return http.StatusForbidden, "inconsistent patient in PAS bundle"
	}
	if cb.HasDiagnosticReport && strings.TrimPrefix(cb.DiagnosticReportSubject, "Patient/") != member {
		return http.StatusForbidden, "inconsistent patient in PAS bundle"
	}
	return 0, ""
}

// handlePASSubmit implements the pas-claim handler (FR-21). Mirrors payer.go
// handlePASInbound guard order and error strings, minus the documented divergences.
// On pend: the ledger record is deferred to commit (runs only after seal+authorize
// succeed), faithful to gateway review-fixes-6 #1 across the handler/pipeline split.
func (r *Responder) handlePASSubmit(w http.ResponseWriter, plaintext []byte, tok Token, corr string, now time.Time) handlerResult {
	cb, err := ParseClaimBundle(plaintext)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse bundle failed")
		return handlerResult{}
	}

	if status, msg := bindBundleSubject(cb); status != 0 {
		respondErr(w, status, msg)
		return handlerResult{}
	}

	dec, err := r.cfg.Adjudicator.PriorAuth(cb.QRJSON, cb.HasDiagnosticReport)
	if err != nil {
		respondErr(w, http.StatusUnprocessableEntity, err.Error())
		return handlerResult{}
	}

	switch dec.Outcome {
	case PASPended:
		pendedJSON, err := BuildPendedResponse(cb.ClaimPatient, corr, dec.NeededItems, now)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, "build pended response failed")
			return handlerResult{}
		}
		// Ledger ordering — commit records the pend AFTER seal+authorize succeed,
		// faithful to gateway review-fixes-6 #1: a response-leg failure (502) leaves
		// no orphan pended entry. No rollback needed: record is the acquiring step (no
		// prior claimed state to undo). The provider retries and gets a fresh pended
		// response (record is idempotent on the same subject+corr key).
		subject := tok.Subject
		return handlerResult{
			payload: pendedJSON,
			commit:  func() { r.ledger.record(subject, corr) },
		}

	case PASApproved:
		crJSON, err := BuildClaimResponse(dec.PreAuthRef, dec.ValidUntil, cb.ClaimPatient, corr, now)
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
		denJSON, err := BuildDeniedResponse(cb.ClaimPatient, corr, rationale, now)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, "build denied response failed")
			return handlerResult{}
		}
		return handlerResult{payload: denJSON}
	}
}

// handlePASUpdate implements the pas-claim-update handler (FR-21). Mirrors
// payer.go handlePASUpdateInbound guard order and error strings exactly, minus
// the documented divergences.
//
// Ledger ordering: finalize runs in commit (after seal+authorize succeed); a
// response-leg failure runs rollback (release), restoring gateway review-fixes-6 #1
// across the handler/pipeline split — no stranded-approved claim.
func (r *Responder) handlePASUpdate(w http.ResponseWriter, plaintext []byte, tok Token, corr string, now time.Time) handlerResult {
	cb, err := ParseClaimBundle(plaintext)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse bundle failed")
		return handlerResult{}
	}

	// Bind subject across the WHOLE bundle BEFORE the pend lock (mirrors gateway:
	// wrong-subject token is rejected 403 before the atomic ledger is touched).
	if status, msg := bindBundleSubject(cb); status != 0 {
		respondErr(w, status, msg)
		return handlerResult{}
	}

	// FR-21: RelatedClaim (Claim.related) is required for a ClaimUpdate.
	if cb.RelatedClaim == "" {
		respondErr(w, http.StatusForbidden, "ClaimUpdate missing original-claim reference (Claim.related)")
		return handlerResult{}
	}

	// ATOMIC test-and-set: only one update can be in flight for a given pended claim.
	// RelatedClaim is the original submit's correlation id (Claim.related[0].claim.identifier.value)
	// — the invisible coupling that lets the update find the pend the submit recorded.
	if !r.ledger.begin(tok.Subject, cb.RelatedClaim) {
		// begin() failed — no claim was acquired; nothing to release.
		respondErr(w, http.StatusConflict, "ClaimUpdate references no pending claim available for this patient")
		return handlerResult{}
	}

	// claimed: release the just-begun claim on any guard-failure return below.
	fail := func(status int, msg string) handlerResult {
		r.ledger.release(tok.Subject, cb.RelatedClaim)
		respondErr(w, status, msg)
		return handlerResult{}
	}

	// FR-32: a ClaimUpdate MUST carry Provenance attributing the supplemental data.
	if cb.ProvenanceJSON == nil {
		return fail(http.StatusForbidden, "ClaimUpdate missing Provenance")
	}
	if len(cb.ProvenanceAgents) == 0 {
		return fail(http.StatusForbidden, "ClaimUpdate Provenance missing agent")
	}

	// Provenance must target the EXACT supplemental resource in this bundle.
	var wantTarget string
	if cb.HasDiagnosticReport {
		if cb.DiagnosticReportID == "" {
			return fail(http.StatusForbidden, "supplemental DiagnosticReport missing id")
		}
		wantTarget = "DiagnosticReport/" + cb.DiagnosticReportID
	} else {
		if cb.QRID == "" {
			return fail(http.StatusForbidden, "supplemental QuestionnaireResponse missing id")
		}
		wantTarget = "QuestionnaireResponse/" + cb.QRID
	}
	targeted := false
	for _, ref := range cb.ProvenanceTargets {
		if ref == wantTarget {
			targeted = true
			break
		}
	}
	if !targeted {
		return fail(http.StatusForbidden, "ClaimUpdate Provenance does not target the supplemental data")
	}

	dec, err := r.cfg.Adjudicator.PriorAuth(cb.QRJSON, cb.HasDiagnosticReport)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, err.Error())
	}
	if dec.Outcome != PASApproved {
		// Still insufficient: release returns the claim to pended so a later,
		// complete amendment can still transition it.
		return fail(http.StatusUnprocessableEntity, "amendment still insufficient")
	}

	crJSON, err := BuildClaimResponse(dec.PreAuthRef, dec.ValidUntil, cb.ClaimPatient, corr, now)
	if err != nil {
		return fail(http.StatusInternalServerError, "build claim response failed")
	}

	// Return commit+rollback so the pipeline controls the finalize/release timing.
	// commit: finalize runs AFTER seal+authorize succeed (review-fixes-6 #1) —
	//   the claim is removed only when the response is actually produced.
	// rollback: a response-leg failure (seal/authorize/encode error) releases the
	//   claim back to pended so the provider can retry — no stranded-approved claim.
	subject := tok.Subject
	relatedClaim := cb.RelatedClaim
	return handlerResult{
		payload:  crJSON,
		commit:   func() { r.ledger.finalize(subject, relatedClaim) },
		rollback: func() { r.ledger.release(subject, relatedClaim) },
	}
}
