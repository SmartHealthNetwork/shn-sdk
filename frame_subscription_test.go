package shnsdk

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// v1110FrameHeaderNames is the allowlist shn-sdk v1.11.0 decodes: everything
// before a topic Subscription's headers.
var v1110FrameHeaderNames = foldedFrameHeaderNames("Content-Type", FrameHeaderContractVersion, FrameHeaderOperation,
	FrameHeaderETag, FrameHeaderLastModified, FrameHeaderRetryAfter,
	FrameHeaderOperationSubpath, FrameHeaderOperationBase, FrameHeaderOperationAddressTags,
	FrameHeaderReadPath, FrameHeaderTaskLocation, FrameHeaderCDSService)

var subscriptionFrameHeaders = []string{FrameHeaderSubscriptionEndpoint, FrameHeaderSubscriptionChannel, FrameHeaderStatedBase, FrameHeaderSubscriptionRequest, FrameHeaderSubscriptionLocation, FrameHeaderIfMatch}

// Each header crosses the seal exactly as framed, in any case given: the
// sealed values on a subscribe request, two sealed endpoints on a search in
// a key overlap, the notifier's base on a 2xx answer.
func TestSubscriptionHeaders_RoundTrip(t *testing.T) {
	for _, row := range []struct {
		status  int
		headers map[string]string
		body    []byte
	}{
		{200, map[string]string{FrameHeaderSubscriptionEndpoint: "ocurkBVBmv4.NDl-amuZ2jPy_kJWQ", FrameHeaderSubscriptionChannel: "ocurkBVBmv4.oKGio6Sl-_"}, []byte(`{"resourceType":"Subscription"}`)},
		{200, map[string]string{FrameHeaderSubscriptionEndpoint: "ocurkBVBmv4.NDl-amuZ BSbP4XLp-HK.8K1q4rqF"}, nil},
		{200, map[string]string{"Content-Type": "application/fhir+json", FrameHeaderStatedBase: "https://payer.example/fhir"}, []byte(`{"resourceType":"Bundle"}`)},
		{201, map[string]string{FrameHeaderStatedBase: "https://provider.example/fhir", FrameHeaderSubscriptionLocation: "Subscription/sub-1/_history/1"}, []byte(`{"resourceType":"Subscription"}`)},
	} {
		frame, err := EncodeHTTPFrameHeaders(row.status, row.headers, row.body)
		if err != nil {
			t.Fatalf("encode %q: %v", row.headers, err)
		}
		hdr, got, err := DecodeHTTPFrame(frame)
		if err != nil {
			t.Fatalf("decode %q: %v", row.headers, err)
		}
		if !bytes.Equal(got, row.body) || len(hdr.Headers) != len(row.headers) {
			t.Fatalf("decoded %q %q, want %q %q", hdr.Headers, got, row.headers, row.body)
		}
		for k, v := range row.headers {
			if hdr.Headers[k] != v {
				t.Fatalf("%s = %q, want %q", k, hdr.Headers[k], v)
			}
		}
	}
	frame, err := EncodeHTTPFrameHeaders(200, map[string]string{"SUBSCRIPTIONENDPOINT": "a.b", "Subscriptionchannel": "c.d", "statedbase": "https://x"}, nil)
	if err != nil {
		t.Fatalf("encode, another case: %v", err)
	}
	hdr, _, err := DecodeHTTPFrame(frame)
	if err != nil || hdr.Headers[FrameHeaderSubscriptionEndpoint] != "a.b" || hdr.Headers[FrameHeaderSubscriptionChannel] != "c.d" || hdr.Headers[FrameHeaderStatedBase] != "https://x" {
		t.Fatalf("decode, another case: %v %q", err, hdr.Headers)
	}
}

// A sealed value is read byte for byte, so a value the frame's JSON would
// change is refused where it is made, for each header.
func TestSubscriptionHeaders_RefuseAValueTheFrameWouldChange(t *testing.T) {
	for _, name := range subscriptionFrameHeaders {
		for _, v := range []string{"a.b\nc", "a.b\x00", "a.b\x7f", "a.\xffb"} {
			if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: v}, nil); !errors.Is(err, ErrFrameHeaderValue) {
				t.Fatalf("%s %q: %v, want ErrFrameHeaderValue", name, v, err)
			}
		}
	}
}

// One header given twice, under two spellings, is refused where it is made,
// and a frame that carries it twice with different values is not read.
func TestSubscriptionHeaders_GivenTwice(t *testing.T) {
	for _, name := range subscriptionFrameHeaders {
		if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: "a.b", strings.ToUpper(name): "c.d"}, nil); !errors.Is(err, ErrFrameHeaderDuplicate) {
			t.Fatalf("encode %s twice: %v, want ErrFrameHeaderDuplicate", name, err)
		}
		frame := mustEncodeRawFrame(t, `{"status":200,"headers":{"`+name+`":"a.b","`+strings.ToUpper(name)+`":"c.d"}}`, nil)
		if _, _, err := DecodeHTTPFrame(frame); !errors.Is(err, ErrFrameHeaderDuplicate) {
			t.Fatalf("decode %s twice: %v, want ErrFrameHeaderDuplicate", name, err)
		}
	}
}

// A frame from this release read by a release before these headers (shn-sdk
// v1.11.0's allowlist): all six are dropped without error, and the status,
// the body and every header that release knows come through unchanged.
func TestSubscriptionHeaders_AnOlderDecoderDropsThem(t *testing.T) {
	body := []byte(`{"resourceType":"Subscription"}`)
	frame, err := EncodeHTTPFrameHeaders(201, map[string]string{
		"Content-Type":                  "application/fhir+json",
		FrameHeaderETag:                 `W/"1"`,
		FrameHeaderSubscriptionEndpoint: "a.b",
		FrameHeaderSubscriptionChannel:  "c.d",
		FrameHeaderStatedBase:           "https://payer.example/fhir",
		FrameHeaderSubscriptionRequest:  "PUT Subscription/sub-1",
		FrameHeaderSubscriptionLocation: "Subscription/sub-1/_history/4",
		FrameHeaderIfMatch:              `W/"3"`,
	}, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	hdr, got, err := decodeHTTPFrame(frame, v1110FrameHeaderNames)
	if err != nil {
		t.Fatalf("older decode: %v", err)
	}
	if hdr.Status != 201 || !bytes.Equal(got, body) {
		t.Fatalf("older decode: status %d body %q", hdr.Status, got)
	}
	want := map[string]string{"Content-Type": "application/fhir+json", FrameHeaderETag: `W/"1"`}
	if len(hdr.Headers) != len(want) {
		t.Fatalf("older decode kept %q, want %q", hdr.Headers, want)
	}
	for k, v := range want {
		if hdr.Headers[k] != v {
			t.Fatalf("older decode: %s = %q, want %q", k, hdr.Headers[k], v)
		}
	}
}

// A frame from a release before these headers, read by this one: an older
// encoder's frame is byte for byte what this release encodes for the same
// headers, and it decodes with none of the six present.
func TestSubscriptionHeaders_ANewerDecoderReadsAnOlderFrame(t *testing.T) {
	body := []byte(`{"resourceType":"Subscription"}`)
	older := mustEncodeRawFrame(t, `{"status":201,"headers":{"Content-Type":"application/fhir+json","cdsService":"s"}}`, body)
	frame, err := EncodeHTTPFrameHeaders(201, map[string]string{"Content-Type": "application/fhir+json", FrameHeaderCDSService: "s"}, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(frame, older) {
		t.Fatalf("this release encodes\n%q\nfor the older release's frame\n%q", frame, older)
	}
	hdr, got, err := DecodeHTTPFrame(older)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("decode: %v %q", err, got)
	}
	for _, name := range subscriptionFrameHeaders {
		if v, present := hdr.Headers[name]; present {
			t.Fatalf("%s present (%q) on a frame that never carried it", name, v)
		}
	}
}

// statedBase is also a request-frame header, on a signal leg: it crosses the
// seal on a request frame exactly as framed, in any case given,
// beside the sealed values a notification carries. The codec reads no shape
// into it, so a value the gateway would refuse is still carried as framed.
func TestStatedBase_OnASignalRequestFrame(t *testing.T) {
	body := []byte(`{"resourceType":"Bundle","type":"history"}`)
	for _, base := range []string{"https://provider.example/fhir", "https://provider.example:8443/r4/tenant-1", "https://provider.example/fhir/"} {
		for _, name := range []string{FrameHeaderStatedBase, "statedbase", "STATEDBASE", "StatedBase"} {
			frame, err := EncodeHTTPFrameHeaders(200, map[string]string{ // a request frame's status is not read
				"Content-Type":                  "application/fhir+json",
				FrameHeaderSubscriptionEndpoint: "ocurkBVBmv4.NDl-amuZ2jPy_kJWQ",
				FrameHeaderSubscriptionChannel:  "ocurkBVBmv4.oKGio6Sl-_",
				name:                            base,
			}, body)
			if err != nil {
				t.Fatalf("encode %s %q: %v", name, base, err)
			}
			hdr, got, err := DecodeHTTPFrame(frame)
			if err != nil || hdr.Status != 200 || !bytes.Equal(got, body) || len(hdr.Headers) != 4 || hdr.Headers[FrameHeaderStatedBase] != base {
				t.Fatalf("decode %s %q: %v %d %q %q", name, base, err, hdr.Status, hdr.Headers, got)
			}
		}
	}
}

// A signal leg's request frame read by a release before these headers
// (shn-sdk v1.11.0's allowlist): statedBase and the sealed values are dropped
// without error, and the body and Content-Type come through unchanged.
func TestStatedBase_AnOlderDecoderDropsItFromARequestFrame(t *testing.T) {
	body := []byte(`{"resourceType":"Bundle","type":"history"}`)
	frame, err := EncodeHTTPFrameHeaders(200, map[string]string{ // a request frame's status is not read
		"Content-Type":                  "application/fhir+json",
		FrameHeaderSubscriptionEndpoint: "a.b",
		FrameHeaderSubscriptionChannel:  "c.d",
		FrameHeaderStatedBase:           "https://provider.example/fhir",
	}, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	hdr, got, err := decodeHTTPFrame(frame, v1110FrameHeaderNames)
	if err != nil || hdr.Status != 200 || !bytes.Equal(got, body) || len(hdr.Headers) != 1 || hdr.Headers["Content-Type"] != "application/fhir+json" {
		t.Fatalf("older decode: %v %d %q %q", err, hdr.Status, hdr.Headers, got)
	}
}
