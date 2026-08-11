package shnsdk

// Contract-version tokens are the generalized capability grammar of the
// multi-version exchange-contracts design (spec 2026-08-10 §3):
// "<contract>@<line>", where a contract names a message-surface family within a
// workstream (pa.pas) and a line is the wire-significant major.minor of the IG
// generation this build speaks natively. Patch versions are never wire-significant.
// Registrar admission validates token SHAPE only (the grammar is open to future
// contracts/lines); membership meaning arrives with version-aware routing (slice 3).
const (
	ContractPACRD20  = "pa.crd@2.0"  // Da Vinci CRD 2.0.x surface
	ContractPADTR20  = "pa.dtr@2.0"  // Da Vinci DTR 2.0.x surface
	ContractPAPAS20  = "pa.pas@2.0"  // Da Vinci PAS 2.0.x surface
	ContractPAPDex21 = "pa.pdex@2.1" // Da Vinci PDex 2.1.x surface
)

// SupportedContractVersions returns the contract-version tokens THIS library
// implements natively. Registration self-declares it (the library, not the app,
// owns the payload builders) — the SupportedMessageFrames precedent.
func SupportedContractVersions() []string {
	return []string{ContractPACRD20, ContractPADTR20, ContractPAPAS20, ContractPAPDex21}
}
