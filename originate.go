package shnsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Endpoints are the substrate base URLs an originating participant talks to.
type Endpoints struct {
	// HubURL is the Hub base URL (e.g. "http://hub:8080"); RunEligibility POSTs the
	// sealed envelope to {HubURL}/route.
	HubURL string
	// AuthzURL is the Authorization Framework base URL; RunEligibility mints a
	// per-leg token via {AuthzURL}/authorize.
	AuthzURL string
}

// Payer identifies the eligibility responder the envelope is routed to.
type Payer struct {
	// ID is the payer's registered holder ID (the envelope Recipient + the expected
	// response Sender / token Holder).
	ID string
	// EncPub is the payer's X25519 public key — the seal target for the request
	// payload (anonymous sealed box, AI-2).
	EncPub *[32]byte
	// AuthzPub is the Authorization Framework's Ed25519 verifying key, used to
	// VerifyBound the payer's response token.
	AuthzPub ed25519.PublicKey
	// MessageFrames are the payer's advertised sealed-frame versions from the
	// /holders feed (Holder.MessageFrames). Empty ⇒ legacy bare payloads.
	MessageFrames []string
}

// RunEligibility runs one coverage-eligibility round-trip through the substrate and
// returns coverage. It is the SDK port of the reference originate flow
// (tools/sampleparticipant): resolve PCI → build CoverageEligibilityRequest →
// random correlation id → Authorize the request leg → Seal+Encode the envelope →
// POST {Hub}/route → Decode → assert envelope metadata → VerifyBound the response
// token → Open → ParseEligibilityResponse.
//
// It uses ONLY the SDK's own primitives (no internal/). The clock for the FHIR
// Created, envelope Timestamp, holder assertions, and token-expiry verification is
// Identity.now() (Identity.Clock, defaulting to time.Now). Tests driving a
// fixed-clock substrate inject Identity.Clock so assertions/tokens fall inside the
// substrate's ~5m skew window; real participants leave it nil.
//
// Wire values (ported from sampleparticipant, verified against the substrate):
//
//	envelope TransactionType: "coverage-eligibility"
//	request  frame/op:        "provider-tpo" / "eligibility-inquiry"
//	response frame/op:        "payer-coverage" / "eligibility-response"
func (id Identity) RunEligibility(ctx context.Context, c *http.Client, ep Endpoints, payer Payer, npi, memberID, birthDate, familyName string) (covered bool, reason string, err error) {
	if c == nil {
		c = http.DefaultClient
	}
	now := id.now()

	// Step 1 — resolve the patient PCI (AI-5). Never appears in Hub-readable Metadata.
	pci := ResolvePCI(memberID, birthDate, familyName)

	// Step 2 — build the FHIR CoverageEligibilityRequest payload.
	cer, err := BuildEligibilityRequest(memberID, npi, now)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: build eligibility request: %w", err)
	}

	// Step 3 — cryptographically random correlation id (16 bytes hex), generated
	// before /authorize so the token binds the exact envelope it rides in.
	var corrRaw [16]byte
	if _, err := rand.Read(corrRaw[:]); err != nil {
		return false, "", fmt.Errorf("shnsdk: generate correlation id: %w", err)
	}
	correlationID := hex.EncodeToString(corrRaw[:])

	// Step 4 — seal the envelope FIRST so the ciphertext exists (AI-2:
	// seal-then-authorize). AuthzToken is cleartext metadata stamped on AFTER the
	// token is minted bound to sha256hex(ciphertext).
	meta := Metadata{
		Sender:          id.HolderID,
		Recipient:       payer.ID,
		TransactionType: "coverage-eligibility",
		AuthorityFrame:  "provider-tpo",
		Timestamp:       now.UTC().Format(time.RFC3339),
		CorrelationID:   correlationID,
	}
	env, err := Seal(meta, cer, payer.EncPub)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: seal envelope: %w", err)
	}

	// Step 5 — authorize the request leg bound to THIS ciphertext: provider-tpo /
	// eligibility-inquiry, payloadHash = sha256hex(ciphertext).
	reqHash := sha256.Sum256(env.Ciphertext)
	tok, err := id.Authorize(ctx, c, ep.AuthzURL, AuthorizeRequest{
		Frame:         "provider-tpo",
		Operation:     "eligibility-inquiry",
		SubjectPCI:    pci,
		CorrelationID: correlationID,
		PayloadHash:   hex.EncodeToString(reqHash[:]),
	})
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: authorize request leg: %w", err)
	}
	tokJSON, err := json.Marshal(tok)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: marshal authz token: %w", err)
	}
	env.Metadata.AuthzToken = string(tokJSON)
	envBytes, err := EncodeEnvelope(env)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: encode envelope: %w", err)
	}

	// Step 6 — route through the Hub; it returns the payer's response envelope
	// synchronously. The /route call carries a holder assertion for audience "hub".
	hubAssertion, err := id.Assertion("hub", now, MaxAssertionTTL)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: build hub assertion: %w", err)
	}
	hubReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.HubURL+"/route", bytes.NewReader(envBytes))
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: build /route request: %w", err)
	}
	hubReq.Header.Set("Content-Type", "application/json")
	hubReq.Header.Set("X-Holder-Assertion", hubAssertion)

	hubResp, err := c.Do(hubReq)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: POST /route: %w", err)
	}
	defer hubResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(hubResp.Body, MaxResponseBytes))
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: read /route response: %w", err)
	}
	if hubResp.StatusCode < 200 || hubResp.StatusCode >= 300 {
		return false, "", fmt.Errorf("shnsdk: /route returned %d: %s", hubResp.StatusCode, respBody)
	}

	// Step 7 — decode and verify the response envelope.
	respEnv, err := DecodeEnvelope(respBody)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: decode response envelope: %w", err)
	}

	// Envelope-metadata invariants, before unmarshalling the token (these catch a
	// mis-routed response before token verification even runs).
	if respEnv.Metadata.CorrelationID != correlationID {
		return false, "", fmt.Errorf("shnsdk: response correlationId %q != request %q", respEnv.Metadata.CorrelationID, correlationID)
	}
	if respEnv.Metadata.Recipient != id.HolderID {
		return false, "", fmt.Errorf("shnsdk: response recipient %q != our holderID %q", respEnv.Metadata.Recipient, id.HolderID)
	}
	if respEnv.Metadata.TransactionType != "coverage-eligibility" {
		return false, "", fmt.Errorf("shnsdk: response transactionType %q != %q", respEnv.Metadata.TransactionType, "coverage-eligibility")
	}
	if respEnv.Metadata.Sender != payer.ID {
		return false, "", fmt.Errorf("shnsdk: response sender %q != expected payer %q", respEnv.Metadata.Sender, payer.ID)
	}

	// Verify the response token is bound to this exchange (H1):
	// payer-coverage / eligibility-response, same correlation, holder=payer,
	// subject=pci (same patient as the request leg).
	var respTok Token
	if err := json.Unmarshal([]byte(respEnv.Metadata.AuthzToken), &respTok); err != nil {
		return false, "", fmt.Errorf("shnsdk: unmarshal response authz token: %w", err)
	}
	respHash := sha256.Sum256(respEnv.Ciphertext)
	if err := VerifyBound(
		respTok,
		payer.AuthzPub,
		now,
		"payer-coverage",                // wantFrame
		"eligibility-response",          // wantOp
		correlationID,                   // wantCorrelationID
		payer.ID,                        // wantHolder
		pci,                             // wantSubject
		hex.EncodeToString(respHash[:]), // wantPayloadHash (AI-2)
	); err != nil {
		return false, "", fmt.Errorf("shnsdk: verify response token: %w", err)
	}

	// Step 8 — decrypt the response, unframe it (a frame-capable payer's non-2xx
	// APPLICATION answer surfaces as *AppAnswerError, verbatim), and parse the FHIR
	// response.
	plaintext, err := Open(respEnv, id.EncPub, id.EncPriv)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: open response envelope: %w", err)
	}
	plaintext, err = unframeAnswer(payer.MessageFrames, plaintext)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: response answer: %w", err)
	}
	covered, reason, err = ParseEligibilityResponse(plaintext)
	if err != nil {
		return false, "", fmt.Errorf("shnsdk: parse eligibility response: %w", err)
	}
	return covered, reason, nil
}
