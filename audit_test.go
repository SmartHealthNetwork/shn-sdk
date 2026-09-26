package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditAppendRequestJSON(t *testing.T) {
	req := AuditAppendRequest{
		AuditRecord: AuditRecord{
			Timestamp: "2026-06-15T00:00:00Z", Sender: "phg", Recipient: "payer",
			TransactionType: "patient-access-read", AuthorityFrame: "patient-access",
			Scope: "patient-access-only", Outcome: "success", SubjectPCI: "pci:abc",
		},
		Signatures: []string{"c2ln"},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"signatures":["c2ln"]`) {
		t.Fatalf("signatures missing; got %s", got)
	}
	for _, k := range []string{`"seq"`, `"prevHash"`, `"recordHash"`} {
		if strings.Contains(got, k) {
			t.Fatalf("client body must omit %s; got %s", k, got)
		}
	}
}

// TestSignableContent_EmptyRecordIDBytesUnchanged pins the EXACT pre-recordId
// canonical bytes: any record without a recordId must keep producing them
// forever, or every existing chain signature breaks (the audit-chain canonical-bytes rule).
func TestSignableContent_EmptyRecordIDBytesUnchanged(t *testing.T) {
	got := string(SignableContent(AuditRecord{
		Timestamp:         "2026-07-21T12:00:00Z",
		Sender:            "hub",
		Recipient:         "payer-1",
		TransactionType:   "crd-request",
		AuthorityFrame:    "frame-1",
		Scope:             "scope-1",
		Outcome:           "routed",
		ConsentRef:        "c-1",
		SubjectPCI:        "pci-1",
		PayloadBundleHash: "abc123",
	}))
	want := `{"timestamp":"2026-07-21T12:00:00Z","sender":"hub","recipient":"payer-1","transactionType":"crd-request","authorityFrame":"frame-1","scope":"scope-1","outcome":"routed","consentRef":"c-1","subjectPCI":"pci-1","payloadBundleHash":"abc123"}`
	if got != want {
		t.Fatalf("canonical bytes drifted:\n got %s\nwant %s", got, want)
	}
}

// TestSignableContent_RecordIDAppendedLast: when present, recordId follows
// every original field — appended, never inserted, so the shared prefix with
// old bytes is preserved. Only involvement, added later the same way, may
// follow it.
func TestSignableContent_RecordIDAppendedLast(t *testing.T) {
	got := string(SignableContent(AuditRecord{
		Timestamp:         "2026-07-21T12:00:00Z",
		PayloadBundleHash: "abc123",
		RecordID:          "01JZX5A7B8C9D0E1F2G3H4J5K6",
	}))
	want := `{"timestamp":"2026-07-21T12:00:00Z","sender":"","recipient":"","transactionType":"","authorityFrame":"","scope":"","outcome":"","consentRef":"","subjectPCI":"","payloadBundleHash":"abc123","recordId":"01JZX5A7B8C9D0E1F2G3H4J5K6"}`
	if got != want {
		t.Fatalf("recordId not appended last:\n got %s\nwant %s", got, want)
	}
}

// TestSignableContent_InvolvementAppendedLast: involvement, when present, is
// the final key, after recordId; a record without it keeps its bytes (the
// empty-involvement case is TestSignableContent_EmptyRecordIDBytesUnchanged
// and TestSignableContent_RecordIDAppendedLast, unchanged).
func TestSignableContent_InvolvementAppendedLast(t *testing.T) {
	got := string(SignableContent(AuditRecord{
		Timestamp:         "2026-07-21T12:00:00Z",
		SubjectPCI:        "pci:b",
		PayloadBundleHash: "abc123",
		RecordID:          "01JZX5A7B8C9D0E1F2G3H4J5K6",
		Involvement:       InvolvementRequestNamed,
	}))
	want := `{"timestamp":"2026-07-21T12:00:00Z","sender":"","recipient":"","transactionType":"","authorityFrame":"","scope":"","outcome":"","consentRef":"","subjectPCI":"pci:b","payloadBundleHash":"abc123","recordId":"01JZX5A7B8C9D0E1F2G3H4J5K6","involvement":"request-named"}`
	if got != want {
		t.Fatalf("involvement not appended last:\n got %s\nwant %s", got, want)
	}
}

// TestInvolvementValues pins the closed set of involvement values and their
// wire spellings.
func TestInvolvementValues(t *testing.T) {
	for v, want := range map[string]string{
		InvolvementRequestNamed: "request-named",
		InvolvementPayerHeld:    "payer-held",
		InvolvementPayerDerived: "payer-derived",
	} {
		if v != want {
			t.Fatalf("involvement %q, want %q", v, want)
		}
		if !ValidInvolvement(v) {
			t.Fatalf("%q must be a valid involvement", v)
		}
	}
	for _, v := range []string{"", "primary", "Request-Named", "payer"} {
		if ValidInvolvement(v) {
			t.Fatalf("%q must not be a valid involvement", v)
		}
	}
}
