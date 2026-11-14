package shnsdk

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// FuzzParseSubscriptionRequest: on any value, ParseSubscriptionRequest either
// refuses with an error that wraps ErrSubscriptionRequest, or reads a value
// that String gives back byte for byte, that reads again the same, and whose
// search carries no url in any case.
func FuzzParseSubscriptionRequest(f *testing.F) {
	for _, seed := range []string{
		"POST Subscription", "GET Subscription/sub-1", "GET Subscription/a/_history/2", "PUT Subscription/a",
		"DELETE Subscription/a", "GET Subscription/a/$status", "GET Subscription?", "GET Subscription?status=requested,active",
		"GET Subscription?%75rl=x", "GET Subscription?URL=x", "GET ?_getpages=abc&_getpagesoffset=20&_count=20&_bundletype=searchset",
		"GET Subscription?_id=a%2Cb&_count=1&_elements=channel", "GET Subscription?status=a%zz",
		"GET Subscription/a?_elements=channel&_summary=true", "GET Subscription/a/_history/1?_summary=false", "GET Subscription/a?_count=1",
		"GET ?_getpages=abc&_bundletype=searchset&_elements=channel,status",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		r, err := ParseSubscriptionRequest(v)
		if err != nil {
			if !errors.Is(err, ErrSubscriptionRequest) {
				t.Fatalf("%q: %v does not wrap ErrSubscriptionRequest", v, err)
			}
			return
		}
		if r.String() != v {
			t.Fatalf("%q reads back as %q", v, r.String())
		}
		again, err := ParseSubscriptionRequest(r.String())
		if err != nil || !reflect.DeepEqual(again, r) {
			t.Fatalf("%q reads again as %+v, %v", v, again, err)
		}
		if r.Interaction == SubscriptionSearch {
			for _, p := range r.Params {
				if base, _, _ := strings.Cut(p.Name, ":"); strings.EqualFold(base, "url") {
					t.Fatalf("%q: a frame's search carries %q", v, p.Name)
				}
			}
		}
	})
}

// FuzzParseSubscriptionSearch: on any raw query, ParseSubscriptionSearch
// either refuses with ErrSubscriptionRequestQuery or
// ErrSubscriptionSearchOneEndpoint, or reads one url naming one absolute http
// or https endpoint with no backslash, and a frame value with no url that
// ParseSubscriptionRequest reads as the same parameters.
func FuzzParseSubscriptionSearch(f *testing.F) {
	for _, seed := range []string{
		"url=https%3A%2F%2Fehr.example%2Fapi%2Fpas%2Fnotification%3Fehr%3Dhttps%253A%252F%252Fehr.example%252Ffhir&status=requested,active",
		"url=https://a.example,https://b.example&_id=s1", `url=https://a.example/a\,b`, "url=https://a.example&url=https://b.example", "status=active",
		"url=https://a.example&_elements=channel&_count=5", "url=https://a.example/a%2", "%75rl=https://a.example/a+b", "url=x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		s, err := ParseSubscriptionSearch(raw)
		if err != nil {
			if !errors.Is(err, ErrSubscriptionRequestQuery) && !errors.Is(err, ErrSubscriptionSearchOneEndpoint) {
				t.Fatalf("%q: %v is neither a query error nor the one-endpoint refusal", raw, err)
			}
			return
		}
		if s.URL == "" || !isSearchEndpoint(s.Endpoint) || strings.ContainsRune(s.Endpoint, '\\') {
			t.Fatalf("%q: url %q read as endpoint %q", raw, s.URL, s.Endpoint)
		}
		frame, err := ParseSubscriptionRequest(s.Request.String())
		if err != nil || frame.Interaction != SubscriptionSearch || !reflect.DeepEqual(frame.Params, s.Params) || frame.String() != s.Request.String() {
			t.Fatalf("%q: the frame value %q reads as %+v, %v; want the params %q", raw, s.Request.String(), frame, err, s.Params)
		}
	})
}
