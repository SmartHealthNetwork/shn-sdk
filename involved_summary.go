package shnsdk

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// InvolvedSummary is a gateway's own signed statement about the involved list
// it sent on one side of a leg (Metadata.Involved): how many entries the list
// has, and how many patients it left out, for which reasons. A gateway that
// knows of no patient it left out states an empty Omitted.
//
// It is the sending gateway's statement, signed with its own holder signing
// key, as its holder assertions are. The network checks that it is that
// gateway's, for that leg and side, and that its list holds what it says it
// listed; it does not check whether the gateway named every patient the
// message involves. A record of a summary that omits no one says the gateway
// stated its list was complete, never that the network verified it complete.
//
// It rides in Metadata.InvolvedSummary, which a gateway sets only for a Hub
// that lists HubAcceptsInvolvedSummary. The Hub reads it to record whether the
// list was complete (Involved), and never forwards it, so a recipient never
// sees it.
type InvolvedSummary struct {
	// Side: InvolvedSideRequest or InvolvedSideAnswer, the side of the leg
	// whose list this is.
	Side string `json:"side"`
	// Leg: InvolvedSummaryLeg of the side's own token (Metadata.AuthzToken),
	// which binds the summary to that token's frame, operation, correlation,
	// holder, payload hash, patient and expiry.
	Leg string `json:"leg"`
	// Listed: how many entries the side's involved list has.
	Listed int `json:"listed"`
	// Omitted: a count (at least 1) per reason the gateway left patients out
	// of the list for, each reason from InvolvedSummaryOmitReasons, sorted by
	// reason with each reason once. Empty when the gateway left no one out.
	Omitted []InvolvedOmission `json:"omitted"`
	// Sig: the sending gateway's Ed25519 signature (SignInvolvedSummary).
	Sig []byte `json:"sig"`
}

// The sides of a leg an InvolvedSummary is sent on.
const (
	InvolvedSideRequest = "request"
	InvolvedSideAnswer  = "answer"
)

// involvedSummaryDomain prefixes the signed bytes, so a signature over a
// summary is never one over any other message a holder key signs.
const involvedSummaryDomain = "shn-involved-summary/v1\n"

// InvolvedSummaryLeg is the Leg an InvolvedSummary for a side states: the
// lowercase hex SHA-256 of that side's own token's signature.
func InvolvedSummaryLeg(legToken Token) string {
	sum := sha256.Sum256(legToken.Signature)
	return hex.EncodeToString(sum[:])
}

// involvedSummarySigningPayload is the bytes an InvolvedSummary's signature
// covers: the domain, then the summary's JSON with Sig empty and a nil
// Omitted encoded as [].
func involvedSummarySigningPayload(s InvolvedSummary) []byte {
	s.Sig = nil
	if s.Omitted == nil {
		s.Omitted = []InvolvedOmission{}
	}
	b, _ := json.Marshal(s) // a struct of strings, ints and byte slices always marshals
	return append([]byte(involvedSummaryDomain), b...)
}

// SignInvolvedSummary returns s signed with the sending gateway's holder
// signing key.
func SignInvolvedSummary(s InvolvedSummary, priv ed25519.PrivateKey) InvolvedSummary {
	s.Sig = ed25519.Sign(priv, involvedSummarySigningPayload(s))
	return s
}

// ErrInvolvedSummary is any failure of VerifyInvolvedSummary.
var ErrInvolvedSummary = errors.New("shnsdk: involved summary does not verify")

// VerifyInvolvedSummary checks that s is signed by pub, the sender's holder
// signing key as registered now, and is for side of the leg whose own token is
// legToken. It checks the signature and the binding only. Whether its reasons
// are ones the reader knows, and whether the list holds what it says it
// listed, are the reader's to judge.
func VerifyInvolvedSummary(s InvolvedSummary, pub ed25519.PublicKey, side string, legToken Token) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no valid verifying key", ErrInvolvedSummary)
	}
	if len(s.Sig) == 0 || !ed25519.Verify(pub, involvedSummarySigningPayload(s), s.Sig) {
		return fmt.Errorf("%w: bad signature", ErrInvolvedSummary)
	}
	if side != InvolvedSideRequest && side != InvolvedSideAnswer || s.Side != side {
		return fmt.Errorf("%w: another side", ErrInvolvedSummary)
	}
	if len(legToken.Signature) == 0 || s.Leg != InvolvedSummaryLeg(legToken) {
		return fmt.Errorf("%w: another leg", ErrInvolvedSummary)
	}
	return nil
}

// EncodeInvolvedSummary encodes s for Metadata.InvolvedSummary.
func EncodeInvolvedSummary(s InvolvedSummary) (string, error) {
	if s.Omitted == nil {
		s.Omitted = []InvolvedOmission{}
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// DecodeInvolvedSummary decodes Metadata.InvolvedSummary; "" decodes as no
// summary (nil), which a reader takes as not knowing whether the list was
// complete, never as complete.
func DecodeInvolvedSummary(v string) (*InvolvedSummary, error) {
	if v == "" {
		return nil, nil
	}
	var s InvolvedSummary
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return nil, fmt.Errorf("shnsdk: decode involved summary: %w", err)
	}
	return &s, nil
}

// InvolvedSummaryOmitReasons returns the closed set of reasons a gateway
// states in an InvolvedSummary, sorted: AuditInvolvedOmitReasons without the
// Hub's own two, AuditInvolvedUnrecognized and AuditInvolvedUnverified, which
// only the Hub writes. The list is a copy.
func InvolvedSummaryOmitReasons() []string {
	return slices.DeleteFunc(AuditInvolvedOmitReasons(), func(r string) bool {
		return r == AuditInvolvedUnrecognized || r == AuditInvolvedUnverified
	})
}
