package shnsdk

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestDeprecatedBuildCards_RefusesNamingReplacement: a CRD answer returns the
// requested order with its coverage information, and a CardCoverage names no
// order, no Coverage, no date and no assertion id. The deprecated card
// builders therefore build nothing and name BuildCRDResponse, at every line,
// instead of writing a card no CRD line defines or inventing those facts.
func TestDeprecatedBuildCards_RefusesNamingReplacement(t *testing.T) {
	covs := []CardCoverage{
		{Covered: CoveredCovered, PANeeded: PANeededAuthNeeded, Questionnaires: []string{"http://x/Q"}},
		{Covered: CoveredNotCovered},
		{Covered: CoveredCovered, PANeeded: PANeededNoAuth},
		{},
	}
	check := func(name string, out []byte, err error) {
		t.Helper()
		if out != nil || !errors.Is(err, ErrBuildCardsReplaced) || !strings.Contains(err.Error(), "BuildCRDResponse") {
			t.Errorf("%s: out=%s err=%v, want no output and ErrBuildCardsReplaced naming BuildCRDResponse", name, out, err)
		}
	}
	for _, cov := range covs {
		out, err := BuildCards(cov)
		check("BuildCards", out, err)
		for _, line := range []string{"2.0", "2.1", "2.2"} {
			out, err := BuildCardsAtLine(line, cov)
			check("BuildCardsAtLine "+line, out, err)
		}
	}
	// An unknown line is still refused as an unknown line.
	if out, err := BuildCardsAtLine("9.9", covs[0]); out != nil || err == nil || errors.Is(err, ErrBuildCardsReplaced) ||
		!strings.Contains(err.Error(), "unknown CRD line") {
		t.Fatalf("unknown line: %s %v", out, err)
	}
}

// crdAnswerWithAdvisoryCard is a payer answer shaped like the reference
// payer's dispatch answer: an advisory card carrying a CDS Hooks extension
// object of the payer's own, and the order returned in an update system
// action with its coverage information.
const crdAnswerWithAdvisoryCard = `{"cards":[{"summary":"Verify supplier status","indicator":"info",` +
	`"source":{"label":"Payer","topic":{"system":"http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp","code":"coverage-info"}},` +
	`"extension":{"davinci-crd.associated-resource":["DeviceRequest/dr1"]}}],` +
	`"systemActions":[{"type":"update","description":"d","resource":{"resourceType":"DeviceRequest","id":"dr1","extension":[` +
	`{"url":"` + CoverageInformationURL + `","extension":[{"url":"covered","valueCode":"conditional"},` +
	`{"url":"questionnaire","valueCanonical":"http://payer.example/Questionnaire/HomeOxygen"}]}]}}]}`

// TestParseCards_CardExtensionOfAnotherShapeIsNotCoverage: a card's CDS Hooks
// extension object is read as this SDK's earlier coverage object only when it
// has that object's shape (a covered value and only that object's members).
// Any other extension object is the payer's own and carries no coverage, so
// the coverage the answer states elsewhere is returned.
func TestParseCards_CardExtensionOfAnotherShapeIsNotCoverage(t *testing.T) {
	want := CardCoverage{Covered: "conditional", Questionnaires: []string{"http://payer.example/Questionnaire/HomeOxygen"}}
	cov, err := ParseCards([]byte(crdAnswerWithAdvisoryCard))
	if err != nil || !reflect.DeepEqual(cov, want) {
		t.Fatalf("ParseCards = %+v, %v; want %+v", cov, err, want)
	}
	obs, err := ParseCRDResponse([]byte(crdAnswerWithAdvisoryCard))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Orders) != 1 || obs.Orders[0].Source != CRDSourceSystemAction {
		t.Fatalf("orders %+v: the advisory card's extension object is not an order", obs.Orders)
	}

	// Coverage stated in a card suggestion of the same card is read too.
	suggested := `{"cards":[{"summary":"s","indicator":"info","source":{"label":"P"},"extension":{"x-payer":{"a":1}},` +
		`"selectionBehavior":"any","suggestions":[{"label":"Save","actions":[{"type":"update","description":"d","resource":` +
		`{"resourceType":"ServiceRequest","id":"sr1","extension":[{"url":"` + CoverageInformationURL + `","extension":[` +
		`{"url":"covered","valueCode":"covered"},{"url":"pa-needed","valueCode":"no-auth"}]}]}}]}]}]}`
	obs, err = ParseCRDResponse([]byte(suggested))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := obs.Primary(); !ok || p.Covered != "covered" || p.PANeeded != "no-auth" || len(obs.Orders) != 1 {
		t.Fatalf("suggested coverage: %+v %v (%+v)", p, ok, obs.Orders)
	}
	if cov, err := ParseCards([]byte(suggested)); err != nil || cov.Covered != "covered" {
		t.Fatalf("ParseCards(suggested) = %+v, %v", cov, err)
	}

	// Rejection rows: none of these is the earlier coverage object, so none
	// becomes coverage, and a non-coverage extension never fails the read.
	for name, ext := range map[string]string{
		"payer's own object":        `{"davinci-crd.associated-resource":["DeviceRequest/dr1"]}`,
		"covered is not a string":   `{"covered":5}`,
		"covered is empty":          `{"covered":""}`,
		"an extra member":           `{"covered":"covered","paNeeded":"no-auth","source":"x"}`,
		"a member in another case":  `{"Covered":"covered"}`,
		"questionnaires not a list": `{"covered":"covered","questionnaires":"http://x/Q"}`,
		"empty object":              `{}`,
	} {
		body := `{"cards":[{"summary":"s","indicator":"info","extension":` + ext + `}]}`
		obs, err := ParseCRDResponse([]byte(body))
		if err != nil || len(obs.Orders) != 0 {
			t.Errorf("%s: ParseCRDResponse = %+v, %v; want no order", name, obs.Orders, err)
		}
		if cov, err := ParseCards([]byte(body)); err != nil || !reflect.DeepEqual(cov, CardCoverage{}) {
			t.Errorf("%s: ParseCards = %+v, %v; want the empty projection", name, cov, err)
		}
	}

	// The earlier coverage object keeps being read, member for member.
	legacy := `{"cards":[{"summary":"s","indicator":"warning","extension":{"covered":"covered","paNeeded":"satisfied",` +
		`"questionnaires":["http://x/Q"],"satisfiedPaId":"pa-1"}}]}`
	want = CardCoverage{Covered: "covered", PANeeded: "satisfied", Questionnaires: []string{"http://x/Q"}, SatisfiedPaID: "pa-1"}
	if cov, err := ParseCards([]byte(legacy)); err != nil || !reflect.DeepEqual(cov, want) {
		t.Fatalf("legacy object: %+v %v", cov, err)
	}
	if obs, err := ParseCRDResponse([]byte(legacy)); err != nil || len(obs.Orders) != 1 || obs.Orders[0].Source != CRDSourceLegacyCard {
		t.Fatalf("legacy object observation: %+v %v", obs, err)
	}
}

// TestDeprecatedCRDRequestBuilders_RefuseWithoutPatient: a CRD request's
// patient prefetch is the participant's own Patient record. Neither deprecated
// request builder takes one (a patient id is not a Patient), so neither
// builds anything: no request, no id-only Patient, no placeholder fhirServer,
// no user, no payer Organization and no rewritten Coverage. Each names
// BuildCRDRequest, which takes the participant's own Patient and carries it
// exactly. Every row is refused whatever else it holds.
func TestDeprecatedCRDRequestBuilders_RefuseWithoutPatient(t *testing.T) {
	sr, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatal(err)
	}
	cov, err := BuildCoverageWithPayer("Patient/MBR-COVERED", "MBR-COVERED", CMSPayerIdentity)
	if err != nil {
		t.Fatal(err)
	}
	dr := []byte(`{"resourceType":"DeviceRequest","id":"dr1","status":"draft","intent":"order","subject":{"reference":"Patient/MBR-OX"}}`)
	check := func(t *testing.T, name string, out []byte, err error) {
		t.Helper()
		if out != nil || !errors.Is(err, errCRDRequestNeedsPatient) ||
			!strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "BuildCRDRequest") {
			t.Fatalf("out=%s err=%v; want no output and the refusal naming %s and BuildCRDRequest", out, err, name)
		}
	}
	for row, in := range map[string]struct {
		sr, cov []byte
		patient string
	}{
		"the earlier happy path":  {sr, cov, "Patient/MBR-COVERED"},
		"a bare patient id":       {sr, cov, "MBR-COVERED"},
		"no patient id":           {sr, cov, ""},
		"a DeviceRequest order":   {dr, cov, "Patient/MBR-OX"},
		"inputs that do not read": {[]byte(`not json`), nil, "MBR-COVERED"},
		"no inputs":               {nil, nil, ""},
	} {
		t.Run("order-select/"+row, func(t *testing.T) {
			out, err := BuildConformantOrderSelectRequest(in.sr, in.cov, in.patient)
			check(t, "BuildConformantOrderSelectRequest", out, err)
		})
	}
	full := OrderDispatchInputs{
		PatientID: "MBR-OX", PatientRef: "Patient/MBR-OX", OrderRef: "DeviceRequest/dr1", PerformerRef: "Organization/sup1",
		DeviceRequest: dr, Supplier: []byte(`{"resourceType":"Organization","id":"sup1"}`), Coverage: cov, Payer: CMSPayerIdentity,
	}
	noPatientID := full
	noPatientID.PatientID, noPatientID.PatientRef = "", ""
	prefixed := full
	prefixed.PatientID = "Patient/MBR-OX"
	for row, in := range map[string]OrderDispatchInputs{
		"the earlier happy path":  full,
		"a prefixed patient id":   prefixed,
		"no patient id":           noPatientID,
		"only a patient id":       {PatientID: "MBR-OX"},
		"inputs that do not read": {PatientID: "MBR-OX", DeviceRequest: []byte(`not json`), Coverage: []byte(`[`)},
		"no inputs":               {},
	} {
		t.Run("order-dispatch/"+row, func(t *testing.T) {
			out, err := BuildConformantOrderDispatchRequest(in)
			check(t, "BuildConformantOrderDispatchRequest", out, err)
		})
	}
}

// TestBuildCRDRequest_ReplacesDeprecatedBuildersWithCallersPatient: the
// replacement the refusal names carries the caller's own Patient byte for
// byte at every hook the deprecated builders served, and never an id-only
// Patient in its place.
func TestBuildCRDRequest_ReplacesDeprecatedBuildersWithCallersPatient(t *testing.T) {
	for _, in := range []CRDRequestInputs{orderSelectInputs(), dispatchInputs()} {
		out, err := BuildCRDRequest(in)
		if err != nil {
			t.Fatalf("%s: %v", in.Hook, err)
		}
		if !strings.Contains(string(out), `"prefetch":{"patient":`+crdReqPatient+`,`) {
			t.Fatalf("%s: the caller's Patient is not carried exactly: %s", in.Hook, out)
		}
		if strings.Contains(string(out), `{"id":"pat-1","resourceType":"Patient"}`) || strings.Contains(string(out), `"fhirServer"`) {
			t.Fatalf("%s: a made-up Patient or FHIR server: %s", in.Hook, out)
		}
	}
}
