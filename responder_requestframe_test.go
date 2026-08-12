package shnsdk

import (
	"net/http"
	"testing"
)

// responder_requestframe_test.go — the Responder's RECEIVER OBLIGATION (spec
// 2026-08-11 slice 4, published-SDK parity — v0.38.0): this SDK self-declares
// requestFrames v1 at registration (always on — SupportedRequestFrames), so it
// MUST decode BOTH a framed and a bare inbound request identically. Decoding does
// NOT depend on ResolveFrames/StampContractVersion — those gate the RESPONSE leg;
// the REQUEST leg is unframed unconditionally (handleInbound step 7).

// buildFramedForwardEnv is buildForwardEnv with the request payload wrapped in a
// v1 HTTP frame carrying an OPTIONAL contractVersion claim (empty ⇒ a
// frames-without-versions request — the "treat like bare" case).
func (h *paTestHarness) buildFramedForwardEnv(t *testing.T, txType, txOp, corrID, claimedContract string, payload []byte) (envBytes []byte, hubHdr string) {
	t.Helper()
	headers := map[string]string{"Content-Type": "application/fhir+json"}
	if claimedContract != "" {
		headers[FrameHeaderContractVersion] = claimedContract
	}
	framed, err := EncodeHTTPFrameHeaders(http.StatusOK, headers, payload)
	if err != nil {
		t.Fatalf("EncodeHTTPFrameHeaders: %v", err)
	}
	return h.buildForwardEnv(t, txType, txOp, corrID, framed)
}

// TestResponderUnframesInboundFramedRequest proves a framed CRD request — carrying
// a genuine contractVersion claim — is unframed BEFORE dispatch and answered
// exactly like the bare equivalent (TestResponder_CRD's pa-required row).
func TestResponderUnframesInboundFramedRequest(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	req := buildConformantCRD(t, "MBR-001", "72148")
	envBytes, hubHdr := h.buildFramedForwardEnv(t, "crd-order-select", "crd-order-select", "d9-crd-1", ContractPACRD20, req)
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
}

// TestResponderUnframesInboundRequestWithNoVersionClaim proves a framed request
// carrying NO contractVersion header (frames-without-versions) is treated exactly
// like bare — the absence-tolerated precedent.
func TestResponderUnframesInboundRequestWithNoVersionClaim(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	req := buildConformantCRD(t, "MBR-001", "72148")
	envBytes, hubHdr := h.buildFramedForwardEnv(t, "crd-order-select", "crd-order-select", "d9-crd-noclaim-1", "", req)
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
}

// TestResponderUnframesInboundFramedPASRequest proves the same for a pas-claim
// leg (a different contract family than CRD), end to end through an approve
// outcome — the unframed body reaches handlePASSubmit's conformant bundle parser.
func TestResponderUnframesInboundFramedPASRequest(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	bundle := buildConformantClaim(t, "MBR-001", "d9-pas-1", qr, h.now)
	envBytes, hubHdr := h.buildFramedForwardEnv(t, "pas-claim", "pas-submit", "d9-pas-1", ContractPAPAS20, bundle)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	result, err := ParseClaimResponse(h.openResponse(t, body))
	if err != nil {
		t.Fatalf("ParseClaimResponse: %v", err)
	}
	if result.Outcome != "approved" {
		t.Errorf("outcome = %q, want approved", result.Outcome)
	}
}

// TestResponderRejectsCorruptInboundFrame is the request-frame receiver's rejection row
// (CLAUDE.md: every guard ships its rejection test): a payload beginning with the
// frame magic byte but failing DecodeHTTPFrame (unsupported version) is a 400,
// exactly like a corrupt envelope at step 2 — never silently passed through to a
// handler as opaque bytes.
func TestResponderRejectsCorruptInboundFrame(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	corrupt := []byte{0x00, 0xFF, 0, 0, 0, 0} // bad frame version byte — mirrors TestUnframeAnswer's corrupt row
	envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "d9-corrupt-1", corrupt)
	resp := postInbound(t, srv, envBytes, hubHdr)
	assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "request frame decode failed")
}

// TestResponderRejectsCorruptInboundFrame_HeaderLenOverrun is a second corrupt-frame
// row (header length claims more bytes than the payload carries) — proves the
// rejection is DecodeHTTPFrame's general guard, not a single hand-picked byte
// sequence.
func TestResponderRejectsCorruptInboundFrame_HeaderLenOverrun(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now})

	corrupt := mustEncodeRawFrame(t, `{"status":200}`, nil)
	corrupt = corrupt[:len(corrupt)-2] // truncate the header JSON mid-way → header len overrun
	envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "d9-corrupt-2", corrupt)
	resp := postInbound(t, srv, envBytes, hubHdr)
	assertError(t, resp, readBody(t, resp), http.StatusBadRequest, "request frame decode failed")
}
