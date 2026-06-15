package shnsdk

import "encoding/json"

// AuditRecord is the SDK-facing representation of an audit record's CONTENT
// fields — the exact set that SignableContent canonicalises. It excludes the
// chain-assigned fields (Seq, PrevHash, RecordHash) and Signatures, which are
// chain-infrastructure, not content.
//
// Field order here matches the internal/audit.signableContent struct exactly.
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
}

// signableContent is the internal canonical type whose json tags and field
// order define the signed byte sequence. It is identical to
// internal/audit.signableContent — one definition, no drift.
//
// CRITICAL: This struct must remain byte-for-byte identical to
// internal/audit.signableContent (same field names, same json tags, same
// order). internal/audit.SignableContent delegates here (FR-31). A field-order
// or additive-field drift between the two would silently break live
// audit-signature verification — which is why there is exactly ONE definition.
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
}

// SignableContent returns the canonical bytes of an audit record's content
// fields (excluding Seq, PrevHash, RecordHash and Signatures). These are the
// bytes the Hub signs with its ed25519 audit-signing key and the Audit Plane
// verifies on append and /verify (FR-31).
//
// The canonical format is pinned by signableAuditContent's field order and
// json tags — a stable, deterministic JSON encoding. internal/audit delegates
// to this function so there is exactly ONE canonicalisation (E3b1).
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
	})
	return b
}
