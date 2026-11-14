package shnsdk

import (
	"errors"
	"testing"
)

// The If-Match a subscriber's system sent on its update or delete crosses the
// seal as sent, in any case given: a frame carries the value, it does not
// judge it as a validator, so a list, "*" and an unquoted value are carried
// too.
func TestIfMatchHeader_RoundTripAsSent(t *testing.T) {
	for _, v := range []string{`W/"3"`, `"3"`, `"a", W/"b"`, "*", "3", "\t\"x\" "} {
		for _, name := range []string{FrameHeaderIfMatch, "if-match", "IF-MATCH", "If-match"} {
			frame, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderSubscriptionRequest: "PUT Subscription/sub-1", name: v}, []byte(`{}`))
			if err != nil {
				t.Fatalf("encode %s %q: %v", name, v, err)
			}
			hdr, _, err := DecodeHTTPFrame(frame)
			if err != nil || hdr.Headers[FrameHeaderIfMatch] != v || len(hdr.Headers) != 2 {
				t.Fatalf("decode %s %q: %v %q", name, v, err, hdr.Headers)
			}
		}
	}
}

// A value the frame's JSON would change is refused where it is made, so the
// recipient never forwards a validator other than the one sent.
func TestIfMatchHeader_RefusesAValueTheFrameWouldChange(t *testing.T) {
	for _, v := range []string{"\"a\"\r\nX-Injected: 1", "\"a\x00\"", "\"a\x7f\"", "\"\xff\""} {
		if _, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderIfMatch: v}, nil); !errors.Is(err, ErrFrameHeaderValue) {
			t.Fatalf("%q: %v, want ErrFrameHeaderValue", v, err)
		}
	}
}
