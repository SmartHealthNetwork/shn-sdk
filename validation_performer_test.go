package shnsdk_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// The full captured synthetic response remains a witness of unavailable
// terminology. All isolated performer cases below are explicitly synthetic.
func TestOperationValidatorExactPerformerAdvisory(t *testing.T) {
	raw, err := os.ReadFile("testdata/validation/hapi-observation-first-response.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "1f5d8adbc2f080168aef828a75163c537e2c20ded52e48fea4cea24d28d6b6f2" {
		t.Fatal("captured response changed")
	}
	const id = "All_observations_should_have_a_performer"
	const system = "http://hl7.org/fhir/java-core-messageId"
	entry := func(s, c string) any { return map[string]any{"system": s, "code": c} }
	isolated := func(edit func(map[string]any)) string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		performer := doc["issue"].([]any)[3].(map[string]any)
		doc["issue"] = []any{performer}
		if edit != nil {
			edit(performer)
		}
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mutate := func(edit func(map[string]any)) string { return isolated(edit) }
	// This derivative removes the actual response's unqualified terminology
	// warning only as a synthetic positive control; it is not a server reply.
	combinedAdvisories := func() string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc["issue"] = doc["issue"].([]any)[1:]
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	code := func(s, c string) string {
		return mutate(func(i map[string]any) { i["details"] = map[string]any{"coding": []any{entry(s, c)}} })
	}
	mixed := func(extra ...any) string {
		var doc map[string]any
		if err := json.Unmarshal([]byte(isolated(nil)), &doc); err != nil {
			t.Fatal(err)
		}
		doc["issue"] = append(doc["issue"].([]any), extra...)
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	issue := func(messageID string) any {
		return map[string]any{"severity": "error", "code": "processing", "details": map[string]any{"coding": []any{entry(system, messageID)}}, "diagnostics": "PRIVATE-SENTINEL"}
	}
	performer := shnsdk.ValidationIssue{Severity: "warning", Code: "observation-performer-advisory"}
	type row struct {
		name, body, profileCode string
		status                  int
		state                   shnsdk.ValidationState
		issues                  []shnsdk.ValidationIssue
		execution               bool
	}
	rows := []row{
		{"full captured response keeps unknown CodeSystem unavailable", string(raw), "profile-support-unproven", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, {Severity: "warning", Code: "extensible-binding-advisory"}, {Severity: "warning", Code: "narrative-advisory"}, performer}, false},
		{"synthetic isolated performer", isolated(nil), "profile-completed", 200, shnsdk.ValidationValid, []shnsdk.ValidationIssue{performer}, false},
		{"synthetic combined closed advisories", combinedAdvisories(), "profile-completed", 200, shnsdk.ValidationValid, []shnsdk.ValidationIssue{{Severity: "warning", Code: "extensible-binding-advisory"}, {Severity: "warning", Code: "narrative-advisory"}, performer}, false},
		{"synthetic diagnostics ignored", mutate(func(i map[string]any) { i["diagnostics"] = "PRIVATE-SENTINEL" }), "profile-completed", 200, shnsdk.ValidationValid, []shnsdk.ValidationIssue{performer}, false},
		{"synthetic content invalid", mixed(issue("Extension_EXT_Type")), "profile-invalid", 200, shnsdk.ValidationInvalid, []shnsdk.ValidationIssue{performer, {Severity: "error", Code: "extension-type"}}, false},
		{"synthetic unsupported", mixed(issue("Validation_VAL_Profile_Unknown")), "profile-support-unproven", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{performer, {Severity: "error", Code: "profile-unsupported"}}, false},
		{"synthetic execution unavailable", mixed(issue("SLICING_CANNOT_BE_EVALUATED")), "execution-unavailable", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{performer, {Severity: "error", Code: "slicing-unavailable"}}, true},
		{"synthetic mixed precedence", mixed(issue("Extension_EXT_Type"), issue("Validation_VAL_Profile_Unknown"), issue("SLICING_CANNOT_BE_EVALUATED")), "execution-unavailable", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{performer, {Severity: "error", Code: "extension-type"}, {Severity: "error", Code: "profile-unsupported"}, {Severity: "error", Code: "slicing-unavailable"}}, true},
	}
	for _, near := range []struct {
		name, body, severity, profileCode string
		execution                         bool
	}{
		{"wrong system", code("https://example.org/untrusted", id), "warning", "profile-support-unproven", false},
		{"empty identity", code(system, ""), "warning", "profile-support-unproven", false},
		{"case changed", code(system, "all_observations_should_have_a_performer"), "warning", "profile-support-unproven", false},
		{"subject neighbor", code(system, "All_observations_should_have_a_subject"), "warning", "profile-support-unproven", false},
		{"time neighbor", code(system, "All_observations_should_have_an_effective_time"), "warning", "profile-support-unproven", false},
		{"duplicate codings", mutate(func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry(system, id)}}
		}), "warning", "profile-support-unproven", false},
		{"conflicting codings", mutate(func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry(system, "Validation_VAL_Profile_Unknown")}}
		}), "warning", "profile-support-unproven", false},
		{"extra foreign coding", mutate(func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry("https://example.org/untrusted", id)}}
		}), "warning", "profile-support-unproven", false},
		{"wrong category", mutate(func(i map[string]any) { i["code"] = "exception" }), "warning", "profile-support-unproven", false},
		{"case changed category", mutate(func(i map[string]any) { i["code"] = "PROCESSING" }), "warning", "profile-support-unproven", false},
		{"information severity", mutate(func(i map[string]any) { i["severity"] = "information" }), "information", "profile-support-unproven", false},
		{"error severity", mutate(func(i map[string]any) { i["severity"] = "error" }), "error", "profile-support-unproven", false},
		{"fatal severity", mutate(func(i map[string]any) { i["severity"] = "fatal" }), "fatal", "profile-support-unproven", false},
		{"extension only", mutate(func(i map[string]any) { delete(i, "details") }), "warning", "execution-unavailable", true},
	} {
		wantCode := "unmapped"
		if near.name == "extension only" {
			wantCode = "processing"
		}
		rows = append(rows, row{near.name, near.body, near.profileCode, 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: near.severity, Code: wantCode}}, near.execution})
	}
	for _, status := range []int{400, 422} {
		rows = append(rows, row{fmt.Sprintf("HTTP %d warning", status), isolated(nil), "execution-unavailable", status, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{performer}, true})
	}
	for _, status := range []int{401, 429, 500} {
		rows = append(rows, row{fmt.Sprintf("HTTP %d warning", status), isolated(nil), "execution-unavailable", status, shnsdk.ValidationUnavailable, nil, true})
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			v := shnsdk.NewOperationValidator(srv.URL)
			ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Observation"}`), "")
			if !ev.ExecutionAttempted || (err != nil) != tc.execution || ev.Profile.State != tc.state || ev.Profile.Code != tc.profileCode || !reflect.DeepEqual(ev.Profile.Issues, tc.issues) || ev.Terminology.State != shnsdk.ValidationUnavailable || ev.Terminology.Code != "terminology-support-unproven" {
				t.Fatalf("evidence=%+v err=%v", ev, err)
			}
			if strings.Contains(fmt.Sprint(ev, err), "PRIVATE-SENTINEL") {
				t.Fatal("private diagnostics leaked")
			}
			legacy, legacyErr := v.Validate(context.Background(), []byte(`{"resourceType":"Observation"}`), "")
			if (legacyErr != nil) != (tc.state == shnsdk.ValidationUnavailable) || legacy.Valid != (tc.state == shnsdk.ValidationValid) {
				t.Fatalf("legacy=%+v err=%v", legacy, legacyErr)
			}
			if tc.state == shnsdk.ValidationValid && len(legacy.Issues) != 0 {
				t.Fatalf("completed advisory has legacy error issues: %+v", legacy)
			}
			if tc.state == shnsdk.ValidationInvalid && (len(legacy.Issues) != 1 || legacy.Issues[0] != "PRIVATE-SENTINEL") {
				t.Fatalf("lost content error: %+v", legacy)
			}
		})
	}
}

func TestHTTPValidatorPerformerIDRemainsUnqualified(t *testing.T) {
	const raw = `{"outcomes":[{"issues":[{"level":"WARNING","type":"PROCESSING","messageId":"All_observations_should_have_a_performer"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) }))
	defer srv.Close()
	v := shnsdk.NewHTTPValidator(srv.URL)
	ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Observation"}`), "")
	if err != nil || !ev.ExecutionAttempted || ev.Profile.State != shnsdk.ValidationUnavailable || ev.Profile.Code != "profile-support-unproven" || !reflect.DeepEqual(ev.Profile.Issues, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}}) || ev.Terminology.State != shnsdk.ValidationUnavailable {
		t.Fatalf("unqualified CLI evidence=%+v err=%v", ev, err)
	}
	if _, err := v.Validate(context.Background(), []byte(`{"resourceType":"Observation"}`), ""); err == nil {
		t.Fatal("unqualified CLI response became legacy success")
	}
}
