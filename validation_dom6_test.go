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

// This captured synthetic response and the independent definition qualification
// are documented in validation_evidence_mapping.md. Near-matches must not acquire
// support merely because the diagnostic or ignored extension still names dom-6.
func TestOperationValidatorEvidencePinnedDom6(t *testing.T) {
	raw, err := os.ReadFile("testdata/validation/hapi-dom6-warning.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "f1d5187bafe37f28125f7b3248cf84bfadfb6d14608de9da57cbe5f12d98385e" {
		t.Fatal("captured response changed")
	}
	const id = "http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6"
	const system = "http://hl7.org/fhir/java-core-messageId"
	mutate := func(f func(map[string]any)) string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		f(doc["issue"].([]any)[0].(map[string]any))
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	coding := func(entries ...any) string {
		return mutate(func(i map[string]any) { i["details"] = map[string]any{"coding": entries} })
	}
	entry := func(s, c string) any { return map[string]any{"system": s, "code": c} }
	field := func(k, v string) string { return mutate(func(i map[string]any) { i[k] = v }) }
	mixed := func(severity, messageID string) string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc["issue"] = append(doc["issue"].([]any), map[string]any{"severity": severity, "code": "processing", "details": map[string]any{"coding": []any{entry(system, messageID)}}, "diagnostics": "PRIVATE-SENTINEL"})
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	type row struct {
		name, body string
		status     int
		state      shnsdk.ValidationState
		code       string
		issues     []shnsdk.ValidationIssue
		execution  bool
	}
	advisory := shnsdk.ValidationIssue{Severity: "warning", Code: "narrative-advisory"}
	rows := []row{
		{"captured", string(raw), 200, shnsdk.ValidationValid, "profile-completed", []shnsdk.ValidationIssue{advisory}, false},
		{"diagnostics ignored", field("diagnostics", "PRIVATE-SENTINEL"), 200, shnsdk.ValidationValid, "profile-completed", []shnsdk.ValidationIssue{advisory}, false},
		{"unknown profile", mixed("error", "Validation_VAL_Profile_Unknown"), 200, shnsdk.ValidationUnavailable, "profile-support-unproven", []shnsdk.ValidationIssue{advisory, {Severity: "error", Code: "profile-unsupported"}}, false},
		{"content error", mixed("error", "Extension_EXT_Type"), 200, shnsdk.ValidationInvalid, "profile-invalid", []shnsdk.ValidationIssue{advisory, {Severity: "error", Code: "extension-type"}}, false},
		{"execution error", mixed("error", "SLICING_CANNOT_BE_EVALUATED"), 200, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{advisory, {Severity: "error", Code: "slicing-unavailable"}}, true},
	}
	for _, near := range []struct{ name, body, severity string }{
		{"wrong system", coding(entry("https://example.org/untrusted", id)), "warning"},
		{"different canonical", coding(entry(system, "http://hl7.org/fhir/StructureDefinition/Patient#dom-6")), "warning"},
		{"unknown invariant", coding(entry(system, "http://hl7.org/fhir/StructureDefinition/DomainResource#dom-7")), "warning"},
		{"duplicate coding", coding(entry(system, id), entry(system, id)), "warning"},
		{"conflicting coding", coding(entry(system, id), entry(system, "Validation_VAL_Profile_Unknown")), "warning"},
		{"extra foreign coding", coding(entry(system, id), entry("https://example.org/untrusted", id)), "warning"},
		{"empty id", coding(entry(system, "")), "warning"},
		{"content category", field("code", "invariant"), "warning"},
		{"exception category", field("code", "exception"), "warning"},
		{"uppercase category", field("code", "PROCESSING"), "warning"},
		{"error severity", field("severity", "error"), "error"},
		{"fatal severity", field("severity", "fatal"), "fatal"},
		{"information severity", field("severity", "information"), "information"},
	} {
		rows = append(rows, row{near.name, near.body, 200, shnsdk.ValidationUnavailable, "profile-support-unproven", []shnsdk.ValidationIssue{{Severity: near.severity, Code: "unmapped"}}, false})
	}
	rows = append(rows, row{"extension alone", mutate(func(i map[string]any) { delete(i, "details") }), 200, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{{Severity: "warning", Code: "processing"}}, true})
	for _, status := range []int{400, 422} {
		rows = append(rows, row{fmt.Sprintf("HTTP %d warning", status), string(raw), status, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{advisory}, true})
	}
	for _, status := range []int{401, 429, 500} {
		rows = append(rows, row{fmt.Sprintf("HTTP %d warning", status), string(raw), status, shnsdk.ValidationUnavailable, "execution-unavailable", nil, true})
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			v := shnsdk.NewOperationValidator(srv.URL)
			ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
			if !ev.ExecutionAttempted || (err != nil) != tc.execution || ev.Profile.State != tc.state || ev.Profile.Code != tc.code || !reflect.DeepEqual(ev.Profile.Issues, tc.issues) || ev.Terminology.State != shnsdk.ValidationUnavailable || ev.Terminology.Code != "terminology-support-unproven" {
				t.Fatalf("evidence=%+v err=%v", ev, err)
			}
			if strings.Contains(fmt.Sprint(ev, err), "PRIVATE-SENTINEL") {
				t.Fatal("diagnostics leaked")
			}
			result, legacyErr := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
			if (legacyErr != nil) != (tc.state == shnsdk.ValidationUnavailable) || result.Valid != (tc.state == shnsdk.ValidationValid) {
				t.Fatalf("legacy=%+v err=%v", result, legacyErr)
			}
			if tc.state == shnsdk.ValidationInvalid && (len(result.Issues) != 1 || result.Issues[0] != "PRIVATE-SENTINEL") {
				t.Fatalf("lost content error: %+v", result)
			}
		})
	}
}

func TestHTTPValidatorEvidenceDom6RemainsUnqualified(t *testing.T) {
	const raw = `{"outcomes":[{"issues":[{"level":"WARNING","type":"PROCESSING","messageId":"http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) }))
	defer srv.Close()
	v := shnsdk.NewHTTPValidator(srv.URL)
	ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil || !ev.ExecutionAttempted || ev.Profile.State != shnsdk.ValidationUnavailable || ev.Profile.Code != "profile-support-unproven" || len(ev.Profile.Issues) != 1 || ev.Profile.Issues[0].Code != "unmapped" || ev.Terminology.State != shnsdk.ValidationUnavailable {
		t.Fatalf("unqualified CLI evidence=%+v err=%v", ev, err)
	}
	if _, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), ""); err == nil {
		t.Fatal("unqualified CLI response became legacy success")
	}
}
