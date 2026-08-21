package shnsdk

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// responder_stamp_test.go — the Responder's contractVersion STAMP (multi-version contracts design,
// published-SDK parity — v0.38.0): ResponderConfig.StampContractVersion
// opts a Responder into stamping FrameHeaderContractVersion on every SUCCESS (2xx)
// framed answer, computed PER LEG (contractTokenForTxType — the same mapping
// RunPriorAuth's originator side verifies against), never on an app-error frame,
// and never on coverage-eligibility (version-neutral, no contract token). false (the
// zero value) is BYTE-IDENTICAL to the pre-v0.38.0 responder — no partner code
// change required to stay on today's wire shape.

// makeStampedResponderSrv is makeFramedResponderSrv plus StampContractVersion.
func (h *paTestHarness) makeStampedResponderSrv(t *testing.T, responderIdent Identity, adj Adjudicator, resolveFrames func(string) []string, stamp bool) *httptest.Server {
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
		ResolveFrames:        resolveFrames,
		Adjudicator:          adj,
		Clock:                func() time.Time { return h.now },
		Client:               h.authzSrv.Client(),
		StampContractVersion: stamp,
	})
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestResponderStampsPerLegContractVersion proves each PA-chain leg is stamped with
// ITS OWN contract-family token — not a single value applied uniformly (which would
// break RunPriorAuth's own per-leg unframeAnswer verification against a stamping
// Responder) — and that coverage-eligibility, having no contract token, is framed
// but never stamped.
func TestResponderStampsPerLegContractVersion(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	srv := h.makeStampedResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now}, framesV1, true)

	t.Run("crd leg stamped pa.crd@2.0", func(t *testing.T) {
		req := buildConformantCRD(t, "MBR-001", "72148")
		envBytes, hubHdr := h.buildForwardEnv(t, "crd-order-select", "crd-order-select", "stamp-crd-1", req)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		hdr, _, err := DecodeHTTPFrame(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("DecodeHTTPFrame: %v", err)
		}
		if got := hdr.Headers[FrameHeaderContractVersion]; got != ContractPACRD20 {
			t.Errorf("contractVersion = %q, want %q", got, ContractPACRD20)
		}
	})

	t.Run("pas-claim leg stamped pa.pas@2.0", func(t *testing.T) {
		qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
		bundle := buildConformantClaim(t, "MBR-001", "stamp-pas-1", qr, h.now)
		envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "stamp-pas-1", bundle)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		hdr, _, err := DecodeHTTPFrame(h.openResponse(t, body))
		if err != nil {
			t.Fatalf("DecodeHTTPFrame: %v", err)
		}
		if got := hdr.Headers[FrameHeaderContractVersion]; got != ContractPAPAS20 {
			t.Errorf("contractVersion = %q, want %q", got, ContractPAPAS20)
		}
	})

	t.Run("eligibility leg framed but never stamped (version-neutral)", func(t *testing.T) {
		cer, err := BuildEligibilityRequest("MBR-001", "9999999999", h.now)
		if err != nil {
			t.Fatalf("BuildEligibilityRequest: %v", err)
		}
		envBytes, hubHdr := h.buildForwardEnv(t, "coverage-eligibility", "eligibility-inquiry", "stamp-elig-1", cer)
		resp := postInbound(t, srv, envBytes, hubHdr)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		plaintext := h.openResponse(t, body)
		if !IsFramed(plaintext) {
			t.Fatal("eligibility response not framed, want frame-capable requester to get a frame")
		}
		hdr, _, err := DecodeHTTPFrame(plaintext)
		if err != nil {
			t.Fatalf("DecodeHTTPFrame: %v", err)
		}
		if got, ok := hdr.Headers[FrameHeaderContractVersion]; ok || got != "" {
			t.Errorf("contractVersion = %q (present=%v), want absent — coverage-eligibility has no contract token", got, ok)
		}
	})
}

// TestResponderStampContractVersionAppErrorNeverStamped proves an app-error frame
// (PriorAuth adjudicator failure → 422) is never stamped even with
// StampContractVersion true — mirrors the gateway's respondLegError, which may
// relay bytes this build did not produce.
func TestResponderStampContractVersionAppErrorNeverStamped(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	srv := h.makeStampedResponderSrv(t, responderIdent, &errPriorAuthAdjudicator{}, framesV1, true)

	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	bundle := buildConformantClaim(t, "MBR-001", "stamp-err-1", qr, h.now)
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "stamp-err-1", bundle)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (framed error relays 200 to Hub); body: %s", resp.StatusCode, body)
	}
	hdr, _, err := DecodeHTTPFrame(h.openResponse(t, body))
	if err != nil {
		t.Fatalf("DecodeHTTPFrame: %v", err)
	}
	if hdr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("frame status = %d, want 422", hdr.Status)
	}
	if got, ok := hdr.Headers[FrameHeaderContractVersion]; ok || got != "" {
		t.Errorf("contractVersion = %q (present=%v), want absent on an app-error frame", got, ok)
	}
}

// TestResponderStampContractVersionDefaultFalseByteIdentical proves
// StampContractVersion's zero value (false — every partner build that never sets
// the new field) produces a success frame BYTE-IDENTICAL to a hand-built
// EncodeHTTPFrame(200, ct, body) call — no contractVersion header appears, exactly
// as before v0.38.0.
func TestResponderStampContractVersionDefaultFalseByteIdentical(t *testing.T) {
	h, responderIdent, _ := newPAHarness(t)
	srv := h.makeStampedResponderSrv(t, responderIdent, &sandboxTestAdjudicator{now: h.now}, framesV1, false)

	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8}, h.now)
	bundle := buildConformantClaim(t, "MBR-001", "nostamp-pas-1", qr, h.now)
	envBytes, hubHdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "nostamp-pas-1", bundle)
	resp := postInbound(t, srv, envBytes, hubHdr)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	plaintext := h.openResponse(t, body)
	hdr, framedBody, err := DecodeHTTPFrame(plaintext)
	if err != nil {
		t.Fatalf("DecodeHTTPFrame: %v", err)
	}
	if _, present := hdr.Headers[FrameHeaderContractVersion]; present {
		t.Fatalf("contractVersion header present with StampContractVersion=false: %v", hdr.Headers)
	}
	want, werr := EncodeHTTPFrame(http.StatusOK, "application/fhir+json", framedBody)
	if werr != nil {
		t.Fatalf("EncodeHTTPFrame: %v", werr)
	}
	if string(want) != string(plaintext) {
		t.Fatalf("frame not byte-identical to legacy EncodeHTTPFrame:\n got  %x\n want %x", plaintext, want)
	}
}
