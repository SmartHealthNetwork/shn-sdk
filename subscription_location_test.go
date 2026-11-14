package shnsdk

import (
	"errors"
	"strings"
	"testing"
)

// Each form is read: a Subscription's path by its id, and by its id and
// version.
func TestParseSubscriptionLocation_Reads(t *testing.T) {
	id64 := strings.Repeat("a", 64)
	for _, row := range []struct{ in, id, vid string }{
		{"Subscription/sub-1", "sub-1", ""},
		{"Subscription/A-z.0", "A-z.0", ""},
		{"Subscription/" + id64, id64, ""},
		{"Subscription/...", "...", ""},
		{"Subscription/sub-1/_history/1", "sub-1", "1"},
		{"Subscription/sub-1/_history/" + id64, "sub-1", id64},
		{"Subscription/a/_history/b.c-2", "a", "b.c-2"},
	} {
		id, vid, err := ParseSubscriptionLocation(row.in)
		if err != nil || id != row.id || vid != row.vid {
			t.Fatalf("ParseSubscriptionLocation(%q) = %q, %q, %v; want %q, %q", row.in, id, vid, err, row.id, row.vid)
		}
	}
}

// Anything else is refused, as ErrSubscriptionLocation, and names nothing:
// the rejection rows of the header.
func TestParseSubscriptionLocation_Refuses(t *testing.T) {
	for _, in := range []string{
		// Empty, or not a Subscription's path.
		"", "Subscription", "Subscription/", "subscription/sub-1", "SUBSCRIPTION/sub-1", "Task/1", "Subscriptions/1", "Subscription1",
		// An absolute URL, or a leading slash.
		"https://provider.example/fhir/Subscription/sub-1", "/Subscription/sub-1", "//Subscription/sub-1", " Subscription/sub-1",
		// Ids and versions that are not FHIR ids, or are dot segments.
		"Subscription/.", "Subscription/..", "Subscription/a_b", "Subscription/é", "Subscription/" + strings.Repeat("a", 65),
		"Subscription/a%2Fb", "Subscription/sub-1/_history/.", "Subscription/sub-1/_history/..", "Subscription/sub-1/_history/a_b",
		"Subscription/sub-1/_history/" + strings.Repeat("a", 65), "Subscription/sub-1/_history/", "Subscription/./_history/1",
		// Another segment.
		"Subscription/sub-1/", "Subscription/sub-1/_history", "Subscription/sub-1/_history/1/", "Subscription/sub-1/_history/1/x",
		"Subscription/sub-1/$status", "Subscription/sub-1/x/1", "Subscription//sub-1", "Subscription/sub-1/_HISTORY/1",
		"Subscription/sub-1/2", "Subscription/sub-1/x", "Subscription/sub-1/history", "Subscription/sub-1/_history.1",
		// A query or a fragment.
		"Subscription/sub-1?_elements=channel", "Subscription/sub-1?", "Subscription/sub-1/_history/1?x=1",
		"Subscription/sub-1#f", "Subscription/sub-1/_history/1#", "Subscription/sub-1\n",
	} {
		id, vid, err := ParseSubscriptionLocation(in)
		if !errors.Is(err, ErrSubscriptionLocation) || id != "" || vid != "" {
			t.Fatalf("ParseSubscriptionLocation(%q) = %q, %q, %v; want ErrSubscriptionLocation", in, id, vid, err)
		}
	}
}

// The header crosses the seal exactly as framed, in any case given.
func TestSubscriptionLocationHeader_RoundTrip(t *testing.T) {
	const v = "Subscription/sub-1/_history/2"
	for _, name := range []string{FrameHeaderSubscriptionLocation, "SubscriptionLocation", "SUBSCRIPTIONLOCATION"} {
		frame, err := EncodeHTTPFrameHeaders(201, map[string]string{name: v}, []byte(`{"resourceType":"Subscription"}`))
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		hdr, _, err := DecodeHTTPFrame(frame)
		if err != nil || hdr.Status != 201 || hdr.Headers[FrameHeaderSubscriptionLocation] != v || len(hdr.Headers) != 1 {
			t.Fatalf("decode %s: %v %d %q", name, err, hdr.Status, hdr.Headers)
		}
	}
}

// FuzzParseSubscriptionLocation: on any value, ParseSubscriptionLocation
// either refuses with ErrSubscriptionLocation, or reads FHIR ids that write
// the value back byte for byte.
func FuzzParseSubscriptionLocation(f *testing.F) {
	for _, seed := range []string{"Subscription/sub-1", "Subscription/a/_history/2", "Subscription/..", "Subscription/a/_history/", "https://x/Subscription/a", "Subscription/a?x=1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		id, vid, err := ParseSubscriptionLocation(v)
		if err != nil {
			if !errors.Is(err, ErrSubscriptionLocation) || id != "" || vid != "" {
				t.Fatalf("%q: %q, %q, %v", v, id, vid, err)
			}
			return
		}
		back := "Subscription/" + id
		if vid != "" {
			back += "/_history/" + vid
		}
		if back != v || !isFHIRPathID(id) || (vid != "" && !isFHIRPathID(vid)) {
			t.Fatalf("%q read as %q, %q", v, id, vid)
		}
	})
}
