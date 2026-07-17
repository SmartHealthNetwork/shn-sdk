package shnsdk

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

	// ResolveFrames returns the advertised messageFrames for a holder id (nil/empty
	// for unknown or legacy holders). OPTIONAL: nil means never frame (legacy-only
	// responder) — safe default for existing constructors. See NewFeedFrameResolver.
	ResolveFrames func(holderID string) []string
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
// Handlers no longer write errors themselves; they report the application answer
// and let handleInbound decide how to relay it (bare legacy vs sealed v1 frame).
//
// Contract:
//   - success ⇒ payload non-nil, appStatus 0 (relayed as frame(200,
//     application/fhir+json,…) to a capable requester, bare otherwise);
//   - app error ⇒ appStatus non-2xx + errMsg, and OPTIONALLY payload+contentType
//     for a FHIR error body (else handleInbound builds {"error":errMsg}).
//
// commit runs AFTER the response leg seals + authorizes successfully (the ledger
// state mutation — pend record / update finalize — must not happen until the
// answer is actually produced). rollback runs if the pipeline FAILS after the
// handler claimed ledger state (release a claimed update so the provider can
// retry). Both are nil for handlers that touch no ledger state, and stay nil on
// app-error results — a handler that claimed ledger state releases it before
// returning the error.
type handlerResult struct {
	payload     []byte
	appStatus   int
	errMsg      string
	contentType string
	commit      func()
	rollback    func()
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

	// Frame negotiation (spec 2026-07-17): frame the response leg iff the requester
	// advertises v1 — capability is two-sided (the responder only frames to a peer
	// that declared it can decode). nil ResolveFrames ⇒ never frame (legacy-only).
	framed := r.cfg.ResolveFrames != nil && SupportsMessageFrameV1(r.cfg.ResolveFrames(env.Metadata.Sender))

	// 8. Dispatch per TransactionType. Each handler returns a handlerResult carrying
	//    either a success payload (appStatus 0) or an application error (appStatus
	//    non-2xx + errMsg). commit/rollback manage ledger state transitions that must
	//    not happen until the response leg succeeds.
	var res handlerResult

	switch env.Metadata.TransactionType {
	case "coverage-eligibility":
		res = r.handleEligibility(plaintext, corr, now)
	case "crd-order-select":
		res = r.handleCRD(plaintext)
	case "dtr-questionnaire-fetch":
		res = r.handleDTR(plaintext)
	case "pas-claim":
		res = r.handlePASSubmit(plaintext, tok, corr, now)
	case "pas-claim-update":
		res = r.handlePASUpdate(plaintext, tok, corr, now)
	default:
		// Defensive: step 5 already rejects unknowns via responderReqOp, but
		// this hardens against a future responderReqOp edit.
		respondErr(w, http.StatusBadRequest, "unknown transaction type")
		return
	}

	// Relay decision. An application non-2xx is a real answer: a legacy requester
	// gets it bare (byte-identical to the pre-frame contract, so the payload-blind
	// Hub reports its generic mechanical failure); a capable requester gets it sealed
	// as a v1 frame carrying the app status, relayed 200-to-Hub — so seal/authorize
	// run for the framed error too (mirrors the engine's respondLegError). Success
	// (appStatus 0, payload non-nil) is framed(200, contentType) or bare.
	if res.appStatus != 0 && res.appStatus/100 != 2 {
		if !framed {
			respondErr(w, res.appStatus, res.errMsg) // pre-frame contract, byte-identical
			return
		}
		if res.payload == nil {
			res.payload, _ = json.Marshal(map[string]string{"error": res.errMsg})
			res.contentType = "application/json"
		}
	} else if res.payload == nil {
		// Defensive: a handler returned neither an answer nor an error.
		return
	}
	sealPayload := res.payload
	if framed {
		st := res.appStatus
		if st == 0 {
			st = http.StatusOK
		}
		ct := res.contentType
		if ct == "" {
			ct = "application/fhir+json"
		}
		var ferr error
		if sealPayload, ferr = EncodeHTTPFrame(st, ct, res.payload); ferr != nil {
			respondErr(w, http.StatusInternalServerError, "frame encode failed")
			return
		}
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
	respEnv, err := Seal(respMeta, sealPayload, senderEncPub)
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
	// BEFORE writing the 200 — the same commit-after-seal ordering the gateway uses
	// across the handler/pipeline split. The residual write-after-commit gap is the
	// same deferred-outbox gap the gateway documents.
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
// handlerResult with payload on success, or {appStatus,errMsg} on an app error.
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
func (r *Responder) handleEligibility(plaintext []byte, corr string, now time.Time) handlerResult {
	member, err := ParseEligibilityRequestMember(plaintext)
	if err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse member failed"}
	}

	covered, reason := r.cfg.Adjudicator.Eligibility(member)
	crrJSON, err := BuildEligibilityResponse(corr, "Patient/"+member, covered, reason, now)
	if err != nil {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build response failed"}
	}
	return handlerResult{payload: crrJSON}
}

// handleDTR implements the dtr-questionnaire-fetch handler. Mirrors payer.go
// handleDTRInbound guard order and error strings, minus the $validate divergence.
func (r *Responder) handleDTR(plaintext []byte) handlerResult {
	var fetch QuestionnaireFetchRequest
	if err := json.Unmarshal(plaintext, &fetch); err != nil {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "parse questionnaire fetch failed"}
	}

	questionnaireJSON, ok := r.cfg.Adjudicator.Questionnaire(fetch.Canonical)
	if !ok {
		return handlerResult{appStatus: http.StatusBadRequest, errMsg: "unknown questionnaire canonical"}
	}
	// §6.2: uniform leg shape — wrap the bare Questionnaire into a one-entry
	// $questionnaire-package collection Bundle (byte-identical to the substrate
	// gateway's buildQuestionnairePackage; test/sdkparity asserts parity). The
	// consumer (RunPriorAuth) extracts the bare Questionnaire on the far side.
	pkg, err := BuildQuestionnairePackage(questionnaireJSON)
	if err != nil {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build questionnaire package failed"}
	}
	return handlerResult{payload: pkg}
}
