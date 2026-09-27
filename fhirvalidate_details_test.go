package shnsdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// laneAnswer is what a real HAPI validator lane answered to one $validate
// request, as the lane sent it: testdata/validation/README.md names the
// request, the lane and the status.
type laneAnswer struct {
	file   string
	status int
}

var (
	// A resource with no error: two warnings, each with its message id,
	// expression and location.
	lanePositive = laneAnswer{"hapi-lane-positive-warnings.json", http.StatusOK}
	// An explicitly requested profile the lane does not have: three warnings
	// and the Validation_VAL_Profile_Unknown error.
	laneProfileUnknown = laneAnswer{"hapi-lane-profile-unknown.json", http.StatusOK}
	// A body the lane could not parse: 400 and one HAPI-0450 error with no
	// message id. The SDK refuses a body that is not JSON before sending it, so
	// tests serve this answer for its shape: a non-2xx OperationOutcome.
	laneUnparseable = laneAnswer{"hapi-lane-unparseable-body-400.json", http.StatusBadRequest}
)

func (a laneAnswer) body(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "validation", a.file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// issues decodes a recorded answer's issues, to state what a test expects
// from the answer's own members.
func (a laneAnswer) issues(t *testing.T) []map[string]any {
	t.Helper()
	var oo struct{ Issue []map[string]any }
	if err := json.Unmarshal(a.body(t), &oo); err != nil || len(oo.Issue) == 0 {
		t.Fatalf("%s: %v", a.file, err)
	}
	return oo.Issue
}

// outcomeStub is a $validate endpoint that answers with status and body, the
// way a lane does (Content-Type included). It refuses what the SDK must not
// send: a method other than POST, or a path that is not /<type>/$validate.
func outcomeStub(t *testing.T, status int, body []byte) *shnsdk.OperationValidator {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !typeValidatePath.MatchString(r.URL.Path) {
			t.Errorf("the validator was sent %s %s, want POST /<type>/$validate", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/fhir+json;charset=UTF-8")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return shnsdk.NewOperationValidator(srv.URL)
}

var typeValidatePath = regexp.MustCompile(`^/[A-Z][A-Za-z]+/\$validate$`)

// laneStub serves a recorded lane answer.
func laneStub(t *testing.T, a laneAnswer) *shnsdk.OperationValidator {
	t.Helper()
	return outcomeStub(t, a.status, a.body(t))
}

// operationOutcomeStub serves body with 200. Its callers test the decoder
// against deliberate shapes no lane sends (malformed members, key spellings,
// message-id precedence); an answer a lane does send comes from laneStub.
func operationOutcomeStub(t *testing.T, body []byte) *shnsdk.OperationValidator {
	t.Helper()
	return outcomeStub(t, http.StatusOK, body)
}

// withIssues returns a recorded answer with extra issues appended after the
// lane's own.
func withIssues(t *testing.T, a laneAnswer, extra ...string) []byte {
	t.Helper()
	var oo map[string]any
	if err := json.Unmarshal(a.body(t), &oo); err != nil {
		t.Fatal(err)
	}
	for _, e := range extra {
		var issue any
		if err := json.Unmarshal([]byte(e), &issue); err != nil {
			t.Fatal(err)
		}
		oo["issue"] = append(oo["issue"].([]any), issue)
	}
	b, err := json.Marshal(oo)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// strs converts a decoded JSON array of strings.
func strs(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		out = append(out, x.(string))
	}
	return out
}

// The lane's real refusal of an unknown profile (three warnings, then the
// error), with two deliberately malformed issues appended after it.
func TestOperationValidator_DetailsCarryEveryIssueInOrder(t *testing.T) {
	v := outcomeStub(t, http.StatusOK, withIssues(t, laneProfileUnknown,
		`{"severity":"fatal","code":{"not":"a string"},"diagnostics":"bad json"}`,
		`{"severity":"information","diagnostics":"no code member"}`))
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"ClaimResponse"}`), "https://example.org/fhir/StructureDefinition/unavailable-profile")
	if err != nil {
		t.Fatal(err)
	}
	lane := laneProfileUnknown.issues(t)
	if len(lane) != 4 || lane[3]["severity"] != "error" {
		t.Fatalf("the recorded refusal changed: %v", lane)
	}
	// Valid and Issues are computed exactly as before: error and fatal
	// diagnostics only, in order.
	if res.Valid || !reflect.DeepEqual(res.Issues, []string{lane[3]["diagnostics"].(string), "bad json"}) {
		t.Fatalf("Valid/Issues changed: %+v", res)
	}
	var want []shnsdk.Issue
	for i, id := range []string{"Terminology_TX_ValueSet_NotFound", "Terminology_PassThrough_TX_Message",
		"http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6", "Validation_VAL_Profile_Unknown"} {
		// location ("Line[1] Col[1126]" and the like) is not FHIRPath, so it
		// never fills Expression: the refusal's error has a location and no
		// expression.
		want = append(want, shnsdk.Issue{Severity: lane[i]["severity"].(string), Code: "processing", MessageID: id,
			Expression: strs(lane[i]["expression"]), Diagnostics: lane[i]["diagnostics"].(string)})
	}
	want = append(want,
		// A non-string code is recorded as "" rather than making the outcome
		// unreadable, so the verdict stands.
		shnsdk.Issue{Severity: "fatal", Code: "", Diagnostics: "bad json"},
		shnsdk.Issue{Severity: "information", Code: "", Diagnostics: "no code member"})
	if !reflect.DeepEqual(res.Details, want) {
		t.Fatalf("Details = %+v, want %+v", res.Details, want)
	}
	if !reflect.DeepEqual(want[2].Expression, []string{"ClaimResponse"}) || want[3].Expression != nil || len(strs(lane[3]["location"])) == 0 {
		t.Fatal("the refusal's error must carry a location and no expression for this row to prove anything")
	}
}

// The lane's answer for a resource with no error: warnings only.
func TestOperationValidator_ValidResultStillCarriesWarningsInDetails(t *testing.T) {
	res, err := laneStub(t, lanePositive).Validate(context.Background(), []byte(`{"resourceType":"Claim"}`), "http://hl7.org/fhir/StructureDefinition/Claim")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Issues != nil {
		t.Fatalf("a warning-only outcome is valid with no Issues, got %+v", res)
	}
	lane := lanePositive.issues(t)
	var want []shnsdk.Issue
	for i, id := range []string{"Reference_REF_MultipleMatches", "http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6"} {
		want = append(want, shnsdk.Issue{Severity: "warning", Code: "processing", MessageID: id,
			Expression: strs(lane[i]["expression"]), Diagnostics: lane[i]["diagnostics"].(string)})
	}
	if !reflect.DeepEqual(res.Details, want) || !reflect.DeepEqual(want[1].Expression, []string{"Claim"}) {
		t.Fatalf("Details = %+v, want %+v", res.Details, want)
	}
}

// An OperationOutcome with no issue member at all. No lane sends one (issue is
// required; a clean lane answer still carries issues), so this is a decoder
// row for a deliberate shape.
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
// verdict, and its issues are in Details: the lane's 400 for a body it could
// not parse (served for its shape; see laneUnparseable), whose one error
// carries no message id.
func TestOperationValidator_Non2xxOutcomeCarriesDetails(t *testing.T) {
	res, err := laneStub(t, laneUnparseable).Validate(context.Background(), []byte(`{"resourceType":"Patient","birthDate":7}`), "")
	if err != nil {
		t.Fatal(err)
	}
	lane := laneUnparseable.issues(t)
	want := []shnsdk.Issue{{Severity: "error", Code: "processing", Diagnostics: lane[0]["diagnostics"].(string)}}
	if res.Valid || !reflect.DeepEqual(res.Details, want) || !strings.HasPrefix(want[0].Diagnostics, "HAPI-0450: ") {
		t.Fatalf("got %+v, want invalid with Details %+v", res, want)
	}
}

// A 502 with a body that is not an OperationOutcome is an outage in front of
// the validator (a proxy's own answer), not a lane answer.
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
