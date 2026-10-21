package shnsdk

import (
	"crypto/sha256"
	"encoding/hex"
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

// TestSignableContent_Version2MembersAppendedInOrder: the version 2 members
// follow every version 1 member, in the order version, correlationId, resends,
// finding, and a Finding always encodes every member in its own order.
func TestSignableContent_Version2MembersAppendedInOrder(t *testing.T) {
	got := string(SignableContent(AuditRecord{
		Timestamp:       "2026-07-21T12:00:00Z",
		Sender:          "payer-b",
		Recipient:       "provider-a",
		TransactionType: AuditTypeConformanceFinding,
		Outcome:         "relayed",
		RecordID:        "01JZX5A7B8C9D0E1F2G3H4J5K6",
		Version:         AuditRecordVersion2,
		CorrelationID:   "corr-1",
		Resends:         "corr-0",
		Finding:         &Finding{Kind: "fhir-ingress", Level: "observe", Whose: "peer"},
	}))
	want := `{"timestamp":"2026-07-21T12:00:00Z","sender":"payer-b","recipient":"provider-a","transactionType":"conformance-finding","authorityFrame":"","scope":"","outcome":"relayed","consentRef":"","subjectPCI":"","payloadBundleHash":"","recordId":"01JZX5A7B8C9D0E1F2G3H4J5K6",` +
		`"version":2,"correlationId":"corr-1","resends":"corr-0",` +
		`"finding":{"kind":"fhir-ingress","level":"observe","whose":"peer","line":"","profile":"","rule":"","verdict":"","issueCount":0}}`
	if got != want {
		t.Fatalf("canonical bytes:\n got %s\nwant %s", got, want)
	}
}

// TestSignableContent_Version2MembersAbsentKeepVersion1Bytes: a record that
// sets no version 2 member keeps exactly its version 1 bytes, so every record
// already on a chain keeps verifying.
func TestSignableContent_Version2MembersAbsentKeepVersion1Bytes(t *testing.T) {
	got := string(SignableContent(AuditRecord{
		Timestamp: "2026-07-21T12:00:00Z", Sender: "hub", Recipient: "payer-1",
		TransactionType: "crd-request", Outcome: "routed",
		RecordID: "01JZX5A7B8C9D0E1F2G3H4J5K6", Involvement: InvolvementRequestNamed,
	}))
	want := `{"timestamp":"2026-07-21T12:00:00Z","sender":"hub","recipient":"payer-1","transactionType":"crd-request","authorityFrame":"","scope":"","outcome":"routed","consentRef":"","subjectPCI":"","payloadBundleHash":"","recordId":"01JZX5A7B8C9D0E1F2G3H4J5K6","involvement":"request-named"}`
	if got != want {
		t.Fatalf("canonical bytes drifted:\n got %s\nwant %s", got, want)
	}
}

// TestSignableContent_SignsEveryVersion2Member: changing any version 2 member
// changes the signed bytes.
func TestSignableContent_SignsEveryVersion2Member(t *testing.T) {
	base := AuditRecord{
		Timestamp: "2026-07-21T12:00:00Z", Sender: "payer-b", Recipient: "provider-a",
		TransactionType: AuditTypeConformanceFinding, Outcome: "relayed",
		Version: AuditRecordVersion2, CorrelationID: "corr-1", Resends: "corr-0",
		Finding: &Finding{Kind: "fhir-ingress", Level: "observe", Whose: "peer", IssueCount: 2},
	}
	want := string(SignableContent(base))
	for name, mutate := range map[string]func(*AuditRecord){
		"version":       func(r *AuditRecord) { r.Version = 0 },
		"correlationId": func(r *AuditRecord) { r.CorrelationID = "corr-2" },
		"resends":       func(r *AuditRecord) { r.Resends = "" },
		"finding":       func(r *AuditRecord) { r.Finding = nil },
		"finding.kind":  func(r *AuditRecord) { r.Finding.Kind = "fhir-egress" },
		"finding.count": func(r *AuditRecord) { r.Finding.IssueCount = 3 },
	} {
		r := base
		f := *base.Finding
		r.Finding = &f
		mutate(&r)
		if string(SignableContent(r)) == want {
			t.Errorf("changing %s left the signed bytes unchanged", name)
		}
	}
}

func TestBoundedCorrelationID(t *testing.T) {
	long := strings.Repeat("a", 65)
	hashOf := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"6b1f0c2a9d3e4f5a8b7c6d5e4f3a2b1c", "6b1f0c2a9d3e4f5a8b7c6d5e4f3a2b1c"},
		{"Claim.urn-1_A", "Claim.urn-1_A"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{long, hashOf(long)},
		{"urn:shn:correlation:1", hashOf("urn:shn:correlation:1")},
		{"has space", hashOf("has space")},
		{hashOf("x"), hashOf("x")},
		{"sha256:" + strings.Repeat("A", 64), hashOf("sha256:" + strings.Repeat("A", 64))},
		{"sha256:" + strings.Repeat("a", 63), hashOf("sha256:" + strings.Repeat("a", 63))},
		// Go's $ matches only at the end of the text, so a trailing newline
		// is out of shape.
		{"abc\n", hashOf("abc\n")},
		{"Ünïcode-1", hashOf("Ünïcode-1")},
	} {
		if got := BoundedCorrelationID(c.in); got != c.want {
			t.Errorf("BoundedCorrelationID(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := BoundedCorrelationID(c.want); got != c.want {
			t.Errorf("BoundedCorrelationID is not idempotent on %q: got %q", c.want, got)
		}
	}
}

// TestSignableContent_ModuleAndGatewayMembers: module and gateway follow
// finding, every member of each is always encoded in its own order, and a nil
// list encodes as [] exactly as an empty one does.
func TestSignableContent_ModuleAndGatewayMembers(t *testing.T) {
	rec := AuditRecord{
		Timestamp: "2026-07-21T12:00:00Z", Sender: "provider-a", Recipient: "payer-b",
		TransactionType: AuditTypeModuleDetails, Outcome: "authored",
		Version: AuditRecordVersion2, CorrelationID: "corr-1",
		Module: &Module{Body: "request", Leg: "pa.pas-claim", Ownership: "authored", Builder: "sdk-pas-submit"},
	}
	got := string(SignableContent(rec))
	if rec.Module.Edits != nil || rec.Module.Loss != nil {
		t.Fatal("SignableContent changed its input's nil lists")
	}
	want := `{"timestamp":"2026-07-21T12:00:00Z","sender":"provider-a","recipient":"payer-b","transactionType":"module-details","authorityFrame":"","scope":"","outcome":"authored","consentRef":"","subjectPCI":"","payloadBundleHash":"",` +
		`"version":2,"correlationId":"corr-1",` +
		`"module":{"body":"request","leg":"pa.pas-claim","ownership":"authored","builder":"sdk-pas-submit","edits":[],"lineFrom":"","lineTo":"","loss":[]}}`
	if got != want {
		t.Fatalf("module-details bytes:\n got %s\nwant %s", got, want)
	}
	rec.Module.Edits, rec.Module.Loss = []string{}, []ModuleLoss{}
	if again := string(SignableContent(rec)); again != want {
		t.Fatalf("empty lists encode differently from nil:\n got %s\nwant %s", again, want)
	}

	start := AuditRecord{
		Timestamp: "2026-07-21T12:00:00Z", Sender: "provider-a", Recipient: "provider-a",
		TransactionType: AuditTypeGatewayStart, Outcome: "started", Version: AuditRecordVersion2,
		Gateway: &Gateway{Version: "v1.0.0"},
	}
	got = string(SignableContent(start))
	if start.Gateway.WireVersions != nil || start.Gateway.Modules != nil {
		t.Fatal("SignableContent changed its input's nil lists")
	}
	want = `{"timestamp":"2026-07-21T12:00:00Z","sender":"provider-a","recipient":"provider-a","transactionType":"gateway-start","authorityFrame":"","scope":"","outcome":"started","consentRef":"","subjectPCI":"","payloadBundleHash":"",` +
		`"version":2,"gateway":{"version":"v1.0.0","wireVersions":[],"modules":[]}}`
	if got != want {
		t.Fatalf("gateway-start bytes:\n got %s\nwant %s", got, want)
	}
}

// TestSignableContent_SignsModuleAndGatewayMembers: changing any module or
// gateway member changes the signed bytes.
func TestSignableContent_SignsModuleAndGatewayMembers(t *testing.T) {
	base := func() AuditRecord {
		return AuditRecord{
			Version: AuditRecordVersion2,
			Module: &Module{Body: "answer", Leg: "pa.pas-claim", Ownership: "edited",
				Edits: []string{"E-08"}, LineFrom: "2.1", LineTo: "2.2",
				Loss: []ModuleLoss{{Kind: "carried", Path: "Claim.item.extension"}}},
			Gateway: &Gateway{Version: "v1.0.0", WireVersions: []string{"2.0.0"}, Modules: []string{ModuleLineBridge}},
		}
	}
	want := string(SignableContent(base()))
	for name, mutate := range map[string]func(*AuditRecord){
		"module.body":          func(r *AuditRecord) { r.Module.Body = "request" },
		"module.leg":           func(r *AuditRecord) { r.Module.Leg = "pa.pas-claim-update" },
		"module.ownership":     func(r *AuditRecord) { r.Module.Ownership = "authored" },
		"module.builder":       func(r *AuditRecord) { r.Module.Builder = "gateway-refusal" },
		"module.edits":         func(r *AuditRecord) { r.Module.Edits = []string{"E-01"} },
		"module.lineFrom":      func(r *AuditRecord) { r.Module.LineFrom = "2.0" },
		"module.lineTo":        func(r *AuditRecord) { r.Module.LineTo = "2.1" },
		"module.loss":          func(r *AuditRecord) { r.Module.Loss[0].Path = "Claim.item" },
		"gateway.version":      func(r *AuditRecord) { r.Gateway.Version = "v1.0.1" },
		"gateway.wireVersions": func(r *AuditRecord) { r.Gateway.WireVersions = nil },
		"gateway.modules":      func(r *AuditRecord) { r.Gateway.Modules = []string{ModuleOnBehalf} },
		"module":               func(r *AuditRecord) { r.Module = nil },
		"gateway":              func(r *AuditRecord) { r.Gateway = nil },
	} {
		r := base()
		mutate(&r)
		if string(SignableContent(r)) == want {
			t.Errorf("changing %s left the signed bytes unchanged", name)
		}
	}
}

func TestInvolvementVocabularies(t *testing.T) {
	v1 := []string{InvolvementRequestNamed, InvolvementPayerHeld, InvolvementPayerDerived}
	v2 := []string{InvolvementRequestNamed, InvolvementResponderHeld, InvolvementResponderDerived}
	for i := range v1 {
		if !ValidInvolvementV1(v1[i]) || !ValidInvolvement(v1[i]) {
			t.Errorf("%q is not a valid version 1 value", v1[i])
		}
		if !ValidInvolvementV2(v2[i]) {
			t.Errorf("%q is not a valid version 2 value", v2[i])
		}
		if got := NeutralInvolvement(v1[i]); got != v2[i] {
			t.Errorf("NeutralInvolvement(%q) = %q, want %q", v1[i], got, v2[i])
		}
		if got := LegacyInvolvement(v2[i]); got != v1[i] {
			t.Errorf("LegacyInvolvement(%q) = %q, want %q", v2[i], got, v1[i])
		}
		if got := NeutralInvolvement(v2[i]); got != v2[i] {
			t.Errorf("NeutralInvolvement(%q) = %q, want it unchanged", v2[i], got)
		}
		if got := LegacyInvolvement(v1[i]); got != v1[i] {
			t.Errorf("LegacyInvolvement(%q) = %q, want it unchanged", v1[i], got)
		}
	}
	for _, v := range []string{InvolvementResponderHeld, InvolvementResponderDerived, "", "other"} {
		if ValidInvolvementV1(v) || ValidInvolvement(v) {
			t.Errorf("%q is a valid version 1 value", v)
		}
	}
	for _, v := range []string{InvolvementPayerHeld, InvolvementPayerDerived, "", "other"} {
		if ValidInvolvementV2(v) {
			t.Errorf("%q is a valid version 2 value", v)
		}
	}
}

func TestBuilderKeepingEditsIsACopy(t *testing.T) {
	got := BuilderKeepingEdits()
	if len(got) != 1 || got[0] != "E-09" {
		t.Fatalf("BuilderKeepingEdits() = %v, want [E-09]", got)
	}
	got[0] = "E-01"
	if BuilderKeepingEdits()[0] != "E-09" {
		t.Fatal("BuilderKeepingEdits returned shared state")
	}
}
