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
// B1 carries eligibility only; later operations (CRD cards, DTR questionnaire,
// PAS) are ADDED as methods — existing methods never change (additive growth).
type Adjudicator interface {
	// Eligibility decides coverage for the member id carried in the inbound
	// CoverageEligibilityRequest. Return covered=false with a human-readable
	// reason to deny.
	Eligibility(memberID string) (covered bool, reason string)
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
	cfg ResponderConfig
	jti *jtiGuard
}

// responderReqOp pins each TransactionType to the request operation the inbound
// token must carry — B1: eligibility only. Unknown types → 400 before token work.
var responderReqOp = map[string]string{
	"coverage-eligibility": "eligibility-inquiry",
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
	return &Responder{cfg: cfg, jti: newJTIGuard(MaxAssertionTTL, 1<<16)}, nil
}

// Handler returns a ServeMux with exactly POST /substrate/inbound wired to
// handleInbound.
func (r *Responder) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /substrate/inbound", r.handleInbound)
	return mux
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
//  7. open + parse member
//  8. adjudicate + build response FHIR
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
	//    (mirrors gateway.go S1: empty frame/corr would skip VerifyBound's binding
	//    checks and produce audit records with empty correlation).
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

	// 7. Open + parse member.
	plaintext, err := Open(env, r.cfg.Identity.EncPub, r.cfg.Identity.EncPriv)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "decryption failed")
		return
	}
	member, err := ParseEligibilityRequestMember(plaintext)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "parse member failed")
		return
	}

	// 8. Adjudicate + build response FHIR.
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
	// response; the SDK responder runs neither. Runtime conformance is a
	// property of gateways the operator runs (payload-blind Hub ⟹ conformance
	// is enforced at operated edges); a partner-run responder is the partner's
	// edge. The response SHAPE is still parity-pinned byte-for-byte against the
	// substrate builder (test/sdkparity fhir parity), so a conformant resource
	// is what this code produces — it is just not re-validated per request here.
	covered, reason := r.cfg.Adjudicator.Eligibility(member)
	crrJSON, err := BuildEligibilityResponse(corr, "Patient/"+member, covered, reason, now)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "build response failed")
		return
	}

	// 9. Resolve sender enc key + seal (AI-2: seal FIRST, then authorize).
	senderEncPub, ok := r.cfg.ResolveEnc(env.Metadata.Sender)
	if !ok {
		respondErr(w, http.StatusBadGateway, "requester key not resolvable")
		return
	}
	respMeta := Metadata{
		Sender:          r.cfg.Identity.HolderID,
		Recipient:       env.Metadata.Sender,
		TransactionType: "coverage-eligibility",
		AuthorityFrame:  "payer-coverage",
		Timestamp:       now.Format(time.RFC3339),
		CorrelationID:   corr,
	}
	respEnv, err := Seal(respMeta, crrJSON, senderEncPub)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "seal failed")
		return
	}

	// 10. AI-2 seal-then-authorize: authorize the response leg bound to
	//     sha256Hex(ciphertext), then stamp the token into the cleartext metadata.
	respTok, err := r.cfg.Identity.Authorize(req.Context(), r.cfg.Client, r.cfg.AuthzURL, AuthorizeRequest{
		Frame:         "payer-coverage",
		Operation:     "eligibility-response",
		SubjectPCI:    tok.Subject,
		CorrelationID: corr,
		PayloadHash:   sha256Hex(respEnv.Ciphertext),
	})
	if err != nil {
		respondErr(w, http.StatusBadGateway, "authorize response leg failed")
		return
	}
	respTokJSON, err := json.Marshal(respTok)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "marshal response token failed")
		return
	}
	respEnv.Metadata.AuthzToken = string(respTokJSON)

	out, err := EncodeEnvelope(respEnv)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "encode failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
