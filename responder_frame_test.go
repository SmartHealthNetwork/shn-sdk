package shnsdk

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// responder_frame_test.go — the Responder's message-frame behavior (spec
// 2026-07-17-opaque-payload-frame-design.md): a frame-capable requester
// (ResolveFrames advertises "v1") gets application answers — success AND app
// non-2xx errors — sealed as a v1 HTTP frame carrying the application status,
// relayed 200-to-Hub; a legacy requester (nil ResolveFrames) gets the
// pre-frame bare-payload contract byte-identically; mechanical failures stay
// bare regardless of capability.

// makeFramedResponderSrv builds a Responder+httptest.Server with a caller-supplied
// ResolveFrames (nil ⇒ legacy-only responder).
func (h *paTestHarness) makeFramedResponderSrv(t *testing.T, responderIdent Identity, adj Adjudicator, resolveFrames func(string) []string) *httptest.Server {
	t.Helper()
	r, err := NewResponder(ResponderConfig{
		Identity:        responderIdent,
		AuthzURL:        h.authzSrv.URL,
		AuthzPub:        h.authzPub,
		HubTransportPub: h.hubPub,
		ResolveEnc: func(holderID string) (*[32]byte, bool) {
			if holderID == h.senderID {
				return h.senderEncPub, true
			}
			return nil, false
		},
		ResolveFrames: resolveFrames,
		Adjudicator:   adj,
		Clock:         func() time.Time { return h.now },
		Client:        h.authzSrv.Client(),
	})
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// framesV1 is the ResolveFrames a capable requester advertises.
func framesV1(string) []string { return []string{MessageFrameV1} }

// TestResponderFramesAppErrorForCapableRequester: an adjudication error
// (PriorAuth → 422) arrives at a capable requester as a sealed frame(422,
// {"error":...}, ct=application/json), relayed HTTP 200 to the Hub (the framed
// error rides the success response leg — seal+authorize run for it too).
func TestResponderFramesAppErrorForCapableRequester(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	srv := h.makeFramedResponderSrv(t, responderIdent, &errPriorAuthAdjudicator{}, framesV1)

	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	bundle := buildConformantClaim(t, "MBR-001", "frame-err-1", qr, h.now)
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "frame-err-1", bundle)

	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200 (framed error relays 200 to Hub); body: %s", resp.StatusCode, body)
	}

	plaintext := h.openResponse(t, body)
	hdr, fbody, err := DecodeHTTPFrame(plaintext)
	if err != nil {
		t.Fatalf("DecodeHTTPFrame: %v; plaintext: %s", err, plaintext)
	}
	if hdr.Status != http.StatusUnprocessableEntity {
		t.Errorf("frame status = %d, want 422", hdr.Status)
	}
	if hdr.Headers["Content-Type"] != "application/json" {
		t.Errorf("frame Content-Type = %q, want application/json", hdr.Headers["Content-Type"])
	}
	wantBody, _ := json.Marshal(map[string]string{"error": "adjudication unavailable"})
	if !bytes.Equal(fbody, wantBody) {
		t.Errorf("frame body = %s, want %s", fbody, wantBody)
	}
}

// TestResponderFramesSuccessForCapableRequester: a success answer arrives at a
// capable requester as frame(200, body, ct=application/fhir+json).
func TestResponderFramesSuccessForCapableRequester(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	srv := h.makeFramedResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now}, framesV1)

	cer, err := BuildEligibilityRequest("MBR-001", "9999999999", h.now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequest: %v", err)
	}
	envBytes, hubHdr := h.buildForwardEnv(t, "coverage-eligibility", "eligibility-inquiry", "frame-ok-1", cer)

	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	plaintext := h.openResponse(t, body)
	hdr, fbody, err := DecodeHTTPFrame(plaintext)
	if err != nil {
		t.Fatalf("DecodeHTTPFrame: %v; plaintext: %s", err, plaintext)
	}
	if hdr.Status != http.StatusOK {
		t.Errorf("frame status = %d, want 200", hdr.Status)
	}
	if hdr.Headers["Content-Type"] != "application/fhir+json" {
		t.Errorf("frame Content-Type = %q, want application/fhir+json", hdr.Headers["Content-Type"])
	}
	covered, _, err := ParseEligibilityResponse(fbody)
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(frame body): %v; body: %s", err, fbody)
	}
	if !covered {
		t.Error("covered = false, want true")
	}
}

// TestResponderLegacyWhenNoResolver: with nil ResolveFrames the responder is
// byte-identical to the pre-frame contract — a bare (unframed) success payload,
// and a bare HTTP 422 {"error":...} (respondErr shape, trailing newline included)
// for an app error.
func TestResponderLegacyWhenNoResolver(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)

	// Success: nil ResolveFrames ⇒ the opened plaintext is the bare FHIR payload,
	// NOT a v1 frame.
	t.Run("success stays bare", func(t *testing.T) {
		srv := h.makeFramedResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now}, nil)
		cer, err := BuildEligibilityRequest("MBR-001", "9999999999", h.now)
		if err != nil {
			t.Fatalf("BuildEligibilityRequest: %v", err)
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "coverage-eligibility", "eligibility-inquiry", "legacy-ok-1", cer)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HTTP status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		if IsFramed(plaintext) {
			t.Fatalf("legacy success payload is framed, want bare; plaintext: %x", plaintext[:6])
		}
		if _, _, err := ParseEligibilityResponse(plaintext); err != nil {
			t.Fatalf("bare payload is not a CoverageEligibilityResponse: %v", err)
		}
	})

	// App error: nil ResolveFrames ⇒ a bare HTTP 422 {"error":...}\n written by
	// respondErr — byte-identical to json.NewEncoder(w).Encode (trailing newline).
	t.Run("app error stays bare byte-identical", func(t *testing.T) {
		srv := h.makeFramedResponderSrv(t, responderIdent, &errPriorAuthAdjudicator{}, nil)
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "legacy-err-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "legacy-err-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("HTTP status = %d, want 422; body: %s", resp.StatusCode, body)
		}
		var want bytes.Buffer
		_ = json.NewEncoder(&want).Encode(map[string]string{"error": "adjudication unavailable"})
		if !bytes.Equal(body, want.Bytes()) {
			t.Errorf("legacy error body = %q, want %q (respondErr byte-identical)", body, want.Bytes())
		}
	})
}

// TestResponderMechanicalErrorsStayBare: a mechanical failure (steps 1-7) stays
// bare regardless of frame capability — a missing hub assertion → bare 403 even
// though the requester advertises v1.
func TestResponderMechanicalErrorsStayBare(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	srv := h.makeFramedResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now}, framesV1)

	cer, err := BuildEligibilityRequest("MBR-001", "9999999999", h.now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequest: %v", err)
	}
	envBytes, _ := h.buildForwardEnv(t, "coverage-eligibility", "eligibility-inquiry", "mech-1", cer)

	// No X-Hub-Assertion header → 403 at step 1, before any framing decision.
	resp := postInbound(t, srv, envBytes, "")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if IsFramed(body) {
		t.Errorf("mechanical error body is framed, want bare JSON")
	}
	var want bytes.Buffer
	_ = json.NewEncoder(&want).Encode(map[string]string{"error": "missing or invalid hub assertion"})
	if !bytes.Equal(body, want.Bytes()) {
		t.Errorf("mechanical error body = %q, want %q (bare respondErr)", body, want.Bytes())
	}
}
