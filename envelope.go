package shnsdk

import (
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/nacl/box"
)

// Metadata is the cleartext, Hub-readable routing header of an Envelope. It
// carries holder IDs only — NEVER a patient identifier (AI-5). PORTED standalone
// from internal/envelope.Metadata with the SAME json tags so the wire form is
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
