package shnsdk

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// MessageFrameV1 is the capability token a holder advertises in its registry
// entry (messageFrames) to negotiate the v1 sealed message frame (spec
// 2026-07-17-opaque-payload-frame-design.md). A leg uses the frame iff BOTH
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

// allowedFrameHeaders is the produce+consume header allowlist (spec §3): relaying
// arbitrary headers through the seal would be a smuggling vector (cookies,
// hop-by-hop, internal headers). Widening it is a spec change.
var allowedFrameHeaders = map[string]bool{"Content-Type": true}

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
// verbatim. Decoding is keyed on the magic byte, NOT on the payer's advertised
// frames: 0x00 cannot begin any bare payload we carry (JSON/X12/XML/HL7v2 text),
// so decode-on-magic never misclassifies, and it closes the inverse stale-feed
// window (a payer that correctly frames to a v1-advertising requester while our
// view of the payer is still pre-upgrade — dynamic re-registration, rolling
// deploys). `frames` is retained for call-site symmetry/observability; it governs
// expectation, not the decode decision.
func unframeAnswer(frames []string, plaintext []byte) ([]byte, error) {
	_ = frames
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
	return body, nil
}
