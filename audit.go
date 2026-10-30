package shnsdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

// AuditRecord is the SDK-facing representation of an audit record's CONTENT
// fields — the exact set that SignableContent canonicalises. It excludes the
// chain-assigned fields (Seq, PrevHash, RecordHash) and Signatures, which are
// chain-infrastructure, not content.
//
// Field order here matches the canonical struct (signableAuditContent) exactly.
// Do NOT reorder fields: JSON marshalling follows struct field order, and the
// byte sequence is load-bearing for ed25519 signature verification (FR-31).
type AuditRecord struct {
	Timestamp         string `json:"timestamp"`
	Sender            string `json:"sender"`
	Recipient         string `json:"recipient"`
	TransactionType   string `json:"transactionType"`
	AuthorityFrame    string `json:"authorityFrame"`
	Scope             string `json:"scope"`
	Outcome           string `json:"outcome"`
	ConsentRef        string `json:"consentRef"`
	SubjectPCI        string `json:"subjectPCI"`
	PayloadBundleHash string `json:"payloadBundleHash"`

	// RecordID is the emitter-minted unique id (ULID; minted by the emitter, never by the chain).
	// APPENDED with omitempty after the original fields: records without one produce
	// byte-identical canonical content to the pre-recordId format, so all existing
	// signatures keep verifying. Do NOT move this field.
	RecordID string `json:"recordId,omitempty"`

	// Involvement says how SubjectPCI is involved in the exchange when it is not
	// the patient the leg's token names: one of the Involvement* values. Empty on
	// the record of the token's own patient. APPENDED LAST with omitempty, after
	// RecordID, the same way: records without it keep their canonical bytes. A
	// verifier that predates it rebuilds the content without it, so it rejects a
	// record that carries it (the signature does not match) rather than accepting
	// it with the field dropped. Do NOT move this field.
	Involvement string `json:"involvement,omitempty"`

	// The version 2 members, appended after Involvement in this order. A
	// version 1 record carries none of them, so its canonical bytes are
	// unchanged. Do NOT move these fields.

	// Version is the record's schema version: 0 (absent) for version 1, or
	// AuditRecordVersion2. An explicit 1 is not a valid encoding.
	Version int `json:"version,omitempty"`
	// CorrelationID is the leg's correlation id, in BoundedCorrelationID's
	// form: the exchange identifier.
	CorrelationID string `json:"correlationId,omitempty"`
	// Resends is the bounded correlation id of the sender's earlier leg that
	// this leg replaces; absent means none.
	Resends string `json:"resends,omitempty"`
	// Finding is the finding of a conformance-finding record, and nil on every
	// other record.
	Finding *Finding `json:"finding,omitempty"`
	// Module is the content of a module-details record, and nil on every
	// other record.
	Module *Module `json:"module,omitempty"`
	// Gateway is the content of a gateway-start record, and nil on every
	// other record.
	Gateway *Gateway `json:"gateway,omitempty"`
}

// signableAuditContent is the canonical type whose json tags and field order
// define the signed byte sequence. It is the one definition: the Audit
// Plane's own canonicalisation delegates to SignableContent (FR-31), so the
// signer and every verifier canonicalise alike. The version 2 members are the
// SDK's alone until the Audit Plane's stored record gains them with the Audit
// Plane release that stores version 2.
//
// CRITICAL: AuditRecord must keep the same field names, json tags and order. A
// field-order or additive-field drift between the two would silently break
// live audit-signature verification.
type signableAuditContent struct {
	Timestamp         string `json:"timestamp"`
	Sender            string `json:"sender"`
	Recipient         string `json:"recipient"`
	TransactionType   string `json:"transactionType"`
	AuthorityFrame    string `json:"authorityFrame"`
	Scope             string `json:"scope"`
	Outcome           string `json:"outcome"`
	ConsentRef        string `json:"consentRef"`
	SubjectPCI        string `json:"subjectPCI"`
	PayloadBundleHash string `json:"payloadBundleHash"`

	// RecordID is the emitter-minted unique id (ULID; minted by the emitter, never by the chain).
	// APPENDED with omitempty after the original fields: records without one produce
	// byte-identical canonical content to the pre-recordId format, so all existing
	// signatures keep verifying. Do NOT move this field.
	RecordID string `json:"recordId,omitempty"`

	// Involvement says how SubjectPCI is involved in the exchange when it is not
	// the patient the leg's token names: one of the Involvement* values. Empty on
	// the record of the token's own patient. APPENDED LAST with omitempty, after
	// RecordID, the same way: records without it keep their canonical bytes. A
	// verifier that predates it rebuilds the content without it, so it rejects a
	// record that carries it (the signature does not match) rather than accepting
	// it with the field dropped. Do NOT move this field.
	Involvement string `json:"involvement,omitempty"`

	// The version 2 members, appended after Involvement in this order. A
	// version 1 record carries none of them, so its canonical bytes are
	// unchanged. Do NOT move these fields.

	// Version is the record's schema version: 0 (absent) for version 1, or
	// AuditRecordVersion2. An explicit 1 is not a valid encoding.
	Version int `json:"version,omitempty"`
	// CorrelationID is the leg's correlation id, in BoundedCorrelationID's
	// form: the exchange identifier.
	CorrelationID string `json:"correlationId,omitempty"`
	// Resends is the bounded correlation id of the sender's earlier leg that
	// this leg replaces; absent means none.
	Resends string `json:"resends,omitempty"`
	// Finding is the finding of a conformance-finding record, and nil on every
	// other record.
	Finding *Finding `json:"finding,omitempty"`
	// Module is the content of a module-details record, and nil on every
	// other record.
	Module *Module `json:"module,omitempty"`
	// Gateway is the content of a gateway-start record, and nil on every
	// other record.
	Gateway *Gateway `json:"gateway,omitempty"`
}

// AuditAppendRequest is the POST /append body a gateway sends to the Audit Plane:
// the signing-input AuditRecord plus the attached signature(s). Signatures[0] is
// the mandatory substrate ed25519 signature over SignableContent(AuditRecord); the
// slice is the reserved multi-party slot (DEF-5). The Audit Plane assigns
// seq/prevHash/recordHash server-side, so they are NOT part of the client body.
type AuditAppendRequest struct {
	AuditRecord
	Signatures []string `json:"signatures"`
}

// SignableContent returns the canonical bytes of an audit record's content
// fields (excluding Seq, PrevHash, RecordHash and Signatures). These are the
// bytes the Hub signs with its ed25519 audit-signing key and the Audit Plane
// verifies on append and /verify (FR-31).
//
// The canonical format is pinned by signableAuditContent's field order and
// json tags — a stable, deterministic JSON encoding. The Audit Plane delegates
// to this function so there is exactly ONE canonicalisation.
func SignableContent(r AuditRecord) []byte {
	b, _ := json.Marshal(signableAuditContent{
		Timestamp:         r.Timestamp,
		Sender:            r.Sender,
		Recipient:         r.Recipient,
		TransactionType:   r.TransactionType,
		AuthorityFrame:    r.AuthorityFrame,
		Scope:             r.Scope,
		Outcome:           r.Outcome,
		ConsentRef:        r.ConsentRef,
		SubjectPCI:        r.SubjectPCI,
		PayloadBundleHash: r.PayloadBundleHash,
		RecordID:          r.RecordID,
		Involvement:       r.Involvement,
		Version:           r.Version,
		CorrelationID:     r.CorrelationID,
		Resends:           r.Resends,
		Finding:           r.Finding,
		Module:            r.Module.canonical(),
		Gateway:           r.Gateway.canonical(),
	})
	return b
}

// AuditRecordVersion2 is AuditRecord.Version on a version 2 record. A version 1
// record leaves Version unset.
const AuditRecordVersion2 = 2

// Version 2 record types that a registered holder's key (or a probe key)
// signs, rather than the Hub's, the Framework's, the Registrar's or Accounts'.
const (
	// AuditTypeConformanceFinding: a gateway's finding about a message on one
	// of its legs, signed by its holder key; it carries a Finding and no
	// subject.
	AuditTypeConformanceFinding = "conformance-finding"
	// AuditTypeProbe: a record a check writes to prove an identity can
	// append; it carries no subject.
	AuditTypeProbe = "audit-probe"
	// AuditTypeModuleDetails: a gateway's statement that a body it
	// transmitted on a leg was edited or authored, not relayed exactly; it
	// carries a Module and no subject.
	AuditTypeModuleDetails = "module-details"
	// AuditTypeGatewayStart: a gateway's statement, at each start, of the
	// build it runs; it carries a Gateway and no leg members.
	AuditTypeGatewayStart = "gateway-start"
)

// Module is the content of a module-details record: how one body a gateway
// transmitted on a leg was not the peer's bytes exactly. Every member is
// always encoded, in this order; a nil list encodes as [].
type Module struct {
	// Body: request, forward or answer.
	Body string `json:"body"`
	// Leg: the leg's prefixed transaction type.
	Leg string `json:"leg"`
	// Ownership: edited or authored; equal to the record's outcome.
	Ownership string `json:"ownership"`
	// Builder: the builder id when authored. When edited, the builder that
	// authored the body before an IG-line bridge translated it as a whole,
	// allowed only when every edit is in BuilderKeepingEdits; otherwise "".
	Builder string `json:"builder"`
	// Edits: the body's catalog edit ids, in order; at least one when edited.
	Edits []string `json:"edits"`
	// LineFrom, LineTo: the IG lines a line bridge translated between, or
	// both "".
	LineFrom string `json:"lineFrom"`
	LineTo   string `json:"lineTo"`
	// Loss: the bridge's loss, sorted and de-duplicated; empty when there
	// are no lines.
	Loss []ModuleLoss `json:"loss"`
}

// ModuleLoss is one structural loss a line bridge reported: its kind
// (carried, synthesized, or truncated when the list was capped) and the IG
// element path, never a value.
type ModuleLoss struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// Gateway is the content of a gateway-start record: the build a gateway runs.
// Every member is always encoded, in this order; a nil list encodes as [].
type Gateway struct {
	// Version: the build's version, or v0.0.0-unstamped.
	Version string `json:"version"`
	// WireVersions: the wire protocol versions the build speaks.
	WireVersions []string `json:"wireVersions"`
	// Modules: the modules the gateway has enabled, sorted (the Module*
	// values).
	Modules []string `json:"modules"`
}

// BuilderKeepingEdits returns the catalog edits a body keeps its builder
// through: an IG-line bridge's translation of the gateway's own message as a
// whole (today E-09 alone). A module-details record of an edited body may
// name a builder only when every edit is one of these; beside any other edit
// its builder must be empty. The list is a copy, in order.
func BuilderKeepingEdits() []string { return []string{"E-09"} }

// The modules a gateway-start record names.
const (
	ModuleOnBehalf    = "on-behalf"
	ModuleLineBridge  = "line-bridge"
	ModuleConformance = "conformance"
)

// canonical returns m with every nil list empty, so a list encodes as [] in
// the signed bytes whether its writer left it nil or empty.
func (m *Module) canonical() *Module {
	if m == nil {
		return nil
	}
	c := *m
	if c.Edits == nil {
		c.Edits = []string{}
	}
	if c.Loss == nil {
		c.Loss = []ModuleLoss{}
	}
	return &c
}

// canonical returns g with every nil list empty, as Module.canonical does.
func (g *Gateway) canonical() *Gateway {
	if g == nil {
		return nil
	}
	c := *g
	if c.WireVersions == nil {
		c.WireVersions = []string{}
	}
	if c.Modules == nil {
		c.Modules = []string{}
	}
	return &c
}

// Finding is the content of a conformance-finding record: what a gateway's
// check found about a message on one leg. It carries no diagnostic text and no
// hash of plaintext. Every member is always encoded, in this order.
type Finding struct {
	// Kind: fhir-ingress, fhir-egress, fhir-bridged, cds-envelope or content.
	Kind string `json:"kind"`
	// Level: the conformance level the check ran at (none, observe,
	// structural or strict).
	Level string `json:"level"`
	// Whose: own, peer or network.
	Whose string `json:"whose"`
	// Line: the IG line checked against, or "".
	Line string `json:"line"`
	// Profile: the canonical URL of the profile checked against, or "".
	Profile string `json:"profile"`
	// Rule: a registered content or CDS rule id, or "".
	Rule string `json:"rule"`
	// Verdict: "" (invalid), unavailable or valid.
	Verdict string `json:"verdict"`
	// IssueCount: the number of issues the check reported (0 to 10000).
	IssueCount int `json:"issueCount"`
}

// boundedCorrelation is the shape a correlation id keeps as it is.
var boundedCorrelation = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// hashedCorrelation is the shape of an id BoundedCorrelationID has already
// hashed.
var hashedCorrelation = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// BoundedCorrelationID returns the form a correlation id is recorded in: ""
// for "", the id itself when it matches ^[A-Za-z0-9._-]{1,64}$ or is already
// of the form sha256:<64 lowercase hex>, and otherwise "sha256:" followed by
// the lowercase hex SHA-256 of the id. It is idempotent, so a bounded value
// read back from a record bounds to itself.
func BoundedCorrelationID(id string) string {
	if id == "" || boundedCorrelation.MatchString(id) || hashedCorrelation.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The ways a patient other than the leg token's own is involved in an exchange,
// recorded as AuditRecord.Involvement on that patient's paired record.
const (
	// InvolvementRequestNamed: another patient the request names, declared by
	// the requester.
	InvolvementRequestNamed = "request-named"
	// InvolvementPayerHeld: the payer's own binding of the member the request
	// names, from a member its system of record holds, when it differs from the
	// token's patient. The version 1 value; a version 2 record carries
	// InvolvementResponderHeld (NeutralInvolvement).
	//
	// Deprecated: the version 1 vocabulary. It stays in use through the wire
	// protocol 2.0.0 window: a version 1 record carries it (LegacyInvolvement),
	// and a gateway on wire protocol 1.1.0 asks for and sends it. A version 2
	// record, and a gateway on 2.0.0, carry InvolvementResponderHeld.
	InvolvementPayerHeld = "payer-held"
	// InvolvementPayerDerived: the payer's own binding of a member its system of
	// record does not hold (a derived identifier), when it differs from the
	// token's patient. The version 1 value; a version 2 record carries
	// InvolvementResponderDerived (NeutralInvolvement).
	//
	// Deprecated: the version 1 vocabulary. It stays in use through the wire
	// protocol 2.0.0 window: a version 1 record carries it (LegacyInvolvement),
	// and a gateway on wire protocol 1.1.0 asks for and sends it. A version 2
	// record, and a gateway on 2.0.0, carry InvolvementResponderDerived.
	InvolvementPayerDerived = "payer-derived"
)

// The neutral involvement values a version 2 record carries in place of the
// payer-named ones. InvolvementRequestNamed is already neutral and is kept.
const (
	// InvolvementResponderHeld: the responder's own binding of the member
	// the request names, from a record its system holds, when it differs
	// from the token's patient.
	InvolvementResponderHeld = "responder-held"
	// InvolvementResponderDerived: the same, for a member its records do not
	// hold.
	InvolvementResponderDerived = "responder-derived"
)

// ValidInvolvement reports whether v is one of the version 1 involvement
// values; it is ValidInvolvementV1.
//
// Deprecated: a record's version fixes its vocabulary. Use
// ValidInvolvementV1 or ValidInvolvementV2 for the record's version.
func ValidInvolvement(v string) bool { return ValidInvolvementV1(v) }

// ValidInvolvementV1 reports whether v is an involvement value a version 1
// record may carry: request-named, payer-held or payer-derived.
func ValidInvolvementV1(v string) bool {
	switch v {
	case InvolvementRequestNamed, InvolvementPayerHeld, InvolvementPayerDerived:
		return true
	}
	return false
}

// ValidInvolvementV2 reports whether v is an involvement value a version 2
// record may carry: request-named, responder-held or responder-derived.
func ValidInvolvementV2(v string) bool {
	switch v {
	case InvolvementRequestNamed, InvolvementResponderHeld, InvolvementResponderDerived:
		return true
	}
	return false
}

// NeutralInvolvement maps a version 1 involvement value to its version 2
// value. Any other value, a version 2 one included, is returned unchanged.
func NeutralInvolvement(v string) string {
	switch v {
	case InvolvementPayerHeld:
		return InvolvementResponderHeld
	case InvolvementPayerDerived:
		return InvolvementResponderDerived
	}
	return v
}

// LegacyInvolvement maps a version 2 involvement value to its version 1
// value, the inverse of NeutralInvolvement. Any other value is returned
// unchanged.
func LegacyInvolvement(v string) string {
	switch v {
	case InvolvementResponderHeld:
		return InvolvementPayerHeld
	case InvolvementResponderDerived:
		return InvolvementPayerDerived
	}
	return v
}
