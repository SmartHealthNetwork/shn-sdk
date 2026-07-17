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

func TestEncodeHTTPFrameRejectsBadStatus(t *testing.T) {
	for _, s := range []int{0, 99, 600} {
		if _, err := EncodeHTTPFrame(s, "", nil); err == nil {
			t.Errorf("EncodeHTTPFrame accepted status %d", s)
		}
	}
}

// TestUnframeAnswer covers the originator side of frame negotiation: a v1 frame
// from a frame-capable payer yields its body (2xx) or an *AppAnswerError (non-2xx);
// a bare payload — legacy payer, or the stale-feed downgrade — passes through.
func TestUnframeAnswer(t *testing.T) {
	oo := []byte(`{"resourceType":"OperationOutcome"}`)
	framedErr, _ := EncodeHTTPFrame(422, "application/fhir+json", oo)
	_, err := unframeAnswer([]string{"v1"}, framedErr)
	var ae *AppAnswerError
	if !errors.As(err, &ae) || ae.Status != 422 || !bytes.Equal(ae.Body, oo) || ae.ContentType != "application/fhir+json" {
		t.Fatalf("framed non-2xx: got %v", err)
	}
	framedOK, _ := EncodeHTTPFrame(200, "application/fhir+json", oo)
	if body, err := unframeAnswer([]string{"v1"}, framedOK); err != nil || !bytes.Equal(body, oo) {
		t.Fatalf("framed 2xx: %q %v", body, err)
	}
	// Stale-feed fallback: capable payer, bare payload → passthrough.
	if body, err := unframeAnswer([]string{"v1"}, oo); err != nil || !bytes.Equal(body, oo) {
		t.Fatalf("bare fallback: %q %v", body, err)
	}
	// Bare payload with nil advertised frames: passthrough (a legacy payer's success).
	if body, err := unframeAnswer(nil, oo); err != nil || !bytes.Equal(body, oo) {
		t.Fatalf("legacy passthrough: %q %v", body, err)
	}
	// Inverse stale-feed window (hardened at final review): a payer that frames its
	// answer while OUR view of it is still pre-upgrade (nil advertised frames). Decode
	// is keyed on the magic byte, not the advertised frames — a framed payload MUST
	// still decode, both the 2xx body and the non-2xx *AppAnswerError.
	if body, err := unframeAnswer(nil, framedOK); err != nil || !bytes.Equal(body, oo) {
		t.Fatalf("nil-frames framed 2xx must decode: %q %v", body, err)
	}
	_, err = unframeAnswer(nil, framedErr)
	var ae2 *AppAnswerError
	if !errors.As(err, &ae2) || ae2.Status != 422 || !bytes.Equal(ae2.Body, oo) {
		t.Fatalf("nil-frames framed non-2xx must decode to *AppAnswerError: got %v", err)
	}
	// Corrupt frame from a capable payer → error.
	if _, err := unframeAnswer([]string{"v1"}, []byte{0x00, 0xFF, 0, 0, 0, 0}); err == nil {
		t.Fatal("corrupt frame accepted")
	}
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
