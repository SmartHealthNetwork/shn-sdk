package shnsdk

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

	// OrderSelect decides whether the procedure code on a draft order requires
	// prior authorization, and if so which DTR questionnaire canonical applies.
	// The code is opaque here (CPT or HCPCS — whatever allowlisted system the
	// draft order's ServiceRequest carried); the parameter name is historical,
	// kept unchanged to hold Adjudicator's additive-only growth rule.
	OrderSelect(cpt string) (paRequired bool, questionnaireCanonical string)

	// Questionnaire returns the FHIR Questionnaire JSON for a canonical this
	// payer advertises via OrderSelect. ok=false → 400 "unknown questionnaire
	// canonical". (A demo Adjudicator serves its own fixture questionnaire here;
	// shn-sdk no longer ships one as a published convenience.)
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

	// StampContractVersion opts this Responder into the contractVersion stamp (multi-version contracts design,
	// published-SDK parity — v0.38.0): every SUCCESS (2xx) framed answer gets
	// a FrameHeaderContractVersion header. The TOKEN is computed internally, per
	// leg, from the request's TransactionType (contractTokenForTxType — the SAME
	// mapping RunPriorAuth's originator side uses to build its expectedToken:
	// crd-order-select→pa.crd@2.0, dtr-questionnaire-fetch→pa.dtr@2.0,
	// pas-claim/pas-claim-update→pa.pas@2.0) rather than a caller-supplied
	// literal — this SDK Responder answers ALL of those legs behind ONE
	// ResponderConfig/Adjudicator (unlike the gateway, which the caller could
	// restrict to a single contract), so a single string value applied
	// uniformly across legs would stamp two of the three contract families
	// WRONG and break RunPriorAuth's own per-leg verification (unframeAnswer's
	// contractVersion check) against a Responder built with this SDK. coverage-eligibility
	// has no contract token (version-neutral, mirrors the gateway) and is NEVER
	// stamped regardless of this field. OPTIONAL: false (the default) preserves
	// today's behavior byte-for-byte — a success frame is sealed with no
	// contractVersion header, exactly as before v0.38.0. Mirrors the gateway's
	// Stamp rule: an app-error frame (respondLegError's sibling here) is NEVER stamped,
	// since it may relay bytes this build did not produce.
	StampContractVersion bool
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
// leg catalog (gateway/engine/workstream_pa.go, paCatalog — the .Op field) for the
// five types this Responder serves: federated-query and patient-dtr are facility/PHG
// roles, not payer, and crd-order-dispatch is a payer leg this Responder does not yet
// serve — both exclusions are pinned deliberately by the network-side lockstep
// conformance fence over ResponderTransactionOperations, so catalog growth cannot
// silently widen the gap. The SDK keeps its own copy because it is the published
// partner-facing surface and does not import the gateway engine.
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

	// RECEIVER OBLIGATION (request-frame contract, published-SDK parity —
	// v0.38.0): this build self-declares requestFrames v1 at registration
	// (RegistrationWithDeclared/Registration — SupportedRequestFrames defaults ON),
	// so it MUST accept BOTH framed and bare inbound requests. Decode-on-magic —
	// same argument as unframeAnswer: 0x00 cannot begin any bare payload this SDK
	// carries, so decoding is safe unconditionally, not gated on having advertised
	// the capability. Unlike the gateway's unframeRequest, this Responder does NOT
	// honor/validate the claim against a native∩laned set — it has no per-line
	// content negotiation (every PA-chain handler builds one fixed native line;
	// see ResponderConfig.StampContractVersion) — so the claim is surfaced to the
	// handler as an additive, currently-unread parameter (a reserved seam for a
	// future per-line-honoring responder — the gateway engine's respondLeg
	// documents the same "unread plumbing until a real consumer exists" precedent
	// for its own per-leg Content-Type field) rather than acted on. A corrupt
	// frame is the one rejection: a magic byte with a body that fails
	// DecodeHTTPFrame is 400, exactly like a corrupt envelope at step 2.
	var claimedContract string
	if IsFramed(plaintext) {
		hdr, body, ferr := DecodeHTTPFrame(plaintext)
		if ferr != nil {
			respondErr(w, http.StatusBadRequest, "request frame decode failed")
			return
		}
		claimedContract = hdr.Headers[FrameHeaderContractVersion]
		plaintext = body
	}

	// Frame negotiation (message-frame contract): frame the response leg iff the requester
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
		res = r.handleCRD(plaintext, claimedContract)
	case "dtr-questionnaire-fetch":
		res = r.handleDTR(plaintext, claimedContract)
	case "pas-claim":
		// R8 re-home (FR-16/FR-27): fence BEFORE dispatch, parity with the
		// substrate gateway's inbound/ingress fence — an unattested clinician/
		// patient QR item is nonconformant regardless of which handler would
		// otherwise run.
		if reason, ok := fenceAttestedItems(plaintext); !ok {
			respondErr(w, http.StatusForbidden, reason)
			return
		}
		res = r.handlePASSubmit(plaintext, tok, corr, now, claimedContract)
	case "pas-claim-update":
		// R8 re-home (FR-16/FR-27): same fence as pas-claim above — the
		// property belongs to any QR item, not only to amends.
		if reason, ok := fenceAttestedItems(plaintext); !ok {
			respondErr(w, http.StatusForbidden, reason)
			return
		}
		res = r.handlePASUpdate(plaintext, tok, corr, now, claimedContract)
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
		// contractVersion stamp (v0.38.0 parity): ONLY a success (2xx) frame is stamped, and
		// only when the deployment opted in (StampContractVersion) — false takes
		// the EXACT same EncodeHTTPFrame call as before this field existed
		// (byte-identical for every partner build that never sets it). The token
		// is this leg's own contract (contractTokenForTxType — "" for
		// coverage-eligibility, which is never stamped). An app-error frame is
		// never stamped (mirrors the gateway's respondLegError: it may relay
		// bytes this build did not produce).
		stampToken := contractTokenForTxType(env.Metadata.TransactionType)
		if r.cfg.StampContractVersion && stampToken != "" && st/100 == 2 {
			sealPayload, ferr = EncodeHTTPFrameHeaders(st, map[string]string{
				"Content-Type":             ct,
				FrameHeaderContractVersion: stampToken,
			}, res.payload)
		} else {
			sealPayload, ferr = EncodeHTTPFrame(st, ct, res.payload)
		}
		if ferr != nil {
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
	// R11: this standalone Responder is a DIFFERENT deployment from the
	// substrate gateway's promoted, Coverage-derived handler — it has no Coverage/SoR
	// of its own to derive a real payer identity from, so it threads the ZERO-VALUE
	// insurer (the occupant's existing data has nothing else to offer here), which
	// BuildEligibilityResponse recognizes and marshals as today's literal
	// Reference{Reference:"Organization/payer"} — byte-identical, unchanged behavior.
	crrJSON, err := BuildEligibilityResponse(corr, "Patient/"+member, covered, reason, PayerIdentifier{}, now)
	if err != nil {
		return handlerResult{appStatus: http.StatusInternalServerError, errMsg: "build response failed"}
	}
	return handlerResult{payload: crrJSON}
}

// handleDTR implements the dtr-questionnaire-fetch handler. Mirrors payer.go
// handleDTRInbound guard order and error strings, minus the $validate divergence.
//
// claimedContract is the inbound request-frame claim (handleInbound step 7,
// "" when the request arrived bare or unclaimed) — currently unread; see
// handleInbound's RECEIVER OBLIGATION comment for why.
func (r *Responder) handleDTR(plaintext []byte, claimedContract string) handlerResult {
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

// ---- FR-16/FR-27 attestation conformance fence (R8 re-home; parity with the
// substrate gateway's gateway/engine/attestfence.go) ----
//
// A QuestionnaireResponse item carrying the FR-17 source-attribution extension
// (informationOriginExt, source="manual") is either clinician-entered
// (author=Practitioner/…) or patient-reported (author=Patient/…): a
// clinician-entered item without a COMPLETE FR-16 clinician attestation
// extension (ClinicianAttestationExt: NPI + attestation text + date), or a
// patient-reported item without a COMPLETE FR-27 patient attestation extension
// (QRSignatureExt, the standard questionnaireresponse-signature: signature type
// + timestamp + signer + identity token), is nonconformant and rejected at the
// inbound gate. A system-sourced item (no manual-source marker at all) is
// untouched — no attestation required. Checks the SAME extension URLs
// BuildManualAttestedItem / BuildPatientAttestedItem write —
// informationOriginExt/ClinicianAttestationExt/QRSignatureExt are the package's
// own constants, not new ones.
//
// COMPLETE is load-bearing: the ATTESTATION is the property, not the element.
// An extension carrying the right url but blank content attests nothing, so the
// fence reads the sub-elements FR-16/FR-27 mandate and requires each to be
// present AND non-empty. A url-only check would let an amend claim a clinician
// attestation with no NPI, no text and no date and still be adjudicated.

// fenceAttestedItems walks EVERY QuestionnaireResponse entry in a
// conformant PAS Bundle (bundleJSON — the pas-claim / pas-claim-update request)
// and checks every item carrying the FR-17 manual-source information-origin
// extension: a clinician-entered item must also carry a COMPLETE FR-16
// clinician attestation extension; a patient-reported item must also carry a
// COMPLETE FR-27 patient attestation extension — complete meaning every
// mandated sub-element is present and non-empty. System-sourced items are
// untouched. Both conformant builders emit exactly one QR entry, so a bundle
// carrying a SECOND one only ever arises hand-built — exactly the door this
// fence exists for, so every entry is walked, not only the first. Returns
// ("", true) when every item in every entry conforms — including when the
// bundle carries no QuestionnaireResponse entry at all (optional on the
// submit leg) or every entry fails to parse (a malformed QR is the concern of
// the existing bundle-parse gates, not this fence). Returns a legible reason
// naming the failing FR, the linkId and the specific absent or empty field,
// and false, on the first nonconformant item found (entries checked in
// bundle order).
func fenceAttestedItems(bundleJSON []byte) (string, bool) {
	qrEntries, found := allQuestionnaireResponseEntries(bundleJSON)
	if !found {
		return "", true
	}
	for _, qrJSON := range qrEntries {
		var qr map[string]any
		if err := json.Unmarshal(qrJSON, &qr); err != nil {
			// A malformed QR is the concern of the existing bundle-parse
			// gates, not this fence — skip it and keep fencing the rest.
			continue
		}
		reason, ok := "", true
		walkFenceItems(qr, func(item map[string]any) bool {
			switch fenceItemOriginRole(item) {
			case "clinician":
				if defect := fenceClinicianAttestationDefect(item); defect != "" {
					reason = fmt.Sprintf("QuestionnaireResponse item %q is clinician-sourced (FR-17) but %s", fenceItemLinkID(item), defect)
					ok = false
					return false
				}
			case "patient":
				if defect := fencePatientAttestationDefect(item); defect != "" {
					reason = fmt.Sprintf("QuestionnaireResponse item %q is patient-reported (FR-17) but %s", fenceItemLinkID(item), defect)
					ok = false
					return false
				}
			}
			return true
		})
		if !ok {
			return reason, false
		}
	}
	return "", true
}

// allQuestionnaireResponseEntries returns every QuestionnaireResponse bundle
// entry's resource bytes, in bundle order, or found=false when the bundle is
// unparseable or carries none. Renamed from the single-entry
// firstQuestionnaireResponseEntry: fenceAttestedItems must walk every QR
// entry, not only the first, or a hand-built bundle can smuggle a second,
// unfenced entry straight past the FR-16/FR-27 gate. Parity with the
// substrate gateway's identical rename (gateway/engine/attestfence.go).
func allQuestionnaireResponseEntries(bundleJSON []byte) (resources [][]byte, found bool) {
	var probe struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &probe); err != nil {
		return nil, false
	}
	for _, e := range probe.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(e.Resource, &rt) == nil && rt.ResourceType == "QuestionnaireResponse" {
			resources = append(resources, e.Resource)
		}
	}
	return resources, len(resources) > 0
}

// walkFenceItems calls fn for every QuestionnaireResponse.item across BOTH
// FHIR nesting axes — item.item and item.answer.item, the same two loci
// dtrWalkAnswers walks in dtr.go (dtrRemapOriginCode) for the identical reason:
// an item nested either way is the SAME element and must be fenced identically.
// Stops early (returns false) the first time fn does.
func walkFenceItems(node map[string]any, fn func(item map[string]any) bool) bool {
	items, _ := node["item"].([]any)
	for _, it := range items {
		im, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if !fn(im) {
			return false
		}
		if !walkFenceItems(im, fn) {
			return false
		}
		answers, _ := im["answer"].([]any)
		for _, a := range answers {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			if !walkFenceItems(am, fn) {
				return false
			}
		}
	}
	return true
}

// fenceItemOriginRole reads item's answer-level FR-17 information-origin
// extension (informationOriginExt) and returns "clinician" when source="manual"
// and the author reference is a Practitioner, "patient" when it is a Patient,
// or "" for a system-sourced item (no manual-source extension at all, or a
// non-"manual" source such as "auto") — untouched by this fence.
func fenceItemOriginRole(item map[string]any) string {
	answers, _ := item["answer"].([]any)
	for _, a := range answers {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		extAny, _ := am["extension"].([]any)
		for _, e := range extAny {
			em, ok := e.(map[string]any)
			if !ok || em["url"] != informationOriginExt {
				continue
			}
			var source, authorRef string
			subAny, _ := em["extension"].([]any)
			for _, s := range subAny {
				sm, ok := s.(map[string]any)
				if !ok {
					continue
				}
				switch sm["url"] {
				case "source":
					source, _ = sm["valueCode"].(string)
				case "author":
					authorSub, _ := sm["extension"].([]any)
					for _, as := range authorSub {
						asm, ok := as.(map[string]any)
						if ok && asm["url"] == "reference" {
							authorRef, _ = asm["valueString"].(string)
						}
					}
				}
			}
			if source != "manual" {
				continue
			}
			switch {
			case strings.HasPrefix(authorRef, "Practitioner/"):
				return "clinician"
			case strings.HasPrefix(authorRef, "Patient/"):
				return "patient"
			}
		}
	}
	return ""
}

// fr16AttestationFields are the sub-extension urls FR-16's clinician
// attestation must carry — the clinician's NPI, the attestation text, and the
// attestation date — in the order the fence reports them. They are exactly the
// three BuildManualAttestedItem writes out of Attestation (NPI/Text/When), and
// exactly what FR-16 mandates: an attestation is a clinician, a statement, and
// a date. Adding a mandated field here is additive; each field carries its own
// rejection row so none can regress unnoticed.
var fr16AttestationFields = []string{"npi", "text", "date"}

// fenceClinicianAttestationDefect returns a legible description of what is
// wrong with item's FR-16 clinician attestation — the extension absent
// altogether, or one of fr16AttestationFields absent or empty — or "" when the
// attestation is complete and the item conforms.
func fenceClinicianAttestationDefect(item map[string]any) string {
	att, found := fenceItemExtension(item, ClinicianAttestationExt)
	if !found {
		return "carries no FR-16 attestation extension"
	}
	for _, field := range fr16AttestationFields {
		if fenceSubExtensionValue(att, field) == "" {
			return fmt.Sprintf("carries an FR-16 attestation extension whose %q is absent or empty", field)
		}
	}
	return ""
}

// fencePatientAttestationDefect returns a legible description of what is wrong
// with item's FR-27 patient attestation — the questionnaireresponse-signature
// extension absent altogether, or a valueSignature whose signature type,
// timestamp, signer identity, or identity token is absent or unusable — or ""
// when the attestation is complete. Those are the elements FR-27 names ("these
// are my own responses" carried as the typed Author's Signature assertion, an
// identity token, and a timestamp) plus the signer the assertion is about, and
// exactly what BuildPatientAttestedItem writes. The signer identity
// (Signature.who) accepts FHIR R4's two legal forms — a Reference or an
// Identifier — so a conformant partner PHG (e.g. DHIN) whose who carries only
// an identifier, with no resolvable reference, is not wrongly refused.
func fencePatientAttestationDefect(item map[string]any) string {
	sig, found := fenceItemExtension(item, QRSignatureExt)
	if !found {
		return "carries no FR-27 patient attestation extension"
	}
	value, _ := sig["valueSignature"].(map[string]any)
	if len(value) == 0 {
		return "carries an FR-27 patient attestation extension with no valueSignature"
	}
	if fenceSignatureTypeCode(value) == "" {
		return `carries an FR-27 patient attestation whose "type" names no signature code`
	}
	if fenceStringField(value, "when") == "" {
		return `carries an FR-27 patient attestation whose "when" is absent or empty`
	}
	who, _ := value["who"].(map[string]any)
	if !fenceWhoUsable(who) {
		return `carries an FR-27 patient attestation whose "who" has neither a non-empty "reference" nor an "identifier" with a non-empty "value" (FHIR R4's two legal forms for Signature.who — identifier.system is not required)`
	}
	if fenceStringField(value, "data") == "" {
		return `carries an FR-27 patient attestation whose "data" (identity token) is absent or empty`
	}
	return ""
}

// fenceWhoUsable reports whether who — a FHIR Signature.who, typed by FHIR R4 as
// Reference(Practitioner|RelatedPerson|Patient|Device|Organization) OR Identifier
// — carries a usable signer identity: a non-empty "reference", or an "identifier"
// with a non-empty "value". FR-27 requires an identity token for the signer, not
// a resolvable reference, so both legal forms are accepted (a conformant partner
// PHG, e.g. DHIN, may sign with an identifier alone). identifier.system is NOT
// required: FHIR R4's Identifier element requires no sub-element structurally,
// and a bare value still names the signer within an implicit/local system — the
// minimum that makes an identifier usable is a non-empty value.
func fenceWhoUsable(who map[string]any) bool {
	if fenceStringField(who, "reference") != "" {
		return true
	}
	ident, _ := who["identifier"].(map[string]any)
	return fenceStringField(ident, "value") != ""
}

// fenceItemExtension returns item's first item-level extension (not an
// answer-level one — FR-16's clinician attestation and FR-27's
// questionnaireresponse-signature both declare item as their context, exactly
// where BuildManualAttestedItem / BuildPatientAttestedItem place them) whose
// url == want.
func fenceItemExtension(item map[string]any, want string) (map[string]any, bool) {
	extAny, _ := item["extension"].([]any)
	for _, e := range extAny {
		em, ok := e.(map[string]any)
		if ok && em["url"] == want {
			return em, true
		}
	}
	return nil, false
}

// fenceSubExtensionValue returns the non-empty string value carried by ext's
// sub-extension whose url == want, across the value[x] flavours the attestation
// builders write (valueString for npi/text, valueDate for date), or "" when the
// sub-extension is absent, carries a non-string value, or carries a blank one.
// Whitespace-only counts as blank: " " attests no more than "".
func fenceSubExtensionValue(ext map[string]any, want string) string {
	subAny, _ := ext["extension"].([]any)
	for _, sub := range subAny {
		sm, ok := sub.(map[string]any)
		if !ok || sm["url"] != want {
			continue
		}
		for _, key := range []string{"valueString", "valueDate", "valueDateTime", "valueInstant"} {
			if v := fenceStringField(sm, key); v != "" {
				return v
			}
		}
	}
	return ""
}

// fenceSignatureTypeCode returns the first non-empty Signature.type coding code
// on a valueSignature, or "" when the signature declares no typed assertion at
// all — a signature that asserts nothing is not an attestation.
func fenceSignatureTypeCode(value map[string]any) string {
	typeAny, _ := value["type"].([]any)
	for _, c := range typeAny {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if code := fenceStringField(cm, "code"); code != "" {
			return code
		}
	}
	return ""
}

// fenceStringField returns node[key] when it is a string with non-whitespace
// content, and "" otherwise (absent, wrong type, or blank).
func fenceStringField(node map[string]any, key string) string {
	v, _ := node[key].(string)
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return v
}

// fenceItemLinkID returns item's linkId, or "" when absent — used only to
// make a rejection reason legible, never to key logic.
func fenceItemLinkID(item map[string]any) string {
	id, _ := item["linkId"].(string)
	return id
}
