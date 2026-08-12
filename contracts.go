package shnsdk

import (
	"fmt"
	"strings"
)

// Contract-version tokens are the generalized capability grammar of the
// multi-version exchange-contracts design (spec 2026-08-10 §3):
// "<contract>@<line>", where a contract names a message-surface family within a
// workstream (pa.pas) and a line is the wire-significant major.minor of the IG
// generation this build speaks natively. Patch versions are never wire-significant.
// Registrar admission validates token SHAPE only (the grammar is open to future
// contracts/lines); membership meaning arrives with version-aware routing (slice 3).
const (
	ContractPACRD20  = "pa.crd@2.0"  // Da Vinci CRD 2.0.x surface
	ContractPACRD21  = "pa.crd@2.1"  // Da Vinci CRD 2.1.x surface
	ContractPACRD22  = "pa.crd@2.2"  // Da Vinci CRD 2.2.x surface
	ContractPADTR20  = "pa.dtr@2.0"  // Da Vinci DTR 2.0.x surface
	ContractPADTR21  = "pa.dtr@2.1"  // Da Vinci DTR 2.1.x surface
	ContractPADTR22  = "pa.dtr@2.2"  // Da Vinci DTR 2.2.x surface
	ContractPAPAS20  = "pa.pas@2.0"  // Da Vinci PAS 2.0.x surface
	ContractPAPAS21  = "pa.pas@2.1"  // Da Vinci PAS 2.1.x surface
	ContractPAPAS22  = "pa.pas@2.2"  // Da Vinci PAS 2.2.x surface
	ContractPAPDex21 = "pa.pdex@2.1" // Da Vinci PDex 2.1.x surface
)

// NativeContractVersions returns EVERY contract-version token this library can
// BUILD natively (capability) — the tri-line CRD/DTR/PAS set plus PDex,
// mirroring tools/contracts/manifest.json's lines. This is a superset of
// SupportedContractVersions: a deployment's declared set need not equal its
// full native capability (e.g. a holder may build 2.0/2.1/2.2 but choose to
// declare only 2.0 while a partner catches up). Stable order: contract, then
// line (crd*, dtr*, pas*, pdex*).
func NativeContractVersions() []string {
	return []string{
		ContractPACRD20, ContractPACRD21, ContractPACRD22,
		ContractPADTR20, ContractPADTR21, ContractPADTR22,
		ContractPAPAS20, ContractPAPAS21, ContractPAPAS22,
		ContractPAPDex21,
	}
}

// SupportedContractVersions returns the contract-version tokens THIS build
// self-DECLARES by default (declaration) — registration self-declares it
// (the library, not the app, owns the payload builders), the
// SupportedMessageFrames precedent. This is deliberately a SUBSET of
// NativeContractVersions: capability (what the library can build) and
// declaration (what a deployment advertises it speaks) are separate axes.
func SupportedContractVersions() []string {
	return []string{ContractPACRD20, ContractPADTR20, ContractPAPAS20, ContractPAPDex21}
}

// ParseDeclaredContractVersions resolves a deployment's DECLARED contract-version
// set from a comma-separated operator string (the SHN_CONTRACT_VERSIONS env, D1a).
// It is the SINGLE parser behind every consumer of the declared set — selection,
// the CapabilityStatement / davinci-configuration builders, and the
// registration/rotation stamp — so a deployment cannot declare one thing locally
// and another to its peers.
//
// Empty ⇒ SupportedContractVersions() (this build's default declaration).
// Otherwise every token must (1) parse as "<contract>@<line>" and (2) be a member
// of NativeContractVersions() — declaring a line this build cannot BUILD is a
// configuration error, not a routing outcome, so it fails closed at boot rather
// than producing a peer-visible declaration the build cannot honor.
func ParseDeclaredContractVersions(csv string) ([]string, error) {
	if strings.TrimSpace(csv) == "" {
		return SupportedContractVersions(), nil
	}
	native := map[string]bool{}
	for _, tok := range NativeContractVersions() {
		native[tok] = true
	}
	var out []string
	for _, raw := range strings.Split(csv, ",") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		contract, line, ok := strings.Cut(tok, "@")
		if !ok || contract == "" || line == "" {
			return nil, fmt.Errorf("contract-version token %q is malformed: want \"<contract>@<line>\" (e.g. pa.pas@2.0)", tok)
		}
		if !native[tok] {
			return nil, fmt.Errorf("contract-version token %q is not natively buildable by this build (native set: %s)", tok, strings.Join(NativeContractVersions(), ","))
		}
		out = append(out, tok)
	}
	if len(out) == 0 {
		return SupportedContractVersions(), nil
	}
	return out, nil
}
