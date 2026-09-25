package shnsdk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// operationOutcomeStub serves body as a $validate response.
func operationOutcomeStub(t *testing.T, body []byte) *shnsdk.OperationValidator {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return shnsdk.NewOperationValidator(srv.URL)
}

func TestOperationValidator_DetailsCarryEveryIssueInOrder(t *testing.T) {
	v := operationOutcomeStub(t, []byte(`{"resourceType":"OperationOutcome","issue":[
		{"severity":"error","code":"processing","diagnostics":"Coverage.status: minimum required = 1, but only found 0",
		 "details":{"coding":[{"system":"http://hl7.org/fhir/java-core-messageId","code":"Validation_VAL_Profile_Minimum"}]},
		 "expression":["Coverage"]},
		{"severity":"warning","code":"processing","diagnostics":"dom-6",
		 "extension":[{"url":"http://hl7.org/fhir/StructureDefinition/operationoutcome-message-id","valueString":"http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6"}],
		 "location":["Coverage","Line[1] Col[2]"]},
		{"severity":"fatal","code":{"not":"a string"},"diagnostics":"bad json"},
		{"severity":"information","diagnostics":"no code member"}]}`))
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Coverage"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	// Valid and Issues are computed exactly as before: error and fatal
	// diagnostics only, in order.
	if res.Valid || !reflect.DeepEqual(res.Issues, []string{"Coverage.status: minimum required = 1, but only found 0", "bad json"}) {
		t.Fatalf("Valid/Issues changed: %+v", res)
	}
	want := []shnsdk.Issue{
		{Severity: "error", Code: "processing", MessageID: "Validation_VAL_Profile_Minimum",
			Expression: []string{"Coverage"}, Diagnostics: "Coverage.status: minimum required = 1, but only found 0"},
		// location is not FHIRPath, so it never fills Expression.
		{Severity: "warning", Code: "processing", MessageID: "http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6",
			Diagnostics: "dom-6"},
		// A non-string code is recorded as "" rather than making the outcome
		// unreadable, so the verdict stands.
		{Severity: "fatal", Code: "", Diagnostics: "bad json"},
		{Severity: "information", Code: "", Diagnostics: "no code member"},
	}
	if !reflect.DeepEqual(res.Details, want) {
		t.Fatalf("Details = %+v, want %+v", res.Details, want)
	}
}

func TestOperationValidator_ValidResultStillCarriesWarningsInDetails(t *testing.T) {
	v := operationOutcomeStub(t, []byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"warning","code":"processing","diagnostics":"dom-6"}]}`))
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Issues != nil {
		t.Fatalf("a warning-only outcome is valid with no Issues, got %+v", res)
	}
	if want := []shnsdk.Issue{{Severity: "warning", Code: "processing", Diagnostics: "dom-6"}}; !reflect.DeepEqual(res.Details, want) {
		t.Fatalf("Details = %+v, want %+v", res.Details, want)
	}
}

func TestOperationValidator_NoIssuesLeavesDetailsNil(t *testing.T) {
	v := operationOutcomeStub(t, []byte(`{"resourceType":"OperationOutcome"}`))
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Issues != nil || res.Details != nil {
		t.Fatalf("an outcome with no issues is valid with nil Issues and Details, got %+v", res)
	}
}

// A real HAPI FHIR validator OperationOutcome, as the validator returned it:
// every issue carries the generic code "processing", and the kind of issue is
// in the java-core message id.
func TestOperationValidator_RealHAPIOutcomeCarriesMessageIDs(t *testing.T) {
	body, err := os.ReadFile("testdata/validation/hapi-operation-outcome.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := operationOutcomeStub(t, body).Validate(context.Background(), []byte(`{"resourceType":"Questionnaire"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Issues) != 1 || len(res.Details) != 20 {
		t.Fatalf("want 1 error Issue and 20 Details, got %d Issues, %d Details", len(res.Issues), len(res.Details))
	}
	for i, d := range res.Details {
		if d.Code != "processing" || d.MessageID == "" {
			t.Errorf("Details[%d] = %s/%q, want code processing and a message id", i, d.Code, d.MessageID)
		}
	}
	e := res.Details[18]
	if e.Severity != "error" || e.MessageID != "Extension_EXT_Version_Invalid" || e.Diagnostics != res.Issues[0] ||
		len(e.Expression) != 1 || e.Expression[0] != "Questionnaire.extension[3][url='http://hl7.org/fhir/5.0/StructureDefinition/extension-Questionnaire.versionAlgorithm[x]']" {
		t.Fatalf("the error issue = %+v", e)
	}
	if w := res.Details[19]; w.Severity != "warning" || w.MessageID != "http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6" {
		t.Fatalf("the warning issue = %+v", w)
	}
}

// A non-2xx response that still carries a parseable OperationOutcome is a
// verdict, and its issues are in Details.
func TestOperationValidator_Non2xxOutcomeCarriesDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"processing","diagnostics":"unparseable",` +
			`"details":{"coding":[{"system":"http://hl7.org/fhir/java-core-messageId","code":"Error_parsing_JSON_"}]}}]}`))
	}))
	defer srv.Close()
	res, err := shnsdk.NewOperationValidator(srv.URL).Validate(context.Background(), []byte(`{"resourceType":"Patient","birthDate":7}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Details) != 1 || res.Details[0].MessageID != "Error_parsing_JSON_" {
		t.Fatalf("got %+v", res)
	}
}

func TestOperationValidator_OutageCarriesNoDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()
	res, err := shnsdk.NewOperationValidator(srv.URL).Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err == nil {
		t.Fatal("a non-OperationOutcome non-2xx body is an outage")
	}
	if res.Details != nil {
		t.Fatalf("an outage carries no Details, got %+v", res.Details)
	}
}

func TestFakeValidator_RejectIssueCarriesDetails(t *testing.T) {
	v := &shnsdk.FakeValidator{RejectIfContains: "BAD", RejectIssue: &shnsdk.Issue{Code: "processing", MessageID: "Validation_VAL_Profile_Minimum"}}
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"BAD"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []shnsdk.Issue{{Severity: "error", Code: "processing", MessageID: "Validation_VAL_Profile_Minimum", Diagnostics: "fake: contains BAD"}}
	if res.Valid || !reflect.DeepEqual(res.Issues, []string{"fake: contains BAD"}) || !reflect.DeepEqual(res.Details, want) {
		t.Fatalf("got %+v, want invalid with Details %+v", res, want)
	}
}

func TestFakeValidator_NoRejectIssueLeavesDetailsNil(t *testing.T) {
	v := &shnsdk.FakeValidator{RejectIfContains: "BAD"}
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"BAD"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res, shnsdk.Result{Valid: false, Issues: []string{"fake: contains BAD"}}) {
		t.Fatalf("result changed without RejectIssue: %+v", res)
	}
	res, err = v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil || !reflect.DeepEqual(res, shnsdk.Result{Valid: true}) {
		t.Fatalf("valid result changed: %+v, %v", res, err)
	}
}

func TestHTTPValidator_LeavesDetailsNil(t *testing.T) {
	v := httpValidatorStub(t, `{"outcomes":[{"issues":[{"level":"ERROR","message":"bad"}]}]}`)
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || res.Details != nil {
		t.Fatalf("HTTPValidator does not populate Details, got %+v", res)
	}
}

// A member of an unexpected shape in an issue never changes the verdict: the
// verdict reads only severity and diagnostics, as it always has, and the
// member's Details field is left empty.
func TestOperationValidator_MalformedIssueMembersKeepTheVerdict(t *testing.T) {
	for name, member := range map[string]string{
		"expression not an array":      `"expression":"Patient"`,
		"location not an array":        `"location":"/f:Patient"`,
		"extension value not a string": `"extension":[{"url":"http://hl7.org/fhir/StructureDefinition/operationoutcome-message-id","valueString":5}]`,
		"coding not an array":          `"details":{"coding":{"system":"x"}}`,
		"code not a string":            `"code":{"x":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			v := operationOutcomeStub(t, []byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"warning","diagnostics":"w",`+member+`}]}`))
			res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
			if err != nil || !res.Valid || res.Issues != nil {
				t.Fatalf("verdict changed: %+v, %v", res, err)
			}
			if len(res.Details) != 1 || res.Details[0].Severity != "warning" || res.Details[0].Diagnostics != "w" {
				t.Fatalf("Details = %+v", res.Details)
			}
		})
	}
}

// The message id comes from details.coding before the extension; within the
// extensions, from the first message-id extension that carries a value, as a
// string or a code.
func TestOperationValidator_MessageIDSources(t *testing.T) {
	const coding = `"details":{"coding":[{"system":"http://hl7.org/fhir/java-core-messageId","code":"FromCoding"}]}`
	const ext = "http://hl7.org/fhir/StructureDefinition/operationoutcome-message-id"
	for name, tc := range map[string]struct{ member, want string }{
		"coding wins over the extension": {coding + `,"extension":[{"url":"` + ext + `","valueString":"FromExtension"}]`, "FromCoding"},
		"extension string":               {`"extension":[{"url":"` + ext + `","valueString":"FromString"}]`, "FromString"},
		"extension code":                 {`"extension":[{"url":"` + ext + `","valueCode":"FromCode"}]`, "FromCode"},
		"an empty extension is skipped":  {`"extension":[{"url":"` + ext + `"},{"url":"` + ext + `","valueString":"Second"}]`, "Second"},
		"another system is ignored":      {`"details":{"coding":[{"system":"urn:other","code":"X"}]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			v := operationOutcomeStub(t, []byte(`{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"processing","diagnostics":"d",`+tc.member+`}]}`))
			res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
			if err != nil || len(res.Details) != 1 {
				t.Fatalf("got %+v, %v", res, err)
			}
			if res.Details[0].MessageID != tc.want {
				t.Fatalf("MessageID = %q, want %q", res.Details[0].MessageID, tc.want)
			}
		})
	}
}

// Details and the verdict agree on every issue's severity and diagnostics,
// whatever the key spelling a validator sends: an invalid result always has a
// matching error entry in Details.
func TestOperationValidator_DetailsAgreeWithTheVerdict(t *testing.T) {
	v := operationOutcomeStub(t, []byte(`{"resourceType":"OperationOutcome","issue":[
		{"Severity":"error","Diagnostics":"x"},
		{"severity":"warning","SEVERITY":"error","diagnostics":"y"}]}`))
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Details) != 2 {
		t.Fatalf("Details = %+v", res.Details)
	}
	errs := 0
	for i, d := range res.Details {
		if d.Severity == "error" || d.Severity == "fatal" {
			errs++
		}
		if i == 0 && (d.Severity != "error" || d.Diagnostics != "x") {
			t.Fatalf("Details[0] = %+v, want the verdict's error x", d)
		}
	}
	if res.Valid || errs != len(res.Issues) {
		t.Fatalf("verdict %+v with %d error Details, want one per Issue", res, errs)
	}
}
