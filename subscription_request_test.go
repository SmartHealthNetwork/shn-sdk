package shnsdk

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// brProviderEndpoint is the reference provider's notification endpoint: its
// own query names the EHR whose store a decision is written to, escaped once.
const brProviderEndpoint = "https://ehr.example/api/pas/notification?ehr=https%3A%2F%2Fehr.example%2Ffhir"

// encodeURIComponent is JavaScript's encodeURIComponent, which the reference
// provider escapes its endpoint with: every byte but A-Z a-z 0-9 - _ . ! ~ * '
// ( ) as %XX, in upper case.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// The reference provider's own pre-subscribe search, exactly as it sends it:
// its endpoint passed through encodeURIComponent as the url, and the status
// list with a literal comma. It is read, the url decoded once to the endpoint
// with its own query intact, and the status read as two values; the frame's
// value carries no url, and reads back as the same search. The same with the
// comma escaped as %2C (in either case) reads the same.
func TestParseSubscriptionSearch_TheReferenceProvidersSearch(t *testing.T) {
	encoded := encodeURIComponent(brProviderEndpoint)
	if want := "https%3A%2F%2Fehr.example%2Fapi%2Fpas%2Fnotification%3Fehr%3Dhttps%253A%252F%252Fehr.example%252Ffhir"; encoded != want {
		t.Fatalf("encodeURIComponent = %q, want %q", encoded, want)
	}
	for _, status := range []string{"requested,active", "requested%2Cactive", "requested%2cactive"} {
		raw := "url=" + encoded + "&status=" + status
		s, err := ParseSubscriptionSearch(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if s.URL != brProviderEndpoint || s.Endpoint != brProviderEndpoint {
			t.Fatalf("url %q, endpoint %q, want the endpoint decoded once, %q", s.URL, s.Endpoint, brProviderEndpoint)
		}
		if !reflect.DeepEqual(s.Params, []SubscriptionQueryParam{{"status", "requested,active"}}) {
			t.Fatalf("params %q", s.Params)
		}
		if got := s.Request.Values("status"); !slices.Equal(got, []string{"requested", "active"}) {
			t.Fatalf("status %q, want two values", got)
		}
		const frame = "GET Subscription?status=requested,active"
		if got := s.Request.String(); got != frame {
			t.Fatalf("frame value %q, want %q", got, frame)
		}
		if s.Request.Query().Has("url") || strings.Contains(s.Request.String(), "url") {
			t.Fatalf("the frame value %q carries url", s.Request.String())
		}
		back, err := ParseSubscriptionRequest(s.Request.String())
		if err != nil || !reflect.DeepEqual(back, s.Request) {
			t.Fatalf("the frame value reads back as %+v, %v; want %+v", back, err, s.Request)
		}
	}
}

// A subscriber's search is read in every form it may take: the url anywhere
// in the query, decoded as HAPI FHIR decodes it and unescaped as FHIR search
// reads a value, naming one endpoint, and the other parameters re-encoded
// canonically for the frame, in the order sent.
func TestParseSubscriptionSearch_Reads(t *testing.T) {
	for _, row := range []struct {
		raw       string
		url       string
		endpoint  string
		params    []SubscriptionQueryParam
		frame     string
		frameVals map[string][]string
	}{
		{"url=https://a.example/n", "https://a.example/n", "https://a.example/n", nil, "GET Subscription?", nil},
		{"url=http://a.example:8080/n?x=1", "http://a.example:8080/n?x=1", "http://a.example:8080/n?x=1", nil, "GET Subscription?", nil},
		{"%75rl=HTTPS://a.example/n", "HTTPS://a.example/n", "HTTPS://a.example/n", nil, "GET Subscription?", nil},
		{"_count=10&url=https%3A%2F%2Fa.example%2Fn&_id=s1,s.2&status=active&_elements=channel,status", "https://a.example/n", "https://a.example/n",
			[]SubscriptionQueryParam{{"_count", "10"}, {"_id", "s1,s.2"}, {"status", "active"}, {"_elements", "channel,status"}},
			"GET Subscription?_count=10&_id=s1,s.2&status=active&_elements=channel,status", map[string][]string{"_id": {"s1", "s.2"}}},
		{"status=%61ctive&url=https://a.example", "https://a.example", "https://a.example", []SubscriptionQueryParam{{"status", "active"}}, "GET Subscription?status=active", nil},
		// FHIR search's escapes: \, \$ and \| are a comma, a dollar sign and a
		// vertical bar inside the one endpoint.
		{"url=https://a.example/x%5C,y", `https://a.example/x\,y`, "https://a.example/x,y", nil, "GET Subscription?", nil},
		{`url=https://a.example/a\$\|b`, `https://a.example/a\$\|b`, `https://a.example/a$|b`, nil, "GET Subscription?", nil},
	} {
		s, err := ParseSubscriptionSearch(row.raw)
		if err != nil {
			t.Fatalf("%q: %v", row.raw, err)
		}
		if s.URL != row.url || s.Endpoint != row.endpoint || !reflect.DeepEqual(s.Params, row.params) {
			t.Fatalf("%q read as %+v", row.raw, s)
		}
		if s.Request.String() != row.frame || s.Request.Interaction != SubscriptionSearch || s.Request.Method != "GET" {
			t.Fatalf("%q: frame value %q (%+v), want %q", row.raw, s.Request.String(), s.Request, row.frame)
		}
		back, err := ParseSubscriptionRequest(s.Request.String())
		if err != nil || !reflect.DeepEqual(back.Params, s.Request.Params) || back.String() != s.Request.String() {
			t.Fatalf("%q: the frame value reads back as %+v, %v", row.raw, back, err)
		}
		for name, want := range row.frameVals {
			if got := back.Values(name); !slices.Equal(got, want) {
				t.Fatalf("%q: %s %q, want %q", row.raw, name, got, want)
			}
		}
	}
}

// A url naming more than one endpoint, FHIR search's alternatives, cannot be
// carried as one sealed endpoint: the refusal is ErrSubscriptionSearchOneEndpoint,
// which a gateway records as routing, and not a query error.
func TestParseSubscriptionSearch_OneEndpoint(t *testing.T) {
	for _, raw := range []string{
		"url=https://a.example,https://b.example",
		"url=https%3A%2F%2Fa.example%2Chttps%3A%2F%2Fb.example",
		"url=https://a.example,https://a.example",
		"status=active&url=https://a.example/x%5C,y,https://b.example",
		"url=a,b,c",
		// It is checked first, whatever else the search breaks.
		"url=ftp://a.example,ftp://b.example",
		"url=https://a.example,https://b.example&_summary=count",
		"url=https://a.example,https://b.example&status:not=off",
		"_format=json&url=https://a.example,https://b.example",
		"url=https://a.example,https://b.example&url=https://c.example",
		"url=https://a.example/a+b,https://b.example",
	} {
		s, err := ParseSubscriptionSearch(raw)
		if !errors.Is(err, ErrSubscriptionSearchOneEndpoint) || !errors.Is(err, ErrSubscriptionRequest) || errors.Is(err, ErrSubscriptionRequestQuery) {
			t.Fatalf("ParseSubscriptionSearch(%q) = %+v, %v; want ErrSubscriptionSearchOneEndpoint alone", raw, s, err)
		}
	}
	if errors.Is(ErrSubscriptionRequestQuery, ErrSubscriptionSearchOneEndpoint) {
		t.Fatal("a query error reads as the one-endpoint refusal")
	}
}

// Every search outside the set is refused, as ErrSubscriptionRequestQuery:
// the rejection rows of the subscriber's ingress.
func TestParseSubscriptionSearch_Refuses(t *testing.T) {
	const okURL = "url=https%3A%2F%2Fa.example%2Fn"
	for _, raw := range []string{
		// No url, or not exactly one, or an empty one.
		"", "status=active", "_id=s1", okURL + "&" + okURL, okURL + "&url=x", "url=", "url",
		"URL=https://a.example", "Url=https://a.example", "%55RL=https://a.example",
		// A url value that cannot be read one way only.
		"url=a,", "url=,a", "url=a,,b", "url=,", `url=a\`, `url=a\x`, "url=a%5C", "url=a%5Cb",
		`url=https://a.example/x\`, `url=https://a.example/x\y`,
		// An endpoint that is not an absolute http or https URL, or holds a
		// backslash, a space (a "+" decoded) or a control character.
		"url=x", "url=a.example/n", "url=/n", "url=//a.example/n", "url=ftp://a.example/n", "url=mailto:a@a.example",
		"url=https:a.example", "url=https://", "url=https:///n", "url=urn:uuid:1", "url=https%3A%2F%2Fa.example%2Fa%5C%5Cb",
		`url=https://a.example/a\\b`, "url=https://a.example/a+b", "url=https://a.example/a%20b", "url=%20https://a.example",
		"url=https://a.example/a%09b", "url=https://a.example/%7F", "url=https://a%2Eexample/n%5C%5C",
		// Modifiers, on url or on any other parameter.
		"url:below=https://a.example", "url%3Aabove=https://a.example", okURL + "&url:missing=true",
		okURL + "&status:not=off", okURL + "&_id:exact=s1",
		// Any other parameter.
		okURL + "&_summary=count", okURL + "&_total=accurate", okURL + "&_format=json", okURL + "&criteria=x",
		okURL + "&_getpages=a", okURL + "&_sort=status",
		// A carried parameter twice, or with a value outside its form.
		okURL + "&status=active&status=off", okURL + "&status=", okURL + "&status=active,", okURL + "&status=Active",
		okURL + `&status=a\,b`, okURL + "&_id=a_b", okURL + "&_id=..", okURL + "&_count=-1", okURL + "&_count=1234567890",
		okURL + "&_elements=channel,", okURL + "&_elements=*", okURL + "&_count=1&_count=2",
		// Empty parameters, and what cannot be decoded or carried.
		okURL + "&", "&" + okURL, okURL + "&&status=active", "=x&" + okURL,
		"url=https%3A%2", "url=https://a.example/a%zz", "url=https://a.example/a%", "url=https://a.example/a%-1", "url=https://a.example/a%+1",
		"url=https://a.example/a%ff", "url=https://a.example/a%0a", "url=https://a.example/a%00", "url=https://a.example/a%7f",
		"url=https://a.example/a b", "url=https://a.example/a\r\nX-Injected: 1", "url=https://a.example/a\tb", "url=https://a.example/\xff",
		"url=https://a.example/a#f",
	} {
		s, err := ParseSubscriptionSearch(raw)
		if !errors.Is(err, ErrSubscriptionRequestQuery) {
			t.Fatalf("ParseSubscriptionSearch(%q) = %+v, %v; want ErrSubscriptionRequestQuery", raw, s, err)
		}
	}
}

// Every form of the closed set is read, and String gives back the value it
// was read from.
func TestParseSubscriptionRequest_ReadsEveryForm(t *testing.T) {
	id64 := strings.Repeat("a", 64)
	for _, row := range []struct {
		in   string
		want SubscriptionRequest
		path string
	}{
		{"POST Subscription", SubscriptionRequest{Interaction: SubscriptionCreate, Method: "POST"}, "Subscription"},
		{"GET Subscription/sub-1", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "sub-1"}, "Subscription/sub-1"},
		{"GET Subscription/" + id64, SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: id64}, "Subscription/" + id64},
		{"GET Subscription/...", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "..."}, "Subscription/..."},
		{"GET Subscription/A-z.0/_history/2", SubscriptionRequest{Interaction: SubscriptionVRead, Method: "GET", ID: "A-z.0", VersionID: "2"}, "Subscription/A-z.0/_history/2"},
		{"PUT Subscription/sub-1", SubscriptionRequest{Interaction: SubscriptionUpdate, Method: "PUT", ID: "sub-1"}, "Subscription/sub-1"},
		{"DELETE Subscription/sub-1", SubscriptionRequest{Interaction: SubscriptionDelete, Method: "DELETE", ID: "sub-1"}, "Subscription/sub-1"},
		{"GET Subscription/sub-1/$status", SubscriptionRequest{Interaction: SubscriptionStatus, Method: "GET", ID: "sub-1"}, "Subscription/sub-1/$status"},
		// A search that carries nothing but the url the notifier's gateway
		// writes.
		{"GET Subscription?", SubscriptionRequest{Interaction: SubscriptionSearch, Method: "GET"}, "Subscription"},
		{"GET Subscription?status=requested,active", SubscriptionRequest{Interaction: SubscriptionSearch, Method: "GET", RawQuery: "status=requested,active",
			Params: []SubscriptionQueryParam{{"status", "requested,active"}}}, "Subscription"},
		{"GET Subscription?_count=10&_id=s1,s.2&status=active&_elements=channel,status", SubscriptionRequest{Interaction: SubscriptionSearch, Method: "GET",
			RawQuery: "_count=10&_id=s1,s.2&status=active&_elements=channel,status",
			Params:   []SubscriptionQueryParam{{"_count", "10"}, {"_id", "s1,s.2"}, {"status", "active"}, {"_elements", "channel,status"}}}, "Subscription"},
		// A name and a value are decoded as HAPI FHIR's decoder reads them.
		{"GET Subscription?%73tatus=%61ctive%2Coff", SubscriptionRequest{Interaction: SubscriptionSearch, Method: "GET", RawQuery: "%73tatus=%61ctive%2Coff",
			Params: []SubscriptionQueryParam{{"status", "active,off"}}}, "Subscription"},
		{"GET ?_getpages=4f1d2c6e-7a3b-4d1e-9c2a-0b1e2d3c4f5a", SubscriptionRequest{Interaction: SubscriptionPage, Method: "GET", RawQuery: "_getpages=4f1d2c6e-7a3b-4d1e-9c2a-0b1e2d3c4f5a",
			Params: []SubscriptionQueryParam{{"_getpages", "4f1d2c6e-7a3b-4d1e-9c2a-0b1e2d3c4f5a"}}}, ""},
		{"GET ?_getpages=abc&_getpagesoffset=20&_count=20&_bundletype=searchset", SubscriptionRequest{Interaction: SubscriptionPage, Method: "GET",
			RawQuery: "_getpages=abc&_getpagesoffset=20&_count=20&_bundletype=searchset",
			Params:   []SubscriptionQueryParam{{"_getpages", "abc"}, {"_getpagesoffset", "20"}, {"_count", "20"}, {"_bundletype", "searchset"}}}, ""},
		{"GET ?_getpages=abc&_count=5", SubscriptionRequest{Interaction: SubscriptionPage, Method: "GET", RawQuery: "_getpages=abc&_count=5",
			Params: []SubscriptionQueryParam{{"_getpages", "abc"}, {"_count", "5"}}}, ""},
		{"GET ?_getpages=abc&_bundletype=searchset", SubscriptionRequest{Interaction: SubscriptionPage, Method: "GET", RawQuery: "_getpages=abc&_bundletype=searchset",
			Params: []SubscriptionQueryParam{{"_getpages", "abc"}, {"_bundletype", "searchset"}}}, ""},
		// A HAPI FHIR server's page link copies the search's _elements after
		// _bundletype.
		{"GET ?_getpages=abc&_getpagesoffset=20&_count=20&_bundletype=searchset&_elements=channel,status", SubscriptionRequest{Interaction: SubscriptionPage, Method: "GET",
			RawQuery: "_getpages=abc&_getpagesoffset=20&_count=20&_bundletype=searchset&_elements=channel,status",
			Params:   []SubscriptionQueryParam{{"_getpages", "abc"}, {"_getpagesoffset", "20"}, {"_count", "20"}, {"_bundletype", "searchset"}, {"_elements", "channel,status"}}}, ""},
		{"GET ?_getpages=abc&_elements=channel", SubscriptionRequest{Interaction: SubscriptionPage, Method: "GET", RawQuery: "_getpages=abc&_elements=channel",
			Params: []SubscriptionQueryParam{{"_getpages", "abc"}, {"_elements", "channel"}}}, ""},
		// A read or a vread with _elements and _summary, carried as sent.
		{"GET Subscription/sub-1?_elements=channel", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "sub-1", RawQuery: "_elements=channel",
			Params: []SubscriptionQueryParam{{"_elements", "channel"}}}, "Subscription/sub-1"},
		{"GET Subscription/sub-1?_summary=true", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "sub-1", RawQuery: "_summary=true",
			Params: []SubscriptionQueryParam{{"_summary", "true"}}}, "Subscription/sub-1"},
		{"GET Subscription/sub-1?_summary=false", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "sub-1", RawQuery: "_summary=false",
			Params: []SubscriptionQueryParam{{"_summary", "false"}}}, "Subscription/sub-1"},
		{"GET Subscription/sub-1?_summary=data&_elements=channel,status", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "sub-1", RawQuery: "_summary=data&_elements=channel,status",
			Params: []SubscriptionQueryParam{{"_summary", "data"}, {"_elements", "channel,status"}}}, "Subscription/sub-1"},
		{"GET Subscription/sub-1?%5Fsummary=text", SubscriptionRequest{Interaction: SubscriptionRead, Method: "GET", ID: "sub-1", RawQuery: "%5Fsummary=text",
			Params: []SubscriptionQueryParam{{"_summary", "text"}}}, "Subscription/sub-1"},
		{"GET Subscription/sub-1/_history/2?_elements=channel&_summary=count", SubscriptionRequest{Interaction: SubscriptionVRead, Method: "GET", ID: "sub-1", VersionID: "2",
			RawQuery: "_elements=channel&_summary=count", Params: []SubscriptionQueryParam{{"_elements", "channel"}, {"_summary", "count"}}}, "Subscription/sub-1/_history/2"},
	} {
		got, err := ParseSubscriptionRequest(row.in)
		if err != nil || !reflect.DeepEqual(got, row.want) {
			t.Fatalf("ParseSubscriptionRequest(%q) = %+v, %v; want %+v", row.in, got, err, row.want)
		}
		if got.String() != row.in || got.Path() != row.path {
			t.Fatalf("%q: String %q, Path %q; want the value and %q", row.in, got.String(), got.Path(), row.path)
		}
	}
	r, _ := ParseSubscriptionRequest("GET Subscription?status=active&_count=2")
	if q := r.Query(); q.Get("status") != "active" || q.Get("_count") != "2" || len(q) != 2 || r.Values("_id") != nil {
		t.Fatalf("Query %v, Values(_id) %q", q, r.Values("_id"))
	}
}

// Every value outside the closed set is refused, each with the error that
// says how: a form not carried, an id that is not a FHIR id, or a query not
// carried. The rejection rows of the header.
func TestParseSubscriptionRequest_Refuses(t *testing.T) {
	const okQ = "status=active"
	for _, row := range []struct {
		in   string
		want error
	}{
		// Forms not carried.
		{"", ErrSubscriptionRequest},
		{"POST", ErrSubscriptionRequest},
		{"POSTSubscription", ErrSubscriptionRequest},
		{"POST  Subscription", ErrSubscriptionRequest},
		{"post Subscription", ErrSubscriptionRequest},
		{"PATCH Subscription/sub-1", ErrSubscriptionRequest},
		{"HEAD Subscription/sub-1", ErrSubscriptionRequest},
		{"POST Subscription/_search", ErrSubscriptionRequest},
		{"POST Subscription/sub-1", ErrSubscriptionRequest},
		{"POST Subscription?" + okQ, ErrSubscriptionRequest},
		{"POST Subscription?", ErrSubscriptionRequest},
		{"PUT Subscription", ErrSubscriptionRequest},
		{"DELETE Subscription", ErrSubscriptionRequest},
		{"DELETE Subscription?" + okQ, ErrSubscriptionRequest},
		// A search is written with its "?", even when its query is empty.
		{"GET Subscription", ErrSubscriptionRequest},
		{"GET Subscription/", ErrSubscriptionRequestID},
		{"GET /Subscription/sub-1", ErrSubscriptionRequest},
		{"GET subscription/sub-1", ErrSubscriptionRequest},
		{"GET subscription?" + okQ, ErrSubscriptionRequest},
		{"GET Task/1", ErrSubscriptionRequest},
		{"GET Subscription/$status", ErrSubscriptionRequest},
		{"GET Subscription/sub-1/_history", ErrSubscriptionRequest},
		{"GET Subscription/_history", ErrSubscriptionRequest},
		{"GET Subscription/sub-1/_history/2/x", ErrSubscriptionRequest},
		{"GET Subscription/sub-1/$STATUS", ErrSubscriptionRequest},
		{"GET Subscription/sub-1/$events", ErrSubscriptionRequest},
		{"PUT Subscription/sub-1/$status", ErrSubscriptionRequest},
		{"DELETE Subscription/sub-1/_history/2", ErrSubscriptionRequest},
		{"GET Subscription/sub-1/$status?x=1", ErrSubscriptionRequest},
		{"GET Subscription/sub-1/$status?_elements=channel", ErrSubscriptionRequest},
		{"PUT Subscription/sub-1?x=1", ErrSubscriptionRequest},
		{"PUT Subscription/sub-1?_elements=channel", ErrSubscriptionRequest},
		{"DELETE Subscription/sub-1?_summary=true", ErrSubscriptionRequest},
		{"DELETE Subscription/sub-1?", ErrSubscriptionRequest},
		{"GET Subscription/sub-1#f", ErrSubscriptionRequest},
		{"GET Subscription?" + okQ + "#f", ErrSubscriptionRequest},
		{"POST Subscription#", ErrSubscriptionRequest},
		{"GET Subscription//sub-1", ErrSubscriptionRequest},
		// Ids that are not FHIR ids.
		{"GET Subscription/a_b", ErrSubscriptionRequestID},
		{"GET Subscription/.", ErrSubscriptionRequestID},
		{"GET Subscription/..", ErrSubscriptionRequestID},
		{"PUT Subscription/" + strings.Repeat("a", 65), ErrSubscriptionRequestID},
		{"DELETE Subscription/a%2Fb", ErrSubscriptionRequestID},
		{"GET Subscription/é", ErrSubscriptionRequestID},
		{"GET Subscription/../$status", ErrSubscriptionRequestID},
		{"GET Subscription/sub-1/_history/..", ErrSubscriptionRequestID},
		{"GET Subscription/sub-1/_history/a_b", ErrSubscriptionRequestID},
		{"GET Subscription/sub-1/_history/", ErrSubscriptionRequestID},
		// A read's or a vread's query outside _elements and _summary, each once.
		{"GET Subscription/sub-1?", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1/_history/2?", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_format=json", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_pretty=true", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_count=1", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?url=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1/_history/2?_include=x", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_elements=channel&_elements=status", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=true&_summary=false", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=true&_elements=channel&_summary=true", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_elements:x=channel", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary%3Atext=true", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=TRUE", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=maybe", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_elements=", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_elements=*", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_elements=channel,", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_elements=channel&", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?&_summary=true", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?=x", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=a%zz", ErrSubscriptionRequestQuery},
		{"GET Subscription/sub-1?_summary=true b", ErrSubscriptionRequestQuery},
		// A frame's search never carries url, in any case or encoding, with or
		// without a modifier or other parameters: the endpoint travels only
		// sealed, and the notifier's gateway writes the url.
		{"GET Subscription?url=https%3A%2F%2Fa.example%2Fn", ErrSubscriptionRequestQuery},
		{"GET Subscription?URL=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription?Url=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription?%75rl=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription?%55%52%4C=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription?" + okQ + "&url=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription?url=", ErrSubscriptionRequestQuery},
		{"GET Subscription?url:below=https://a.example", ErrSubscriptionRequestQuery},
		{"GET Subscription?URL%3Amissing=true", ErrSubscriptionRequestQuery},
		// A search's query outside the set.
		{"GET Subscription?url", ErrSubscriptionRequestQuery},
		{"GET Subscription?" + okQ + "&", ErrSubscriptionRequestQuery},
		{"GET Subscription?&" + okQ, ErrSubscriptionRequestQuery},
		{"GET Subscription?&", ErrSubscriptionRequestQuery},
		{"GET Subscription?" + okQ + "&&_count=1", ErrSubscriptionRequestQuery},
		{"GET Subscription?=x&" + okQ, ErrSubscriptionRequestQuery},
		{"GET Subscription?status:not=off", ErrSubscriptionRequestQuery},
		{"GET Subscription?status%3Anot=off", ErrSubscriptionRequestQuery},
		{"GET Subscription?_summary=count", ErrSubscriptionRequestQuery},
		{"GET Subscription?_total=accurate", ErrSubscriptionRequestQuery},
		{"GET Subscription?_format=json", ErrSubscriptionRequestQuery},
		{"GET Subscription?criteria=x", ErrSubscriptionRequestQuery},
		{"GET Subscription?_getpages=a", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=active&status=off", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=active,", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=Active", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a\\,b", ErrSubscriptionRequestQuery},
		{"GET Subscription?_id=a_b", ErrSubscriptionRequestQuery},
		{"GET Subscription?_id=..", ErrSubscriptionRequestQuery},
		{"GET Subscription?_count=-1", ErrSubscriptionRequestQuery},
		{"GET Subscription?_count=1234567890", ErrSubscriptionRequestQuery},
		{"GET Subscription?_elements=channel,", ErrSubscriptionRequestQuery},
		{"GET Subscription?_elements=*", ErrSubscriptionRequestQuery},
		// What cannot be read one way only, or carried as a query.
		{"GET Subscription?status=a%2", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%zz", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%-1", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%+1", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%ff", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%0a", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%00", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a%7f", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a b", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a\r\nX-Injected: 1", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=a\tb", ErrSubscriptionRequestQuery},
		{"GET Subscription?status=\xff", ErrSubscriptionRequestQuery},
		// A page's query outside its form.
		{"GET ?", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a/b", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a%2Fb", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_getpages=b", ErrSubscriptionRequestQuery},
		{"GET ?_count=5&_getpages=a", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_count=5&_getpagesoffset=20", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_count=5&_count=5", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_bundletype=history", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_getpagesoffset=x", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_pretty=true", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_format=json", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&url=x", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_getpagesoffset:missing=1", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_elements=channel&_bundletype=searchset", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_elements=channel&_count=5", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_elements=channel&_elements=status", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_elements=*", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_elements=", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a&_summary=true", ErrSubscriptionRequestQuery},
		{"GET ?_getpages=a%zz", ErrSubscriptionRequestQuery},
		{"POST ?_getpages=a", ErrSubscriptionRequest},
		{"GET Task?_getpages=a", ErrSubscriptionRequest},
	} {
		got, err := ParseSubscriptionRequest(row.in)
		if !errors.Is(err, row.want) {
			t.Fatalf("ParseSubscriptionRequest(%q) = %+v, %v; want %v", row.in, got, err, row.want)
		}
		if !errors.Is(err, ErrSubscriptionRequest) {
			t.Fatalf("ParseSubscriptionRequest(%q): %v does not wrap ErrSubscriptionRequest", row.in, err)
		}
		// Each error says one thing: an id error is not a query error, and
		// a form error is neither.
		if (row.want == ErrSubscriptionRequest) && (errors.Is(err, ErrSubscriptionRequestID) || errors.Is(err, ErrSubscriptionRequestQuery)) {
			t.Fatalf("ParseSubscriptionRequest(%q) = %v; want a form error alone", row.in, err)
		}
		if row.want == ErrSubscriptionRequestID && errors.Is(err, ErrSubscriptionRequestQuery) {
			t.Fatalf("ParseSubscriptionRequest(%q) = %v; want an id error alone", row.in, err)
		}
	}
}

// A frame's url refusal says why, for url in any case or encoding, with or
// without a modifier: the notifier's gateway writes the url.
func TestParseSubscriptionRequest_URLRefusalSaysWhy(t *testing.T) {
	for _, q := range []string{"url=x", "URL=x", "Url=x", "uRL=x", "%75rl=x", "%55%52%4C=x", "url:below=x", "URL%3Amissing=true", "status=active&url=x", "url=", "url=x&url=y"} {
		_, err := ParseSubscriptionRequest("GET Subscription?" + q)
		if !errors.Is(err, ErrSubscriptionRequestQuery) || !strings.Contains(err.Error(), "never carries url") {
			t.Fatalf("%q: %v, want the url refusal", q, err)
		}
	}
}

// A modifier is refused as a modifier, on any parameter, at the subscriber's
// ingress and in a frame's search.
func TestSubscriptionSearch_ModifierRefusalSaysWhy(t *testing.T) {
	for _, q := range []string{"status:not=off", "status%3Anot=off", "_id:exact=s1", "_count:x=1"} {
		_, errFrame := ParseSubscriptionRequest("GET Subscription?" + q)
		_, errIngress := ParseSubscriptionSearch("url=https://a.example&" + q)
		for _, err := range []error{errFrame, errIngress} {
			if !errors.Is(err, ErrSubscriptionRequestQuery) || !strings.Contains(err.Error(), "carries a modifier") {
				t.Fatalf("%q: %v, want the modifier refusal", q, err)
			}
		}
	}
	if _, err := ParseSubscriptionSearch("url:below=x"); !errors.Is(err, ErrSubscriptionRequestQuery) || !strings.Contains(err.Error(), "carries a modifier") {
		t.Fatalf("url:below: %v, want the modifier refusal", err)
	}
}

// The canonical encoding writes every byte but an unreserved character and a
// comma as %XX, in upper case, and HAPI FHIR's decoding reads it back.
func TestCanonicalQueryComponent(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "",
		"active,off":       "active,off",
		"A-z.0_~":          "A-z.0_~",
		"a b+c&d=e%f":      "a%20b%2Bc%26d%3De%25f",
		"é":                "%C3%A9",
		"url:below?#/\\$|": "url%3Abelow%3F%23%2F%5C%24%7C",
	} {
		got := canonicalQueryComponent(in)
		if got != want {
			t.Errorf("canonicalQueryComponent(%q) = %q, want %q", in, got, want)
		}
		if back, err := decodeQueryComponent(got); err != nil || back != in {
			t.Errorf("decodeQueryComponent(%q) = %q, %v; want %q", got, back, err, in)
		}
	}
}

// The header crosses the seal exactly as framed, in any case given, and a
// value the frame's JSON would change is refused where it is made.
func TestSubscriptionRequestHeader_RoundTrip(t *testing.T) {
	s, err := ParseSubscriptionSearch("url=" + url.QueryEscape(brProviderEndpoint) + "&status=requested,active")
	if err != nil {
		t.Fatal(err)
	}
	v := s.Request.String()
	for _, name := range []string{FrameHeaderSubscriptionRequest, "SUBSCRIPTIONREQUEST"} {
		frame, err := EncodeHTTPFrameHeaders(200, map[string]string{name: v}, nil)
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		hdr, _, err := DecodeHTTPFrame(frame)
		if err != nil || hdr.Headers[FrameHeaderSubscriptionRequest] != v {
			t.Fatalf("decode %s: %v %q", name, err, hdr.Headers)
		}
	}
	for _, bad := range []string{"GET Subscription/a\nb", "GET Subscription/a\x00", "GET \xff"} {
		if _, err := EncodeHTTPFrameHeaders(200, map[string]string{FrameHeaderSubscriptionRequest: bad}, nil); !errors.Is(err, ErrFrameHeaderValue) {
			t.Fatalf("%q: %v, want ErrFrameHeaderValue", bad, err)
		}
	}
}
