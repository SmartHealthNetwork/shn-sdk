package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
)

// RegistrationRequest is the JSON wire body for POST {registrar}/register. Its
// fields mirror the substrate's holder registration DTO (internal/registrar): the
// keys are std-base64-encoded and pop is the proof-of-possession signature. The
// substrate's registrar VERIFIES pop with ed25519.Verify(signPub,
// canonicalStatement, pop) — so this byte format must match exactly. Parity is
// proven behaviorally against the real registrar in
// test/sdkparity/registration_parity_test.go.
type RegistrationRequest struct {
	// ID is the holder id being registered.
	ID string `json:"id"`
	// Role is one of provider|payer|facility|phg (registrar-enforced enum).
	Role string `json:"role"`
	// EncPub is base64.StdEncoding of the 32-byte X25519 encryption public key.
	EncPub string `json:"encPub"`
	// SignPub is base64.StdEncoding of the ed25519 signing public key.
	SignPub string `json:"signPub"`
	// BaseURL is the holder's externally reachable base URL.
	BaseURL string `json:"baseURL"`
	// Pop is base64.StdEncoding of this identity's ed25519 signature over the
	// canonical registration statement, proving control of SignPub.
	Pop string `json:"pop"`
}

// registrationSigningPayload is the canonical byte layout an identity signs to
// prove control of the signPub it is registering (proof-of-possession). Fields are
// newline-joined in a fixed order — byte-identical to the substrate's
// internal/registrar.registrationSigningPayload, so the registrar's verifier
// accepts an SDK-built registration. The encPub/signPub here are the SAME
// std-base64 strings carried in the wire body.
func registrationSigningPayload(id, role, encPub, signPub, baseURL string) []byte {
	return []byte(id + "\n" + role + "\n" + encPub + "\n" + signPub + "\n" + baseURL)
}

// Registration builds a proof-of-possession registration request for this
// identity. The encPub/signPub are std-base64, and pop is this identity's ed25519
// signature over the canonical newline-joined statement
// (id\nrole\nencPub\nsignPub\nbaseURL), proving control of signPub. role must be
// one of provider|payer|facility|phg for the registrar to accept it.
//
// The SDK only BUILDS and self-signs this proof-of-possession; it never mints the
// Trust-admin credential that gates the registrar's POST /register (that authority
// is substrate/portal-side). The caller supplies the admin credential out of band.
func (id Identity) Registration(role, baseURL string) RegistrationRequest {
	encPub := base64.StdEncoding.EncodeToString(id.EncPub[:])
	signPub := base64.StdEncoding.EncodeToString(id.SignPub)
	pop := ed25519.Sign(id.SignPriv, registrationSigningPayload(id.HolderID, role, encPub, signPub, baseURL))
	return RegistrationRequest{
		ID:      id.HolderID,
		Role:    role,
		EncPub:  encPub,
		SignPub: signPub,
		BaseURL: baseURL,
		Pop:     base64.StdEncoding.EncodeToString(pop),
	}
}
