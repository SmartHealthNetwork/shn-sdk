package shnsdk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Inquiry test records: the requester's own resources, with byte-level
// details (decimals, large integers, escapes, unknown members) the inquiry must
// carry unchanged.
func inquiryPatient(memberType bool) []byte {
	typ := ""
	if memberType {
		typ = `"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},`
	}
	return []byte(" {\"resourceType\":\"Patient\", \"id\":\"pat-1\",\"identifier\":[{" + typ +
		"\"system\":\"http://payer.test/member\",\"value\":\"MBR-1\"}],\"name\":[{\"family\":\"Caf" + bs + "u00e9\"}]," +
		"\"_x\":{\"n\":9007199254740993,\"d\":1.50},\"gender\":\"female\",\"birthDate\":\"1970-01-01\"}\n")
}

var (
	inquiryCoverage = []byte(`{"resourceType":"Coverage","id":"cov-1","status":"active","subscriberId":"MBR-1",` +
		`"beneficiary":{"reference":"Patient/pat-1"},"payor":[{"reference":"Organization/payer-1"}],"costToBeneficiary":[{"valueMoney":{"value":20.00}}]}`)
	inquiryProvider = []byte(`{"resourceType":"Organization","id":"prov-1","identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1234567893"}],"name":"Provider"}`)
	inquiryInsurer  = []byte(`{"resourceType":"Organization","id":"payer-1","identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"9876543210"}],"name":"Payer"}`)
)

func inquiryItem(seq int) PASInquiryItem {
	return PASInquiryItem{
		Sequence:         seq,
		ProductOrService: PASCoding{System: "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", Code: "E0431"},
		ServiceDate:      "2026-09-01",
		TraceNumber:      PASIdentifier{System: PASItemTraceSystem, Value: "corr-1." + string(rune('0'+seq))},
	}
}

func testInquiryInputs(line string) PASInquiryInputs {
	return PASInquiryInputs{
		ID:              "inquiry-1",
		Identifier:      PASIdentifier{System: PASInquiryIdentifierSystem, Value: "inq-1"},
		ClaimIdentifier: PASIdentifier{System: PASInquiryIdentifierSystem, Value: "inq-1"},
		Timestamp:       time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		ClaimType:       PASCoding{System: "http://terminology.hl7.org/CodeSystem/claim-type", Code: "professional"},
		Priority:        PASCoding{System: "http://terminology.hl7.org/CodeSystem/processpriority", Code: "normal"},
		MemberID:        "MBR-1",
		Patient:         inquiryPatient(true),
		Coverage:        inquiryCoverage,
		Provider:        inquiryProvider,
		Insurer:         inquiryInsurer,
		Items:           []PASInquiryItem{inquiryItem(1)},
	}
}

type inquiryBundle struct {
	ResourceType string          `json:"resourceType"`
	Identifier   PASIdentifier   `json:"identifier"`
	Type         string          `json:"type"`
	Timestamp    string          `json:"timestamp"`
	Entry        []inquiryEntry  `json:"entry"`
	Meta         json.RawMessage `json:"meta"`
}

type inquiryEntry struct {
	FullURL  string          `json:"fullUrl"`
	Resource json.RawMessage `json:"resource"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
	Search   json.RawMessage `json:"search"`
}

type inquiryClaim struct {
	Extension  []map[string]json.RawMessage `json:"extension"`
	Identifier []PASIdentifier              `json:"identifier"`
	Use        string                       `json:"use"`
	Patient    struct{ Reference string }   `json:"patient"`
	Insurer    struct{ Reference string }   `json:"insurer"`
	Provider   struct{ Reference string }   `json:"provider"`
	Insurance  []struct {
		Focal    bool                       `json:"focal"`
		Coverage struct{ Reference string } `json:"coverage"`
	} `json:"insurance"`
	Item []struct {
		Sequence     int                          `json:"sequence"`
		Extension    []map[string]json.RawMessage `json:"extension"`
		ServicedDate string                       `json:"servicedDate"`
	} `json:"item"`
}

func decodeInquiry(t *testing.T, body []byte) (inquiryBundle, inquiryClaim) {
	t.Helper()
	var b inquiryBundle
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("decode inquiry: %v\n%s", err, body)
	}
	var c inquiryClaim
	if len(b.Entry) == 0 || json.Unmarshal(b.Entry[0].Resource, &c) != nil {
		t.Fatalf("inquiry has no leading Claim: %s", body)
	}
	return b, c
}

func extValues(exts []map[string]json.RawMessage, url string) []string {
	var out []string
	for _, e := range exts {
		if string(e["url"]) == `"`+url+`"` {
			for k, v := range e {
				if k != "url" {
					out = append(out, string(v))
				}
			}
		}
	}
	return out
}

// TestBuildPASInquiryBundle_ShapeAndEmbeds: at every line the inquiry is a
// collection Bundle with identifier and timestamp, the Claim first, every
// reference resolving to an entry, no entry request/response/search, and the
// requester's resources embedded as their exact bytes.
func TestBuildPASInquiryBundle_ShapeAndEmbeds(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		in := testInquiryInputs(line)
		in.Items = []PASInquiryItem{inquiryItem(2), inquiryItem(1)}
		out, err := BuildPASInquiryBundle(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		b, c := decodeInquiry(t, out.Body)
		if b.ResourceType != "Bundle" || b.Type != "collection" || b.Identifier != in.Identifier || b.Timestamp != "2026-09-16T12:00:00Z" {
			t.Errorf("%s: bundle head %+v", line, b)
		}
		urls := map[string]bool{}
		for _, e := range b.Entry {
			if e.Request != nil || e.Response != nil || e.Search != nil || e.FullURL == "" {
				t.Errorf("%s: entry %s carries request/response/search or no fullUrl", line, e.FullURL)
			}
			urls[e.FullURL] = true
		}
		if len(b.Entry) != 5 {
			t.Fatalf("%s: %d entries", line, len(b.Entry))
		}
		for _, ref := range []string{c.Patient.Reference, c.Insurer.Reference, c.Provider.Reference, c.Insurance[0].Coverage.Reference} {
			if !urls[ref] {
				t.Errorf("%s: reference %q resolves to no entry", line, ref)
			}
		}
		if c.Use != "preauthorization" || !c.Insurance[0].Focal || len(c.Identifier) != 1 || c.Identifier[0] != in.ClaimIdentifier {
			t.Errorf("%s: claim %+v", line, c)
		}
		if len(c.Item) != 2 || c.Item[0].Sequence != 1 || c.Item[1].Sequence != 2 || c.Item[0].ServicedDate != "2026-09-01" {
			t.Errorf("%s: items %+v", line, c.Item)
		}
		for _, it := range c.Item {
			traces := extValues(it.Extension, pasExtItemTraceNumber)
			if len(traces) != 1 || !strings.Contains(traces[0], `"corr-1.`) {
				t.Errorf("%s: item %d traces %v", line, it.Sequence, traces)
			}
		}
		inputs := map[string][]byte{"Patient": in.Patient, "Coverage": in.Coverage, "Provider": in.Provider, "Insurer": in.Insurer}
		if len(out.Copied) != 4 {
			t.Fatalf("%s: copied = %+v", line, out.Copied)
		}
		for _, sp := range out.Copied {
			src := inputs[sp.Input][sp.Start:sp.End]
			if !bytes.Equal(out.Body[sp.At:sp.At+len(src)], src) {
				t.Errorf("%s: %s not copied exactly", line, sp.Input)
			}
			if !bytes.Equal(bytes.TrimSpace(inputs[sp.Input]), src) {
				t.Errorf("%s: %s span is not the whole resource", line, sp.Input)
			}
		}
		if !bytes.Contains(out.Body, []byte(`9007199254740993,"d":1.50`)) || !bytes.Contains(out.Body, []byte(`"value":20.00`)) {
			t.Errorf("%s: numeric lexemes changed", line)
		}
	}
}

// TestBuildPASInquiryBundle_AuthorizationNumberPosition: the payer's item
// numbers sit on Claim.item at every line — PAS 2.2.1's Claim-level slices
// for them are not writable, since both extensions allow only item contexts —
// and each item keeps its own values; without numbers no extension is
// written at any level.
func TestBuildPASInquiryBundle_AuthorizationNumberPosition(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		in := testInquiryInputs(line)
		one, two := inquiryItem(1), inquiryItem(2)
		one.AuthorizationNumber, one.AdministrationReferenceNumber = "AUTH-1", "ADMIN-1"
		two.AuthorizationNumber = "AUTH-2"
		in.Items = []PASInquiryItem{one, two}
		out, err := BuildPASInquiryBundle(line, in)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		_, c := decodeInquiry(t, out.Body)
		if c.Extension != nil {
			t.Errorf("%s: Claim-level extensions %v", line, c.Extension)
		}
		if got := extValues(c.Item[0].Extension, pasExtAuthorizationNumber); len(got) != 1 || got[0] != `"AUTH-1"` {
			t.Errorf("%s: item 1 authorization %v", line, got)
		}
		if got := extValues(c.Item[0].Extension, pasExtAdministrationReferenceNumber); len(got) != 1 || got[0] != `"ADMIN-1"` {
			t.Errorf("%s: item 1 administration reference %v", line, got)
		}
		if got := extValues(c.Item[1].Extension, pasExtAuthorizationNumber); len(got) != 1 || got[0] != `"AUTH-2"` {
			t.Errorf("%s: item 2 authorization %v", line, got)
		}
		if got := extValues(c.Item[1].Extension, pasExtAdministrationReferenceNumber); len(got) != 0 {
			t.Errorf("%s: item 2 administration reference invented: %v", line, got)
		}
		plain, err := BuildPASInquiryBundle(line, testInquiryInputs(line))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(plain.Body, []byte("authorizationNumber")) || bytes.Contains(plain.Body, []byte("administrationReferenceNumber")) {
			t.Errorf("%s: numbers written without input", line)
		}
	}
}

// TestBuildPASInquiryBundle_RequiresMemberIdentifier: a payer matches an
// inquiry on the member identifier, so the Patient must carry it with a
// system — typed MB at PAS 2.1, the one line that slices it — and the builder
// names the gap otherwise.
func TestBuildPASInquiryBundle_RequiresMemberIdentifier(t *testing.T) {
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		in := testInquiryInputs(line)
		in.MemberID = ""
		if _, err := BuildPASInquiryBundle(line, in); err == nil || !strings.Contains(err.Error(), "member identifier is required") {
			t.Errorf("%s: no member id: %v", line, err)
		}
		in = testInquiryInputs(line)
		in.MemberID = "MBR-OTHER"
		if _, err := BuildPASInquiryBundle(line, in); err == nil || !strings.Contains(err.Error(), "member") {
			t.Errorf("%s: Patient without the member id: %v", line, err)
		}
		in = testInquiryInputs(line)
		in.Patient = []byte(`{"resourceType":"Patient","id":"pat-1","identifier":[{"value":"MBR-1"}]}`)
		if _, err := BuildPASInquiryBundle(line, in); err == nil || !strings.Contains(err.Error(), "system") {
			t.Errorf("%s: a member identifier without a system was accepted: %v", line, err)
		}
		in = testInquiryInputs(line)
		in.Patient = inquiryPatient(false)
		_, err := BuildPASInquiryBundle(line, in)
		if line != "2.1" {
			if err != nil {
				t.Errorf("%s: an untyped member identifier was refused: %v", line, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "typed MB") {
			t.Errorf("2.1: an untyped member identifier was accepted: %v", err)
		}
	}
}

// TestBuildPASInquiryBundle_Refusals: every other gap is an error.
func TestBuildPASInquiryBundle_Refusals(t *testing.T) {
	for _, row := range []struct {
		name, line string
		mutate     func(*PASInquiryInputs)
		want       string
	}{
		{"unknown line", "3.0", func(*PASInquiryInputs) {}, "unknown PAS line"},
		{"bad id", "2.0", func(in *PASInquiryInputs) { in.ID = "a b" }, "not a FHIR id"},
		{"no identifier", "2.0", func(in *PASInquiryInputs) { in.Identifier = PASIdentifier{} }, "inquiry identifier"},
		{"no claim identifier at 2.1", "2.1", func(in *PASInquiryInputs) { in.ClaimIdentifier = PASIdentifier{} }, "Claim identifier"},
		{"half claim identifier at 2.0", "2.0", func(in *PASInquiryInputs) { in.ClaimIdentifier = PASIdentifier{Value: "x"} }, "Claim identifier"},
		{"no timestamp", "2.0", func(in *PASInquiryInputs) { in.Timestamp = time.Time{} }, "timestamp"},
		{"no claim type", "2.0", func(in *PASInquiryInputs) { in.ClaimType = PASCoding{} }, "type coding"},
		{"no priority", "2.0", func(in *PASInquiryInputs) { in.Priority = PASCoding{} }, "priority code"},
		{"no items", "2.0", func(in *PASInquiryInputs) { in.Items = nil }, "at least one item"},
		{"patient not a Patient", "2.0", func(in *PASInquiryInputs) { in.Patient = inquiryProvider }, "not Patient"},
		{"patient without id", "2.0", func(in *PASInquiryInputs) { in.Patient = []byte(`{"resourceType":"Patient"}`) }, "no valid id"},
		{"patient duplicate member", "2.0", func(in *PASInquiryInputs) { in.Patient = []byte(`{"resourceType":"Patient","id":"a","id":"b"}`) }, "Patient"},
		{"coverage for another patient", "2.0", func(in *PASInquiryInputs) {
			in.Coverage = bytes.Replace(inquiryCoverage, []byte("Patient/pat-1"), []byte("Patient/pat-2"), 1)
		}, "not the Patient"},
		{"coverage group beneficiary", "2.0", func(in *PASInquiryInputs) {
			in.Coverage = bytes.Replace(inquiryCoverage, []byte("Patient/pat-1"), []byte("Group/pat-1"), 1)
		}, "not the Patient"},
		{"practitioner provider", "2.0", func(in *PASInquiryInputs) {
			in.Provider = []byte(`{"resourceType":"Practitioner","id":"p1"}`)
		}, "not Organization or PractitionerRole"},
		{"insurer not an Organization", "2.0", func(in *PASInquiryInputs) { in.Insurer = inquiryCoverage }, "not Organization"},
		{"provider is insurer", "2.0", func(in *PASInquiryInputs) { in.Provider = inquiryInsurer }, "same resource"},
		{"item sequence zero", "2.0", func(in *PASInquiryInputs) { in.Items[0].Sequence = 0 }, "below 1"},
		{"item twice", "2.0", func(in *PASInquiryInputs) { in.Items = append(in.Items, in.Items[0]) }, "listed twice"},
		{"item without product", "2.0", func(in *PASInquiryInputs) { in.Items[0].ProductOrService = PASCoding{} }, "product or service"},
		{"item without trace", "2.0", func(in *PASInquiryInputs) { in.Items[0].TraceNumber = PASIdentifier{} }, "trace number"},
		{"item bad date", "2.0", func(in *PASInquiryInputs) { in.Items[0].ServiceDate = "09/01/2026" }, "not a FHIR date"},
	} {
		in := testInquiryInputs(row.line)
		row.mutate(&in)
		if _, err := BuildPASInquiryBundle(row.line, in); err == nil || !strings.Contains(err.Error(), row.want) {
			t.Errorf("%s: err = %v, want %q", row.name, err, row.want)
		}
	}
}
