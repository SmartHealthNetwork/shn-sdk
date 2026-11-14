package shnsdk

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// MessageFrameV1 is the capability token a holder advertises in its registry
// entry (messageFrames) to negotiate the v1 sealed message frame (the opaque-payload
// message-frame contract). A leg uses the frame iff BOTH
// ends advertise it; absent ⇒ the pre-v0.27.0 bare-payload contract.
const MessageFrameV1 = "v1"

// SupportedMessageFrames returns the frame versions THIS library implements.
// Registration self-declares it (the library, not the app, owns the codec).
func SupportedMessageFrames() []string { return []string{MessageFrameV1} }

// SupportsMessageFrameV1 reports whether a holder's advertised frames include v1.
func SupportsMessageFrameV1(frames []string) bool {
	for _, f := range frames {
		if f == MessageFrameV1 {
			return true
		}
	}
	return false
}

// RequestFrameV1 is the capability token a holder advertises in its registry
// entry (requestFrames) to accept a v1 sealed frame on the REQUEST payload
// (the request-frame contract). It reuses the message-frame codec verbatim —
// the only new thing is WHO may emit it: an originator frames a request iff the
// leg is contract-mapped AND the recipient declares this capability, so a peer
// that never declares it keeps receiving byte-identical bare requests. Receivers
// that declare it MUST accept BOTH framed and bare requests (bare = a
// pre-request-frame sender or a version-neutral leg — always tolerated).
const RequestFrameV1 = "v1"

// SupportedRequestFrames returns the request-frame capabilities THIS library
// implements: RequestFrameV1 and RequestFrameV1Op. Registration self-declares
// it (SupportedMessageFrames precedent) — the capability defaults ON for SHN
// builds. A registration keeps what its build declared until it registers or
// rotates again, so a holder registered with an earlier build declares v1op
// only after it re-declares from this one.
func SupportedRequestFrames() []string { return []string{RequestFrameV1, RequestFrameV1Op} }

// SupportsRequestFrameV1 reports whether a holder's advertised request frames
// include v1. Absent ⇒ NOT capable (the request-frame fence: no framed request is ever sent
// to a peer that did not declare it).
func SupportsRequestFrameV1(frames []string) bool {
	for _, f := range frames {
		if f == RequestFrameV1 {
			return true
		}
	}
	return false
}

// RequestFrameV1Op is the capability token a holder advertises in its
// registry entry (requestFrames) when it accepts a DTR request frame that
// names its operation in the FrameHeaderOperation header, with the
// operation's own input as the body. A receiver that does not know that
// header drops it without error, so a requester sends a framed DTR operation
// only to a holder that declares this token, and otherwise refuses before
// sending (ErrFramedDTRUnsupported).
//
// Declaring v1op also declares that the holder accepts request frames for
// dtr-questionnaire-fetch: a requester frames the operation to a v1op peer
// whether or not the peer also declares RequestFrameV1. Other transaction
// types are framed only to a peer that declares RequestFrameV1.
//
// SupportedRequestFrames lists it: this library builds and serves framed DTR
// operations, so every registration it builds declares the capability.
const RequestFrameV1Op = "v1op"

// SupportsRequestFrameV1Op reports whether a holder's advertised request
// frames include v1op. Absent means the holder does not accept framed DTR
// operations.
func SupportsRequestFrameV1Op(frames []string) bool {
	for _, f := range frames {
		if f == RequestFrameV1Op {
			return true
		}
	}
	return false
}

// RequestFrameV1OpSub is the capability token a holder advertises in its
// registry entry (requestFrames) when it accepts the
// FrameHeaderOperationSubpath header on a framed DTR operation. A receiver
// that does not know that header drops it without error, so a requester
// sends the header only to a holder that declares this token.
//
// SupportedRequestFrames does not list it: reading the header is the
// receiver's own capability, which this library's codec does not supply by
// itself, so a holder declares the token in its own registration.
const RequestFrameV1OpSub = "v1opsub"

// SupportsRequestFrameV1OpSub reports whether a holder's advertised request
// frames include v1opsub. Absent means the holder does not accept
// FrameHeaderOperationSubpath.
func SupportsRequestFrameV1OpSub(frames []string) bool {
	for _, f := range frames {
		if f == RequestFrameV1OpSub {
			return true
		}
	}
	return false
}

// RequestFrameCDexLoc is the capability token a provider's holder advertises
// in its registry entry (requestFrames) when its answer to a payer's CDex
// attachment-request Task frames FrameHeaderTaskLocation on every 2xx answer
// whose Location lies under the provider's own Task endpoint. A payer's
// gateway reads the token to tell a provider that framed no location for a
// reason of its own from one whose gateway predates the header.
//
// SupportedRequestFrames does not list it: framing the header is the
// answering side's own capability, which this library's codec does not supply
// by itself, so a holder declares the token in its own registration.
const RequestFrameCDexLoc = "cdexloc"

// SupportsRequestFrameCDexLoc reports whether a holder's advertised request
// frames include cdexloc. Absent means the holder does not frame
// FrameHeaderTaskLocation.
func SupportsRequestFrameCDexLoc(frames []string) bool {
	for _, f := range frames {
		if f == RequestFrameCDexLoc {
			return true
		}
	}
	return false
}

// RequestFrameV1CDS is the capability token a holder advertises in its
// registry entry (requestFrames) when it accepts FrameHeaderCDSService on a
// framed CDS Hooks request, and calls the service that header names. A
// receiver that does not know the header drops it without error and would
// choose a service by the request's hook instead, so a requester sends the
// header only to a holder that declares this token.
//
// SupportedRequestFrames does not list it: calling the named service is the
// receiver's own capability, which this library's codec does not supply by
// itself, so a holder declares the token in its own declaration.
const RequestFrameV1CDS = "v1cds"

// SupportsRequestFrameV1CDS reports whether a holder's advertised request
// frames include v1cds. Absent means the holder does not accept
// FrameHeaderCDSService.
func SupportsRequestFrameV1CDS(frames []string) bool {
	for _, f := range frames {
		if f == RequestFrameV1CDS {
			return true
		}
	}
	return false
}

// FrameHeaderCDSService is the request-frame header naming the recipient's
// own CDS service a CDS Hooks request is for: an id from the listing the
// recipient's system declared (CapabilityDeclaration.CDSServices). It sits
// inside the sealed payload, so the Hub never sees it, and is sent only to a
// holder that declares RequestFrameV1CDS.
const FrameHeaderCDSService = "cdsService"

// FrameHeaderOperation is the request-frame header naming the DTR operation
// whose input is the frame body: FrameOperationQuestionnairePackage (the
// body is the $questionnaire-package input Parameters) or
// FrameOperationNextQuestion (the body is the SDC $next-question input, a
// Parameters or a bare QuestionnaireResponse). It sits inside the sealed
// payload, so the Hub never sees it.
const FrameHeaderOperation = "operation"

// DTR operations named by FrameHeaderOperation.
const (
	FrameOperationQuestionnairePackage = "questionnaire-package"
	FrameOperationNextQuestion         = "next-question"
)

// The headers of a framed DTR operation's address. Each sits inside the
// sealed payload, so the Hub never sees it, and each is framed only for a
// framed DTR operation (FrameHeaderOperation).
const (
	// FrameHeaderOperationSubpath is a request-frame header, beside
	// FrameHeaderOperation, naming the address below the operation's own that
	// the operation was called at: "/payer/<line>/<tag><remainder>" or
	// "/fetch-server/<line>/<tag>". <line> is a contract line, <tag> is 22
	// base64url characters, and <remainder> is empty or one or more segments,
	// each "/" then one or more unreserved characters, none of them "." or
	// "..". A requester sends it
	// only to a holder that declares RequestFrameV1OpSub.
	FrameHeaderOperationSubpath = "operationSubpath"

	// FrameHeaderOperationBase is an answer-frame header, framed only for a 2xx
	// answer: the base the answering side added the operation's path to, an
	// absolute URL with no query, no fragment and no ending slash. It is the
	// one header on the allowlist that is an address into a participant's
	// system.
	FrameHeaderOperationBase = "operationBase"

	// FrameHeaderOperationAddressTags is an answer-frame header, framed only
	// for a 2xx answer: one "<remainder>=<tag>" pair per address, separated by
	// spaces. <remainder> is what the answer's body writes after
	// FrameHeaderOperationBase, possibly empty, or "*" for an address the
	// body states as the boolean true; <tag> is 22 base64url characters.
	FrameHeaderOperationAddressTags = "operationAddressTags"
)

// The headers of a payer's CDex Task at a provider. Each sits inside the
// sealed payload, so the Hub never sees it. Each is a path relative to a base
// the receiving gateway supplies, never an address into a participant's
// system (FrameHeaderOperationBase stays the one header that is).
// ParseCDexTaskReadPath and ParseCDexTaskLocation read their values.
const (
	// FrameHeaderReadPath is a request-frame header, framed only on the
	// cdex-task-status leg, whose request has no body: the path of the
	// payer system's read below its base, "Task/<id>" or
	// "Task/<id>/_history/<vid>", then "?" and the query as sent, if the
	// read had one. <id> and <vid> are FHIR ids.
	FrameHeaderReadPath = "readPath"

	// FrameHeaderTaskLocation is an answer-frame header, framed only for a
	// 2xx answer to a payer's CDex attachment-request Task: the tail of the
	// Location the provider's system answered, "Task/<id>" or
	// "Task/<id>/_history/<vid>", when that Location lies under the
	// provider's own Task endpoint. <id> and <vid> are FHIR ids. A provider's
	// holder that frames it declares RequestFrameCDexLoc.
	FrameHeaderTaskLocation = "taskLocation"
)

// The headers of a topic Subscription and its notifications, which cross the
// network sealed. Each sits inside the
// sealed payload, so the Hub never sees it, and each is carried exactly. Each
// is framed only on a subscription's subscribe or signal leg, so
// taking the leg implies them and no request-frame token declares them.
const (
	// FrameHeaderSubscriptionEndpoint is a request-frame header. On a
	// subscribe leg's create or update, it is the subscriber's
	// channel.endpoint sealed by its gateway, "<kid>.<sealed>", with <kid> 11
	// base64url characters and <sealed> base64url; on a search by url during
	// a sealing-key overlap, two such values separated by one space. On a
	// signal leg, it is the sealed endpoint read from the
	// notification address the notifier's system posted to.
	FrameHeaderSubscriptionEndpoint = "subscriptionEndpoint"

	// FrameHeaderSubscriptionChannel is a request-frame header: the
	// subscriber's channel.header values, sealed by its gateway beside the
	// sealed endpoint, "<kid>.<sealed>". It is framed on a subscribe leg's
	// create or update, and on every notification.
	FrameHeaderSubscriptionChannel = "subscriptionChannel"

	// FrameHeaderStatedBase is the notifier's FHIR base that a message's
	// absolute addresses lie under, an absolute URL with no query, no
	// fragment and no ending slash, carried exactly. It is framed in two
	// places: as an answer-frame header for a 2xx answer on a subscribe leg,
	// whose absolute addresses (an entry's fullUrl, a paging link) it names
	// the base of; and as a request-frame header on a signal leg, for the
	// notification's absolute addresses. The subscriber's gateway decides
	// which absolute addresses it re-keys to its per-counterpart base by a
	// match against this base at a path segment's boundary, never by text.
	// This library reads no shape into the value: checking it is the
	// gateway's. Like FrameHeaderOperationBase, it is an address into a
	// participant's system.
	FrameHeaderStatedBase = "statedBase"

	// FrameHeaderSubscriptionLocation is an answer-frame header, framed only
	// for a 2xx answer to a subscribe leg's create or update: the tail of the
	// Location the notifier's system answered, "Subscription/<id>" or
	// "Subscription/<id>/_history/<vid>", when that Location lies under the
	// notifier's own base, matched at a path segment's boundary and never as
	// a string prefix. Deciding that is the notifier's gateway's; otherwise it
	// frames no location. <id> and <vid> are FHIR ids, and
	// ParseSubscriptionLocation reads the value. Like FrameHeaderTaskLocation,
	// it is a path relative to a base the receiving gateway supplies, never
	// an address into a participant's system.
	FrameHeaderSubscriptionLocation = "subscriptionLocation"

	// FrameHeaderSubscriptionRequest is a request-frame header, framed on
	// every request of a subscription's subscribe leg: the interaction the
	// subscriber's system made at its per-counterpart base, as the method,
	// one space, and the path below that base with its query, from a closed
	// set (ParseSubscriptionRequest reads it): "POST Subscription",
	// "GET Subscription/<id>[?<query>]",
	// "GET Subscription/<id>/_history/<vid>[?<query>]",
	// "PUT Subscription/<id>", "DELETE Subscription/<id>",
	// "GET Subscription/<id>/$status", "GET Subscription?<query>" and
	// "GET ?_getpages=<x>…". A search's query never carries url: the
	// endpoint travels only sealed, in FrameHeaderSubscriptionEndpoint, and
	// the notifier's gateway writes the url (ParseSubscriptionSearch writes
	// the value from the subscriber's own search). Like FrameHeaderReadPath,
	// it is a path relative to a base the receiving gateway supplies, never
	// an address into a participant's system.
	FrameHeaderSubscriptionRequest = "subscriptionRequest"
)

// FrameHeaderIfMatch is a request-frame header, framed only on a
// subscription's subscribe leg's update and delete: the If-Match the
// subscriber's system sent, carried exactly as sent. The network adds none. Like the answer headers, it is spelled as the
// HTTP field it carries, and a frame carries the value, it does not judge it
// as a validator.
const FrameHeaderIfMatch = "If-Match"

// ErrFramedDTRUnsupported: the payer has not declared RequestFrameV1Op, so a
// framed DTR operation is not sent to it.
var ErrFramedDTRUnsupported = errors.New("payer gateway does not support framed DTR operations (upgrade required)")

const (
	frameMagic    byte = 0x00 // illegal first byte of every text format we carry (JSON/X12/XML/HL7v2)
	frameVersion1 byte = 0x01
	// maxFrameHeaderBytes caps the header segment (DoS guard); the body is
	// already capped by MaxRequestBytes/MaxResponseBytes at the wire edge.
	maxFrameHeaderBytes = 64 << 10
)

// HTTPFrameHeader is the HTTP-family v1 frame header: the application status a
// transport status can no longer carry, plus allowlisted headers.
type HTTPFrameHeader struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
}

// FrameHeaderContractVersion is the frame header carrying the full
// "<contract>@<line>" token of the line the sealed payload was BUILT at
// (each frame is one contract's message; the token is
// content-descriptive like Content-Type, not a negotiation echo). Inside the
// ciphertext, so the Hub cannot see it. Absence means "pre-version contract"
// (the frames-absent lane precedent) and is always tolerated.
const FrameHeaderContractVersion = "contractVersion"

// The headers a producer may frame for a participant's 2xx answer. They describe
// the answer; none is an address into the participant's system. (The one answer
// header that is such an address is FrameHeaderOperationBase, framed only for a
// framed DTR operation's answer; see its comment.) A frame carries
// a value as sent, and never judges it as a validator or a date. The codec reads
// no status: keeping them to a 2xx answer is the producer's rule. A decoder keeps
// a value as it came, so a consumer that writes one on checks it with
// FrameCarriesHeaderValue first.
const (
	FrameHeaderETag         = "ETag"
	FrameHeaderLastModified = "Last-Modified"
	FrameHeaderRetryAfter   = "Retry-After"
)

// allowedFrameHeaders is the produce+consume header allowlist: relaying
// arbitrary headers through the seal would be a smuggling vector (cookies,
// hop-by-hop, internal headers). Widening it is a spec change — contractVersion
// was added with the multi-version contracts design, operation with framed
// DTR operations, the answer's own headers above to carry a 2xx as sent, the
// operation's address headers (operationSubpath, operationBase,
// operationAddressTags) with framed DTR operations' addresses, the CDex
// Task's headers (readPath, taskLocation) with a payer's read of its Task,
// cdsService with a CDS Hooks request naming the recipient's own service, and
// a topic Subscription's headers (subscriptionEndpoint, subscriptionChannel,
// statedBase, subscriptionRequest, subscriptionLocation) and If-Match with
// notifications across the network.
var allowedFrameHeaders = map[string]bool{
	"Content-Type": true, FrameHeaderContractVersion: true, FrameHeaderOperation: true,
	FrameHeaderETag: true, FrameHeaderLastModified: true, FrameHeaderRetryAfter: true,
	FrameHeaderOperationSubpath: true, FrameHeaderOperationBase: true, FrameHeaderOperationAddressTags: true,
	FrameHeaderReadPath: true, FrameHeaderTaskLocation: true, FrameHeaderCDSService: true,
	FrameHeaderSubscriptionEndpoint: true, FrameHeaderSubscriptionChannel: true, FrameHeaderStatedBase: true,
	FrameHeaderSubscriptionRequest: true, FrameHeaderSubscriptionLocation: true, FrameHeaderIfMatch: true,
}

// frameHeaderNames maps each allowlisted header's name, folded to ASCII lower
// case, to the spelling a frame carries: the allowlist's own. HTTP field names
// are case-insensitive (RFC 9110 section 5.1), so "ETag", "Etag" (Go's
// http.CanonicalHeaderKey) and "etag" are one header. Only ASCII letters fold,
// so no other character can come to spell an allowlisted name.
var frameHeaderNames = func() map[string]string {
	m := make(map[string]string, len(allowedFrameHeaders))
	for k := range allowedFrameHeaders {
		m[asciiLower(k)] = k
	}
	return m
}()

// frameHeaderName is the frame's spelling of the allowlisted header k names,
// in any case, and whether k names one.
func frameHeaderName(k string) (string, bool) {
	n, ok := frameHeaderNames[asciiLower(k)]
	return n, ok
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 0x41 && c <= 0x5a { // an ASCII upper-case letter
			b[i] = c + 0x20
		}
	}
	return string(b)
}

// exactFrameHeaders are the headers whose values a frame must carry exactly:
// the three answer headers above, carried as sent, the operation's address
// headers, whose tags are read byte for byte, the CDex Task's headers, a
// path a gateway sends on as it reads it, cdsService, a service id the
// recipient's system declared, a topic Subscription's headers, sealed
// values, a base, an interaction and a location read byte for byte, and
// If-Match, carried as sent. Content-Type keeps the encoder's older rule.
var exactFrameHeaders = map[string]bool{
	FrameHeaderETag: true, FrameHeaderLastModified: true, FrameHeaderRetryAfter: true,
	FrameHeaderOperationSubpath: true, FrameHeaderOperationBase: true, FrameHeaderOperationAddressTags: true,
	FrameHeaderReadPath: true, FrameHeaderTaskLocation: true, FrameHeaderCDSService: true,
	FrameHeaderSubscriptionEndpoint: true, FrameHeaderSubscriptionChannel: true, FrameHeaderStatedBase: true,
	FrameHeaderSubscriptionRequest: true, FrameHeaderSubscriptionLocation: true, FrameHeaderIfMatch: true,
}

// ErrFrameHeaderTooLarge is a header segment over the limit DecodeHTTPFrame
// reads. The frame would not be readable, so it is not made.
var ErrFrameHeaderTooLarge = errors.New("shnsdk: frame header segment over the limit")

// ErrFrameHeaderDuplicate is one allowlisted header given twice in a frame's
// header segment, under one spelling or two (names match in any case). A
// producer that gives it twice is refused; a frame that carries it twice with
// different values cannot say which it means, so it is not read: it is
// malformed framing, a failure of the frame's integrity. The same value twice
// is that value.
var ErrFrameHeaderDuplicate = errors.New("shnsdk: frame header given twice")

// ErrFrameHeaderValue is a value of one of the answer headers, of the
// address headers, or of the CDex Task's headers, that a frame cannot carry
// exactly (FrameCarriesHeaderValue).
// The frame's JSON would change it, so it is not made.
var ErrFrameHeaderValue = errors.New("shnsdk: frame header value cannot be carried exactly")

// FrameCarriesHeaderValue reports whether a frame's encoding keeps v exactly:
// it is an HTTP field value, with no control character but a tab (RFC 9110
// section 5.5), and valid UTF-8, since a frame's header segment is JSON, which
// would replace any other byte. That is all it answers. A value it passes and
// that is odd as a validator or a date is carried as sent. It does not judge
// size, which EncodeHTTPFrameHeaders reports for the segment as a whole, and an
// empty value passes but is omitted, as every empty header is.
func FrameCarriesHeaderValue(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for i := 0; i < len(v); i++ {
		if b := v[i]; (b < 0x20 && b != '\t') || b == 0x7f {
			return false
		}
	}
	return true
}

// IsFramed reports whether payload begins with the v1 frame magic. Bare legacy
// payloads are all text formats, which cannot begin 0x00 — see the spec's
// stale-feed fallback argument.
func IsFramed(payload []byte) bool { return len(payload) > 0 && payload[0] == frameMagic }

// EncodeHTTPFrame seals an application answer (status + optional Content-Type +
// raw body) into a v1 message frame. The body is carried raw — the sealed box
// already handles arbitrary bytes, so there is no inner base64. A header
// segment DecodeHTTPFrame would refuse is ErrFrameHeaderTooLarge.
func EncodeHTTPFrame(status int, contentType string, body []byte) ([]byte, error) {
	if status < 100 || status > 599 {
		return nil, fmt.Errorf("shnsdk: frame status %d out of range", status)
	}
	hdr := HTTPFrameHeader{Status: status}
	if contentType != "" {
		hdr.Headers = map[string]string{"Content-Type": contentType}
	}
	hj, err := json.Marshal(hdr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal frame header: %w", err)
	}
	if len(hj) > maxFrameHeaderBytes {
		return nil, ErrFrameHeaderTooLarge
	}
	out := make([]byte, 0, 6+len(hj)+len(body))
	out = append(out, frameMagic, frameVersion1)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(hj)))
	out = append(out, l[:]...)
	out = append(out, hj...)
	return append(out, body...), nil
}

// EncodeHTTPFrameHeaders seals an application answer with an explicit header
// map. Every header must be allowlisted — producing a header the consumer
// would strip (DecodeHTTPFrame deletes non-allowlisted keys) is a caller bug
// surfaced as an error, never a silent drop. A name is matched in any case and
// framed under the allowlist's spelling; two keys naming one header are an
// error too. Empty-valued headers are omitted.
// A header segment DecodeHTTPFrame would refuse is ErrFrameHeaderTooLarge, and
// a value of one of the answer headers, of the address headers, of the CDex
// Task's headers, or of cdsService, that the frame would change is
// ErrFrameHeaderValue.
func EncodeHTTPFrameHeaders(status int, headers map[string]string, body []byte) ([]byte, error) {
	if status < 100 || status > 599 {
		return nil, fmt.Errorf("shnsdk: frame status %d out of range", status)
	}
	hdr := HTTPFrameHeader{Status: status}
	named := make(map[string]string, len(headers))
	for _, k := range slices.Sorted(maps.Keys(headers)) {
		v := headers[k]
		name, ok := frameHeaderName(k)
		if !ok {
			return nil, fmt.Errorf("shnsdk: frame header %q is not allowlisted", k)
		}
		if other, dup := named[name]; dup {
			return nil, fmt.Errorf("%w: %s, as %q and %q", ErrFrameHeaderDuplicate, name, other, k)
		}
		named[name] = k
		if exactFrameHeaders[name] && !FrameCarriesHeaderValue(v) {
			return nil, fmt.Errorf("%w: %s", ErrFrameHeaderValue, name)
		}
		if v == "" {
			continue
		}
		if hdr.Headers == nil {
			hdr.Headers = map[string]string{}
		}
		hdr.Headers[name] = v
	}
	hj, err := json.Marshal(hdr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal frame header: %w", err)
	}
	if len(hj) > maxFrameHeaderBytes {
		return nil, ErrFrameHeaderTooLarge
	}
	out := make([]byte, 0, 6+len(hj)+len(body))
	out = append(out, frameMagic, frameVersion1)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(hj)))
	out = append(out, l[:]...)
	out = append(out, hj...)
	return append(out, body...), nil
}

// DecodeHTTPFrame strictly decodes a v1 frame: magic, version, header length
// (bounded), header JSON, status range; non-allowlisted headers are dropped on
// consume. An allowlisted header is read in any case and returned under the
// allowlist's spelling; one given twice with different values, under one
// spelling or two, is ErrFrameHeaderDuplicate. The remaining bytes are the
// verbatim application body.
func DecodeHTTPFrame(payload []byte) (HTTPFrameHeader, []byte, error) {
	return decodeHTTPFrame(payload, frameHeaderNames)
}

// decodeHTTPFrame is DecodeHTTPFrame reading the allowlist names gives (each
// name folded to ASCII lower case, mapped to its frame spelling), so a test
// can decode as a release with another allowlist does.
func decodeHTTPFrame(payload []byte, names map[string]string) (HTTPFrameHeader, []byte, error) {
	if len(payload) < 6 || payload[0] != frameMagic {
		return HTTPFrameHeader{}, nil, errors.New("shnsdk: not a message frame")
	}
	if payload[1] != frameVersion1 {
		return HTTPFrameHeader{}, nil, fmt.Errorf("shnsdk: unsupported frame version 0x%02x", payload[1])
	}
	hlen := binary.BigEndian.Uint32(payload[2:6])
	if hlen > maxFrameHeaderBytes || uint64(hlen) > uint64(len(payload)-6) {
		return HTTPFrameHeader{}, nil, errors.New("shnsdk: frame header length out of bounds")
	}
	var hdr HTTPFrameHeader
	if err := json.Unmarshal(payload[6:6+hlen], &hdr); err != nil {
		return HTTPFrameHeader{}, nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
	}
	if hdr.Status < 100 || hdr.Status > 599 {
		return HTTPFrameHeader{}, nil, fmt.Errorf("shnsdk: frame status %d out of range", hdr.Status)
	}
	if hdr.Headers != nil {
		// The map Unmarshal filled keeps the last of two equal keys; the segment's
		// own key/value pairs keep every one, so a header given twice is seen
		// however it is spelled.
		pairs, err := frameHeaderPairs(payload[6 : 6+hlen])
		if err != nil {
			return HTTPFrameHeader{}, nil, err
		}
		named := make(map[string]string, len(pairs))
		for _, kv := range pairs {
			name, ok := names[asciiLower(kv[0])]
			if !ok {
				continue
			}
			if prev, dup := named[name]; dup && prev != kv[1] {
				return HTTPFrameHeader{}, nil, fmt.Errorf("%w: %s, with different values", ErrFrameHeaderDuplicate, name)
			}
			named[name] = kv[1]
		}
		hdr.Headers = named
	}
	return hdr, payload[6+hlen:], nil
}

// frameHeaderPairs is every key and value of a header segment's headers
// object, in order, duplicates included, read as json.Unmarshal reads them into
// HTTPFrameHeader.Headers: encoding/json matches the field name in any case, so
// every top-level key that names headers is read into the one set, and a null
// one empties it, as it sets the map to nil. The segment has already
// unmarshalled, so a value is a string or null, which reads as "", as Unmarshal
// reads it.
func frameHeaderPairs(segment []byte) ([][2]string, error) {
	dec := json.NewDecoder(bytes.NewReader(segment))
	if _, err := dec.Token(); err != nil { // {
		return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
	}
	var pairs [][2]string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
		}
		key, _ := t.(string)
		if !strings.EqualFold(key, "headers") {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
			}
			continue
		}
		if t, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
		} else if t == nil { // null
			pairs = pairs[:0]
			continue
		}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
			}
			var v string
			if err := dec.Decode(&v); err != nil {
				return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
			}
			k, _ := kt.(string)
			pairs = append(pairs, [2]string{k, v})
		}
		if _, err := dec.Token(); err != nil { // }
			return nil, fmt.Errorf("shnsdk: frame header not valid JSON: %w", err)
		}
	}
	return pairs, nil
}

// AppAnswerError is a recipient's non-2xx APPLICATION answer, carried verbatim
// out of a framed response leg. It is the SDK-surface sibling of the gateway
// engine's RelayError: the exchange machinery succeeded — the counterparty
// answered, negatively. Callers errors.As for it to show the real payload.
type AppAnswerError struct {
	Status      int
	ContentType string
	Body        []byte
}

func (e *AppAnswerError) Error() string {
	const max = 512
	b := e.Body
	if len(b) > max {
		b = b[:max]
	}
	return fmt.Sprintf("shnsdk: recipient answered %d: %s", e.Status, b)
}

// unframeAnswer applies the originator side of frame negotiation to an opened
// response payload: any payload bearing the frame magic is decoded — its body
// (2xx) or an *AppAnswerError (non-2xx) — and a bare payload passes through
// verbatim. Decoding is keyed solely on the magic byte, NOT on the payer's
// advertised frames: 0x00 cannot begin any bare payload we carry (JSON/X12/XML/
// HL7v2 text), so decode-on-magic never misclassifies, and it closes the inverse
// stale-feed window (a payer that correctly frames to a v1-advertising requester
// while our view of the payer is still pre-upgrade — dynamic re-registration,
// rolling deploys). The payer's advertised frames are therefore advisory only and
// not an input here.
//
// expectedToken is the contractVersion stamp-verify check (multi-version contracts design, published-SDK
// parity — v0.38.0): when non-empty (the caller knows the contract-version token
// this leg was routed/built at) AND the 2xx frame carries a non-empty
// FrameHeaderContractVersion stamp that DIFFERS from it, the answer is rejected —
// tamper or skew, either way not the payload this leg negotiated. Verbatim
// semantics of the Smart Gateway's own response-leg verify. An ABSENT stamp is
// always tolerated (the frames-absent-lane
// precedent — a pre-version responder, or a responder that never sets
// ResponderConfig.StampContractVersion), and expectedToken == "" (the caller has no
// routed-token expectation, e.g. RunEligibility — coverage-eligibility is not a
// contract) skips the check entirely.
func unframeAnswer(plaintext []byte, expectedToken string) ([]byte, error) {
	if !IsFramed(plaintext) {
		return plaintext, nil
	}
	hdr, body, err := DecodeHTTPFrame(plaintext)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: decode response frame: %w", err)
	}
	if hdr.Status/100 != 2 {
		return nil, &AppAnswerError{Status: hdr.Status, ContentType: hdr.Headers["Content-Type"], Body: body}
	}
	if expectedToken != "" {
		if stamped := hdr.Headers[FrameHeaderContractVersion]; stamped != "" && stamped != expectedToken {
			return nil, fmt.Errorf("shnsdk: response contract version mismatch: frame declares %s, leg routed %s", stamped, expectedToken)
		}
	}
	return body, nil
}
