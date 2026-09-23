package shnsdk

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// only after it re-declares from this one. A gateway with an additional receiver
// must declare that capability from its verified serving artifact; the SDK
// registrant alone does not establish gateway support.
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

// FrameHeaderOperation is the request-frame header naming the DTR operation
// whose input is the frame body: FrameOperationQuestionnairePackage (the
// body is the $questionnaire-package input Parameters) or
// FrameOperationNextQuestion (the body is the SDC $next-question input, a
// Parameters or a bare QuestionnaireResponse). It sits inside the sealed
// payload, so the Hub never sees it.
const FrameHeaderOperation = "operation"

// FrameHeaderCRDHook is request-only CRD addressing, inside the sealed frame.
// It is never an HTTP header or plaintext routing credential.
const FrameHeaderCRDHook = "crdHook"

// RequestFrameV1CRD declares CRD hook-aware request service selection. Codec
// support alone does not entitle a responder to advertise this capability.
// It does not imply DTR operation support.
const RequestFrameV1CRD = "v1crd"

func SupportsRequestFrameV1CRD(frames []string) bool {
	for _, f := range frames {
		if f == RequestFrameV1CRD {
			return true
		}
	}
	return false
}

// DTR operations named by FrameHeaderOperation.
const (
	FrameOperationQuestionnairePackage = "questionnaire-package"
	FrameOperationNextQuestion         = "next-question"
)

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
// "<contract>@<line>" token of the representation its producer declares (or
// a gateway builder actually built). It is content-descriptive like
// Content-Type, not a negotiation echo or a validation certificate. Inside the
// ciphertext, so the Hub cannot see it. Absence means "pre-version contract"
// (the frames-absent lane precedent) and is always tolerated.
const FrameHeaderContractVersion = "contractVersion"

// allowedFrameHeaders is the produce+consume header allowlist: relaying
// arbitrary headers through the seal would be a smuggling vector (cookies,
// hop-by-hop, internal headers). Widening it is a spec change — contractVersion
// was added with the multi-version contracts design, and operation with framed
// DTR operations.
var allowedFrameHeaders = map[string]bool{"Content-Type": true, FrameHeaderContractVersion: true, FrameHeaderOperation: true, FrameHeaderCRDHook: true}

// IsFramed reports whether payload begins with the v1 frame magic. Bare legacy
// payloads are all text formats, which cannot begin 0x00 — see the spec's
// stale-feed fallback argument.
func IsFramed(payload []byte) bool { return len(payload) > 0 && payload[0] == frameMagic }

// EncodeHTTPFrame seals an application answer (status + optional Content-Type +
// raw body) into a v1 message frame. The body is carried raw — the sealed box
// already handles arbitrary bytes, so there is no inner base64.
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
// surfaced as an error, never a silent drop. Empty-valued headers are omitted.
func EncodeHTTPFrameHeaders(status int, headers map[string]string, body []byte) ([]byte, error) {
	if status < 100 || status > 599 {
		return nil, fmt.Errorf("shnsdk: frame status %d out of range", status)
	}
	hdr := HTTPFrameHeader{Status: status}
	for k, v := range headers {
		if !allowedFrameHeaders[k] {
			return nil, fmt.Errorf("shnsdk: frame header %q is not allowlisted", k)
		}
		if v == "" {
			continue
		}
		if hdr.Headers == nil {
			hdr.Headers = map[string]string{}
		}
		hdr.Headers[k] = v
	}
	hj, err := json.Marshal(hdr)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal frame header: %w", err)
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
// consume. The remaining bytes are the verbatim application body.
func DecodeHTTPFrame(payload []byte) (HTTPFrameHeader, []byte, error) {
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
	for k := range hdr.Headers {
		if !allowedFrameHeaders[k] {
			delete(hdr.Headers, k)
		}
	}
	return hdr, payload[6+hlen:], nil
}

// AppAnswerError is a recipient's non-2xx APPLICATION answer, carried verbatim
// out of a framed response leg. It is the SDK-surface sibling of the gateway
// engine's RelayError: the exchange machinery succeeded — the counterparty
// answered, negatively. Callers errors.As for it to show the real payload.
type AppAnswerError struct {
	Status          int
	ContentType     string
	ContractVersion string
	Body            []byte
}

func (e *AppAnswerError) Error() string {
	return fmt.Sprintf("shnsdk: recipient answered %d", e.Status)
}

// PriorAuthConsumptionError reports that an authored PA workflow could not
// consume an already authenticated application answer or build its next local
// action after one. Body is the peer's exact, size-limited latest reply; Leg
// names the authored step that failed. Error deliberately omits the body and
// frame metadata. It is distinct from an authority or transport refusal.
type PriorAuthConsumptionError struct {
	Leg             string
	Code            string
	Status          int
	ContentType     string
	ContractVersion string
	Body            []byte
	Cause           error
}

func (e *PriorAuthConsumptionError) Error() string {
	if e.Leg != "" && e.Code != "" {
		return fmt.Sprintf("shnsdk: %s: %s", e.Leg, e.Code)
	}
	return "shnsdk: prior authorization could not consume received answer"
}

func (e *PriorAuthConsumptionError) Unwrap() error { return e.Cause }

type receivedAnswer struct {
	status          int
	contentType     string
	contractVersion string
	body            []byte
}

func consumptionFailure(leg, code string, answer receivedAnswer, cause error) error {
	return &PriorAuthConsumptionError{Leg: leg, Code: code, Status: answer.status,
		ContentType: answer.contentType, ContractVersion: answer.contractVersion,
		Body: answer.body, Cause: cause}
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
// expectedToken identifies the representation this SDK built for an authored
// request, not a required echo on the response. A different producer declaration
// is consumed only where this workflow has a proven reader; otherwise the exact
// received answer is available through PriorAuthConsumptionError. An absent stamp
// retains legacy behavior, and the version-neutral eligibility leg has no check.
func unframeAnswer(plaintext []byte, expectedToken string) ([]byte, error) {
	if !IsFramed(plaintext) {
		return plaintext, nil
	}
	hdr, body, err := DecodeHTTPFrame(plaintext)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: decode response frame: %w", err)
	}
	if hdr.Headers[FrameHeaderCRDHook] != "" {
		return nil, errors.New("shnsdk: CRD hook is request-only")
	}
	if hdr.Headers[FrameHeaderOperation] != "" {
		return nil, errors.New("shnsdk: operation is request-only")
	}
	if hdr.Status/100 != 2 {
		return nil, &AppAnswerError{Status: hdr.Status, ContentType: hdr.Headers["Content-Type"], ContractVersion: hdr.Headers[FrameHeaderContractVersion], Body: body}
	}
	if expectedToken != "" {
		if stamped := hdr.Headers[FrameHeaderContractVersion]; stamped != "" && stamped != expectedToken &&
			!(expectedToken == ContractPACRD20 && (stamped == ContractPACRD21 || stamped == ContractPACRD22)) &&
			!readablePASReplyLine(expectedToken, stamped) {
			return nil, &PriorAuthConsumptionError{Status: hdr.Status, ContentType: hdr.Headers["Content-Type"], ContractVersion: stamped, Body: body}
		}
	}
	return body, nil
}

// PAS request and answer lines are independently declared. This SDK's typed
// PAS reader accepts the supported PAS line family; the later response parser
// still checks the actual ClaimResponse content and returns the exact producer
// body on a local consumption failure. Unknown lines remain unavailable.
func readablePASReplyLine(requestToken, answerToken string) bool {
	requestContract, requestLine, requestOK := strings.Cut(requestToken, "@")
	answerContract, answerLine, answerOK := strings.Cut(answerToken, "@")
	if !requestOK || !answerOK || requestContract != "pa.pas" || answerContract != "pa.pas" {
		return false
	}
	_, requestSupported := PASLineDef(requestLine)
	_, answerSupported := PASLineDef(answerLine)
	return requestSupported && answerSupported
}
