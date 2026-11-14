package shnsdk

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SubscriptionInteraction is the FHIR interaction a FrameHeaderSubscriptionRequest
// value names on a subscription's subscribe leg.
type SubscriptionInteraction string

// The interactions a subscribe leg carries (ParseSubscriptionRequest).
const (
	// SubscriptionCreate is "POST Subscription".
	SubscriptionCreate SubscriptionInteraction = "create"
	// SubscriptionRead is "GET Subscription/<id>", optionally with a query of
	// _elements and _summary.
	SubscriptionRead SubscriptionInteraction = "read"
	// SubscriptionVRead is "GET Subscription/<id>/_history/<vid>", optionally
	// with a query of _elements and _summary.
	SubscriptionVRead SubscriptionInteraction = "vread"
	// SubscriptionUpdate is "PUT Subscription/<id>".
	SubscriptionUpdate SubscriptionInteraction = "update"
	// SubscriptionDelete is "DELETE Subscription/<id>".
	SubscriptionDelete SubscriptionInteraction = "delete"
	// SubscriptionStatus is the instance operation "GET Subscription/<id>/$status".
	SubscriptionStatus SubscriptionInteraction = "status"
	// SubscriptionSearch is "GET Subscription?<query>", the query possibly
	// empty. A frame's search never carries url: the notifier's gateway writes
	// it.
	SubscriptionSearch SubscriptionInteraction = "search"
	// SubscriptionPage is a page of a search's answer, "GET ?_getpages=<x>…",
	// at the base itself.
	SubscriptionPage SubscriptionInteraction = "page"
)

// SubscriptionQueryParam is one parameter of a search's or a page's query, as
// a FHIR server built on HAPI FHIR reads it: the query split at each "&", a
// parameter at its first "=", and the name and the value each URL-decoded as
// Java's URLDecoder decodes them ("+" a space, "%XX" a byte, the bytes UTF-8).
type SubscriptionQueryParam struct {
	Name, Value string
}

// SubscriptionRequest is a FrameHeaderSubscriptionRequest value as
// ParseSubscriptionRequest reads it.
type SubscriptionRequest struct {
	// Interaction is the interaction the value names.
	Interaction SubscriptionInteraction
	// Method is the HTTP method: GET, POST, PUT or DELETE.
	Method string
	// ID is the Subscription's logical id, a FHIR id; empty for a create, a
	// search and a page.
	ID string
	// VersionID is the version a vread names, a FHIR id; empty otherwise.
	VersionID string
	// RawQuery is a search's, a page's, a read's or a vread's query after the
	// "?", exactly as framed; empty for every other interaction, which carries
	// none, for a read or vread written without one, and for a search that
	// carries no parameter but the url the notifier's gateway writes.
	RawQuery string
	// Params are RawQuery's parameters, in order, decoded as
	// SubscriptionQueryParam says.
	Params []SubscriptionQueryParam
}

// Path is the path below the subscriber's per-counterpart base that the value
// names, with no query: "Subscription", "Subscription/<id>",
// "Subscription/<id>/_history/<vid>", "Subscription/<id>/$status", or "" for a
// page, which is at the base itself.
func (r SubscriptionRequest) Path() string {
	switch r.Interaction {
	case SubscriptionCreate, SubscriptionSearch:
		return "Subscription"
	case SubscriptionPage:
		return ""
	case SubscriptionVRead:
		return "Subscription/" + r.ID + "/_history/" + r.VersionID
	case SubscriptionStatus:
		return "Subscription/" + r.ID + "/$status"
	}
	return "Subscription/" + r.ID
}

// String is the value as a frame header carries it: the value it was read
// from, byte for byte.
func (r SubscriptionRequest) String() string {
	s := r.Method + " " + r.Path()
	if r.Interaction == SubscriptionSearch || r.Interaction == SubscriptionPage || r.RawQuery != "" {
		s += "?" + r.RawQuery
	}
	return s
}

// Query is Params as url.Values, each name's values in the order sent.
func (r SubscriptionRequest) Query() url.Values {
	q := url.Values{}
	for _, p := range r.Params {
		q[p.Name] = append(q[p.Name], p.Value)
	}
	return q
}

// Values is the one parameter name's decoded value split at each comma, the
// values FHIR search reads as alternatives ("status=requested,active" is two;
// "%2C" decodes to a comma, and is read the same). It is nil when the query
// does not carry name.
func (r SubscriptionRequest) Values(name string) []string {
	for _, p := range r.Params {
		if p.Name == name {
			return strings.Split(p.Value, ",")
		}
	}
	return nil
}

// ErrSubscriptionRequest is a FrameHeaderSubscriptionRequest value that is not
// one of the interactions a subscribe leg carries: the gateway that reads it
// refuses it, and sends nothing on. ErrSubscriptionRequestID and
// ErrSubscriptionRequestQuery wrap it for the two ways a value of a carried
// form can still fail.
var ErrSubscriptionRequest = errors.New("shnsdk: not a Subscription interaction this network carries")

// ErrSubscriptionRequestID is a value of a carried form whose id or version is
// not a FHIR id, or is a dot segment. It wraps ErrSubscriptionRequest.
var ErrSubscriptionRequestID = fmt.Errorf("%w: an id or version is not a FHIR id", ErrSubscriptionRequest)

// ErrSubscriptionRequestQuery is a search or a page whose query is outside the
// set a subscribe leg carries, or cannot be read one way only. It wraps
// ErrSubscriptionRequest. ParseSubscriptionSearch returns it too, for a
// subscriber's search outside its set.
var ErrSubscriptionRequestQuery = fmt.Errorf("%w: the query is not one carried", ErrSubscriptionRequest)

// ErrSubscriptionSearchOneEndpoint is a subscriber's search
// (ParseSubscriptionSearch) whose url names more than one endpoint, FHIR
// search's comma-separated alternatives: only one sealed endpoint can be
// carried for a search (FrameHeaderSubscriptionEndpoint), so the search
// cannot be addressed, and a gateway records the refusal as routing. It wraps
// ErrSubscriptionRequest and not ErrSubscriptionRequestQuery, so a caller
// tells the two apart by errors.Is.
var ErrSubscriptionSearchOneEndpoint = fmt.Errorf("%w: a search names more than one endpoint, and only one sealed endpoint can be carried", ErrSubscriptionRequest)

// The parameters a read or a vread carries:
// _elements and _summary, each at most once, carried as sent. The
// notifier's ownership test decides what a subset that hides channel gets.
var subscriptionReadParams = map[string]func(string) bool{
	"_elements": func(v string) bool { return allOf(v, subscriptionElement.MatchString) },
	"_summary":  func(v string) bool { return v == "true" || v == "false" || v == "text" || v == "data" || v == "count" },
}

// The parameters a search carries besides url: status, _id, _count and
// _elements, each at most once.
var subscriptionSearchParams = map[string]func(string) bool{
	"status":    func(v string) bool { return allOf(v, subscriptionStatusCode) },
	"_id":       func(v string) bool { return allOf(v, isFHIRPathID) },
	"_count":    subscriptionCount.MatchString,
	"_elements": func(v string) bool { return allOf(v, subscriptionElement.MatchString) },
}

var (
	subscriptionStatusCode = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`).MatchString
	subscriptionCount      = regexp.MustCompile(`^[0-9]{1,9}$`)
	subscriptionElement    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.]*$`)
	subscriptionPageID     = regexp.MustCompile(`^[A-Za-z0-9\-._~]{1,256}$`)
)

// allOf reports whether every comma-separated part of v passes ok.
func allOf(v string, ok func(string) bool) bool {
	for _, p := range strings.Split(v, ",") {
		if !ok(p) {
			return false
		}
	}
	return true
}

// ParseSubscriptionRequest reads a FrameHeaderSubscriptionRequest value: an
// HTTP method, one space, and a path below the subscriber's per-counterpart
// base with its query, from this closed set and nothing else:
//
//	POST Subscription                                  create
//	GET Subscription/<id>[?<query>]                    read
//	GET Subscription/<id>/_history/<vid>[?<query>]     vread
//	PUT Subscription/<id>                              update
//	DELETE Subscription/<id>                           delete
//	GET Subscription/<id>/$status                      instance $status
//	GET Subscription?<query>                           search
//	GET ?_getpages=<x>[&_getpagesoffset=<n>][&_count=<n>][&_bundletype=searchset][&_elements=<e>]   page
//
// <id> and <vid> are FHIR ids ([A-Za-z0-9\-\.]{1,64}) other than the dot
// segments "." and "..". A fragment, any other method (PATCH among them), any
// other path (POST Subscription/_search, an instance or type-level _history, a
// type-level $status among them), a search written without its "?" and a
// query on any form but a search, a page, a read or a vread are
// ErrSubscriptionRequest.
//
// A search's query is decoded as a FHIR server built on HAPI FHIR decodes it
// (SubscriptionQueryParam). It never carries url: the endpoint travels only
// sealed, in FrameHeaderSubscriptionEndpoint, and the notifier's gateway
// writes the url its system is searched by. A parameter whose decoded name is
// url in any case, with or without a modifier, is refused. The query may be
// empty, and otherwise holds only status, _id, _count and _elements, each at
// most once: status a comma-separated list of codes, _id of FHIR ids,
// _elements of element names, and _count a number. A comma, written or
// decoded from "%2C", separates alternatives (Values). No parameter carries a
// modifier. A page names, in that order, one _getpages of unreserved
// characters, then optionally _getpagesoffset and _count, each a number,
// _bundletype=searchset and _elements, as a HAPI FHIR server writes a page's
// link. A read's or a vread's query, when it has one, holds only _elements
// (element names) and _summary (true, false, text, data or count), each at
// most once, with no modifier, carried as sent: what a subset that hides
// channel gets is the notifier's ownership test's to decide. Any other query,
// an empty parameter, a parameter with no "=", a malformed escape ("%" not
// followed by two hexadecimal digits), decoded bytes that are not UTF-8 or a
// control character are ErrSubscriptionRequestQuery; a raw query holding a
// space, a control character, a "#" or bytes that are not UTF-8, which cannot
// be carried as a query, too. ParseSubscriptionSearch writes a
// search's value from the query a subscriber's system sent.
func ParseSubscriptionRequest(v string) (SubscriptionRequest, error) {
	method, target, ok := strings.Cut(v, " ")
	if !ok {
		return SubscriptionRequest{}, fmt.Errorf("%w: no method and path", ErrSubscriptionRequest)
	}
	if strings.Contains(v, "#") {
		return SubscriptionRequest{}, fmt.Errorf("%w: it carries a fragment", ErrSubscriptionRequest)
	}
	path, rawQuery, hasQuery := strings.Cut(target, "?")
	r := SubscriptionRequest{Method: method}
	switch {
	case method == "POST" && path == "Subscription" && !hasQuery:
		r.Interaction = SubscriptionCreate
		return r, nil
	case method == "GET" && path == "Subscription" && hasQuery:
		r.Interaction, r.RawQuery = SubscriptionSearch, rawQuery
		params, err := parseFrameSearch(rawQuery)
		if err != nil {
			return SubscriptionRequest{}, err
		}
		r.Params = params
		return r, nil
	case method == "GET" && path == "" && hasQuery:
		r.Interaction, r.RawQuery = SubscriptionPage, rawQuery
		params, err := parseSubscriptionPage(rawQuery)
		if err != nil {
			return SubscriptionRequest{}, err
		}
		r.Params = params
		return r, nil
	}
	segs := strings.Split(path, "/")
	if segs[0] != "Subscription" || len(segs) < 2 {
		return SubscriptionRequest{}, fmt.Errorf("%w: the path is not one of a Subscription (a search is written GET Subscription?<query>)", ErrSubscriptionRequest)
	}
	if strings.HasPrefix(segs[1], "$") || strings.HasPrefix(segs[1], "_") {
		// A type-level operation ($status) or history (_history), not an id.
		return SubscriptionRequest{}, fmt.Errorf("%w: %s %s is a type-level interaction, which a subscribe leg does not carry", ErrSubscriptionRequest, method, path)
	}
	switch {
	case len(segs) == 2 && method == "GET":
		r.Interaction = SubscriptionRead
	case len(segs) == 2 && method == "PUT":
		r.Interaction = SubscriptionUpdate
	case len(segs) == 2 && method == "DELETE":
		r.Interaction = SubscriptionDelete
	case len(segs) == 3 && method == "GET" && segs[2] == "$status":
		r.Interaction = SubscriptionStatus
	case len(segs) == 4 && method == "GET" && segs[2] == "_history":
		r.Interaction = SubscriptionVRead
		if !isFHIRPathID(segs[3]) {
			return SubscriptionRequest{}, ErrSubscriptionRequestID
		}
		r.VersionID = segs[3]
	default:
		return SubscriptionRequest{}, fmt.Errorf("%w: %s %s is not an interaction a subscribe leg carries", ErrSubscriptionRequest, method, path)
	}
	if !isFHIRPathID(segs[1]) {
		return SubscriptionRequest{}, ErrSubscriptionRequestID
	}
	r.ID = segs[1]
	if !hasQuery {
		return r, nil
	}
	if r.Interaction != SubscriptionRead && r.Interaction != SubscriptionVRead {
		return SubscriptionRequest{}, fmt.Errorf("%w: only a search, a page, a read or a vread carries a query", ErrSubscriptionRequest)
	}
	params, err := parseReadQuery(rawQuery)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	r.RawQuery, r.Params = rawQuery, params
	return r, nil
}

// parseReadQuery reads a read's or a vread's query (ParseSubscriptionRequest):
// one or more of _elements and _summary, each once.
func parseReadQuery(raw string) ([]SubscriptionQueryParam, error) {
	params, err := decodeSubscriptionQuery(raw)
	if err != nil {
		return nil, err
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("%w: a read's query, when written, names a parameter", ErrSubscriptionRequestQuery)
	}
	seen := map[string]bool{}
	for _, p := range params {
		check, ok := subscriptionReadParams[p.Name]
		switch {
		case !ok:
			return nil, fmt.Errorf("%w: %q is not a parameter a read carries (_elements, _summary)", ErrSubscriptionRequestQuery, p.Name)
		case seen[p.Name]:
			return nil, fmt.Errorf("%w: %q is given twice", ErrSubscriptionRequestQuery, p.Name)
		case !check(p.Value):
			return nil, fmt.Errorf("%w: %q has a value that is not one it carries", ErrSubscriptionRequestQuery, p.Name)
		}
		seen[p.Name] = true
	}
	return params, nil
}

// parseFrameSearch reads a frame's search query (ParseSubscriptionRequest):
// possibly empty, never with url.
func parseFrameSearch(raw string) ([]SubscriptionQueryParam, error) {
	params, err := decodeSubscriptionQuery(raw)
	if err != nil {
		return nil, err
	}
	for _, p := range params {
		if base, _, _ := strings.Cut(p.Name, ":"); asciiLower(base) == "url" {
			return nil, fmt.Errorf("%w: a frame's search never carries url (%q): the notifier's gateway writes it", ErrSubscriptionRequestQuery, p.Name)
		}
	}
	if err := checkSearchParams(params); err != nil {
		return nil, err
	}
	return params, nil
}

// checkSearchParams holds each of params to subscriptionSearchParams: no
// modifier, no other name, each once, each value of its form.
func checkSearchParams(params []SubscriptionQueryParam) error {
	seen := map[string]bool{}
	for _, p := range params {
		check, ok := subscriptionSearchParams[p.Name]
		switch {
		case strings.Contains(p.Name, ":"):
			return fmt.Errorf("%w: %q carries a modifier", ErrSubscriptionRequestQuery, p.Name)
		case !ok:
			return fmt.Errorf("%w: %q is not a parameter a search carries (url, status, _id, _count, _elements)", ErrSubscriptionRequestQuery, p.Name)
		case seen[p.Name]:
			return fmt.Errorf("%w: %q is given twice", ErrSubscriptionRequestQuery, p.Name)
		case !check(p.Value):
			return fmt.Errorf("%w: %q has a value that is not one it carries", ErrSubscriptionRequestQuery, p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// parseSubscriptionPage reads a page's query (ParseSubscriptionRequest): its
// parameters in the order a HAPI FHIR server writes a page's link.
func parseSubscriptionPage(raw string) ([]SubscriptionQueryParam, error) {
	params, err := decodeSubscriptionQuery(raw)
	if err != nil {
		return nil, err
	}
	if len(params) == 0 || params[0].Name != "_getpages" || !subscriptionPageID.MatchString(params[0].Value) {
		return nil, fmt.Errorf("%w: a page opens with one _getpages", ErrSubscriptionRequestQuery)
	}
	next := 0
	optional := []struct {
		name string
		ok   func(string) bool
	}{
		{"_getpagesoffset", subscriptionCount.MatchString},
		{"_count", subscriptionCount.MatchString},
		{"_bundletype", func(v string) bool { return v == "searchset" }},
		{"_elements", func(v string) bool { return allOf(v, subscriptionElement.MatchString) }},
	}
	for _, p := range params[1:] {
		for next < len(optional) && optional[next].name != p.Name {
			next++
		}
		if next == len(optional) || !optional[next].ok(p.Value) {
			return nil, fmt.Errorf("%w: a page carries _getpages, then only _getpagesoffset, _count, _bundletype=searchset and _elements, in that order, each once", ErrSubscriptionRequestQuery)
		}
		next++
	}
	return params, nil
}

// decodeSubscriptionQuery splits and decodes raw as SubscriptionQueryParam
// says, refusing what cannot be carried as a query or read one way only. An
// empty raw is no parameter.
func decodeSubscriptionQuery(raw string) ([]SubscriptionQueryParam, error) {
	if !carriesAsQuery(raw) {
		return nil, fmt.Errorf("%w: it holds a control character, a space, a # or bytes that are not UTF-8", ErrSubscriptionRequestQuery)
	}
	if raw == "" {
		return nil, nil
	}
	var out []SubscriptionQueryParam
	for _, part := range strings.Split(raw, "&") {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%w: a parameter is empty or has no \"=\"", ErrSubscriptionRequestQuery)
		}
		n, err := decodeQueryComponent(name)
		if err != nil {
			return nil, err
		}
		v, err := decodeQueryComponent(value)
		if err != nil {
			return nil, err
		}
		out = append(out, SubscriptionQueryParam{Name: n, Value: v})
	}
	return out, nil
}

// decodeQueryComponent decodes s as Java's URLDecoder does, which HAPI FHIR's
// query parsing uses: "+" is a space, "%XX" is the byte XX, and the bytes are
// UTF-8. A "%" not followed by two hexadecimal digits, which that decoder
// refuses, is refused, as are decoded bytes that are not UTF-8 (which it would
// replace) and a decoded control character.
func decodeQueryComponent(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '+':
			b.WriteByte(' ')
		case '%':
			if i+2 >= len(s) {
				return "", fmt.Errorf("%w: a malformed %% escape", ErrSubscriptionRequestQuery)
			}
			v, err := hex.DecodeString(s[i+1 : i+3])
			if err != nil {
				return "", fmt.Errorf("%w: a malformed %% escape", ErrSubscriptionRequestQuery)
			}
			b.Write(v)
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	out := b.String()
	if !utf8.ValidString(out) {
		return "", fmt.Errorf("%w: decoded bytes that are not UTF-8", ErrSubscriptionRequestQuery)
	}
	for i := 0; i < len(out); i++ {
		if c := out[i]; c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("%w: a decoded control character", ErrSubscriptionRequestQuery)
		}
	}
	return out, nil
}

// SubscriptionSearchQuery is a Subscription search a subscriber's system sent to
// its own gateway, as ParseSubscriptionSearch reads it.
type SubscriptionSearchQuery struct {
	// URL is the url parameter's value, decoded once (SubscriptionQueryParam),
	// its own query intact.
	URL string
	// Endpoint is URL read as FHIR search reads a parameter's value, the one
	// endpoint the search names: "\,", "\$", "\|" and "\\" unescaped to a
	// comma, a dollar sign, a vertical bar and a backslash.
	Endpoint string
	// Params are the search's other parameters, in the order sent, decoded.
	Params []SubscriptionQueryParam
	// Request is the frame's FrameHeaderSubscriptionRequest value for the
	// same search: "GET Subscription?" and Params re-encoded canonically
	// (each name and value as decoded, every byte but an unreserved character
	// and a comma written %XX in upper case, joined by "&" in the order sent),
	// with no url. Request.String() is the header value.
	Request SubscriptionRequest
}

// ParseSubscriptionSearch reads the raw query, after the "?" and exactly as
// sent, of a Subscription search a subscriber's system made at its own
// gateway's per-counterpart base (GET [base]/Subscription?<query>). It decodes
// the query as ParseSubscriptionRequest does (as a FHIR server built on HAPI
// FHIR decodes it), and requires exactly one url parameter, named exactly
// "url", with no modifier. Its value is read as FHIR search reads one: split
// at each comma that is not escaped, every backslash escaping a comma, a
// dollar sign, a vertical bar or a backslash, and no part empty. It must name
// exactly one endpoint, or the search is ErrSubscriptionSearchOneEndpoint;
// that endpoint, unescaped, must be an absolute http or https URL with a host
// and no backslash, space or control character, so a "+" decoded to a space
// is refused rather than read either way. Otherwise the query holds only
// status, _id, _count and _elements, each at most once, as
// ParseSubscriptionRequest holds a frame's search. Anything else is
// ErrSubscriptionRequestQuery, and the gateway sends nothing on.
//
// The one-endpoint rule is checked as soon as the url value is read: before
// the endpoint rule, before the other parameters' rules and before a later
// url is refused, so a search that names more than one endpoint is
// ErrSubscriptionSearchOneEndpoint, recorded as routing, even when another
// rule would also refuse it. Only a query that cannot be decoded, and a url
// value that cannot be split one way, whose endpoints cannot be counted, are
// refused before it.
//
// Endpoint is the published reading of a url search value, which the
// network's gateways read the same way.
func ParseSubscriptionSearch(rawQuery string) (SubscriptionSearchQuery, error) {
	params, err := decodeSubscriptionQuery(rawQuery)
	if err != nil {
		return SubscriptionSearchQuery{}, err
	}
	var s SubscriptionSearchQuery
	urls := 0
	for _, p := range params {
		if p.Name != "url" {
			s.Params = append(s.Params, p)
			continue
		}
		if urls++; urls > 1 {
			return SubscriptionSearchQuery{}, fmt.Errorf("%w: a search carries exactly one url", ErrSubscriptionRequestQuery)
		}
		values, err := searchURLValues(p.Value)
		if err != nil {
			return SubscriptionSearchQuery{}, err
		}
		if len(values) != 1 {
			return SubscriptionSearchQuery{}, ErrSubscriptionSearchOneEndpoint
		}
		if !isSearchEndpoint(values[0]) {
			return SubscriptionSearchQuery{}, fmt.Errorf("%w: the url value is not an absolute http or https URL with no backslash, space or control character", ErrSubscriptionRequestQuery)
		}
		s.URL, s.Endpoint = p.Value, values[0]
	}
	if err := checkSearchParams(s.Params); err != nil {
		return SubscriptionSearchQuery{}, err
	}
	if urls == 0 {
		return SubscriptionSearchQuery{}, fmt.Errorf("%w: a search carries exactly one url", ErrSubscriptionRequestQuery)
	}
	parts := make([]string, len(s.Params))
	for i, p := range s.Params {
		parts[i] = canonicalQueryComponent(p.Name) + "=" + canonicalQueryComponent(p.Value)
	}
	s.Request = SubscriptionRequest{Interaction: SubscriptionSearch, Method: "GET", RawQuery: strings.Join(parts, "&"), Params: s.Params}
	return s, nil
}

// searchURLValues splits a url search value as FHIR search does
// (ParseSubscriptionSearch). A backslash before any other character or at the end, or an empty
// part, cannot be read one way only.
func searchURLValues(v string) ([]string, error) {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '\\':
			if i+1 == len(v) || !strings.ContainsRune(`,$|\`, rune(v[i+1])) {
				return nil, fmt.Errorf("%w: the url value holds a backslash that escapes nothing", ErrSubscriptionRequestQuery)
			}
			i++
			cur.WriteByte(v[i])
		case ',':
			if cur.Len() == 0 {
				return nil, fmt.Errorf("%w: the url value names an empty endpoint", ErrSubscriptionRequestQuery)
			}
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() == 0 {
		return nil, fmt.Errorf("%w: the url value names an empty endpoint", ErrSubscriptionRequestQuery)
	}
	return append(out, cur.String()), nil
}

// isSearchEndpoint reports whether v, a url search value's one endpoint
// unescaped, is an absolute http or https URL with a host, and holds no
// backslash, space or control character.
func isSearchEndpoint(v string) bool {
	// url.Parse refuses a control character.
	if strings.ContainsAny(v, " \\") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// unreservedQueryBytes are the bytes canonicalQueryComponent writes as
// themselves: RFC 3986's unreserved characters, and the comma.
const unreservedQueryBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~,"

// canonicalQueryComponent writes s with every byte but an unreserved
// character (RFC 3986: letters, digits, "-", ".", "_", "~") and a comma as
// %XX, in upper case.
func canonicalQueryComponent(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreservedQueryBytes, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}
