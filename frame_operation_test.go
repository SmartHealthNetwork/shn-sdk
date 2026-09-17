package shnsdk

import (
	"bytes"
	"slices"
	"testing"
)

// TestFrameOperationHeader_RoundTrip: the operation header is allowlisted, so
// a request frame naming a DTR operation keeps it through encode and decode,
// and the body comes back byte for byte.
func TestFrameOperationHeader_RoundTrip(t *testing.T) {
	body := []byte("{\"resourceType\":\"Parameters\",\r\n\t\"parameter\":[{\"name\":\"context\",\"valueString\":\"a\\u00e9 1.50\"}]}")
	for _, op := range []string{FrameOperationQuestionnairePackage, FrameOperationNextQuestion} {
		framed, err := EncodeHTTPFrameHeaders(200, map[string]string{
			"Content-Type":             "application/fhir+json",
			FrameHeaderContractVersion: ContractPADTR20,
			FrameHeaderOperation:       op,
		}, body)
		if err != nil {
			t.Fatalf("%s: encode: %v", op, err)
		}
		hdr, got, err := DecodeHTTPFrame(framed)
		if err != nil {
			t.Fatalf("%s: decode: %v", op, err)
		}
		if hdr.Headers[FrameHeaderOperation] != op {
			t.Fatalf("operation header lost: %v", hdr.Headers)
		}
		if hdr.Headers[FrameHeaderContractVersion] != ContractPADTR20 || hdr.Headers["Content-Type"] != "application/fhir+json" {
			t.Fatalf("other headers changed: %v", hdr.Headers)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("body changed:\n got %q\nwant %q", got, body)
		}
	}
	if FrameHeaderOperation != "operation" {
		t.Fatalf("FrameHeaderOperation = %q, want the wire name \"operation\"", FrameHeaderOperation)
	}
}

// TestFrameOperationHeader_UnknownStillDropped: widening the allowlist by one
// name keeps every other name out on both sides.
func TestFrameOperationHeader_UnknownStillDropped(t *testing.T) {
	frame := mustEncodeRawFrame(t, `{"status":200,"headers":{"operation":"questionnaire-package","Operation-Id":"x","X-Operation":"y","Content-Type":"application/fhir+json"}}`, []byte(`{}`))
	hdr, _, err := DecodeHTTPFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(hdr.Headers) != 2 || hdr.Headers[FrameHeaderOperation] != "questionnaire-package" || hdr.Headers["Content-Type"] != "application/fhir+json" {
		t.Fatalf("decoded headers = %v, want only Content-Type and operation", hdr.Headers)
	}
	if _, err := EncodeHTTPFrameHeaders(200, map[string]string{"Operation": "questionnaire-package"}, nil); err == nil {
		t.Fatal("a header differing from operation only by case must be refused at encode")
	}
}

// TestSupportsRequestFrameV1Op: only the exact v1op token declares the framed
// operation capability; v1 alone does not.
func TestSupportsRequestFrameV1Op(t *testing.T) {
	if RequestFrameV1Op != "v1op" {
		t.Fatalf("RequestFrameV1Op = %q, want v1op", RequestFrameV1Op)
	}
	cases := []struct {
		frames []string
		want   bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"v1"}, false},
		{[]string{"v1", "v1op"}, true},
		{[]string{"v1op"}, true},
		{[]string{"V1OP"}, false},
		{[]string{"v1op2"}, false},
		{[]string{"v1-op"}, false},
		{[]string{" v1op"}, false},
	}
	for _, c := range cases {
		if got := SupportsRequestFrameV1Op(c.frames); got != c.want {
			t.Errorf("SupportsRequestFrameV1Op(%q) = %v, want %v", c.frames, got, c.want)
		}
	}
	// Declaring v1op does not make a peer look like a plain v1 peer, and a v1
	// peer does not look like a v1op peer.
	if SupportsRequestFrameV1([]string{"v1op"}) {
		t.Error("v1op alone must not satisfy SupportsRequestFrameV1")
	}
}

// TestSupportedRequestFrames_DeclaresFramedOperation: this library builds and
// serves framed DTR operations, so it declares both request-frame
// capabilities, v1 first, and a registration built with it declares them too.
func TestSupportedRequestFrames_DeclaresFramedOperation(t *testing.T) {
	want := []string{RequestFrameV1, RequestFrameV1Op}
	if got := SupportedRequestFrames(); !slices.Equal(got, want) {
		t.Fatalf("SupportedRequestFrames() = %q, want %q", got, want)
	}
	if !SupportsRequestFrameV1(SupportedRequestFrames()) || !SupportsRequestFrameV1Op(SupportedRequestFrames()) {
		t.Fatalf("SupportedRequestFrames() = %q must satisfy both capability checks", SupportedRequestFrames())
	}
	// Each call returns a fresh slice: a caller that edits its copy cannot
	// change what the next registration declares.
	got := SupportedRequestFrames()
	got[0] = "x"
	if again := SupportedRequestFrames(); !slices.Equal(again, want) {
		t.Fatalf("SupportedRequestFrames() after a caller edit = %q, want %q", again, want)
	}

	id, err := GenerateIdentity("h1")
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]RegistrationRequest{
		"Registration":             id.Registration("payer", "https://example.test"),
		"RegistrationWithDeclared": id.RegistrationWithDeclared("payer", "https://example.test", []string{ContractPADTR20}),
	} {
		if !slices.Equal(req.RequestFrames, want) {
			t.Errorf("%s declares requestFrames %q, want %q", name, req.RequestFrames, want)
		}
	}
}
