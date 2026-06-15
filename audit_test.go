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
