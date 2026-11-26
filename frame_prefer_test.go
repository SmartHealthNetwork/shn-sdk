package shnsdk

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// v1160FrameHeaderNames is the allowlist shn-sdk v1.16.0 decodes: everything
// before Prefer.
var v1160FrameHeaderNames = foldedFrameHeaderNames("Content-Type", FrameHeaderContractVersion, FrameHeaderOperation,
	FrameHeaderETag, FrameHeaderLastModified, FrameHeaderRetryAfter,
	FrameHeaderOperationSubpath, FrameHeaderOperationBase, FrameHeaderOperationAddressTags,
	FrameHeaderReadPath, FrameHeaderTaskLocation, FrameHeaderCDSService,
	FrameHeaderSubscriptionEndpoint, FrameHeaderSubscriptionChannel, FrameHeaderStatedBase, FrameHeaderSubscriptionRequest,
	FrameHeaderSubscriptionLocation, FrameHeaderIfMatch)

// The return preference a subscriber's system sent crosses the seal as sent,
// in any case given: each value FHIR defines, and any other, since a frame
// carries the value, it does not judge it.
func TestPreferHeader_RoundTripAsSent(t *testing.T) {
	for _, v := range []string{"return=minimal", "return=representation", "return=OperationOutcome",
		"return=other", `return="minimal"`, "RETURN=minimal", "return = minimal"} {
		for _, name := range []string{FrameHeaderPrefer, "prefer", "PREFER", "pReFeR"} {
			frame, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderSubscriptionRequest: "POST Subscription", name: v}, []byte(`{}`))
			if err != nil {
				t.Fatalf("encode %s %q: %v", name, v, err)
			}
			hdr, _, err := DecodeHTTPFrame(frame)
			if err != nil || hdr.Headers[FrameHeaderPrefer] != v || len(hdr.Headers) != 2 {
				t.Fatalf("decode %s %q: %v %q", name, v, err, hdr.Headers)
			}
		}
	}
}

// A frame carries Prefer under its one wire spelling, "Prefer", whatever
// spelling it was given in.
func TestPreferHeader_WireSpelling(t *testing.T) {
	for _, name := range []string{FrameHeaderPrefer, "prefer", "PREFER"} {
		frame, err := EncodeHTTPFrameHeaders(200, map[string]string{name: "return=minimal"}, nil)
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		if !bytes.Contains(frame, []byte(`"Prefer":"return=minimal"`)) {
			t.Fatalf("encode %s: the header segment %q does not spell it Prefer", name, frame)
		}
	}
}

// A value the frame's JSON would change is refused where it is made, so the
// recipient never sends its system a preference other than the one sent.
func TestPreferHeader_RefusesAValueTheFrameWouldChange(t *testing.T) {
	for _, v := range []string{"return=minimal\r\nX-Injected: 1", "return=minimal\x00", "return=\x7f", "return=\xff"} {
		if _, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderPrefer: v}, nil); !errors.Is(err, ErrFrameHeaderValue) {
			t.Fatalf("%q: %v, want ErrFrameHeaderValue", v, err)
		}
	}
}

// Prefer given twice, under two spellings, is refused where it is made, and
// a frame that carries it twice with different values is not read.
func TestPreferHeader_GivenTwice(t *testing.T) {
	if _, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderPrefer: "return=minimal", strings.ToLower(FrameHeaderPrefer): "return=representation"}, nil); !errors.Is(err, ErrFrameHeaderDuplicate) {
		t.Fatalf("encode twice: %v, want ErrFrameHeaderDuplicate", err)
	}
	frame := mustEncodeRawFrame(t, `{"status":200,"headers":{"Prefer":"return=minimal","prefer":"return=representation"}}`, nil)
	if _, _, err := DecodeHTTPFrame(frame); !errors.Is(err, ErrFrameHeaderDuplicate) {
		t.Fatalf("decode twice: %v, want ErrFrameHeaderDuplicate", err)
	}
}

// A decoder before shn-sdk v1.17.0 drops Prefer without error and reads the
// rest of the frame as before, so its gateway sends its system no preference,
// as every gateway did before.
func TestPreferHeader_OlderDecoderDropsIt(t *testing.T) {
	body := []byte(`{"resourceType":"Subscription"}`)
	frame, err := EncodeHTTPFrameHeaders(200, map[string]string{
		"Content-Type":                  "application/fhir+json",
		FrameHeaderSubscriptionRequest:  "POST Subscription",
		FrameHeaderSubscriptionEndpoint: "a.b",
		FrameHeaderPrefer:               "return=representation",
	}, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	hdr, got, err := decodeHTTPFrame(frame, v1160FrameHeaderNames)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("older decode: %v %q", err, got)
	}
	want := map[string]string{"Content-Type": "application/fhir+json", FrameHeaderSubscriptionRequest: "POST Subscription", FrameHeaderSubscriptionEndpoint: "a.b"}
	if len(hdr.Headers) != len(want) {
		t.Fatalf("older decode kept %q, want %q", hdr.Headers, want)
	}
	for k, v := range want {
		if hdr.Headers[k] != v {
			t.Fatalf("older decode: %s = %q, want %q", k, hdr.Headers[k], v)
		}
	}
}
