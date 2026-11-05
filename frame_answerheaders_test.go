package shnsdk

import (
	"bytes"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

// A 2xx answer's validators and retry time cross the seal as the payer wrote
// them. The frame carries a value, it does not judge one: an unquoted ETag and
// an odd date are carried as sent.
func TestAnswerHeaders_RoundTripAsSent(t *testing.T) {
	for name, value := range map[string]map[string]string{
		"validators":     {FrameHeaderETag: `"v1"`, FrameHeaderLastModified: "Sat, 31 Oct 2026 21:00:00 GMT"},
		"weak etag":      {FrameHeaderETag: `W/"v1"`},
		"unquoted etag":  {FrameHeaderETag: "v1"},
		"odd date":       {FrameHeaderLastModified: "yesterday"},
		"retry seconds":  {FrameHeaderRetryAfter: "120"},
		"retry date":     {FrameHeaderRetryAfter: "Sat, 31 Oct 2026 21:00:00 GMT"},
		"retry not time": {FrameHeaderRetryAfter: "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{"Content-Type": "application/fhir+json"}
			for k, v := range value {
				headers[k] = v
			}
			framed, err := EncodeHTTPFrameHeaders(202, headers, []byte(`{}`))
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			hdr, body, err := DecodeHTTPFrame(framed)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if hdr.Status != 202 || !bytes.Equal(body, []byte(`{}`)) {
				t.Fatalf("status %d body %q", hdr.Status, body)
			}
			for k, v := range value {
				if hdr.Headers[k] != v {
					t.Fatalf("%s = %q, want %q", k, hdr.Headers[k], v)
				}
			}
		})
	}
}

// The allowlist widened by exactly the three answer headers, and then by the
// three operation address headers: an HTTP address into the payer's system (a
// Location), a credential and a cookie are still neither produced nor
// consumed, in any case (frame_headercase_test.go holds the allowlisted
// names' cases).
func TestAnswerHeaders_NothingElseCrosses(t *testing.T) {
	want := []string{"Content-Type", FrameHeaderContractVersion, FrameHeaderETag, FrameHeaderLastModified, FrameHeaderOperation, FrameHeaderRetryAfter,
		FrameHeaderOperationSubpath, FrameHeaderOperationBase, FrameHeaderOperationAddressTags}
	if got := slices.Sorted(maps.Keys(allowedFrameHeaders)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Fatalf("allowlist %q, want %q", got, want)
	}
	for _, name := range []string{
		"Location", "Content-Location", "Set-Cookie", "Cookie", "Authorization", "WWW-Authenticate",
		"Vary", "Cache-Control", "Link", "location", "SET-COOKIE", "authorization",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: "x"}, nil); err == nil {
				t.Fatal("encoded")
			}
			raw := mustEncodeRawFrame(t, `{"status":200,"headers":{"`+name+`":"x"}}`, nil)
			hdr, _, err := DecodeHTTPFrame(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, kept := hdr.Headers[name]; kept {
				t.Fatalf("decoded %s", name)
			}
		})
	}
}

// A header segment the decoder would refuse is refused where it is made, by
// name, so a producer can leave out what cannot be carried: a frame that cannot
// be read loses the whole answer.
func TestEncodeHTTPFrameHeaders_HoldsTheCap(t *testing.T) {
	segment := func(n int) map[string]string {
		base, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderETag: ""}, nil)
		if err != nil {
			t.Fatal(err)
		}
		empty := len(base) - 6 // {"status":200}
		over := len(`,"headers":{"ETag":""}`)
		return map[string]string{FrameHeaderETag: strings.Repeat("a", n-empty-over)}
	}
	at, err := EncodeHTTPFrameHeaders(200, segment(maxFrameHeaderBytes), nil)
	if err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if hdr, _, err := DecodeHTTPFrame(at); err != nil || hdr.Headers[FrameHeaderETag] == "" {
		t.Fatalf("at the cap decoded %v, %v", hdr, err)
	}
	if _, err := EncodeHTTPFrameHeaders(200, segment(maxFrameHeaderBytes+1), nil); !errors.Is(err, ErrFrameHeaderTooLarge) {
		t.Fatalf("over the cap: %v, want ErrFrameHeaderTooLarge", err)
	}
}

// The decoder reads to the cap and no further, whatever produced the frame.
func TestDecodeHTTPFrame_HoldsTheCap(t *testing.T) {
	header := func(n int) string {
		return `{"status":200,"headers":{"ETag":"` + strings.Repeat("a", n-len(`{"status":200,"headers":{"ETag":""}}`)) + `"}}`
	}
	if _, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, header(maxFrameHeaderBytes), nil)); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if _, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, header(maxFrameHeaderBytes+1), nil)); err == nil {
		t.Fatal("over the cap decoded")
	}
}

// An answer header's value the frame's JSON would change is refused where it is
// made: encoded, it would reach the peer as a different validator. Content-Type
// keeps its own rule.
func TestEncodeHTTPFrameHeaders_RefusesAValueItWouldChange(t *testing.T) {
	for _, name := range []string{FrameHeaderETag, FrameHeaderLastModified, FrameHeaderRetryAfter} {
		for _, value := range []string{"caf\xe9", "\xff", "a\r\nSet-Cookie: x", "a\x00b"} {
			if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: value}, nil); !errors.Is(err, ErrFrameHeaderValue) {
				t.Errorf("%s %q: %v, want ErrFrameHeaderValue", name, value, err)
			}
		}
	}
	if _, err := EncodeHTTPFrameHeaders(200, map[string]string{"Content-Type": "caf\xe9"}, nil); err != nil {
		t.Fatalf("Content-Type: %v", err)
	}
}

// The Content-Type encoder holds the same cap.
func TestEncodeHTTPFrame_HoldsTheCap(t *testing.T) {
	room := maxFrameHeaderBytes - len(`{"status":200,"headers":{"Content-Type":""}}`)
	at, err := EncodeHTTPFrame(200, strings.Repeat("a", room), nil)
	if err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if _, _, err := DecodeHTTPFrame(at); err != nil {
		t.Fatalf("at the cap decoded: %v", err)
	}
	if _, err := EncodeHTTPFrame(200, strings.Repeat("a", room+1), nil); !errors.Is(err, ErrFrameHeaderTooLarge) {
		t.Fatalf("over the cap: %v, want ErrFrameHeaderTooLarge", err)
	}
	// The limit is on the bytes the decoder reads: an escaped byte counts as six.
	if _, err := EncodeHTTPFrame(200, strings.Repeat("<", room/6+1), nil); !errors.Is(err, ErrFrameHeaderTooLarge) {
		t.Fatalf("over the cap once escaped: %v, want ErrFrameHeaderTooLarge", err)
	}
}

// FrameCarriesHeaderValue answers whether a frame can carry a value exactly,
// never whether it is a good one. Bytes that are not UTF-8 are legal in a field
// value, but the frame's JSON would replace them, so what it passes round-trips,
// but for an empty value, which the encoder omits.
func TestFrameCarriesHeaderValue(t *testing.T) {
	for value, want := range map[string]bool{
		`"v1"`:         true,
		`W/"v1"`:       true,
		"v1":           true,
		"yesterday":    true,
		"a\tb":         true,
		"a b":          true,
		"caf\xc3\xa9":  true,
		"\xff":         false,
		"caf\xe9":      false,
		"<>&":          true,
		"\\\\":         true,
		"\"":           true,
		"\u2028\u2029": true,
		"\ufffd":       true,
		"\\\\u0041":    true,
		"\U0001f600":   true,
		"":             true,
		"a\r\nSet: x":  false,
		"a\nb":         false,
		"a\rb":         false,
		"a\x00b":       false,
		"a\x1fb":       false,
		"a\x7fb":       false,
	} {
		if got := FrameCarriesHeaderValue(value); got != want {
			t.Errorf("FrameCarriesHeaderValue(%q) = %v, want %v", value, got, want)
		}
		if !want || value == "" {
			continue
		}
		framed, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderETag: value}, nil)
		if err != nil {
			t.Fatalf("encode %q: %v", value, err)
		}
		if hdr, _, err := DecodeHTTPFrame(framed); err != nil || hdr.Headers[FrameHeaderETag] != value {
			t.Errorf("%q round-tripped to %q, %v", value, hdr.Headers[FrameHeaderETag], err)
		}
	}
}
