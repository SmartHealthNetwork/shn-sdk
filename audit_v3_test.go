package shnsdk

import (
	"slices"
	"testing"
)

// v2Record is a version 2 exchange record carrying every version 2 member a
// record can hold at once, for the rows that pin version 2's bytes.
func v2Record() AuditRecord {
	return AuditRecord{
		Timestamp: "2026-11-18T12:00:00Z", Sender: "provider-a", Recipient: "payer-b",
		TransactionType: "pa.pas-claim", AuthorityFrame: "provider-tpo", Scope: "pas-bundle",
		Outcome: "routed", ConsentRef: "consent-1", SubjectPCI: "pci:synthetic-0001",
		PayloadBundleHash: "6b1f0c2a9d3e4f5a8b7c6d5e4f3a2b1c6b1f0c2a9d3e4f5a8b7c6d5e4f3a2b1c",
		RecordID:          "01JZX5A7B8C9D0E1F2G3H4J5K6", Involvement: InvolvementResponderHeld,
		Version: AuditRecordVersion2, CorrelationID: "corr-1", Resends: "corr-0",
		Module:  &Module{Body: "request", Leg: "pa.pas-claim", Ownership: "authored", Builder: "sdk-pas-submit"},
		Gateway: &Gateway{Version: "v1.0.0"},
	}
}

// TestSignableContent_Version2BytesUnchangedByVersion3: a version 2 record
// signs exactly the bytes it signed before version 3 existed. The want
// string was taken from the SDK before Involved was added.
func TestSignableContent_Version2BytesUnchangedByVersion3(t *testing.T) {
	want := `{"timestamp":"2026-11-18T12:00:00Z","sender":"provider-a","recipient":"payer-b","transactionType":"pa.pas-claim","authorityFrame":"provider-tpo","scope":"pas-bundle","outcome":"routed","consentRef":"consent-1","subjectPCI":"pci:synthetic-0001","payloadBundleHash":"6b1f0c2a9d3e4f5a8b7c6d5e4f3a2b1c6b1f0c2a9d3e4f5a8b7c6d5e4f3a2b1c","recordId":"01JZX5A7B8C9D0E1F2G3H4J5K6","involvement":"responder-held","version":2,"correlationId":"corr-1","resends":"corr-0","module":{"body":"request","leg":"pa.pas-claim","ownership":"authored","builder":"sdk-pas-submit","edits":[],"lineFrom":"","lineTo":"","loss":[]},"gateway":{"version":"v1.0.0","wireVersions":[],"modules":[]}}`
	if got := string(SignableContent(v2Record())); got != want {
		t.Fatalf("version 2 bytes drifted:\n got %s\nwant %s", got, want)
	}
}

// TestSignableContent_Version3Bytes pins a version 3 record's signed bytes:
// involved is the last member, after gateway, with completeness then
// omitted, each always present and a nil omitted list encoded as [].
func TestSignableContent_Version3Bytes(t *testing.T) {
	r := AuditRecord{
		Timestamp: "2026-11-18T12:00:00Z", Sender: "provider-a", Recipient: "payer-b",
		TransactionType: "pa.pas-claim", AuthorityFrame: "provider-tpo", Scope: "pas-bundle",
		Outcome: "routed", SubjectPCI: "pci:synthetic-0001", RecordID: "01JZX5A7B8C9D0E1F2G3H4J5K6",
		Version: AuditRecordVersion3, CorrelationID: "corr-1",
		Involved: &Involved{Completeness: InvolvedIncomplete, Omitted: []InvolvedOmission{{Reason: "budget", Count: 1}, {Reason: "overflow", Count: 3}}},
	}
	prefix := `{"timestamp":"2026-11-18T12:00:00Z","sender":"provider-a","recipient":"payer-b","transactionType":"pa.pas-claim","authorityFrame":"provider-tpo","scope":"pas-bundle","outcome":"routed","consentRef":"","subjectPCI":"pci:synthetic-0001","payloadBundleHash":"","recordId":"01JZX5A7B8C9D0E1F2G3H4J5K6","version":3,"correlationId":"corr-1",`
	want := prefix + `"involved":{"completeness":"incomplete","omitted":[{"reason":"budget","count":1},{"reason":"overflow","count":3}]}}`
	if got := string(SignableContent(r)); got != want {
		t.Fatalf("version 3 bytes:\n got %s\nwant %s", got, want)
	}

	// Involved follows gateway: a record holding both (no class does, but
	// the order is the struct's) puts involved last.
	both := v2Record()
	both.Version = AuditRecordVersion3
	both.Involved = &Involved{Completeness: InvolvedComplete}
	got := string(SignableContent(both))
	if want := `"gateway":{"version":"v1.0.0","wireVersions":[],"modules":[]},"involved":{"completeness":"complete","omitted":[]}}`; len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Fatalf("involved is not the last member, after gateway: %s", got)
	}
}

// TestSignableContent_InvolvedNilOmittedIsEmpty: a nil omitted list signs as
// [], exactly as an empty one, and SignableContent leaves its input's list
// as it was.
func TestSignableContent_InvolvedNilOmittedIsEmpty(t *testing.T) {
	rec := func(o []InvolvedOmission) AuditRecord {
		return AuditRecord{Version: AuditRecordVersion3, Involved: &Involved{Completeness: InvolvedUnknown, Omitted: o}}
	}
	nilList, emptyList := rec(nil), rec([]InvolvedOmission{})
	a, b := string(SignableContent(nilList)), string(SignableContent(emptyList))
	if a != b {
		t.Fatalf("nil and [] omitted sign differently:\n%s\n%s", a, b)
	}
	if want := `"involved":{"completeness":"unknown","omitted":[]}}`; a[len(a)-len(want):] != want {
		t.Fatalf("a nil omitted list is not encoded as []: %s", a)
	}
	if nilList.Involved.Omitted != nil {
		t.Fatal("SignableContent changed its input's nil list")
	}
}

// TestSignableContent_SignsEveryInvolvedMember: changing any involved member
// changes the signed bytes.
func TestSignableContent_SignsEveryInvolvedMember(t *testing.T) {
	base := func() AuditRecord {
		return AuditRecord{Version: AuditRecordVersion3, Involved: &Involved{Completeness: InvolvedIncomplete,
			Omitted: []InvolvedOmission{{Reason: "budget", Count: 1}, {Reason: "overflow", Count: 2}}}}
	}
	want := string(SignableContent(base()))
	for name, mutate := range map[string]func(*AuditRecord){
		"involved":              func(r *AuditRecord) { r.Involved = nil },
		"involved.completeness": func(r *AuditRecord) { r.Involved.Completeness = InvolvedUnknown },
		"involved.omitted":      func(r *AuditRecord) { r.Involved.Omitted = r.Involved.Omitted[:1] },
		"omitted.reason":        func(r *AuditRecord) { r.Involved.Omitted[0].Reason = "authorization-failed" },
		"omitted.count":         func(r *AuditRecord) { r.Involved.Omitted[1].Count = 3 },
	} {
		r := base()
		mutate(&r)
		if string(SignableContent(r)) == want {
			t.Errorf("changing %s left the signed bytes unchanged", name)
		}
	}
}

func TestAuditRecordVersionSupported(t *testing.T) {
	for v, want := range map[int]bool{0: true, 1: false, AuditRecordVersion2: true, AuditRecordVersion3: true, 4: false, -1: false, 99: false} {
		if got := AuditRecordVersionSupported(v); got != want {
			t.Errorf("AuditRecordVersionSupported(%d) = %v, want %v", v, got, want)
		}
	}
	if AuditRecordVersion3 != 3 {
		t.Fatalf("AuditRecordVersion3 = %d", AuditRecordVersion3)
	}
}

// TestAuditInvolvedOmitReasons pins the closed reason set, sorted, and that
// the list is a copy.
func TestAuditInvolvedOmitReasons(t *testing.T) {
	want := []string{"authorization-failed", "authorization-refused", "budget", "involvement-not-carried",
		"overflow", "request-unreadable", "system-of-record-unavailable", "unknown-member", "unrecognized", "unverified"}
	got := AuditInvolvedOmitReasons()
	if !slices.Equal(got, want) {
		t.Fatalf("AuditInvolvedOmitReasons() = %v, want %v", got, want)
	}
	// The Hub's own two reasons, each pinned to its wire string.
	for c, wire := range map[string]string{AuditInvolvedUnverified: "unverified", AuditInvolvedUnrecognized: "unrecognized"} {
		if c != wire || !slices.Contains(got, c) {
			t.Errorf("the Hub's reason %q, want %q in the set", c, wire)
		}
	}
	got[0] = "other"
	if AuditInvolvedOmitReasons()[0] != want[0] {
		t.Fatal("AuditInvolvedOmitReasons returned shared state")
	}
	// Each completeness constant is its own wire string, so two swapped
	// constants fail here.
	for c, wire := range map[string]string{InvolvedComplete: "complete", InvolvedIncomplete: "incomplete", InvolvedUnknown: "unknown"} {
		if c != wire {
			t.Errorf("completeness constant %q, want %q", c, wire)
		}
	}
}

// TestValidInvolved is every closed-set rule, accepted and refused.
func TestValidInvolved(t *testing.T) {
	om := func(pairs ...any) []InvolvedOmission {
		var out []InvolvedOmission
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, InvolvedOmission{Reason: pairs[i].(string), Count: pairs[i+1].(int)})
		}
		return out
	}
	valid := map[string]*Involved{
		"complete":                       {Completeness: InvolvedComplete},
		"complete, empty omitted":        {Completeness: InvolvedComplete, Omitted: []InvolvedOmission{}},
		"unknown":                        {Completeness: InvolvedUnknown},
		"incomplete, one reason":         {Completeness: InvolvedIncomplete, Omitted: om("budget", 1)},
		"incomplete, the Hub's reason":   {Completeness: InvolvedIncomplete, Omitted: om(AuditInvolvedUnverified, 2)},
		"incomplete, unrecognized":       {Completeness: InvolvedIncomplete, Omitted: om(AuditInvolvedUnrecognized, 2)},
		"incomplete, the Hub's two":      {Completeness: InvolvedIncomplete, Omitted: om("budget", 1, AuditInvolvedUnrecognized, 3, AuditInvolvedUnverified, 1)},
		"incomplete, every reason":       {Completeness: InvolvedIncomplete, Omitted: everyReason()},
		"incomplete, sorted two reasons": {Completeness: InvolvedIncomplete, Omitted: om("authorization-failed", 1, "overflow", 7)},
	}
	for name, i := range valid {
		if !ValidInvolved(i) {
			t.Errorf("%s: refused", name)
		}
	}
	refused := map[string]*Involved{
		"nil":                       nil,
		"no completeness":           {},
		"completeness outside":      {Completeness: "partial"},
		"completeness case":         {Completeness: "Complete"},
		"omitted on complete":       {Completeness: InvolvedComplete, Omitted: om("budget", 1)},
		"omitted on unknown":        {Completeness: InvolvedUnknown, Omitted: om("budget", 1)},
		"unrecognized on complete":  {Completeness: InvolvedComplete, Omitted: om(AuditInvolvedUnrecognized, 3)},
		"unrecognized on unknown":   {Completeness: InvolvedUnknown, Omitted: om(AuditInvolvedUnrecognized, 3)},
		"incomplete, no omitted":    {Completeness: InvolvedIncomplete},
		"incomplete, empty omitted": {Completeness: InvolvedIncomplete, Omitted: []InvolvedOmission{}},
		"unknown reason":            {Completeness: InvolvedIncomplete, Omitted: om("someone-else", 1)},
		"empty reason":              {Completeness: InvolvedIncomplete, Omitted: om("", 1)},
		"count 0":                   {Completeness: InvolvedIncomplete, Omitted: om("budget", 0)},
		"negative count":            {Completeness: InvolvedIncomplete, Omitted: om("budget", -1)},
		"unsorted":                  {Completeness: InvolvedIncomplete, Omitted: om("overflow", 1, "budget", 1)},
		"repeated":                  {Completeness: InvolvedIncomplete, Omitted: om("budget", 1, "budget", 2)},
	}
	for name, i := range refused {
		if ValidInvolved(i) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func everyReason() []InvolvedOmission {
	var out []InvolvedOmission
	for _, r := range AuditInvolvedOmitReasons() {
		out = append(out, InvolvedOmission{Reason: r, Count: 1})
	}
	return out
}
