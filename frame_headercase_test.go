package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// caseVariants are the spellings of an allowlisted name a producer or a peer
// may use: the allowlist's own, Go's canonical form, all lower and all upper.
func caseVariants(name string) []string {
	return slices.Compact(slices.Sorted(slices.Values([]string{
		name, http.CanonicalHeaderKey(name), strings.ToLower(name), strings.ToUpper(name),
	})))
}

// A header field name is case-insensitive (RFC 9110 section 5.1). Every
// allowlisted header is framed in any case under the allowlist's spelling, the
// one spelling on the wire, and decodes, in any case, back to it.
func TestFrameHeaders_AnyCaseIsTheAllowlistedName(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(allowedFrameHeaders)) {
		for _, k := range caseVariants(name) {
			t.Run(name+"/"+k, func(t *testing.T) {
				frame, err := EncodeHTTPFrameHeaders(200, map[string]string{k: "v1"}, nil)
				if err != nil {
					t.Fatalf("encode %q: %v", k, err)
				}
				if !bytes.Contains(frame, []byte(`"`+name+`":"v1"`)) {
					t.Fatalf("encode %q did not frame it as %q: %q", k, name, frame)
				}
				if k != name && bytes.Contains(frame, []byte(`"`+k+`"`)) {
					t.Fatalf("encode %q put its own spelling on the wire: %q", k, frame)
				}
				hdr, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, `{"status":200,"headers":{"`+k+`":"v2"}}`, nil))
				if err != nil {
					t.Fatalf("decode %q: %v", k, err)
				}
				if len(hdr.Headers) != 1 || hdr.Headers[name] != "v2" {
					t.Fatalf("decode %q = %v, want only %s", k, hdr.Headers, name)
				}
			})
		}
	}
	// The three answer headers: the constant, Go's canonical form, lower and
	// upper case.
	for _, k := range []string{"ETag", "Etag", "etag", "ETAG"} {
		hdr, _, err := DecodeHTTPFrame(mustEncode(t, map[string]string{k: `"a1"`}))
		if err != nil || hdr.Headers[FrameHeaderETag] != `"a1"` {
			t.Fatalf("%s: %v, %v; want ETag carried", k, hdr.Headers, err)
		}
	}
}

// A header off the allowlist stays off in any case: refused at encode, not
// carried at decode. Only ASCII letters fold: a name that matches an
// allowlisted one only under Unicode folding (the long s, U+017F, folds to s)
// is not that header. A fold that also changed the ASCII symbols next to the
// letters (@ [ \ ] ^ _) would pass these rows: no allowlisted name holds one
// of those or what they fold to, so it cannot make a name match, and no row
// can tell it apart.
func TestFrameHeaders_OffTheAllowlistInAnyCase(t *testing.T) {
	for _, k := range []string{"location", "LOCATION", "set-cookie", "Set-Cookie", "AUTHORIZATION", "x-etag", "ETag2", "Laſt-Modified", "Content-Type "} {
		t.Run(k, func(t *testing.T) {
			if _, err := EncodeHTTPFrameHeaders(200, map[string]string{k: "x"}, nil); err == nil {
				t.Fatal("encoded")
			}
			hdr, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, `{"status":200,"headers":{"`+k+`":"x"}}`, nil))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(hdr.Headers) != 0 {
				t.Fatalf("decoded %v", hdr.Headers)
			}
		})
	}
}

// One header given twice, under one spelling or two, is the frame's own
// framing gone wrong. A producer that gives it twice is refused; a frame that
// carries it twice with different values cannot say which it means, and is not
// read (ErrFrameHeaderDuplicate, a failure of the frame's integrity). The same
// value twice is that value.
func TestFrameHeaders_OneHeaderGivenTwice(t *testing.T) {
	if _, err := EncodeHTTPFrameHeaders(200, map[string]string{"ETag": `"a"`, "etag": `"a"`}, nil); !errors.Is(err, ErrFrameHeaderDuplicate) {
		t.Fatalf("encode: %v, want ErrFrameHeaderDuplicate", err)
	}
	if _, err := EncodeHTTPFrameHeaders(200, map[string]string{"Content-Type": "application/json", "content-type": ""}, nil); !errors.Is(err, ErrFrameHeaderDuplicate) {
		t.Fatalf("encode: %v; an empty second spelling is still a second spelling", err)
	}
	for _, seg := range []string{
		`{"status":200,"headers":{"ETag":"\"a\"","etag":"\"b\""}}`,             // two spellings
		`{"status":200,"headers":{"ETag":"\"a\"","ETag":"\"b\""}}`,             // one spelling twice
		`{"status":200,"headers":{"ETag":"\"a\""},"Headers":{"etag":"\"b\""}}`, // the headers object twice
	} {
		if _, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, seg, nil)); !errors.Is(err, ErrFrameHeaderDuplicate) {
			t.Fatalf("decode %s: %v, want ErrFrameHeaderDuplicate", seg, err)
		}
	}
	for _, seg := range []string{
		`{"status":200,"headers":{"ETag":"\"a\"","etag":"\"a\""}}`,
		`{"status":200,"headers":{"ETag":"\"a\"","ETag":"\"a\""}}`,
	} {
		hdr, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, seg, nil))
		if err != nil || len(hdr.Headers) != 1 || hdr.Headers[FrameHeaderETag] != `"a"` {
			t.Fatalf("decode same value %s: %v, %v", seg, hdr.Headers, err)
		}
	}
	// An off-list header given twice is dropped, not judged.
	hdr, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, `{"status":200,"headers":{"Location":"a","location":"b","ETag":"\"c\""}}`, nil))
	if err != nil || len(hdr.Headers) != 1 || hdr.Headers[FrameHeaderETag] != `"c"` {
		t.Fatalf("decode off-list twice: %v, %v", hdr.Headers, err)
	}
	if hdr, _, err := DecodeHTTPFrame(mustEncodeRawFrame(t, `{"status":200,"headers":null}`, nil)); err != nil || hdr.Headers != nil {
		t.Fatalf("decode null headers: %v, %v", hdr.Headers, err)
	}
	// The encoder names the first two spellings it meets, in order.
	_, err = EncodeHTTPFrameHeaders(200, map[string]string{"ETag": `"a"`, "etag": `"a"`, "ETAG": `"a"`}, nil)
	if err == nil || !strings.HasSuffix(err.Error(), `ETag, as "ETAG" and "ETag"`) {
		t.Fatalf("encode three spellings: %v", err)
	}
}

// The headers a decoder reads are the ones json.Unmarshal reads: every key that
// names the headers field, in any case (the long s folds to s there too), and a
// null one empties what came before it, as it sets the map to nil.
func TestFrameHeaders_ReadAsUnmarshalReadsThem(t *testing.T) {
	for _, c := range []struct {
		seg  string
		want map[string]string
	}{
		{`{"status":200,"headers":null,"headers":{"ETag":"\"a\""}}`, map[string]string{FrameHeaderETag: `"a"`}},
		{`{"status":200,"headers":{"ETag":"\"a\""},"headers":null,"headers":{}}`, map[string]string{}},
		{`{"status":200,"headers":{"ETag":"\"a\""},"headers":null,"headers":{"etag":"\"b\""}}`, map[string]string{FrameHeaderETag: `"b"`}},
		{`{"status":200,"headers":{"ETag":"\"a\""},"headers":null}`, nil},
		{`{"status":200,"heade\u0072s":{"ETag":"\"a\""}}`, map[string]string{FrameHeaderETag: `"a"`}},
		{`{"status":200,"header\u017f":{"ETag":"\"a\""}}`, map[string]string{FrameHeaderETag: `"a"`}},
	} {
		var plain HTTPFrameHeader
		raw := mustEncodeRawFrame(t, c.seg, nil)
		if err := json.Unmarshal([]byte(c.seg), &plain); err != nil {
			t.Fatal(err)
		}
		hdr, _, err := DecodeHTTPFrame(raw)
		if err != nil {
			t.Fatalf("decode %s: %v", c.seg, err)
		}
		if !maps.Equal(hdr.Headers, c.want) || (hdr.Headers == nil) != (c.want == nil) || (plain.Headers == nil) != (c.want == nil) || len(plain.Headers) != len(c.want) {
			t.Fatalf("decode %s = %#v, Unmarshal %#v; want %#v", c.seg, hdr.Headers, plain.Headers, c.want)
		}
	}
}

// No two allowlisted names fold to one: each spelling the fold maps to is the
// one allowlisted name it stands for.
func TestFrameHeaders_AllowlistFoldsApart(t *testing.T) {
	if len(frameHeaderNames) != len(allowedFrameHeaders) {
		t.Fatalf("%d folded names for %d allowlisted headers", len(frameHeaderNames), len(allowedFrameHeaders))
	}
}

func mustEncode(t *testing.T, headers map[string]string) []byte {
	t.Helper()
	frame, err := EncodeHTTPFrameHeaders(200, headers, nil)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}
