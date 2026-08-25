package shnsdk

// Discovery is the SHN network discovery descriptor (accountsvc GET /discovery): the
// machine-readable, FR-37 conformance surface for the sealed-envelope protocol. It is
// SUFFICIENT to drive the loop — endpoints + AuthzPublicKeyURL + persona payerId
// (legacy descriptors: DemoResponders) resolve the Payer{ID,EncPub,AuthzPub}
// RunEligibility needs (the holder matched via the registrar /holders feed; AuthzPub
// from /pubkey). No keys are embedded (no drift).
// MUST stay wire-identical to the substrate's accountsvc.Discovery (test/sdkparity).
type Discovery struct {
	Demo                bool              `json:"demo"`
	SyntheticDataOnly   bool              `json:"syntheticDataOnly"`
	WireProtocolVersion string            `json:"wireProtocolVersion"`
	IGVersions          map[string]string `json:"igVersions"`
	// ContractVersions is LEGACY and no longer populated (2026-08-13): what any
	// participant declares — including SHN-operated test participants — is participant
	// truth (registrar feed §2.3 / directory §3), not a network property. The field
	// stays declared for wire compatibility (grow-only); the network's contract
	// capability surface is BridgedContractVersions. Producer: internal/accountsvc/discovery.go.
	ContractVersions []string `json:"contractVersions,omitempty"`
	// RequestFrames are the sealed REQUEST-frame versions this substrate's
	// holders accept ("v1" — the request-frame contract). ADDITIVE optional
	// field — an older consumer ignores it; does NOT bump wireProtocolVersion.
	// Producer: internal/accountsvc/discovery.go.
	RequestFrames     []string           `json:"requestFrames,omitempty"`
	Endpoints         DiscoveryEndpoints `json:"endpoints"`
	AuthzPublicKeyURL string             `json:"authzPublicKeyURL"`
	// HubTransportKeyURL is the URL of the Hub's X-Hub-Assertion verification
	// key (hub GET /transport-key). Consumers use this to verify per-hop
	// transport assertions on inbound messages. ADDITIVE optional field — an
	// older consumer ignores it; does NOT bump wireProtocolVersion (same
	// precedent as ExpectedPriorAuth in DiscoveryPersona). No embedded key
	// (no drift). Producer: internal/accountsvc/discovery.go.
	HubTransportKeyURL string `json:"hubTransportKeyURL"`
	// FHIRValidateURL is the FHIR $validate endpoint the gateway uses for
	// per-message operation-level validation (FR-36). ADDITIVE optional field —
	// an older consumer ignores it; does NOT bump wireProtocolVersion. A
	// zero-config gateway image MUST use this URL so it validates against our
	// published profiles without any extra env. Empty ⇒ not advertised.
	// Producer: internal/accountsvc/discovery.go.
	FHIRValidateURL string               `json:"fhirValidateURL,omitempty"`
	DemoResponders  []DiscoveryResponder `json:"demoResponders"`
	Operations      []DiscoveryOp        `json:"operations"`
	DemoPersonas    []DiscoveryPersona   `json:"demoPersonas"`
	Docs            string               `json:"docs"`
	// PublishedVersions are the live SDK/gateway/kit version pins this network
	// deployment was built against, derived at image build time from
	// repo-tracked pins — never hand-set.
	// ADDITIVE optional field — an older consumer ignores it; does NOT bump
	// wireProtocolVersion. Nil ⇒ omitted (a build without the
	// PUBLISHED_VERSIONS_FILE env). Producer: internal/accountsvc/discovery.go.
	PublishedVersions *PublishedVersions `json:"publishedVersions,omitempty"`
	// IGVersionsByLine maps each contract line ("2.0", "2.1", "2.2") to the IG
	// package pin set that line validates against — same composition and short
	// keys as the flat IGVersions, which stays as the 2.0-line snapshot for
	// published-SDK consumers (grow-only wire). Additive.
	IGVersionsByLine map[string]map[string]string `json:"igVersionsByLine,omitempty"`
	// BridgedContractVersions lists the contract lines the Hub-side gateways can
	// build or bridge (NativeContractVersions) — the network's contract capability
	// surface (ContractVersions above is legacy and unpopulated). Additive.
	BridgedContractVersions []string `json:"bridgedContractVersions,omitempty"`
}

// PublishedVersions is the discovery descriptor's live-pin block.
// Gateway alone carries omitempty: SDK and Kit are structural to every build
// of the network's services, but a gateway image is a separate, optionally-
// bundled artifact — the pin derivation still fails loudly rather than emit
// a blank gateway pin when the source it reads is unparsable.
type PublishedVersions struct {
	SDK     string `json:"sdk"`
	Gateway string `json:"gateway,omitempty"`
	Kit     string `json:"kit"`
}

type DiscoveryEndpoints struct {
	Hub           string `json:"hub"`
	Authz         string `json:"authz"`
	Registrar     string `json:"registrar"`
	PatientAccess string `json:"patientAccess"`
	Accounts      string `json:"accounts"`
	Consent       string `json:"consent,omitempty"`
	Audit         string `json:"audit,omitempty"`
	PHG           string `json:"phg,omitempty"`
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
	// (prior auth with local supplemental evidence): "approved" for a pended persona
	// whose resume completes, "" = no resume stage. ADDITIVE optional field (an older
	// consumer safely ignores it) ⇒ does NOT bump wireProtocolVersion.
	// Producer: internal/accountsvc/discovery.go.
	ExpectedAfterAmend string `json:"expectedAfterAmend"` // "approved" | "" (n/a)
	// PayerID is the payer-identity claim (system,value) of the seeded member's
	// Coverage payor — fixture truth, not a participant property. A consumer
	// resolves the test counterparty by matching it against holder-attested
	// payerIds in the public /holders feed (FR-G41 semantics; unique by AI-G12).
	// ADDITIVE optional field — an older consumer ignores it; does NOT bump
	// wireProtocolVersion (ExpectedPriorAuth precedent). Absent ⇒ consumer falls
	// back to demoResponders. Producer: internal/accountsvc/discovery.go.
	PayerID *PayerIdentifier `json:"payerId,omitempty"`
	// Order is the persona's advertised prior-auth order: a payer verdict is a
	// function of the ORDER CODE, so a descriptor that advertises a per-persona
	// VERDICT while leaving the ORDER generic is incomplete by construction — it
	// advertises an effect without its cause. Present
	// (non-nil) exactly when ExpectedPriorAuth is non-empty — an eligibility-only
	// persona (no PA leg) carries no order. ADDITIVE optional field — an older
	// consumer ignores it; does NOT bump wireProtocolVersion (ExpectedPriorAuth
	// precedent). Producer: internal/accountsvc/discovery.go, DERIVED from
	// gateway/engine's DemoOrderCodes() — never a hand-copied second table.
	Order *DiscoveryOrder `json:"order,omitempty"`
}

// DiscoveryOrder is a persona's advertised prior-auth order: the code SYSTEM (CPT or
// HCPCS — feeds PriorAuthRequest.ProcedureSystem/BuildServiceRequestCoded), the
// procedure CODE + DISPLAY, and the ICD-10-CM DIAGNOSIS. A partner drives RunPriorAuth
// with exactly these four values for this persona, rather than a fixed generic order —
// the mirrored reference-payer families decide their verdict off the code alone, so the
// order and the advertised verdict must travel together.
type DiscoveryOrder struct {
	System    string `json:"system"`
	Code      string `json:"code"`
	Display   string `json:"display"`
	Diagnosis string `json:"diagnosis"`
}
