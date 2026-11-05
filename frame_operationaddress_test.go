package shnsdk

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

const testAddressTag = "AbCdEfGhIjKlMnOpQrStUv"

// operationAddressHeaders is a framed DTR operation's address headers, as a
// request and an answer frame carry them, with the operation's own headers.
func operationAddressHeaders() map[string]string {
	return map[string]string{
		FrameHeaderContractVersion:      "pa.dtr@2.2",
		FrameHeaderOperation:            FrameOperationNextQuestion,
		FrameHeaderOperationSubpath:     "/payer/2.2/" + testAddressTag + "/forms/a-b",
		FrameHeaderOperationBase:        "https://payer.example/fhir/dtr",
		FrameHeaderOperationAddressTags: "/forms/a-b=" + testAddressTag + " =" + testAddressTag + " *=" + testAddressTag,
	}
}

// foldedFrameHeaderNames is the decoder's name map for an allowlist.
func foldedFrameHeaderNames(allowlist ...string) map[string]string {
	m := make(map[string]string, len(allowlist))
	for _, k := range allowlist {
		m[asciiLower(k)] = k
	}
	return m
}

// v170FrameHeaderNames is the allowlist shn-sdk v1.7.0 decodes: everything
// before the operation's address headers.
var v170FrameHeaderNames = foldedFrameHeaderNames("Content-Type", FrameHeaderContractVersion, FrameHeaderOperation,
	FrameHeaderETag, FrameHeaderLastModified, FrameHeaderRetryAfter)

// The address headers cross the seal exactly as framed, in any case given, and
// the body comes back byte for byte.
func TestOperationAddressHeaders_RoundTrip(t *testing.T) {
	body := []byte(`{"resourceType":"QuestionnaireResponse"}`)
	in := operationAddressHeaders()
	frame, err := EncodeHTTPFrameHeaders(200, in, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	hdr, got, err := DecodeHTTPFrame(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body %q, want %q", got, body)
	}
	for k, v := range in {
		if hdr.Headers[k] != v {
			t.Fatalf("%s = %q, want %q", k, hdr.Headers[k], v)
		}
	}
	// A name in another case is framed under the allowlist's spelling.
	frame, err = EncodeHTTPFrameHeaders(200, map[string]string{"OPERATIONBASE": "https://payer.example/fhir", "operationsubpath": "/fetch-server/2.2/" + testAddressTag}, nil)
	if err != nil {
		t.Fatalf("encode, another case: %v", err)
	}
	hdr, _, err = DecodeHTTPFrame(frame)
	if err != nil || hdr.Headers[FrameHeaderOperationBase] != "https://payer.example/fhir" || hdr.Headers[FrameHeaderOperationSubpath] != "/fetch-server/2.2/"+testAddressTag {
		t.Fatalf("decode, another case: %v %q", err, hdr.Headers)
	}
}

// A tag is read byte for byte, so a value the frame's JSON would change is
// refused where it is made, for each of the three headers.
func TestOperationAddressHeaders_RefuseAValueTheFrameWouldChange(t *testing.T) {
	for _, name := range []string{FrameHeaderOperationSubpath, FrameHeaderOperationBase, FrameHeaderOperationAddressTags} {
		for _, v := range []string{"a\nb", "a\x00b", "a\x7fb", "a\xffb"} {
			if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: v}, nil); !errors.Is(err, ErrFrameHeaderValue) {
				t.Fatalf("%s %q: %v, want ErrFrameHeaderValue", name, v, err)
			}
		}
	}
}

// A frame from this release read by a release before the address headers
// (shn-sdk v1.7.0's allowlist): the three are dropped without error, and the
// status, the body and every header that release knows come through unchanged.
func TestOperationAddressHeaders_AnOlderDecoderDropsThem(t *testing.T) {
	body := []byte(`{"resourceType":"Parameters"}`)
	frame, err := EncodeHTTPFrameHeaders(200, operationAddressHeaders(), body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	hdr, got, err := decodeHTTPFrame(frame, v170FrameHeaderNames)
	if err != nil {
		t.Fatalf("older decode: %v", err)
	}
	if hdr.Status != 200 || !bytes.Equal(got, body) {
		t.Fatalf("older decode: status %d body %q", hdr.Status, got)
	}
	want := map[string]string{FrameHeaderContractVersion: "pa.dtr@2.2", FrameHeaderOperation: FrameOperationNextQuestion}
	if len(hdr.Headers) != len(want) {
		t.Fatalf("older decode kept %q, want %q", hdr.Headers, want)
	}
	for k, v := range want {
		if hdr.Headers[k] != v {
			t.Fatalf("older decode: %s = %q, want %q", k, hdr.Headers[k], v)
		}
	}
}

// A frame from a release before the address headers, read by this one: an
// older encoder's frame is byte for byte what this release encodes for the
// same headers, and it decodes with no address header present.
func TestOperationAddressHeaders_ANewerDecoderReadsAnOlderFrame(t *testing.T) {
	body := []byte(`{"resourceType":"Parameters"}`)
	older := mustEncodeRawFrame(t, `{"status":200,"headers":{"contractVersion":"pa.dtr@2.2","operation":"next-question"}}`, body)
	frame, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderContractVersion: "pa.dtr@2.2", FrameHeaderOperation: FrameOperationNextQuestion}, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(frame, older) {
		t.Fatalf("this release encodes\n%q\nfor the older release's frame\n%q", frame, older)
	}
	hdr, got, err := DecodeHTTPFrame(older)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, body) || hdr.Headers[FrameHeaderOperation] != FrameOperationNextQuestion {
		t.Fatalf("decode: %q %q", hdr.Headers, got)
	}
	for _, name := range []string{FrameHeaderOperationSubpath, FrameHeaderOperationBase, FrameHeaderOperationAddressTags} {
		if v, present := hdr.Headers[name]; present {
			t.Fatalf("%s present (%q) on a frame that never carried it", name, v)
		}
	}
}

// Only the exact v1opsub token declares the capability; v1op, which every
// registration this library builds declares, does not.
func TestSupportsRequestFrameV1OpSub(t *testing.T) {
	for _, row := range []struct {
		frames []string
		want   bool
	}{
		{[]string{RequestFrameV1, RequestFrameV1Op, RequestFrameV1OpSub}, true},
		{[]string{RequestFrameV1OpSub}, true},
		{[]string{RequestFrameV1, RequestFrameV1Op}, false},
		{[]string{"V1OPSUB", "v1opsub ", "v1opsubx", "v1op-sub"}, false},
		{nil, false},
	} {
		if got := SupportsRequestFrameV1OpSub(row.frames); got != row.want {
			t.Fatalf("SupportsRequestFrameV1OpSub(%q) = %v, want %v", row.frames, got, row.want)
		}
	}
}

// SupportedRequestFrames does not list v1opsub: the codec alone does not read
// the subpath for a holder, so a registration built with this library does not
// declare it unless the holder adds it.
func TestSupportedRequestFrames_DoesNotDeclareV1OpSub(t *testing.T) {
	if got := SupportedRequestFrames(); slices.Contains(got, RequestFrameV1OpSub) || SupportsRequestFrameV1OpSub(got) {
		t.Fatalf("SupportedRequestFrames() = %q declares %s", got, RequestFrameV1OpSub)
	}
}
