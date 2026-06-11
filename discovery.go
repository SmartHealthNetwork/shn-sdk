package shnsdk

// Discovery is the SHN sandbox discovery descriptor (accountsvc GET /discovery): the
// machine-readable, FR-37 conformance surface for the sealed-envelope protocol. It is
// SUFFICIENT to drive the loop — endpoints + AuthzPublicKeyURL + SandboxResponders
// resolve the Payer{ID,EncPub,AuthzPub} RunEligibility needs (EncPub from the
// registrar /holders feed; AuthzPub from /pubkey). No keys are embedded (no drift).
// MUST stay wire-identical to the substrate's accountsvc.Discovery (test/sdkparity).
type Discovery struct {
	Sandbox             bool                 `json:"sandbox"`
	SyntheticDataOnly   bool                 `json:"syntheticDataOnly"`
	WireProtocolVersion string               `json:"wireProtocolVersion"`
	IGVersions          map[string]string    `json:"igVersions"`
	Endpoints           DiscoveryEndpoints   `json:"endpoints"`
	AuthzPublicKeyURL   string               `json:"authzPublicKeyURL"`
	SandboxResponders   []DiscoveryResponder `json:"sandboxResponders"`
	Operations          []DiscoveryOp        `json:"operations"`
	SandboxPersonas     []DiscoveryPersona   `json:"sandboxPersonas"`
	Docs                string               `json:"docs"`
}

type DiscoveryEndpoints struct {
	Hub           string `json:"hub"`
	Authz         string `json:"authz"`
	Registrar     string `json:"registrar"`
	PatientAccess string `json:"patientAccess"`
	Accounts      string `json:"accounts"`
}

type DiscoveryResponder struct {
	Role     string `json:"role"`
	HolderID string `json:"holderId"`
}

type DiscoveryOp struct {
	Frame           string `json:"frame"`
	Operation       string `json:"operation"`
	TransactionType string `json:"transactionType"`
}

type DiscoveryPersona struct {
	MemberID            string `json:"memberId"`
	DOB                 string `json:"dob"`
	Family              string `json:"family"`
	ExpectedEligibility string `json:"expectedEligibility"` // "covered" | "not-covered"
	// ExpectedPriorAuth tells shn priorauth/doctor which PA outcome to expect for this
	// persona. ADDITIVE optional field: an older consumer safely ignores it, so it does
	// NOT bump wireProtocolVersion (only a breaking wire change does). This is the
	// additive-change precedent — see internal/accountsvc/discovery.go for the producer.
	ExpectedPriorAuth string `json:"expectedPriorAuth"` // "approved"|"pended"|"denied"|"" (n/a)
	// ExpectedAfterAmend tells doctor the outcome to expect AFTER resuming a pended PA
	// (UC-04): "approved" for a pended persona whose resume completes, "" = no resume
	// stage. ADDITIVE optional field (an older consumer safely ignores it) ⇒ does NOT
	// bump wireProtocolVersion. Producer: internal/accountsvc/discovery.go.
	ExpectedAfterAmend string `json:"expectedAfterAmend"` // "approved" | "" (n/a)
}
