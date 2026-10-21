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

// cdexTaskNaming is validCDexTask naming its patient by forRef in Task.for and
// by queryRef in its query, with contained, when set, as its contained
// resources.
func cdexTaskNaming(t *testing.T, forRef, queryRef, contained string) string {
	t.Helper()
	task := string(validCDexTask(t))
	for old, repl := range map[string]string{
		`"for":{"reference":"Patient/MBR-UC05"}`: `"for":{"reference":` + quoteJSON(forRef) + `}`,
		`patient=Patient/MBR-UC05`:               `patient=` + queryRef,
	} {
		if strings.Count(task, old) != 1 {
			t.Fatalf("the built Task does not carry %s once: %s", old, task)
		}
		task = strings.Replace(task, old, repl, 1)
	}
	if contained != "" {
		task = strings.TrimSuffix(task, "}") + `,"contained":` + contained + `}`
	}
	return task
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestParseCDex_PatientReferenceForms pins the patient a federated query's
// Task names in each form: a well-formed relative, absolute, versioned or
// contained reference is read as "Patient/<id>" in Task.for and in the query,
// so the two compare as one patient; a reference that is not well-formed, or
// any reference in a Task that reads two ways, is read as written.
func TestParseCDex_PatientReferenceForms(t *testing.T) {
	const abs = "https://provider.example/fhir/Patient/MBR-UC05"
	pat := func(id, value string) string {
		return `{"resourceType":"Patient","id":"` + id + `","identifier":[{"system":"urn:shn:member","value":"` + value + `"}]}`
	}
	reads := map[string]string{
		"relative":                         cdexTaskNaming(t, "Patient/MBR-UC05", "Patient/MBR-UC05", ""),
		"absolute":                         cdexTaskNaming(t, abs, abs, ""),
		"versioned":                        cdexTaskNaming(t, "Patient/MBR-UC05/_history/2", "Patient/MBR-UC05/_history/2", ""),
		"versioned absolute, relative":     cdexTaskNaming(t, abs+"/_history/2", "Patient/MBR-UC05", ""),
		"relative, absolute":               cdexTaskNaming(t, "Patient/MBR-UC05", abs, ""),
		"contained":                        cdexTaskNaming(t, "#pat", "#pat", `[`+pat("pat", "MBR-UC05")+`]`),
		"contained, relative":              cdexTaskNaming(t, "#pat", "Patient/MBR-UC05", `[`+pat("pat", "MBR-UC05")+`]`),
		"contained beside others":          cdexTaskNaming(t, "#pat", "#pat", `[{"resourceType":"Organization","id":"o"},"x",`+pat("pat", "MBR-UC05")+`]`),
		"a member name not read, folded":   strings.Replace(cdexTaskNaming(t, abs, abs, ""), `"status":`, `"Status":`, 1),
		"a contained resource name folded": cdexTaskNaming(t, "#pat", "#pat", `[{"resourceType":"Patient","id":"pat","Text":{},"identifier":[{"system":"urn:shn:member","value":"MBR-UC05"}]}]`),
		// A Task that reads two ways is read as written: its relative
		// references still name the patient.
		"read two ways, relative": strings.Replace(cdexTaskNaming(t, "Patient/MBR-UC05", "Patient/MBR-UC05", ""), `"for":`, `"for":{"reference":"Patient/MBR-OTHER"},"for":`, 1),
	}
	for name, task := range reads {
		got, err := shnsdk.ParseCDexTaskDataRequest([]byte(task))
		if err != nil || got.PatientRef != "Patient/MBR-UC05" || len(got.Queries) != 1 || got.Queries[0].PatientRef != "Patient/MBR-UC05" {
			t.Errorf("%s: read (%+v, %v), want a query for Patient/MBR-UC05", name, got, err)
		}
	}
	base := cdexTaskNaming(t, abs, abs, "")
	refused := map[string]string{
		"operation after the id":                                       cdexTaskNaming(t, "Patient/MBR-UC05/$everything", "Patient/MBR-UC05/$everything", ""),
		"escaped tail":                                                 cdexTaskNaming(t, "Patient/MBR-UC05", "Patient/MBR-UC05/%zz", ""),
		"escaped absolute query":                                       cdexTaskNaming(t, abs, "https%3A%2F%2Fprovider.example%2Ffhir%2FPatient%2FMBR-UC05", ""),
		"smuggled date bound":                                          cdexTaskNaming(t, "Patient/MBR-UC05", abs+"/x%26date%3Dge1900-01-01", ""),
		"contained member, not an id":                                  cdexTaskNaming(t, "#pat", "#pat", `[`+pat("pat", "A/x")+`]`),
		"contained id not carried":                                     cdexTaskNaming(t, "#nobody", "Patient/MBR-UC05", `[`+pat("pat", "MBR-UC05")+`]`),
		"contained twice":                                              cdexTaskNaming(t, "#pat", "Patient/MBR-UC05", `[`+pat("pat", "MBR-UC05")+`,`+pat("pat", "MBR-UC05")+`]`),
		"contained resources not a list":                               cdexTaskNaming(t, "#pat", "#pat", pat("pat", "MBR-UC05")),
		"another patient in the query":                                 cdexTaskNaming(t, abs, "https://provider.example/fhir/Patient/MBR-OTHER", ""),
		"an id past 64 characters":                                     cdexTaskNaming(t, "https://x/Patient/"+strings.Repeat("a", 65), "https://x/Patient/"+strings.Repeat("a", 65), ""),
		"read two ways: a repeated for":                                strings.Replace(base, `"for":`, `"for":{"reference":"Patient/MBR-UC05"},"for":`, 1),
		"read two ways: a folded for":                                  strings.Replace(base, `"for":`, `"For":`, 1),
		"read two ways: a folded query":                                strings.Replace(base, `"valueString":"DiagnosticReport`, `"valueſtring":"DiagnosticReport`, 1),
		"read two ways: a folded coding":                               strings.Replace(base, `"system":`, `"System":`, 1),
		"read two ways: folded contained":                              strings.Replace(cdexTaskNaming(t, "#pat", "#pat", `[`+pat("pat", "MBR-UC05")+`]`), `"contained":`, `"Contained":`, 1),
		"read two ways: the type in another case":                      strings.Replace(base, `"resourceType":"Task"`, `"ResourceType":"Task"`, 1),
		"read two ways: code in another case":                          strings.Replace(base, `"code":{"coding":`, `"Code":{"coding":`, 1),
		"read two ways: code's coding in another case":                 strings.Replace(base, `"code":{"coding":`, `"code":{"Coding":`, 1),
		"read two ways: code's coding code in another case":            strings.Replace(base, `{"code":"data-request-query"`, `{"Code":"data-request-query"`, 1),
		"read two ways: the reference in another case":                 strings.Replace(base, `"for":{"reference":`, `"for":{"Reference":`, 1),
		"read two ways: input in another case":                         strings.Replace(base, `"input":[`, `"Input":[`, 1),
		"read two ways: an input type in another case":                 strings.Replace(base, `{"type":{"coding":[{"code":"data-query"`, `{"Type":{"coding":[{"code":"data-query"`, 1),
		"read two ways: an input type's coding in another case":        strings.Replace(base, `{"type":{"coding":[{"code":"data-query"`, `{"type":{"Coding":[{"code":"data-query"`, 1),
		"read two ways: an input type's coding code in another case":   strings.Replace(base, `{"code":"data-query"`, `{"Code":"data-query"`, 1),
		"read two ways: an input type's coding system in another case": strings.Replace(base, `"system":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp"`, `"System":"http://hl7.org/fhir/us/davinci-hrex/CodeSystem/hrex-temp"`, 1),
		"read two ways: the query under its name in another case":      strings.Replace(base, `"valueString":"DiagnosticReport`, `"ValueString":"DiagnosticReport`, 1),
		"read two ways: a repeat elsewhere":                            strings.TrimSuffix(base, "}") + `,"note":[{"a":1,"A":2}]}`,
		"past the strict reader's bounds":                              strings.TrimSuffix(base, "}") + `,"note":"\ud800"}`,
	}
	for name, task := range refused {
		if task == base {
			t.Fatalf("%s: the fixture did not change the Task", name)
		}
		got, err := shnsdk.ParseCDexTaskDataRequest([]byte(task))
		if err == nil {
			t.Errorf("%s: read %+v, want refused", name, got)
		} else if strings.HasPrefix(err.Error(), "cdex: parse:") {
			t.Errorf("%s: the fixture is not JSON: %v", name, err)
		}
	}
}
