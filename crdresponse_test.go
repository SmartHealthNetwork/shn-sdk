package shnsdk

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// readRealPayerCRDResponse reads the Da Vinci reference payer's recorded
// order-sign answer (a copy of the network's retained real response).
func readRealPayerCRDResponse(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/brpayer/crd-response.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func valueURLs(ci CoverageInformation) []string {
	var out []string
	for _, v := range ci.Values {
		out = append(out, v.URL)
	}
	return out
}

// TestParseCRDResponse_RealPayerFixture reads every coverage-information
// sub-extension of the reference payer's answer, the updated order exactly as
// sent, and the legacy projection.
func TestParseCRDResponse_RealPayerFixture(t *testing.T) {
	body := readRealPayerCRDResponse(t)
	obs, err := ParseCRDResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Orders) != 1 {
		t.Fatalf("orders: %+v", obs.Orders)
	}
	o := obs.Orders[0]
	if o.Source != CRDSourceSystemAction || o.Path != "systemActions[0]" || o.ActionType != "update" ||
		o.ResourceType != "DeviceRequest" || o.ID != "prior-auth-required-device-request" {
		t.Fatalf("order header: %+v", o)
	}
	var wire struct {
		SystemActions []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"systemActions"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(o.Order, wire.SystemActions[0].Resource) {
		t.Fatalf("updated order is not the payer's bytes:\n%s\nwant\n%s", o.Order, wire.SystemActions[0].Resource)
	}
	if len(o.Coverage) != 1 {
		t.Fatalf("coverage-information count %d", len(o.Coverage))
	}
	ci := o.Coverage[0]
	wantURLs := []string{"coverage", "covered", "pa-needed", "doc-needed", "questionnaire", "billingCode", "date", "coverage-assertion-id"}
	if got := valueURLs(ci); !slices.Equal(got, wantURLs) {
		t.Fatalf("sub-extension order %v, want %v", got, wantURLs)
	}
	want := CoverageInformation{
		Coverage:            "Coverage/coverage-1",
		Covered:             "covered",
		PANeeded:            "auth-needed",
		DocNeeded:           []string{"no-doc"},
		Questionnaires:      []string{"http://example.org/fhir/Questionnaire/PriorAuthRequired"},
		BillingCodes:        []CRDCoding{{System: "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", Code: "L8000"}},
		Date:                "2026-06-19",
		CoverageAssertionID: "prior-auth-2026-06-19-coverage-1",
	}
	got := ci
	got.Raw, got.Values = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed values\n got %+v\nwant %+v", got, want)
	}
	if !bytes.Contains(o.Order, ci.Raw) || !bytes.HasPrefix(ci.Raw, []byte(`{`)) {
		t.Fatalf("coverage-information span is not the payer's bytes: %s", ci.Raw)
	}
	for _, v := range ci.Values {
		if !bytes.Contains(ci.Raw, v.Raw) {
			t.Fatalf("sub-extension %s span not within the extension: %s", v.URL, v.Raw)
		}
	}
	cov, ok := obs.Primary()
	if !ok {
		t.Fatal("no primary coverage")
	}
	if !reflect.DeepEqual(cov, CardCoverage{Covered: "covered", PANeeded: "auth-needed",
		Questionnaires: []string{"http://example.org/fhir/Questionnaire/PriorAuthRequired"}}) {
		t.Fatalf("primary %+v", cov)
	}
	// The deprecated reader now sees the same coverage.
	legacy, err := ParseCards(body)
	if err != nil || !reflect.DeepEqual(legacy, cov) {
		t.Fatalf("ParseCards: %+v %v", legacy, err)
	}
}

// TestParseCRDResponse_EverySubExtensionKind covers the sub-extensions the
// reference payer's answer does not carry, repeated values in order, unknown
// sub-extensions as exact spans, several orders and several
// coverage-information instances per order.
func TestParseCRDResponse_EverySubExtensionKind(t *testing.T) {
	ci1 := `{"url":"` + CoverageInformationURL + `","extension":[` +
		`{"url":"coverage","valueReference":{"reference":"Coverage/a"}},` +
		`{"url":"covered","valueCode":"conditional"},` +
		`{"url":"pa-needed","valueCode":"satisfied"},` +
		`{"url":"doc-needed","valueCode":"clinical"},{"url":"doc-needed","valueCode":"admin"},` +
		`{"url":"doc-purpose","valueCode":"withorder"},` +
		`{"url":"info-needed","valueCode":"performer"},{"url":"info-needed","valueCode":"OTH"},` +
		`{"url":"reason","valueCodeableConcept":{"text":"Needs  a  performer"}},` +
		`{"url":"detail","extension":[{"url":"code","valueCodeableConcept":{"text":"visits"}},{"url":"value","valueString":"12"}]},` +
		`{"url":"questionnaire","valueCanonical":"http://payer.example/Q/a|1.0"},{"url":"questionnaire","valueCanonical":"http://payer.example/Q/b"},` +
		`{"url":"response","valueReference":{"reference":"QuestionnaireResponse/r1"}},` +
		`{"url":"dependency","valueReference":{"reference":"ServiceRequest/sr0"}},` +
		`{"url":"date","valueDate":"2026-09-16"},` +
		`{"url":"expiry-date","valueDate":"2026-12-31"},` +
		`{"url":"coverage-assertion-id","valueString":"ca-1"},` +
		`{"url":"satisfied-pa-id","valueString":"pa-9"},` +
		`{"url":"contact","valueContactPoint":{"system":"phone","value":"555-0100"}},` +
		`{ "url" : "future-thing" , "valueInteger" : 1.50 }` +
		`]}`
	ci2 := `{"url":"` + CoverageInformationURL + `","extension":[{"url":"coverage","valueReference":{"reference":"Coverage/b"}},{"url":"covered","valueCode":"not-covered"},{"url":"date","valueDate":"2026-09-16"},{"url":"coverage-assertion-id","valueString":"ca-2"}]}`
	body := `{"cards":[],"systemActions":[` +
		`{"type":"update","description":"d","resource":{"resourceType":"ServiceRequest","id":"sr1","extension":[` + ci1 + `,` + ci2 + `]}},` +
		`{"type":"update","description":"no coverage information","resource":{"resourceType":"Task","id":"t1"}},` +
		`{"type":"create","description":"d","resource":{"resourceType":"MedicationRequest","id":"mr1","extension":[{"url":"http://other.example/ext","valueString":"x"},` + ci2 + `]}}` +
		`]}`
	obs, err := ParseCRDResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Orders) != 2 || obs.Orders[0].ID != "sr1" || obs.Orders[1].ID != "mr1" ||
		obs.Orders[1].ActionType != "create" || obs.Orders[1].Path != "systemActions[2]" {
		t.Fatalf("orders: %+v", obs.Orders)
	}
	if len(obs.Orders[0].Coverage) != 2 || obs.Orders[0].Coverage[1].Coverage != "Coverage/b" {
		t.Fatalf("coverage-information per order: %+v", obs.Orders[0].Coverage)
	}
	got := obs.Orders[0].Coverage[0]
	if string(got.Raw) != ci1 {
		t.Fatalf("raw span:\n%s\nwant\n%s", got.Raw, ci1)
	}
	if !slices.Equal(got.DocNeeded, []string{"clinical", "admin"}) ||
		!slices.Equal(got.DocPurpose, []string{"withorder"}) ||
		!slices.Equal(got.InfoNeeded, []string{"performer", "OTH"}) ||
		!slices.Equal(got.Questionnaires, []string{"http://payer.example/Q/a|1.0", "http://payer.example/Q/b"}) ||
		!slices.Equal(got.Responses, []string{"QuestionnaireResponse/r1"}) ||
		!slices.Equal(got.Dependencies, []string{"ServiceRequest/sr0"}) ||
		got.Covered != "conditional" || got.PANeeded != "satisfied" || got.SatisfiedPAID != "pa-9" ||
		got.Date != "2026-09-16" || got.ExpiryDate != "2026-12-31" || got.CoverageAssertionID != "ca-1" {
		t.Fatalf("typed values: %+v", got)
	}
	if len(got.Reasons) != 1 || string(got.Reasons[0]) != `{"text":"Needs  a  performer"}` {
		t.Fatalf("reasons: %s", got.Reasons)
	}
	if len(got.Details) != 1 || !strings.HasPrefix(string(got.Details[0]), `{"url":"detail","extension":[`) {
		t.Fatalf("details: %s", got.Details)
	}
	if len(got.Contacts) != 1 || string(got.Contacts[0]) != `{"system":"phone","value":"555-0100"}` {
		t.Fatalf("contacts: %s", got.Contacts)
	}
	if len(got.Unknown) != 1 || got.Unknown[0].URL != "future-thing" ||
		string(got.Unknown[0].Raw) != `{ "url" : "future-thing" , "valueInteger" : 1.50 }` {
		t.Fatalf("unknown: %+v", got.Unknown)
	}
	if len(got.Values) != 20 || got.Values[19].URL != "future-thing" {
		t.Fatalf("values: %d", len(got.Values))
	}
	if len(got.Nonconformant) != 0 {
		t.Fatalf("nonconformant: %v", got.Nonconformant)
	}
	cov, ok := obs.Primary()
	if !ok || cov.Covered != "conditional" || cov.PANeeded != "satisfied" || cov.SatisfiedPaID != "pa-9" || len(cov.Questionnaires) != 2 {
		t.Fatalf("primary %+v", cov)
	}
}

// TestParseCRDResponse_LegacyCardShape reads coverage information from the
// two card shapes that predate system actions: a card suggestion carrying the
// updated order, and this network's earlier card extension object.
func TestParseCRDResponse_LegacyCardShape(t *testing.T) {
	t.Run("card suggestion", func(t *testing.T) {
		body := `{"cards":[{"summary":"s","indicator":"info","source":{"label":"P"}},` +
			`{"summary":"Coverage","indicator":"info","source":{"label":"P"},"selectionBehavior":"any","suggestions":[` +
			`{"label":"keep"},{"label":"Save coverage information","actions":[{"type":"update","description":"d","resource":` +
			`{"resourceType":"ServiceRequest","id":"sr1","extension":[{"url":"` + CoverageInformationURL + `","extension":[` +
			`{"url":"coverage","valueReference":{"reference":"Coverage/c1"}},{"url":"covered","valueCode":"covered"},` +
			`{"url":"pa-needed","valueCode":"no-auth"},{"url":"date","valueDate":"2026-09-16"},{"url":"coverage-assertion-id","valueString":"x"}]}]}}]}]}]}`
		obs, err := ParseCRDResponse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(obs.Orders) != 1 || obs.Orders[0].Source != CRDSourceCardSuggestion ||
			obs.Orders[0].Path != "cards[1].suggestions[1].actions[0]" || obs.Orders[0].ID != "sr1" {
			t.Fatalf("orders %+v", obs.Orders)
		}
		cov, ok := obs.Primary()
		if !ok || !reflect.DeepEqual(cov, CardCoverage{Covered: "covered", PANeeded: "no-auth"}) {
			t.Fatalf("primary %+v", cov)
		}
	})
	t.Run("card extension object", func(t *testing.T) {
		body, err := BuildCards(CardCoverage{Covered: CoveredCovered, PANeeded: PANeededAuthNeeded,
			Questionnaires: []string{"http://payer.example/Q/a"}})
		if err != nil {
			t.Fatal(err)
		}
		obs, err := ParseCRDResponse(body)
		if err != nil {
			t.Fatal(err)
		}
		if len(obs.Orders) != 1 || obs.Orders[0].Source != CRDSourceLegacyCard || obs.Orders[0].Path != "cards[0].extension" ||
			obs.Orders[0].Order != nil {
			t.Fatalf("orders %+v", obs.Orders)
		}
		ci := obs.Orders[0].Coverage[0]
		if ci.Covered != "covered" || ci.PANeeded != "auth-needed" || !slices.Equal(ci.Questionnaires, []string{"http://payer.example/Q/a"}) ||
			string(ci.Raw) != `{"covered":"covered","paNeeded":"auth-needed","questionnaires":["http://payer.example/Q/a"]}` {
			t.Fatalf("legacy coverage %+v", ci)
		}
		cov, ok := obs.Primary()
		if !ok || cov.Covered != "covered" || cov.PANeeded != "auth-needed" {
			t.Fatalf("primary %+v", cov)
		}
	})
	t.Run("no coverage information", func(t *testing.T) {
		obs, err := ParseCRDResponse([]byte(`{"cards":[]}`))
		if err != nil || len(obs.Orders) != 0 {
			t.Fatalf("%+v %v", obs, err)
		}
		if _, ok := obs.Primary(); ok {
			t.Fatal("primary from nothing")
		}
	})
}

// TestParseCRDResponse_Refusals: a body that is not one strict JSON object is
// refused; a value of the wrong type is reported, never guessed.
func TestParseCRDResponse_Refusals(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        `{"cards":`,
		"not an object":   `[]`,
		"duplicate names": `{"cards":[],"cards":[]}`,
		"trailing value":  `{"cards":[]} {}`,
	} {
		if _, err := ParseCRDResponse([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	body := `{"cards":[],"systemActions":[{"type":"update","description":"d","resource":{"resourceType":"ServiceRequest","id":"sr1","extension":[` +
		`{"url":"` + CoverageInformationURL + `","extension":[{"url":"covered","valueString":"covered"},{"url":"date"},{"valueCode":"x"}]}]}}]}`
	obs, err := ParseCRDResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ci := obs.Orders[0].Coverage[0]
	if ci.Covered != "" || len(ci.Nonconformant) != 3 {
		t.Fatalf("nonconformant values: %+v", ci)
	}
	if _, ok := obs.Primary(); ok {
		t.Fatal("a coverage-information without a covered value is no primary coverage")
	}
}

// crdGoldenOrderSR is a draft order the builder tests carry.
const crdGoldenOrderSR = `{"resourceType":"ServiceRequest","id":"sr1","status":"draft","intent":"order",` +
	`"code":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"72148"}]},"subject":{"reference":"Patient/MBR-COVERED"}}`

func paRequiredInputs(line string) CRDResponseInputs {
	return CRDResponseInputs{Orders: []CRDOrderCoverage{{
		Order:       []byte(crdGoldenOrderSR),
		Description: "Add coverage information to ServiceRequest",
		Coverage: []CoverageInformationInput{{
			Coverage:            "Coverage/c1",
			Covered:             "covered",
			PANeeded:            "auth-needed",
			DocNeeded:           []string{"clinical"},
			Questionnaires:      []string{"http://payer.example/Questionnaire/lumbar|1.0.0"},
			Date:                "2026-09-16",
			CoverageAssertionID: "assertion-" + line,
		}},
	}}}
}

// TestBuildCRDResponse_CertifiesAtEachLine: at every CRD line the built
// answer is a certified CDS Hooks response whose coverage information travels
// in an update system action on the order, reads back unchanged, and leaves
// the order's own bytes untouched apart from the added extension.
func TestBuildCRDResponse_CertifiesAtEachLine(t *testing.T) {
	topic := CRDCoding{System: "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp", Code: "coverage-info"}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		t.Run(line, func(t *testing.T) {
			in := paRequiredInputs(line)
			out, err := BuildCRDResponse(line, in)
			if err != nil {
				t.Fatal(err)
			}
			if v := CheckCDSHooksResponse(out, line); len(v) != 0 {
				t.Fatalf("violations %+v\n%s", v, out)
			}
			if !bytes.HasPrefix(out, []byte(`{"cards":[],"systemActions":[{"type":"update","description":"Add coverage information to ServiceRequest","resource":`)) {
				t.Fatalf("shape: %s", out)
			}
			obs, err := ParseCRDResponse(out)
			if err != nil {
				t.Fatal(err)
			}
			if len(obs.Orders) != 1 || obs.Orders[0].Source != CRDSourceSystemAction || obs.Orders[0].ID != "sr1" {
				t.Fatalf("orders %+v", obs.Orders)
			}
			order := obs.Orders[0].Order
			wantPrefix := strings.TrimSuffix(crdGoldenOrderSR, "}") + `,"extension":[`
			if !bytes.HasPrefix(order, []byte(wantPrefix)) || !bytes.HasSuffix(order, []byte("]}")) {
				t.Fatalf("order bytes changed beyond the added extension:\n%s", order)
			}
			ci := obs.Orders[0].Coverage[0]
			wantIn := in.Orders[0].Coverage[0]
			if ci.Coverage != wantIn.Coverage || ci.Covered != wantIn.Covered || ci.PANeeded != wantIn.PANeeded ||
				!slices.Equal(ci.DocNeeded, wantIn.DocNeeded) || !slices.Equal(ci.Questionnaires, wantIn.Questionnaires) ||
				ci.Date != wantIn.Date || ci.CoverageAssertionID != "assertion-"+line || len(ci.Unknown) != 0 {
				t.Fatalf("read back %+v", ci)
			}

			// A human card carries the participant's label and topic.
			in.Cards = []CRDCard{{UUID: "6f1c1b7e-2d1a-4c5e-9d1e-0a1b2c3d4e5f", Summary: "Prior authorization required", Indicator: "warning",
				Source: CRDCardSource{Label: "Example Health Plan", Topic: topic},
				Links:  []CRDLink{{Label: "Policy", URL: "https://payer.example/policy", Type: "absolute"}}}}
			withCard, err := BuildCRDResponse(line, in)
			if err != nil {
				t.Fatal(err)
			}
			if v := CheckCDSHooksResponse(withCard, line); len(v) != 0 {
				t.Fatalf("violations %+v\n%s", v, withCard)
			}
			if !bytes.Contains(withCard, []byte(`"source":{"label":"Example Health Plan","topic":{"system":"http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp","code":"coverage-info"}}`)) {
				t.Fatalf("card source: %s", withCard)
			}
		})
	}
}

// TestBuildCRDResponse_OrderWithExtensionsKept: an order that already has
// extensions keeps them, byte for byte, and gains the coverage information at
// the end of its extension array.
func TestBuildCRDResponse_OrderWithExtensionsKept(t *testing.T) {
	order := "{\n  \"resourceType\": \"DeviceRequest\",\n  \"id\": \"dr1\",\n  \"extension\": [\n    {\"url\": \"http://other.example/x\", \"valueDecimal\": 1.50}\n  ],\n  \"status\": \"draft\"\n}"
	in := paRequiredInputs("2.0")
	in.Orders[0].Order = []byte(order)
	out, err := BuildCRDResponse("2.0", in)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ParseCRDResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(obs.Orders[0].Order)
	head := "{\n  \"resourceType\": \"DeviceRequest\",\n  \"id\": \"dr1\",\n  \"extension\": [\n    {\"url\": \"http://other.example/x\", \"valueDecimal\": 1.50},\n    {\"url\":\"" + CoverageInformationURL + "\""
	tail := "\n  ],\n  \"status\": \"draft\"\n}"
	if !strings.HasPrefix(got, head) || !strings.HasSuffix(got, tail) {
		t.Fatalf("order:\n%s", got)
	}
}

// TestBuildCRDResponse_Refusals: the builder emits only what the participant
// supplied and refuses a coverage-information the line does not allow.
func TestBuildCRDResponse_Refusals(t *testing.T) {
	type mut func(*CRDResponseInputs)
	ci := func(f func(*CoverageInformationInput)) mut {
		return func(in *CRDResponseInputs) { f(&in.Orders[0].Coverage[0]) }
	}
	rows := []struct {
		name string
		line string
		m    mut
	}{
		{"unknown line", "3.0", func(*CRDResponseInputs) {}},
		{"no orders", "2.0", func(in *CRDResponseInputs) { in.Orders = nil }},
		{"order not json", "2.0", func(in *CRDResponseInputs) { in.Orders[0].Order = []byte(`{"resourceType":`) }},
		{"order not a resource", "2.0", func(in *CRDResponseInputs) { in.Orders[0].Order = []byte(`{"id":"x"}`) }},
		{"order without id", "2.0", func(in *CRDResponseInputs) {
			in.Orders[0].Order = []byte(`{"resourceType":"ServiceRequest","status":"draft"}`)
		}},
		{"order extension not an array", "2.0", func(in *CRDResponseInputs) {
			in.Orders[0].Order = []byte(`{"resourceType":"ServiceRequest","id":"a","extension":{}}`)
		}},
		{"no description", "2.0", func(in *CRDResponseInputs) { in.Orders[0].Description = "" }},
		{"no coverage information", "2.0", func(in *CRDResponseInputs) { in.Orders[0].Coverage = nil }},
		{"no coverage reference", "2.0", ci(func(c *CoverageInformationInput) { c.Coverage = "" })},
		{"no covered", "2.0", ci(func(c *CoverageInformationInput) { c.Covered = "" })},
		{"covered outside the line", "2.0", ci(func(c *CoverageInformationInput) { c.Covered = "indeterminate" })},
		{"pa-needed outside the value set", "2.2", ci(func(c *CoverageInformationInput) { c.PANeeded = "maybe" })},
		{"doc-needed outside the line", "2.0", ci(func(c *CoverageInformationInput) { c.DocNeeded = []string{"patient"} })},
		{"doc-needed repeated at 2.0", "2.0", ci(func(c *CoverageInformationInput) { c.DocNeeded = []string{"clinical", "admin"} })},
		{"doc-purpose outside the value set", "2.1", ci(func(c *CoverageInformationInput) { c.DocPurpose = []string{"PA"} })},
		{"info-needed outside the line", "2.1", ci(func(c *CoverageInformationInput) {
			c.Covered = "conditional"
			c.InfoNeeded = []string{"detail-code"}
		})},
		{"no date", "2.0", ci(func(c *CoverageInformationInput) { c.Date = "" })},
		{"date not a date", "2.0", ci(func(c *CoverageInformationInput) { c.Date = "16/09/2026" })},
		{"expiry-date at 2.0", "2.0", ci(func(c *CoverageInformationInput) { c.ExpiryDate = "2026-12-31" })},
		{"no assertion id", "2.2", ci(func(c *CoverageInformationInput) { c.CoverageAssertionID = "" })},
		{"not-covered with pa-needed", "2.0", ci(func(c *CoverageInformationInput) {
			c.Covered = "not-covered"
			c.Questionnaires, c.DocNeeded = nil, nil
		})},
		{"covered without pa-needed at 2.0", "2.0", ci(func(c *CoverageInformationInput) { c.PANeeded = "" })},
		{"satisfied without id", "2.0", ci(func(c *CoverageInformationInput) { c.PANeeded = "satisfied" })},
		{"id without satisfied", "2.0", ci(func(c *CoverageInformationInput) { c.SatisfiedPAID = "pa-1" })},
		{"questionnaire without doc-needed", "2.1", ci(func(c *CoverageInformationInput) { c.DocNeeded = nil })},
		{"conditional without info-needed", "2.0", ci(func(c *CoverageInformationInput) { c.PANeeded = "conditional" })},
		{"OTH info-needed without reason", "2.1", ci(func(c *CoverageInformationInput) {
			c.PANeeded = "conditional"
			c.InfoNeeded = []string{"OTH"}
		})},
		{"doc-purpose without reason at 2.2", "2.2", ci(func(c *CoverageInformationInput) { c.DocPurpose = []string{"withpa"} })},
		{"indeterminate without reason at 2.2", "2.2", ci(func(c *CoverageInformationInput) { c.PANeeded = "indeterminate" })},
		{"withpa when satisfied at 2.1", "2.1", ci(func(c *CoverageInformationInput) {
			c.PANeeded, c.SatisfiedPAID = "satisfied", "pa-1"
			c.DocPurpose = []string{"withpa"}
		})},
		{"info-needed without conditional at 2.0", "2.0", ci(func(c *CoverageInformationInput) { c.InfoNeeded = []string{"performer"} })},
		{"info-needed without conditional at 2.1", "2.1", ci(func(c *CoverageInformationInput) { c.InfoNeeded = []string{"performer"} })},
		{"info-needed without conditional at 2.2", "2.2", ci(func(c *CoverageInformationInput) { c.InfoNeeded = []string{"performer"} })},
		{"conditional without info-needed at 2.1", "2.1", ci(func(c *CoverageInformationInput) { c.Covered = "conditional" })},
		{"conditional doc-needed without info-needed at 2.2", "2.2", ci(func(c *CoverageInformationInput) { c.DocNeeded = []string{"conditional"} })},
		{"withpa when satisfied at 2.0", "2.0", ci(func(c *CoverageInformationInput) {
			c.PANeeded, c.SatisfiedPAID = "satisfied", "pa-1"
			c.DocPurpose = []string{"withpa"}
		})},
		{"withpa when no-auth at 2.0", "2.0", ci(func(c *CoverageInformationInput) {
			c.PANeeded = "no-auth"
			c.DocPurpose = []string{"withpa"}
		})},
		{"withpa when no-auth at 2.2", "2.2", ci(func(c *CoverageInformationInput) {
			c.PANeeded = "no-auth"
			c.DocPurpose = []string{"withpa"}
			c.Reasons = []json.RawMessage{json.RawMessage(`{"text":"r"}`)}
		})},
		{"withpa when not covered at 2.1", "2.1", ci(func(c *CoverageInformationInput) {
			c.Covered, c.PANeeded = "not-covered", ""
			c.DocPurpose = []string{"withpa"}
		})},
		{"ContactDetail at 2.0", "2.0", ci(func(c *CoverageInformationInput) {
			c.Contacts = []json.RawMessage{json.RawMessage(`{"name":"UM desk","telecom":[{"system":"phone","value":"555"}]}`)}
		})},
		{"ContactDetail at 2.1", "2.1", ci(func(c *CoverageInformationInput) {
			c.Contacts = []json.RawMessage{json.RawMessage(`{"telecom":[{"system":"phone","value":"555"}]}`)}
		})},
		{"ContactPoint at 2.2", "2.2", ci(func(c *CoverageInformationInput) {
			c.Contacts = []json.RawMessage{json.RawMessage(`{"system":"phone","value":"555"}`)}
		})},
		{"card without uuid at 2.2", "2.2", func(in *CRDResponseInputs) {
			in.Cards = []CRDCard{{Summary: "s", Indicator: "info", Source: CRDCardSource{Label: "P", Topic: CRDCoding{System: "s", Code: "c"}}}}
		}},
		{"reason not an object", "2.0", ci(func(c *CoverageInformationInput) { c.Reasons = []json.RawMessage{json.RawMessage(`"text"`)} })},
		{"detail not a detail extension", "2.0", ci(func(c *CoverageInformationInput) {
			c.Details = []json.RawMessage{json.RawMessage(`{"url":"reason"}`)}
		})},
		{"contact not an object", "2.0", ci(func(c *CoverageInformationInput) { c.Contacts = []json.RawMessage{json.RawMessage(`[]`)} })},
		{"billing code without code", "2.0", ci(func(c *CoverageInformationInput) {
			c.BillingCodes = []CRDCoding{{System: "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"}}
		})},
		{"card without topic", "2.0", func(in *CRDResponseInputs) {
			in.Cards = []CRDCard{{Summary: "s", Indicator: "info", Source: CRDCardSource{Label: "P"}}}
		}},
		{"card without label", "2.0", func(in *CRDResponseInputs) {
			in.Cards = []CRDCard{{Summary: "s", Indicator: "info", Source: CRDCardSource{Topic: CRDCoding{System: "s", Code: "c"}}}}
		}},
		{"card summary too long", "2.0", func(in *CRDResponseInputs) {
			in.Cards = []CRDCard{{Summary: strings.Repeat("x", 140), Indicator: "info",
				Source: CRDCardSource{Label: "P", Topic: CRDCoding{System: "s", Code: "c"}}}}
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			in := paRequiredInputs(r.line)
			r.m(&in)
			if out, err := BuildCRDResponse(r.line, in); err == nil {
				t.Fatalf("built %s", out)
			}
		})
	}
	// Each refusal row mutates one thing: the unmutated inputs build.
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		if _, err := BuildCRDResponse(line, paRequiredInputs(line)); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
}

// TestBuildCRDResponse_EmitsOnlySuppliedValues: optional sub-extensions appear
// only when supplied, in the profile's order, with the supplied bytes.
func TestBuildCRDResponse_EmitsOnlySuppliedValues(t *testing.T) {
	in := paRequiredInputs("2.2")
	c := &in.Orders[0].Coverage[0]
	c.DocPurpose = []string{"withpa"}
	c.Reasons = []json.RawMessage{json.RawMessage(`{"text":"Imaging  policy <b>"}`)}
	c.BillingCodes = []CRDCoding{{System: "http://www.ama-assn.org/go/cpt", Code: "72148"}}
	c.ExpiryDate = "2026-12-31"
	c.Contacts = []json.RawMessage{json.RawMessage(`{"telecom":[{"system":"phone","value":"555-0100"}]}`)}
	out, err := BuildCRDResponse("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ParseCRDResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	ci := obs.Orders[0].Coverage[0]
	want := []string{"coverage", "covered", "pa-needed", "doc-needed", "doc-purpose", "billingCode", "reason", "questionnaire", "date", "coverage-assertion-id", "contact", "expiry-date"}
	if got := valueURLs(ci); !slices.Equal(got, want) {
		t.Fatalf("sub-extensions %v, want %v", got, want)
	}
	if string(ci.Reasons[0]) != `{"text":"Imaging  policy <b>"}` || string(ci.Contacts[0]) != `{"telecom":[{"system":"phone","value":"555-0100"}]}` {
		t.Fatalf("supplied bytes changed: %s %s", ci.Reasons[0], ci.Contacts[0])
	}
	minimal := paRequiredInputs("2.0")
	minimal.Orders[0].Coverage[0] = CoverageInformationInput{Coverage: "Coverage/c1", Covered: "covered", PANeeded: "no-auth", Date: "2026-09-16", CoverageAssertionID: "a1"}
	out, err = BuildCRDResponse("2.0", minimal)
	if err != nil {
		t.Fatal(err)
	}
	obs, err = ParseCRDResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := valueURLs(obs.Orders[0].Coverage[0]); !slices.Equal(got, []string{"coverage", "covered", "pa-needed", "date", "coverage-assertion-id"}) {
		t.Fatalf("minimal sub-extensions %v", got)
	}
}

// TestDeprecatedBuildCards_OutputUnchanged pins the deprecated card builder's
// output byte for byte: callers that still read the card extension object keep
// working until they move to BuildCRDResponse.
func TestDeprecatedBuildCards_OutputUnchanged(t *testing.T) {
	rows := []struct {
		cov  CardCoverage
		want string
	}{
		{CardCoverage{Covered: "covered", PANeeded: "auth-needed", Questionnaires: []string{"http://x/Q"}},
			`{"cards":[{"summary":"Prior authorization required","indicator":"warning","extension":{"covered":"covered","paNeeded":"auth-needed","questionnaires":["http://x/Q"]}}]}`},
		{CardCoverage{Covered: "not-covered"},
			`{"cards":[{"summary":"Service not covered","indicator":"warning","extension":{"covered":"not-covered"}}]}`},
		{CardCoverage{Covered: "covered", PANeeded: "no-auth"},
			`{"cards":[{"summary":"No prior authorization required","indicator":"info","extension":{"covered":"covered","paNeeded":"no-auth"}}]}`},
	}
	for _, r := range rows {
		for _, line := range []string{"2.0", "2.1", "2.2"} {
			got, err := BuildCardsAtLine(line, r.cov)
			if err != nil || string(got) != r.want {
				t.Errorf("%s: %s %v", line, got, err)
			}
		}
		got, err := BuildCards(r.cov)
		if err != nil || string(got) != r.want {
			t.Errorf("BuildCards: %s %v", got, err)
		}
	}
}

// TestDeprecatedParseCards_LegacyFirst: the deprecated reader returns the card
// extension object exactly as before when the first card carries one, and
// otherwise the coverage information the response carries.
func TestDeprecatedParseCards_LegacyFirst(t *testing.T) {
	mixed := `{"cards":[{"summary":"s","indicator":"info","extension":{"covered":"not-covered"}}],"systemActions":[` +
		`{"type":"update","description":"d","resource":{"resourceType":"ServiceRequest","id":"a","extension":[{"url":"` + CoverageInformationURL +
		`","extension":[{"url":"covered","valueCode":"covered"}]}]}}]}`
	if cov, err := ParseCards([]byte(mixed)); err != nil || cov.Covered != "not-covered" {
		t.Fatalf("legacy card first: %+v %v", cov, err)
	}
	if _, err := ParseCards([]byte(`{"cards":[]}`)); err == nil {
		t.Fatal("no cards and no coverage information must still error")
	}
	if _, err := ParseCards([]byte(`not json`)); err == nil {
		t.Fatal("garbage accepted")
	}
	cov, err := ParseCards([]byte(`{"cards":[{"summary":"s","indicator":"info"}]}`))
	if err != nil || !reflect.DeepEqual(cov, CardCoverage{}) {
		t.Fatalf("a card without coverage keeps the earlier empty result: %+v %v", cov, err)
	}
}

// TestBuildCRDResponse_LineRules: values each line allows build, with the
// contact written as the line's own type, both directions of crd-ci-q3 hold,
// and a card without a uuid is built below 2.2.
func TestBuildCRDResponse_LineRules(t *testing.T) {
	for _, r := range []struct {
		line, contact, key string
	}{
		{"2.0", `{"system":"phone","value":"555","use":"work","rank":1,"period":{"start":"2026-01-01"},"id":"c","extension":[]}`, "valueContactPoint"},
		{"2.1", `{"system":"url","value":"https://payer.example/um"}`, "valueContactPoint"},
		{"2.2", `{"name":"UM desk","telecom":[{"system":"phone","value":"555"}],"id":"c","extension":[]}`, "valueContactDetail"},
	} {
		in := paRequiredInputs(r.line)
		in.Orders[0].Coverage[0].Contacts = []json.RawMessage{json.RawMessage(r.contact)}
		out, err := BuildCRDResponse(r.line, in)
		if err != nil {
			t.Fatalf("%s: %v", r.line, err)
		}
		if !bytes.Contains(out, []byte(`{"url":"contact","`+r.key+`":`+r.contact+`}`)) {
			t.Fatalf("%s: contact not written as %s: %s", r.line, r.key, out)
		}
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		in := paRequiredInputs(line)
		c := &in.Orders[0].Coverage[0]
		c.PANeeded = "conditional"
		c.InfoNeeded = []string{"performer"}
		if _, err := BuildCRDResponse(line, in); err != nil {
			t.Fatalf("%s: conditional with info-needed: %v", line, err)
		}
		// withpa stays allowed when authorization is needed.
		in = paRequiredInputs(line)
		in.Orders[0].Coverage[0].DocPurpose = []string{"withpa"}
		in.Orders[0].Coverage[0].Reasons = []json.RawMessage{json.RawMessage(`{"text":"r"}`)}
		if _, err := BuildCRDResponse(line, in); err != nil {
			t.Fatalf("%s: auth-needed with withpa: %v", line, err)
		}
	}
	for _, line := range []string{"2.0", "2.1"} {
		in := paRequiredInputs(line)
		in.Cards = []CRDCard{{Summary: "s", Indicator: "info", Source: CRDCardSource{Label: "P", Topic: CRDCoding{System: "s", Code: "c"}}}}
		if _, err := BuildCRDResponse(line, in); err != nil {
			t.Fatalf("%s: card without uuid: %v", line, err)
		}
	}
}
