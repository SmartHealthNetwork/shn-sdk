package shnsdk

import "encoding/json"

// Discovery is the SHN network discovery descriptor (accountsvc GET /discovery): the
// machine-readable, FR-37 conformance surface for the sealed-envelope protocol. It is
// SUFFICIENT to drive the loop — endpoints + AuthzPublicKeyURL + persona payerId
// (legacy descriptors: DemoResponders) resolve the Payer{ID,EncPub,AuthzPub}
// RunEligibility needs (the holder matched via the registrar /holders feed; AuthzPub
// from /pubkey). No keys are embedded (no drift).
// MUST stay wire-identical to the substrate's accountsvc.Discovery (test/sdkparity).
type Discovery struct {
	Demo                bool   `json:"demo"`
	SyntheticDataOnly   bool   `json:"syntheticDataOnly"`
	WireProtocolVersion string `json:"wireProtocolVersion"`
	// WireProtocolVersionsAccepted lists the wire protocol versions the
	// network accepts beside WireProtocolVersion, while it moves from one
	// version to the next. Additive; absent means only WireProtocolVersion.
	// Producer: internal/accountsvc/discovery.go.
	WireProtocolVersionsAccepted []string          `json:"wireProtocolVersionsAccepted,omitempty"`
	IGVersions                   map[string]string `json:"igVersions"`
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
	// HubAccepts lists the optional envelope fields the network's Hub reads
	// (HubAcceptsInvolved). A gateway sends such a field only to a Hub that
	// lists it: an older Hub would drop it without an error. Additive.
	HubAccepts []string `json:"hubAccepts,omitempty"`
	// ScenarioPersonas are the personas SHN's own /scenario routes originate
	// for, one per (scenario, lane, persona set), each with that persona's own
	// statement of its order and claim facts. A driver of a route sends the
	// persona's member, its order's system and code, and Order.ClaimFacts().
	// ADDITIVE optional field; no wire protocol version bump. The network's
	// descriptor does not state it yet: until it does, the field is absent and
	// ScenarioPersona finds no persona.
	ScenarioPersonas []DiscoveryScenarioPersona `json:"scenarioPersonas,omitempty"`
}

// HubAcceptsInvolved is the Discovery.HubAccepts entry for Metadata.Involved:
// the Hub records a leg under every patient it names.
const HubAcceptsInvolved = "involved"

// HubAcceptsField reports whether the Hub lists field in HubAccepts.
func (d Discovery) HubAcceptsField(field string) bool {
	for _, f := range d.HubAccepts {
		if f == field {
			return true
		}
	}
	return false
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
	Hub       string `json:"hub"`
	Authz     string `json:"authz"`
	Registrar string `json:"registrar"`
	// PatientAccess is always empty: the network names no Patient Access
	// API. The key stays on the wire, always present, until the discovery
	// protocol is versioned.
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
	// SupplementalReport is the DiagnosticReport this persona's own synthetic system
	// holds as the evidence for its pended order, as JSON, its subject the persona's
	// Patient/<memberId>: what a participant's system would send with ResumePriorAuth
	// (SupplementalReport.Report). Present exactly when ExpectedAfterAmend is non-empty.
	// ADDITIVE optional field — an older consumer ignores it; does NOT bump
	// wireProtocolVersion (ExpectedPriorAuth precedent). Absent ⇒ the network states no
	// report, and a consumer has none to send. Producer: internal/accountsvc/discovery.go.
	SupplementalReport json.RawMessage `json:"supplementalReport,omitempty"`
}

// DiscoveryOrder is a persona's advertised prior-auth order: the code SYSTEM (CPT or
// HCPCS — feeds PriorAuthRequest.ProcedureSystem/BuildServiceRequestCoded), the
// procedure CODE + DISPLAY, and the ICD-10-CM DIAGNOSIS. A partner drives RunPriorAuth
// with exactly these four values for this persona, rather than a fixed generic order —
// the mirrored reference-payer families decide their verdict off the code alone, so the
// order and the advertised verdict must travel together.
//
// From shn-sdk v0.62.0 (additive, omitempty) it also carries the persona's own
// statement of the request facts a prior-authorization claim states — claim
// type, priority, X12 1365 service category, place of service (a CMS place of
// service or NUBC type of bill) and X12 1525 request type. They are the test participant's statement, per persona; SHN's
// claim builder supplies none of them. ClaimFacts turns them into the
// request's PASClaimFacts; an older descriptor simply lacks them.
type DiscoveryOrder struct {
	System          string     `json:"system"`
	Code            string     `json:"code"`
	Display         string     `json:"display"`
	Diagnosis       string     `json:"diagnosis"`
	ClaimType       *PASCoding `json:"claimType,omitempty"`
	Priority        *PASCoding `json:"priority,omitempty"`
	ServiceCategory *PASCoding `json:"serviceCategory,omitempty"`
	PlaceOfService  *PASCoding `json:"placeOfService,omitempty"`
	RequestType     *PASCoding `json:"requestType,omitempty"`
}

// ClaimFacts is the persona's stated request facts as PASClaimFacts, each
// coding exactly as the descriptor states it; a fact the descriptor does not
// state stays nil (the builder then refuses or omits it per line).
func (o DiscoveryOrder) ClaimFacts() (PASClaimFacts, error) {
	var f PASClaimFacts
	for _, x := range []struct {
		c   *PASCoding
		out *json.RawMessage
	}{
		{o.ClaimType, &f.ClaimType}, {o.Priority, &f.Priority}, {o.ServiceCategory, &f.ServiceCategory},
		{o.PlaceOfService, &f.PlaceOfService}, {o.RequestType, &f.RequestType},
	} {
		if x.c == nil {
			continue
		}
		raw, err := json.Marshal(x.c)
		if err != nil {
			return PASClaimFacts{}, err
		}
		*x.out = raw
	}
	return f, nil
}

// DiscoveryScenarioPersona is the persona one SHN /scenario route originates
// for on one origination lane ("demo", "provider-data" or "conformance") and
// persona set ("" or "canary"), and that persona's own statement of its order
// and the request facts its prior-authorization claim states. The statement is
// the test participant's, per persona; the route carries what the driver sends
// and supplies none of it. Order is nil for a scenario that originates no
// prior authorization (an eligibility check).
type DiscoveryScenarioPersona struct {
	Scenario   string          `json:"scenario"`
	Lane       string          `json:"lane"`
	PersonaSet string          `json:"personaSet,omitempty"`
	MemberID   string          `json:"memberId"`
	Order      *DiscoveryOrder `json:"order,omitempty"`
	// Amendment is the persona's statement about the amendments its scenario
	// sends (absent when the scenario sends none).
	Amendment *DiscoveryAmendment `json:"amendment,omitempty"`
}

// DiscoveryAmendment is a persona's own statement about an amendment it sends:
// the X12 1322 certificationType it states on the amending Claim (required
// from PAS 2.1, carried at 2.0 when stated). A driver passes it as the route's
// certificationType.
type DiscoveryAmendment struct {
	CertificationType *PASCoding `json:"certificationType,omitempty"`
}

// ScenarioPersona returns the persona the descriptor states for a route's
// scenario, lane and persona set; ok is false when it states none.
func (d Discovery) ScenarioPersona(scenario, lane, personaSet string) (DiscoveryScenarioPersona, bool) {
	for _, p := range d.ScenarioPersonas {
		if p.Scenario == scenario && p.Lane == lane && p.PersonaSet == personaSet {
			return p, true
		}
	}
	return DiscoveryScenarioPersona{}, false
}
