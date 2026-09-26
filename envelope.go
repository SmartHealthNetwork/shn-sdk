package shnsdk

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/nacl/box"
)

// Metadata is the cleartext, Hub-readable routing header of an Envelope. Its
// routing fields carry holder IDs only — NEVER a patient identifier (AI-5); a
// patient appears only as the subject of an Authorization-Framework-signed token
// (AuthzToken, and each Involved token). PORTED standalone from
// internal/envelope.Metadata with the SAME json tags so the wire form is
// identical (test/sdkparity/envelope_parity_test.go).
type Metadata struct {
	Sender          string `json:"sender"`
	Recipient       string `json:"recipient"`
	TransactionType string `json:"transactionType"`
	AuthorityFrame  string `json:"authorityFrame"`
	ConsentRef      string `json:"consentRef,omitempty"`
	AuthzToken      string `json:"authzToken"`
	Timestamp       string `json:"timestamp"`
	CorrelationID   string `json:"correlationId"`
	// Involved names the other patients this leg involves: a JSON-encoded list
	// of InvolvedToken (EncodeInvolved), a string like AuthzToken so Metadata
	// stays comparable. Each token is minted for this leg (same frame,
	// operation, correlation, holder and payload hash as AuthzToken) with that
	// patient as subject. The Hub records the exchange under each of them as
	// well. A recipient ignores it.
	Involved string `json:"involved,omitempty"`
}

// InvolvedToken is one other patient a leg involves: a JSON-encoded Token
// with that patient as subject, and how the patient is involved (an
// Involvement* value).
type InvolvedToken struct {
	Token       string `json:"token"`
	Involvement string `json:"involvement"`
}

// Envelope is one substrate hop: plaintext metadata + opaque ciphertext. The
// ciphertext is a NaCl anonymous sealed box to the recipient's X25519 public key,
// so the Hub (lacking the recipient private key) is payload-blind (AI-2/AI-7).
type Envelope struct {
	Metadata   Metadata `json:"metadata"`
	Ciphertext []byte   `json:"ciphertext"`
}

// Seal encrypts payload to recipientEncPub using an anonymous sealed box
// (box.SealAnonymous). The sender needs only the recipient's public key and
// shares no secret with the Hub. Ported from internal/envelope.Seal.
func Seal(meta Metadata, payload []byte, recipientEncPub *[32]byte) (Envelope, error) {
	ct, err := box.SealAnonymous(nil, payload, recipientEncPub, rand.Reader)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Metadata: meta, Ciphertext: ct}, nil
}

// Open decrypts an Envelope using the recipient's own X25519 key pair
// (box.OpenAnonymous). Any party lacking the private key fails — the structural
// basis of payload-blind routing (AI-2). Ported from internal/envelope.Open.
func Open(env Envelope, encPub, encPriv *[32]byte) ([]byte, error) {
	pt, ok := box.OpenAnonymous(nil, env.Ciphertext, encPub, encPriv)
	if !ok {
		return nil, errors.New("shnsdk: envelope decryption failed")
	}
	return pt, nil
}

// EncodeInvolved encodes the other patients a leg involves for
// Metadata.Involved; none encodes as "" (the field is then omitted).
func EncodeInvolved(involved []InvolvedToken) (string, error) {
	if len(involved) == 0 {
		return "", nil
	}
	b, err := json.Marshal(involved)
	return string(b), err
}

// DecodeInvolved decodes Metadata.Involved; "" decodes as none.
func DecodeInvolved(s string) ([]InvolvedToken, error) {
	if s == "" {
		return nil, nil
	}
	var out []InvolvedToken
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("shnsdk: decode involved: %w", err)
	}
	return out, nil
}
