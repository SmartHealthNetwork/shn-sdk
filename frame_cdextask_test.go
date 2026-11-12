package shnsdk

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

// v1100FrameHeaderNames is the allowlist shn-sdk v1.10.0 decodes: everything
// before the CDex Task's headers.
var v1100FrameHeaderNames = foldedFrameHeaderNames("Content-Type", FrameHeaderContractVersion, FrameHeaderOperation,
	FrameHeaderETag, FrameHeaderLastModified, FrameHeaderRetryAfter,
	FrameHeaderOperationSubpath, FrameHeaderOperationBase, FrameHeaderOperationAddressTags)

// Both headers cross the seal exactly as framed, in any case given: a read's
// path on its empty-bodied request, and a create's location on its 2xx answer.
func TestCDexTaskHeaders_RoundTrip(t *testing.T) {
	for _, row := range []struct {
		status  int
		headers map[string]string
		body    []byte
	}{
		{0, map[string]string{FrameHeaderReadPath: "Task/abc-1.2/_history/3?_format=json&x=%2F"}, nil},
		{201, map[string]string{"Content-Type": "application/fhir+json", FrameHeaderTaskLocation: "Task/abc/_history/1"}, []byte(`{"resourceType":"Task"}`)},
	} {
		status := row.status
		if status == 0 {
			status = 200 // a request frame's status is not read
		}
		frame, err := EncodeHTTPFrameHeaders(status, row.headers, row.body)
		if err != nil {
			t.Fatalf("encode %q: %v", row.headers, err)
		}
		hdr, got, err := DecodeHTTPFrame(frame)
		if err != nil {
			t.Fatalf("decode %q: %v", row.headers, err)
		}
		if !bytes.Equal(got, row.body) {
			t.Fatalf("body %q, want %q", got, row.body)
		}
		for k, v := range row.headers {
			if hdr.Headers[k] != v {
				t.Fatalf("%s = %q, want %q", k, hdr.Headers[k], v)
			}
		}
	}
	frame, err := EncodeHTTPFrameHeaders(200, map[string]string{"READPATH": "Task/1", "TaskLocation": "Task/2"}, nil)
	if err != nil {
		t.Fatalf("encode, another case: %v", err)
	}
	hdr, _, err := DecodeHTTPFrame(frame)
	if err != nil || hdr.Headers[FrameHeaderReadPath] != "Task/1" || hdr.Headers[FrameHeaderTaskLocation] != "Task/2" {
		t.Fatalf("decode, another case: %v %q", err, hdr.Headers)
	}
}

// A path is sent on as it is read, so a value the frame's JSON would change
// is refused where it is made, for both headers.
func TestCDexTaskHeaders_RefuseAValueTheFrameWouldChange(t *testing.T) {
	for _, name := range []string{FrameHeaderReadPath, FrameHeaderTaskLocation} {
		for _, v := range []string{"Task/a\nb", "Task/a\x00b", "Task/a\x7fb", "Task/a\xffb"} {
			if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: v}, nil); !errors.Is(err, ErrFrameHeaderValue) {
				t.Fatalf("%s %q: %v, want ErrFrameHeaderValue", name, v, err)
			}
		}
	}
}

// One header given twice, under two spellings, is refused where it is made,
// and a frame that carries it twice with different values is not read.
func TestCDexTaskHeaders_GivenTwice(t *testing.T) {
	for _, name := range []string{FrameHeaderReadPath, FrameHeaderTaskLocation} {
		if _, err := EncodeHTTPFrameHeaders(200, map[string]string{name: "Task/1", strings.ToUpper(name): "Task/2"}, nil); !errors.Is(err, ErrFrameHeaderDuplicate) {
			t.Fatalf("encode %s twice: %v, want ErrFrameHeaderDuplicate", name, err)
		}
		frame := mustEncodeRawFrame(t, `{"status":200,"headers":{"`+name+`":"Task/1","`+strings.ToUpper(name)+`":"Task/2"}}`, nil)
		if _, _, err := DecodeHTTPFrame(frame); !errors.Is(err, ErrFrameHeaderDuplicate) {
			t.Fatalf("decode %s twice: %v, want ErrFrameHeaderDuplicate", name, err)
		}
	}
}

// A frame from this release read by a release before the CDex Task's
// headers (shn-sdk v1.10.0's allowlist): both are dropped without error, and
// the status, the body and every header that release knows come through
// unchanged.
func TestCDexTaskHeaders_AnOlderDecoderDropsThem(t *testing.T) {
	body := []byte(`{"resourceType":"Task"}`)
	frame, err := EncodeHTTPFrameHeaders(201, map[string]string{
		"Content-Type":          "application/fhir+json",
		FrameHeaderETag:         `W/"1"`,
		FrameHeaderReadPath:     "Task/1",
		FrameHeaderTaskLocation: "Task/1/_history/1",
	}, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	hdr, got, err := decodeHTTPFrame(frame, v1100FrameHeaderNames)
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

// A frame from a release before the CDex Task's headers, read by this one:
// an older encoder's frame is byte for byte what this release encodes for the
// same headers, and it decodes with neither header present.
func TestCDexTaskHeaders_ANewerDecoderReadsAnOlderFrame(t *testing.T) {
	body := []byte(`{"resourceType":"Task"}`)
	older := mustEncodeRawFrame(t, `{"status":201,"headers":{"Content-Type":"application/fhir+json"}}`, body)
	frame, err := EncodeHTTPFrameHeaders(201, map[string]string{"Content-Type": "application/fhir+json"}, body)
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
	for _, name := range []string{FrameHeaderReadPath, FrameHeaderTaskLocation} {
		if v, present := hdr.Headers[name]; present {
			t.Fatalf("%s present (%q) on a frame that never carried it", name, v)
		}
	}
}

// A read's path is read as a Task id, an optional version and an optional
// query, and String gives back the value it was read from.
func TestParseCDexTaskReadPath_Reads(t *testing.T) {
	id64 := strings.Repeat("a", 64)
	for _, row := range []struct {
		in   string
		want CDexTaskPath
	}{
		{"Task/1", CDexTaskPath{ID: "1"}},
		{"Task/" + id64, CDexTaskPath{ID: id64}},
		{"Task/A-z.0/_history/2", CDexTaskPath{ID: "A-z.0", VersionID: "2"}},
		{"Task/...", CDexTaskPath{ID: "..."}},
		{"Task/1?", CDexTaskPath{ID: "1", HasQuery: true}},
		{"Task/1?_format=json&_summary=true", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "_format=json&_summary=true"}},
		{"Task/1/_history/x?a=%2Fb&c=d:e@f/g?h", CDexTaskPath{ID: "1", VersionID: "x", HasQuery: true, RawQuery: "a=%2Fb&c=d:e@f/g?h"}},
		{"Task/1?a=-._~!$&'()*+,;=", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "a=-._~!$&'()*+,;="}},
		// A query is carried exactly as sent, and the provider's system judges
		// it: bytes outside a URI query's grammar, a malformed escape and
		// UTF-8 included.
		{"Task/1?a=|b", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "a=|b"}},
		{"Task/1?a=\"b\"<c>[1]\\^`{}", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "a=\"b\"<c>[1]\\^`{}"}},
		{"Task/1?a=%zz&b=%", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "a=%zz&b=%"}},
		{"Task/1?a=é", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "a=é"}},
		{"Task/1?../../Patient/2", CDexTaskPath{ID: "1", HasQuery: true, RawQuery: "../../Patient/2"}},
	} {
		got, err := ParseCDexTaskReadPath(row.in)
		if err != nil || got != row.want {
			t.Fatalf("ParseCDexTaskReadPath(%q) = %+v, %v; want %+v", row.in, got, err, row.want)
		}
		if got.String() != row.in {
			t.Fatalf("String() = %q, want %q", got.String(), row.in)
		}
	}
}

// Every value that is not a path to a Task by its id is refused, before
// anything is sent: the rejection rows of the read's path.
func TestParseCDexTaskReadPath_Refuses(t *testing.T) {
	for _, in := range []string{
		"",
		"Task",
		"Task/",
		"task/1",
		"/Task/1",
		"Patient/1",
		"Task//1",
		"Task/1/",
		"Task/.",
		"Task/..",
		"Task/../Patient/1",
		"Task/1/_history",
		"Task/1/_history/",
		"Task/1/_history/..",
		"Task/1/_history/2/x",
		"Task/1/x/2",
		"Task/1/_HISTORY/2",
		"Task/" + strings.Repeat("a", 65),
		"Task/1/_history/" + strings.Repeat("a", 65),
		"Task/a_b",
		"Task/a%2Fb",
		"Task/a%2e%2e",
		"Task/a b",
		"Task/é",
		"Task/1#frag",
		"Task/1?a#frag",
		// What cannot be carried as a query.
		"Task/1?a b",
		"Task/1?a\r\nX-Injected: 1",
		"Task/1?a\nb",
		"Task/1?a\rb",
		"Task/1?a\tb",
		"Task/1?a=\x00",
		"Task/1?a=\x1f",
		"Task/1?a=\x7f",
		"Task/1?a=\xff",
		"Task/1?a=\xc3",
	} {
		if got, err := ParseCDexTaskReadPath(in); !errors.Is(err, ErrCDexTaskPath) {
			t.Fatalf("ParseCDexTaskReadPath(%q) = %+v, %v; want ErrCDexTaskPath", in, got, err)
		}
	}
}

// A create's location is read as a Task id and an optional version; it
// carries no query.
func TestParseCDexTaskLocation(t *testing.T) {
	for _, row := range []struct {
		in   string
		want CDexTaskPath
	}{
		{"Task/1", CDexTaskPath{ID: "1"}},
		{"Task/1/_history/1", CDexTaskPath{ID: "1", VersionID: "1"}},
	} {
		got, err := ParseCDexTaskLocation(row.in)
		if err != nil || got != row.want || got.String() != row.in {
			t.Fatalf("ParseCDexTaskLocation(%q) = %+v, %v; want %+v", row.in, got, err, row.want)
		}
	}
	for _, in := range []string{"", "Task/1?", "Task/1?a=b", "Task/..", "Task/1/_history/2?x", "https://provider.example/fhir/Task/1", "Task/1#f"} {
		if got, err := ParseCDexTaskLocation(in); !errors.Is(err, ErrCDexTaskPath) {
			t.Fatalf("ParseCDexTaskLocation(%q) = %+v, %v; want ErrCDexTaskPath", in, got, err)
		}
	}
}

// Only the exact cdexloc token declares the capability.
func TestSupportsRequestFrameCDexLoc(t *testing.T) {
	for _, row := range []struct {
		frames []string
		want   bool
	}{
		{[]string{RequestFrameV1, RequestFrameV1Op, RequestFrameCDexLoc}, true},
		{[]string{RequestFrameCDexLoc}, true},
		{[]string{RequestFrameV1, RequestFrameV1Op, RequestFrameV1OpSub}, false},
		{[]string{"CDEXLOC", "cdexloc ", "cdexlocx", "cdex-loc"}, false},
		{nil, false},
	} {
		if got := SupportsRequestFrameCDexLoc(row.frames); got != row.want {
			t.Fatalf("SupportsRequestFrameCDexLoc(%q) = %v, want %v", row.frames, got, row.want)
		}
	}
}

// SupportedRequestFrames does not list cdexloc: framing the location is the
// answering side's own capability, so a registration built with this library
// does not declare it unless the holder adds it.
func TestSupportedRequestFrames_DoesNotDeclareCDexLoc(t *testing.T) {
	if got := SupportedRequestFrames(); slices.Contains(got, RequestFrameCDexLoc) || SupportsRequestFrameCDexLoc(got) {
		t.Fatalf("SupportedRequestFrames() = %q declares %s", got, RequestFrameCDexLoc)
	}
}
