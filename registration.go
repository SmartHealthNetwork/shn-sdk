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
	// MessageFrames are the sealed message-frame versions this build negotiates
	// (SupportedMessageFrames; library-self-declared). Deliberately OUTSIDE the
	// PoP signing payload — registrationSigningPayload is a frozen 5-field layout.
	MessageFrames []string `json:"messageFrames,omitempty"`
	// ContractVersions are the exchange-contract version tokens this build speaks
	// natively ("<contract>@<line>", SupportedContractVersions; library-self-declared).
	// Deliberately OUTSIDE the PoP signing payload — registrationSigningPayload is a
	// frozen 5-field layout (same rule as MessageFrames).
	ContractVersions []string `json:"contractVersions,omitempty"`
	// RequestFrames are the sealed REQUEST-frame versions this build accepts
	// (SupportedRequestFrames; library-self-declared — the request-frame
	// contract). An originator frames a contract-mapped request ONLY toward a peer whose
	// registry entry declares this, so the wire stays additive in both directions.
	// Deliberately OUTSIDE the PoP signing payload — registrationSigningPayload is
	// a frozen 5-field layout (same rule as MessageFrames/ContractVersions).
	RequestFrames []string `json:"requestFrames,omitempty"`
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
	return id.RegistrationWithDeclared(role, baseURL, nil)
}

// RegistrationWithDeclared is Registration with an EXPLICIT declared
// contract-version set (the request-frame contract). A deployment whose
// declared set is operator-configured (the gateway's SHN_CONTRACT_VERSIONS
// accessor) must stamp THAT set into its registry entry — peers select off
// entry.ContractVersions, so an override that only reroutes local selection
// would desync own-selection from the peers' view of this holder.
//
// contractVersions == nil ⇒ SupportedContractVersions(), i.e. Registration's
// build-constant default, so library users are unaffected. The declared set is
// deliberately outside the frozen 5-field PoP payload, so the override does not
// change the proof-of-possession signature.
func (id Identity) RegistrationWithDeclared(role, baseURL string, contractVersions []string) RegistrationRequest {
	encPub := base64.StdEncoding.EncodeToString(id.EncPub[:])
	signPub := base64.StdEncoding.EncodeToString(id.SignPub)
	pop := ed25519.Sign(id.SignPriv, registrationSigningPayload(id.HolderID, role, encPub, signPub, baseURL))
	if contractVersions == nil {
		contractVersions = SupportedContractVersions()
	}
	return RegistrationRequest{
		ID:               id.HolderID,
		Role:             role,
		EncPub:           encPub,
		SignPub:          signPub,
		BaseURL:          baseURL,
		MessageFrames:    SupportedMessageFrames(),
		ContractVersions: contractVersions,
		RequestFrames:    SupportedRequestFrames(),
		Pop:              base64.StdEncoding.EncodeToString(pop),
	}
}
