package shnsdk_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

var testCDexMeta = shnsdk.CDexTaskMeta{
	AuthoredOn: time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC),
	Requester:  "provider-gw",
	Owner:      "facility-gw",
}

// validCDexTask is the positive control: a narrow, consented single-type CDex request.
// cdex-9 mandates exactly one data-query input per Task; FR-24 two named types ⇒ two Tasks/legs.
func validCDexTask(t *testing.T) []byte {
	t.Helper()
	b, err := shnsdk.BuildCDexTaskDataRequest("Patient/MBR-UC05", "DiagnosticReport", "2024-01-01", "2026-12-31", testCDexMeta)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseCDex_ValidPasses(t *testing.T) {
	got, err := shnsdk.ParseCDexTaskDataRequest(validCDexTask(t))
	if err != nil {
		t.Fatalf("valid CDex request rejected: %v", err)
	}
	if got.PatientRef != "Patient/MBR-UC05" || len(got.Queries) != 1 {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

// TestParseCDex_EvasionMatrix is the §3.1 allowlist guard — one row per evasion vector (A1–A13).
// Each mutates the valid task's data-query (or Task.for) and MUST be rejected.
func TestParseCDex_EvasionMatrix(t *testing.T) {
	mk := func(query string) []byte { // single-data-query task with a custom query string + valid POU + for
		return []byte(`{"resourceType":"Task","meta":{"profile":["` + "http://hl7.org/fhir/us/davinci-cdex/StructureDefinition/cdex-task-data-request" + `"]},` +
			`"status":"requested","intent":"order","code":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-cdex/CodeSystem/cdex-temp","code":"data-request-query"}]},` +
			`"for":{"reference":"Patient/MBR-UC05"},` +
			`"input":[{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp","code":"data-query"}]},"valueString":"` + query + `"}]}`)
	}
	for _, tc := range []struct{ name, query string }{
		{"A1_disallowed_type", "Observation?patient=Patient/MBR-UC05&date=ge2024-01-01&date=le2026-12-31"},
		{"A2_smuggled_type_param", "DiagnosticReport?patient=Patient/MBR-UC05&_type=DiagnosticReport,Observation&date=ge2024-01-01&date=le2026-12-31"}, // caught as a disallowed param (_type not in allowedQueryParams)
		{"A3_unbounded_no_date", "DiagnosticReport?patient=Patient/MBR-UC05"},
		{"A4_missing_upper_bound", "DiagnosticReport?patient=Patient/MBR-UC05&date=ge2024-01-01"},
		{"A5_inverted_range", "DiagnosticReport?patient=Patient/MBR-UC05&date=ge2026-12-31&date=le2024-01-01"},
		{"A5b_impossible_date", "DiagnosticReport?patient=Patient/MBR-UC05&date=ge2024-13-45&date=le2026-12-31"},
		{"A6_everything_op", "Patient/MBR-UC05/$everything"},
		{"A6b_export", "DiagnosticReport?patient=Patient/MBR-UC05&_type=DiagnosticReport&date=ge2024-01-01&date=le2026-12-31&$export=true"},
		{"A7_count", "DiagnosticReport?patient=Patient/MBR-UC05&_count=100000&date=ge2024-01-01&date=le2026-12-31"},
		{"A7b_include", "DiagnosticReport?patient=Patient/MBR-UC05&_include=DiagnosticReport:result&date=ge2024-01-01&date=le2026-12-31"},
		{"A7c_chained", "DiagnosticReport?patient.name=Smith&date=ge2024-01-01&date=le2026-12-31"},
		{"A8_encoded_type", "Diagnostic%52eport?patient=Patient/MBR-UC05&date=ge2024-01-01&date=le2026-12-31"},
		{"A9_wrong_patient", "DiagnosticReport?patient=Patient/EVIL&date=ge2024-01-01&date=le2026-12-31"},
		{"A14_dup_lower_bound_widens", "DiagnosticReport?patient=Patient/MBR-UC05&date=ge2024-01-01&date=ge2000-01-01&date=le2026-12-31"},
		{"A14b_dup_upper_bound_widens", "DiagnosticReport?patient=Patient/MBR-UC05&date=ge2024-01-01&date=le2026-12-31&date=le2099-12-31"},
		{"A14c_dup_patient", "DiagnosticReport?patient=Patient/MBR-UC05&patient=Patient/EVIL&date=ge2024-01-01&date=le2026-12-31"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := shnsdk.ParseCDexTaskDataRequest(mk(tc.query)); err == nil {
				t.Fatalf("%s: expected rejection, got nil error", tc.name)
			}
		})
	}

	// A10: zero data-query inputs.
	noInput := []byte(`{"resourceType":"Task","code":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-cdex/CodeSystem/cdex-temp","code":"data-request-query"}]},"for":{"reference":"Patient/MBR-UC05"},"input":[]}`)
	if _, err := shnsdk.ParseCDexTaskDataRequest(noInput); err == nil {
		t.Fatal("A10: expected rejection for zero data-query inputs")
	}
	// A11: Task.for != data-query patient.
	mismatch := []byte(strings.Replace(string(validCDexTask(t)), `"for":{"reference":"Patient/MBR-UC05"}`, `"for":{"reference":"Patient/OTHER"}`, 1))
	if _, err := shnsdk.ParseCDexTaskDataRequest(mismatch); err == nil {
		t.Fatal("A11: expected rejection for Task.for != data-query patient")
	}
	// A12: wrong Task.code (data-request-questionnaire instead of data-request-query).
	// Note: json.Marshal sorts map keys alphabetically so "code" precedes "system" in the coding
	// object, producing `"code":"data-request-query"` in the output — verified against actual build.
	wrongCode := []byte(strings.Replace(string(validCDexTask(t)), `"code":"data-request-query"`, `"code":"data-request-questionnaire"`, 1))
	if _, err := shnsdk.ParseCDexTaskDataRequest(wrongCode); err == nil {
		t.Fatal("A12: expected rejection for non-query Task.code")
	}
	// A13: cdex-9 — a Task with TWO data-query inputs must be rejected.
	// FR-24 two named types ⇒ two Tasks/legs (one data-query per Task, not two in one Task).
	// JSON-surgery: prepend a second data-query input to the valid single-type Task.
	twoInput := []byte(strings.Replace(string(validCDexTask(t)),
		`"input":[`,
		`"input":[{"type":{"coding":[{"system":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp","code":"data-query"}]},"valueString":"DocumentReference?patient=Patient/MBR-UC05&date=ge2024-01-01&date=le2026-12-31"},`, 1))
	if _, err := shnsdk.ParseCDexTaskDataRequest(twoInput); err == nil {
		t.Fatal("cdex-9: expected rejection for two data-query inputs")
	}
}

func TestCDexQueryResultRoundTrip(t *testing.T) {
	dr := []byte(`{"resourceType":"DiagnosticReport","id":"dr-uc05","status":"final","subject":{"reference":"Patient/MBR-UC05"}}`)
	prov := []byte(`{"resourceType":"Provenance","id":"p","target":[{"reference":"DiagnosticReport/dr-uc05"}]}`)
	inner, _ := shnsdk.BuildRecordsBundle([][]byte{dr, prov})
	req, err := shnsdk.BuildCDexTaskDataRequest("Patient/MBR-UC05", "DiagnosticReport", "2024-01-01", "2026-12-31", testCDexMeta)
	if err != nil {
		t.Fatal(err)
	}
	task, err := shnsdk.BuildCDexQueryResult(req, inner)
	if err != nil {
		t.Fatal(err)
	}
	// the completed Task must retain the request's input (cdex-task-data-request min=1) + status completed
	var got map[string]any
	if err := json.Unmarshal(task, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "completed" {
		t.Fatalf("status = %v, want completed", got["status"])
	}
	if in, _ := got["input"].([]any); len(in) == 0 {
		t.Fatal("completed Task lost its input (cdex-task-data-request requires input min=1)")
	}
	gotDR, gotProv, err := shnsdk.ExtractCDexEvidence(task)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var drOut, provOut map[string]any
	if err := json.Unmarshal(gotDR, &drOut); err != nil {
		t.Fatalf("gotDR not JSON: %v", err)
	}
	if err := json.Unmarshal(gotProv, &provOut); err != nil {
		t.Fatalf("gotProv not JSON: %v", err)
	}
	if drOut["resourceType"] != "DiagnosticReport" || drOut["id"] != "dr-uc05" {
		t.Fatalf("round-trip wrong DR: %v", drOut)
	}
	if provOut["resourceType"] != "Provenance" {
		t.Fatalf("round-trip wrong Provenance: %v", provOut)
	}
}

func TestBuildCDexTaskDataRequest_RequiresMeta(t *testing.T) {
	if _, err := shnsdk.BuildCDexTaskDataRequest("Patient/MBR-UC05", "DiagnosticReport", "2024-01-01", "2026-12-31", shnsdk.CDexTaskMeta{}); err == nil {
		t.Fatal("expected error for zero-value CDexTaskMeta")
	}
}
