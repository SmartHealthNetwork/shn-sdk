package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// HubRequesterBindingPath is the Hub's route for a requester-side binding
// record: POST, with an X-Holder-Assertion for the audience "hub" whose
// BodyHash binds the body, and a RequesterBinding as the body.
const HubRequesterBindingPath = "/requester-binding"

// MaxRequesterBindingBytes caps a RequesterBinding body. Three tokens fit in
// it with room to spare; a body over it cannot be read whole.
const MaxRequesterBindingBytes = 64 << 10

// The requester-side binding outcomes, a closed pair.
const (
	// RequesterBindingBound: the requester bound the patient the answer
	// disclosed, and Involved carries a token for that binding.
	RequesterBindingBound = "bound"
	// RequesterBindingUnavailable: the answer disclosed a patient the
	// requester could not bind. No patient is named.
	RequesterBindingUnavailable = "unavailable"
)

// RequesterBinding is the body of a requester-side binding record, sent to
// the Hub at HubRequesterBindingPath after an answer on a leg whose request
// names no patient (cdex-task-status): the requester's own binding of the
// patient the answer disclosed, so the Hub records the exchange under it
// beside the responder's.
//
// It names the answer, never its content: by what the response token binds,
// its correlation id and payload hash. A patient appears only as the subject
// of a token the Authorization Framework signed (Involved), as everywhere at
// the Hub.
type RequesterBinding struct {
	// RequestToken is the leg's request token, as the request envelope's
	// Metadata.AuthzToken carried it: a JSON-encoded Token. Required.
	RequestToken string `json:"requestToken"`
	// ResponseToken is the answer's token, as the answer envelope's
	// Metadata.AuthzToken carried it: a JSON-encoded Token. Required.
	ResponseToken string `json:"responseToken"`
	// Binding is RequesterBindingBound or RequesterBindingUnavailable.
	// Required, so a record with no token never reads as unavailable.
	Binding string `json:"binding"`
	// Involved is the requester's binding: present exactly when Binding is
	// RequesterBindingBound. Its token's subject is the requester's binding of
	// the patient, and its involvement is InvolvementRequesterHeld or
	// InvolvementRequesterDerived, the same as the token's own.
	Involved *InvolvedToken `json:"involved,omitempty"`
}

// ErrRequesterBinding is a RequesterBinding that is not well formed. The Hub
// refuses it 400, as message integrity.
var ErrRequesterBinding = errors.New("shnsdk: not a well-formed requester-side binding record")

// ErrRequesterBindingTooLarge is a body over MaxRequesterBindingBytes. The
// Hub refuses it 413, as message integrity: it cannot be read whole.
var ErrRequesterBindingTooLarge = errors.New("shnsdk: requester-side binding record over the limit")

// Validate checks a RequesterBinding's structure, and nothing more: both
// tokens present and each a JSON-encoded Token, Binding one of the closed
// pair, Involved present exactly when bound, and then a token with a
// requester-side involvement equal to its own. It verifies no signature and
// no binding between the tokens: those are the Hub's checks.
func (r RequesterBinding) Validate() error {
	if _, err := decodeRecordToken("requestToken", r.RequestToken); err != nil {
		return err
	}
	if _, err := decodeRecordToken("responseToken", r.ResponseToken); err != nil {
		return err
	}
	switch r.Binding {
	case RequesterBindingBound:
		if r.Involved == nil {
			return fmt.Errorf("%w: a bound record carries its involved token", ErrRequesterBinding)
		}
		if r.Involved.Involvement != InvolvementRequesterHeld && r.Involved.Involvement != InvolvementRequesterDerived {
			return fmt.Errorf("%w: involvement %q is not a requester-side value", ErrRequesterBinding, r.Involved.Involvement)
		}
		tok, err := decodeRecordToken("involved.token", r.Involved.Token)
		if err != nil {
			return err
		}
		if tok.Involvement != r.Involved.Involvement {
			return fmt.Errorf("%w: the involved token says %q, the record %q", ErrRequesterBinding, tok.Involvement, r.Involved.Involvement)
		}
	case RequesterBindingUnavailable:
		if r.Involved != nil {
			return fmt.Errorf("%w: an unavailable binding names no patient", ErrRequesterBinding)
		}
	default:
		return fmt.Errorf("%w: binding %q is neither %q nor %q", ErrRequesterBinding, r.Binding, RequesterBindingBound, RequesterBindingUnavailable)
	}
	return nil
}

// DecodeRequesterBinding reads a RequesterBinding body strictly: at most
// MaxRequesterBindingBytes (else ErrRequesterBindingTooLarge), UTF-8, and one
// JSON object, nothing after it, whose members, and those of its involved
// token, are only the ones the type names, each spelled exactly and given
// once, so the body cannot be read two ways. An absent involved token is
// absent: "involved": null is refused. It then validates (Validate). Anything
// else is ErrRequesterBinding.
func DecodeRequesterBinding(body []byte) (RequesterBinding, error) {
	if len(body) > MaxRequesterBindingBytes {
		return RequesterBinding{}, ErrRequesterBindingTooLarge
	}
	if !utf8.Valid(body) {
		return RequesterBinding{}, fmt.Errorf("%w: not UTF-8", ErrRequesterBinding)
	}
	members, err := exactMembers(body, "requestToken", "responseToken", "binding", "involved")
	if err != nil {
		return RequesterBinding{}, err
	}
	if inv, ok := members["involved"]; ok {
		if _, err := exactMembers(inv, "token", "involvement"); err != nil {
			return RequesterBinding{}, err
		}
	}
	var r RequesterBinding
	if err := json.Unmarshal(body, &r); err != nil {
		return RequesterBinding{}, fmt.Errorf("%w: %v", ErrRequesterBinding, err)
	}
	if err := r.Validate(); err != nil {
		return RequesterBinding{}, err
	}
	return r, nil
}

// exactMembers reads raw as one JSON object and nothing after it, whose
// members are among names, each spelled exactly and given once, and returns
// each member's raw value. Go's decoder would match a member in another case,
// and keep the last of two, so neither is left to it.
func exactMembers(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("%w: not a JSON object", ErrRequesterBinding)
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRequesterBinding, err)
		}
		key, _ := t.(string)
		known := false
		for _, n := range names {
			known = known || key == n
		}
		if !known {
			return nil, fmt.Errorf("%w: member %q is not one it names", ErrRequesterBinding, key)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%w: member %q given twice", ErrRequesterBinding, key)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRequesterBinding, err)
		}
		out[key] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, fmt.Errorf("%w: not a JSON object", ErrRequesterBinding)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: more than one JSON value", ErrRequesterBinding)
	}
	return out, nil
}

// decodeRecordToken reads one of a record's tokens: non-empty, and a
// JSON-encoded Token, an object.
func decodeRecordToken(name, s string) (Token, error) {
	if s == "" {
		return Token{}, fmt.Errorf("%w: %s is missing", ErrRequesterBinding, name)
	}
	if t := bytes.TrimLeft([]byte(s), " \t\r\n"); len(t) == 0 || t[0] != '{' {
		return Token{}, fmt.Errorf("%w: %s is not a JSON object", ErrRequesterBinding, name)
	}
	var tok Token
	if err := json.Unmarshal([]byte(s), &tok); err != nil {
		return Token{}, fmt.Errorf("%w: %s is not a token: %v", ErrRequesterBinding, name, err)
	}
	return tok, nil
}
