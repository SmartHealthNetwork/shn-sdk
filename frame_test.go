package shnsdk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestSupportedMessageFrames(t *testing.T) {
	fr := SupportedMessageFrames()
	if len(fr) != 1 || fr[0] != MessageFrameV1 {
		t.Fatalf("SupportedMessageFrames() = %v, want [%q]", fr, MessageFrameV1)
	}
	// Callers may append; a second call must not observe mutation.
	fr[0] = "mutated"
	if got := SupportedMessageFrames(); got[0] != MessageFrameV1 {
		t.Fatalf("SupportedMessageFrames is not defensive-copied: %v", got)
	}
	if !SupportsMessageFrameV1([]string{"v1"}) || SupportsMessageFrameV1(nil) || SupportsMessageFrameV1([]string{"v2"}) {
		t.Fatal("SupportsMessageFrameV1 membership check wrong")
	}
}

func TestHTTPFrameRoundTrip(t *testing.T) {
	body := []byte(`{"resourceType":"OperationOutcome"}`)
	f, err := EncodeHTTPFrame(422, "application/fhir+json", body)
	if err != nil {
		t.Fatal(err)
	}
	if !IsFramed(f) {
		t.Fatal("encoded frame not recognized by IsFramed")
	}
	if IsFramed(body) {
		t.Fatal("bare JSON misclassified as framed")
	}
	hdr, got, err := DecodeHTTPFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Status != 422 || hdr.Headers["Content-Type"] != "application/fhir+json" || !bytes.Equal(got, body) {
		t.Fatalf("round trip lost data: %+v %q", hdr, got)
	}
	// Empty body + no content type also round-trips.
	f2, err := EncodeHTTPFrame(204, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr2, got2, err := DecodeHTTPFrame(f2)
	if err != nil || hdr2.Status != 204 || len(got2) != 0 || len(hdr2.Headers) != 0 {
		t.Fatalf("empty round trip: %+v %q %v", hdr2, got2, err)
	}
}

// Rejection table — every decode guard ships its rejection row (CLAUDE.md discipline).
func TestDecodeHTTPFrameRejects(t *testing.T) {
	valid, _ := EncodeHTTPFrame(400, "application/json", []byte(`{"error":"x"}`))
	hdrLen := func(n uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, n); return b }
	cases := map[string][]byte{
		"empty":                   {},
		"bad magic":               append([]byte{0x7b}, valid[1:]...),
		"unknown version":         append([]byte{0x00, 0x02}, valid[2:]...),
		"truncated header":        valid[:4],
		"header len overrun":      append(append([]byte{0x00, 0x01}, hdrLen(1<<20)...), []byte(`{}`)...),
		"hlen under cap over end": append(append([]byte{0x00, 0x01}, hdrLen(50)...), []byte(`{}`)...), // 50 ≤ 64KiB cap but > the 2 bytes present — isolates the remaining-payload bounds arm
		"header over cap":         append(append([]byte{0x00, 0x01}, hdrLen((64<<10)+1)...), make([]byte, (64<<10)+1)...),
		"non-JSON header":         append(append([]byte{0x00, 0x01}, hdrLen(3)...), []byte("nope")...),
		"status too low":          mustEncodeRawFrame(t, `{"status":99}`, nil),
		"status too high":         mustEncodeRawFrame(t, `{"status":600}`, nil),
		"status missing":          mustEncodeRawFrame(t, `{}`, nil),
	}
	for name, payload := range cases {
		if _, _, err := DecodeHTTPFrame(payload); err == nil {
			t.Errorf("%s: decode accepted a malformed frame", name)
		}
	}
}

func TestDecodeHTTPFrameDropsNonAllowlistedHeaders(t *testing.T) {
	f := mustEncodeRawFrame(t, `{"status":400,"headers":{"Content-Type":"application/json","Set-Cookie":"evil","X-Internal":"leak"}}`, []byte(`{}`))
	hdr, _, err := DecodeHTTPFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(hdr.Headers) != 1 || hdr.Headers["Content-Type"] != "application/json" {
		t.Fatalf("allowlist not enforced on consume: %+v", hdr.Headers)
	}
}

// TestEncodeHTTPFrameHeaders_ContractVersion: the slice-3 allowlist widening
// (spec 2026-08-10 §4 "frame header carries the full token"; the 2026-07-17
// frame spec §3 names widening a documented spec change). The contractVersion
// header round-trips produce→consume; the legacy Content-Type-only encoder is
// byte-unchanged; a non-allowlisted header is refused at ENCODE (producing a
// header the consumer would strip is a caller bug, not a silent drop).
func TestEncodeHTTPFrameHeaders_ContractVersion(t *testing.T) {
	body := []byte(`{"resourceType":"ClaimResponse"}`)
	framed, err := EncodeHTTPFrameHeaders(200, map[string]string{
		"Content-Type":             "application/fhir+json",
		FrameHeaderContractVersion: "pa.pas@2.0",
	}, body)
	if err != nil {
		t.Fatalf("EncodeHTTPFrameHeaders: %v", err)
	}
	hdr, got, err := DecodeHTTPFrame(framed)
	if err != nil {
		t.Fatalf("DecodeHTTPFrame: %v", err)
	}
	if hdr.Status != 200 || !bytes.Equal(got, body) {
		t.Fatalf("round-trip: status=%d body=%q", hdr.Status, got)
	}
	if hdr.Headers[FrameHeaderContractVersion] != "pa.pas@2.0" {
		t.Fatalf("contractVersion header lost on consume: %v", hdr.Headers)
	}
	if hdr.Headers["Content-Type"] != "application/fhir+json" {
		t.Fatalf("Content-Type lost: %v", hdr.Headers)
	}

	// Refuse a non-allowlisted header at encode.
	if _, err := EncodeHTTPFrameHeaders(200, map[string]string{"Cookie": "x"}, body); err == nil {
		t.Fatal("non-allowlisted header must be refused at encode")
	}

	// The legacy encoder's bytes are UNCHANGED (wire regression fence).
	legacy1, err1 := EncodeHTTPFrame(200, "application/fhir+json", body)
	legacy2, err2 := EncodeHTTPFrameHeaders(200, map[string]string{"Content-Type": "application/fhir+json"}, body)
	if err1 != nil || err2 != nil || !bytes.Equal(legacy1, legacy2) {
		t.Fatal("EncodeHTTPFrameHeaders with Content-Type only must be byte-identical to EncodeHTTPFrame")
	}
}

// TestDecodeHTTPFrame_StillDropsUnknownHeaders: consume-side allowlisting is
// unchanged for anything outside the widened set (no smuggling regression).
// EncodeHTTPFrameHeaders refuses the smuggled key, so build the raw frame via
// the file's EXISTING mustEncodeRawFrame helper (frame_test.go:68's idiom —
// read its signature and match it).
func TestDecodeHTTPFrame_StillDropsUnknownHeaders(t *testing.T) {
	frame := mustEncodeRawFrame(t, `{"status":200,"headers":{"Content-Type":"application/json","X-Internal":"leak","contractVersion":"pa.pas@2.0"}}`, []byte(`{}`))
	hdr, _, err := DecodeHTTPFrame(frame)
	if err != nil {
		t.Fatalf("DecodeHTTPFrame: %v", err)
	}
	if _, leaked := hdr.Headers["X-Internal"]; leaked {
		t.Fatal("non-allowlisted header survived consume")
	}
	if hdr.Headers[FrameHeaderContractVersion] != "pa.pas@2.0" {
		t.Fatal("allowlisted contractVersion must survive consume")
	}
}

func TestEncodeHTTPFrameRejectsBadStatus(t *testing.T) {
	for _, s := range []int{0, 99, 600} {
		if _, err := EncodeHTTPFrame(s, "", nil); err == nil {
			t.Errorf("EncodeHTTPFrame accepted status %d", s)
		}
	}
}

// TestUnframeAnswer covers the originator side of frame negotiation, which keys
// solely on the magic byte (the payer's advertised frames are advisory, not an
// input): a v1 frame yields its body (2xx) or an *AppAnswerError (non-2xx); a bare
// payload — legacy payer, or either stale-feed direction — passes through; a
// corrupt frame errors. expectedToken "" throughout — the contractVersion stamp-verify rows
// live in TestUnframeAnswer_StampVerify.
func TestUnframeAnswer(t *testing.T) {
	oo := []byte(`{"resourceType":"OperationOutcome"}`)
	framedErr, _ := EncodeHTTPFrame(422, "application/fhir+json", oo)
	_, err := unframeAnswer(framedErr, "")
	var ae *AppAnswerError
	if !errors.As(err, &ae) || ae.Status != 422 || !bytes.Equal(ae.Body, oo) || ae.ContentType != "application/fhir+json" {
		t.Fatalf("framed non-2xx: got %v", err)
	}
	framedOK, _ := EncodeHTTPFrame(200, "application/fhir+json", oo)
	if body, err := unframeAnswer(framedOK, ""); err != nil || !bytes.Equal(body, oo) {
		t.Fatalf("framed 2xx: %q %v", body, err)
	}
	// Bare payload → passthrough. This is BOTH a legacy payer's success and the
	// stale-feed downgrade case (we advertised nothing, or advertised v1 and the
	// payer answered bare); the decode decision is identical because it is
	// magic-keyed, so these no longer need distinct advertised-frame inputs.
	if body, err := unframeAnswer(oo, ""); err != nil || !bytes.Equal(body, oo) {
		t.Fatalf("bare passthrough: %q %v", body, err)
	}
	// Corrupt frame → error.
	if _, err := unframeAnswer([]byte{0x00, 0xFF, 0, 0, 0, 0}, ""); err == nil {
		t.Fatal("corrupt frame accepted")
	}
}

// TestUnframeAnswer_StampVerify covers the contractVersion stamp-verify rows (spec 2026-08-10
// §4, published-SDK parity — v0.38.0): unframeAnswer(frame, expectedToken)
// verbatim-mirrors the gateway's response-leg verify (gateway/engine/gateway.go
// roundTripInner) — non-empty expectation + present stamp + MISMATCH → reject;
// matching stamp → accept; ABSENT stamp is always tolerated regardless of
// expectation (the frames-absent-lane precedent); expectedToken == "" (the
// caller has no routed-token expectation) skips the check even when a stamp is
// present.
func TestUnframeAnswer_StampVerify(t *testing.T) {
	body := []byte(`{"resourceType":"ClaimResponse"}`)

	stamped := func(t *testing.T, token string) []byte {
		t.Helper()
		headers := map[string]string{"Content-Type": "application/fhir+json"}
		if token != "" {
			headers[FrameHeaderContractVersion] = token
		}
		f, err := EncodeHTTPFrameHeaders(200, headers, body)
		if err != nil {
			t.Fatalf("EncodeHTTPFrameHeaders: %v", err)
		}
		return f
	}

	t.Run("matching stamp accepted", func(t *testing.T) {
		got, err := unframeAnswer(stamped(t, "pa.pas@2.0"), "pa.pas@2.0")
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("got %q, %v; want body, nil", got, err)
		}
	})

	t.Run("mismatched stamp rejected", func(t *testing.T) {
		_, err := unframeAnswer(stamped(t, "pa.pas@2.1"), "pa.pas@2.0")
		if err == nil {
			t.Fatal("mismatched contractVersion stamp must be rejected")
		}
	})

	t.Run("absent stamp tolerated despite expectation", func(t *testing.T) {
		got, err := unframeAnswer(stamped(t, ""), "pa.pas@2.0")
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("got %q, %v; want body, nil (absent stamp always tolerated)", got, err)
		}
	})

	t.Run("no expectation skips the check even with a stamp present", func(t *testing.T) {
		got, err := unframeAnswer(stamped(t, "pa.pas@9.9"), "")
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("got %q, %v; want body, nil (expectedToken \"\" never verifies)", got, err)
		}
	})
}

// mustEncodeRawFrame hand-builds magic+version+len+headerJSON+body, bypassing
// EncodeHTTPFrame's validation, to drive decode-side rejection rows.
func mustEncodeRawFrame(t *testing.T, headerJSON string, body []byte) []byte {
	t.Helper()
	out := []byte{0x00, 0x01}
	l := make([]byte, 4)
	binary.BigEndian.PutUint32(l, uint32(len(headerJSON)))
	out = append(out, l...)
	out = append(out, headerJSON...)
	return append(out, body...)
}
