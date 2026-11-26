package shnsdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
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

	// Version is the record's schema version: 0 (absent) for version 1,
	// AuditRecordVersion2 or AuditRecordVersion3. An explicit 1 is not a
	// valid encoding.
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

	// The version 3 member, APPENDED after Gateway. A version 1 or version 2
	// record carries none, so its canonical bytes are unchanged. Do NOT move
	// this field.

	// Involved is whether the involved list of a list leg was complete, on
	// a version 3 record, and nil on every other record.
	Involved *Involved `json:"involved,omitempty"`
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

	// Version is the record's schema version: 0 (absent) for version 1,
	// AuditRecordVersion2 or AuditRecordVersion3. An explicit 1 is not a
	// valid encoding.
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

	// The version 3 member, APPENDED after Gateway. A version 1 or version 2
	// record carries none, so its canonical bytes are unchanged. Do NOT move
	// this field.

	// Involved is whether the involved list of a list leg was complete, on
	// a version 3 record, and nil on every other record.
	Involved *Involved `json:"involved,omitempty"`
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
		Involved:          r.Involved.canonical(),
	})
	return b
}

// AuditRecordVersion2 is AuditRecord.Version on a version 2 record. A version 1
// record leaves Version unset.
const AuditRecordVersion2 = 2

// AuditRecordVersion3 is AuditRecord.Version on a version 3 record: version 2
// plus Involved. A record is version 3 exactly when it carries Involved, and
// only the Hub writes one, on its record of a list leg.
const AuditRecordVersion3 = 3

// AuditRecordVersionSupported reports whether v is an AuditRecord.Version
// this SDK reads: 0 (version 1), AuditRecordVersion2 or AuditRecordVersion3.
//
// A verifier checks the version first, before the signature. SignableContent
// rebuilds only the members this SDK knows, so a record of a later version
// would fail its signature with no other hint; checked first, it is reported
// as an unsupported version instead.
func AuditRecordVersionSupported(v int) bool {
	switch v {
	case 0, AuditRecordVersion2, AuditRecordVersion3:
		return true
	}
	return false
}

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

// Involved is the content of a version 3 record's involved member: whether
// the list of the other patients a list leg involves was complete. Every
// member is always encoded, in this order; a nil list encodes as [].
type Involved struct {
	// Completeness: InvolvedComplete, InvolvedIncomplete or InvolvedUnknown.
	Completeness string `json:"completeness"`
	// Omitted: why the list was incomplete, a count per reason (of patients,
	// or, for AuditInvolvedUnverified, of entries), each reason from
	// AuditInvolvedOmitReasons, sorted by reason with each reason once. It is
	// empty unless Completeness is InvolvedIncomplete, and holds at least one
	// reason when it is.
	Omitted []InvolvedOmission `json:"omitted"`
}

// InvolvedOmission is one reason an involved list was incomplete, and its
// count (at least 1): for a requester's reason, how many patients it left
// out; for AuditInvolvedUnverified, how many entries of the list the Hub
// rejected (see AuditInvolvedUnverified), which is not a count of patients.
type InvolvedOmission struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// The completeness values of Involved.
const (
	// InvolvedComplete: the gateway that sent the list stated, in its signed
	// InvolvedSummary, that it left no patient out, and the list held every
	// entry the summary counted. It is that gateway's own statement; the
	// network does not judge whether the list named every patient the
	// message involves.
	InvolvedComplete = "complete"
	// InvolvedIncomplete: the list was not complete, for the reasons
	// Omitted gives.
	InvolvedIncomplete = "incomplete"
	// InvolvedUnknown: whether the list was complete is not known.
	InvolvedUnknown = "unknown"
)

// The Hub's own Involved omission reasons. A gateway never reports either:
// the Hub writes them on its record of the leg.
const (
	// AuditInvolvedUnverified: entries of the list the Hub rejected, so it
	// recorded no patient from them. The count is of entries, not patients:
	// each entry that fails the Hub's checks (an involvement the leg does not
	// allow, a token that does not verify, or no patient identifier), and
	// every entry of a list over the Hub's limit, which it rejects unread, so
	// some may repeat a patient or name the leg's own. An entry that verifies
	// but names a patient already covered is not counted, and a list the Hub
	// cannot read at all (one that does not decode as a list, or is over the
	// envelope metadata's size bound) makes the list unknown, not incomplete.
	// It also counts the entries the sending gateway's InvolvedSummary, one
	// that verifies, lists beyond those the list held: entries that did not
	// arrive.
	AuditInvolvedUnverified = "unverified"
	// AuditInvolvedUnrecognized: patients a gateway reported omitting under a
	// reason the Hub does not know (one outside the list it was built with).
	// The record keeps the count, not which reason it was, and the list reads
	// incomplete; the Hub's own counter is how the network sees that drift.
	AuditInvolvedUnrecognized = "unrecognized"
)

// AuditInvolvedOmitReasons returns the closed set of reasons an Involved
// omission may give, sorted: the reasons a gateway leaves a patient out of
// its involved list for, and the Hub's own two, AuditInvolvedUnrecognized
// and AuditInvolvedUnverified. The list is a copy.
func AuditInvolvedOmitReasons() []string {
	return []string{
		"authorization-failed",
		"authorization-refused",
		"budget",
		"involvement-not-carried",
		"overflow",
		"request-unreadable",
		"system-of-record-unavailable",
		"unknown-member",
		AuditInvolvedUnrecognized,
		AuditInvolvedUnverified,
	}
}

// ValidInvolved reports whether i is a version 3 record's involved member
// within its closed sets: its completeness one of the three values, its
// omitted list empty unless incomplete and not empty when it is, each reason
// from AuditInvolvedOmitReasons, each count at least 1, and the reasons
// sorted, each once. A nil i is not valid.
func ValidInvolved(i *Involved) bool {
	if i == nil {
		return false
	}
	switch i.Completeness {
	case InvolvedComplete, InvolvedUnknown:
		return len(i.Omitted) == 0
	case InvolvedIncomplete:
		if len(i.Omitted) == 0 {
			return false
		}
	default:
		return false
	}
	reasons := AuditInvolvedOmitReasons()
	for n, o := range i.Omitted {
		if o.Count < 1 || !slices.Contains(reasons, o.Reason) {
			return false
		}
		if n > 0 && i.Omitted[n-1].Reason >= o.Reason {
			return false
		}
	}
	return true
}

// canonical returns i with a nil omitted list empty, as Module.canonical does.
func (i *Involved) canonical() *Involved {
	if i == nil {
		return nil
	}
	c := *i
	if c.Omitted == nil {
		c.Omitted = []InvolvedOmission{}
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

// The requester-side involvement values, version 2 only: a requester's own
// binding of the patient an answer discloses, on a leg whose request names no
// patient (a payer's read of its CDex Task). The requester binds the patient
// the answer names through its own records and reports the binding, so the
// exchange is recorded under it beside the responder's. They have no
// version 1 form.
const (
	// InvolvementRequesterHeld: the requester's own binding of the patient
	// the answer discloses, from a record its system holds.
	InvolvementRequesterHeld = "requester-held"
	// InvolvementRequesterDerived: the same, for a patient its records do not
	// hold.
	InvolvementRequesterDerived = "requester-derived"
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
// record may carry: request-named, responder-held, responder-derived,
// requester-held or requester-derived.
func ValidInvolvementV2(v string) bool {
	switch v {
	case InvolvementRequestNamed, InvolvementResponderHeld, InvolvementResponderDerived,
		InvolvementRequesterHeld, InvolvementRequesterDerived:
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
