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

// TestOperationValidatorEvidencePinnedExtensibleBindingAdvisory catches any
// broadening beyond the one pinned OperationOutcome identity. The fixture is the
// complete synthetic Patient response documented in validation_evidence_mapping.md.
func TestOperationValidatorEvidencePinnedExtensibleBindingAdvisory(t *testing.T) {
	raw, err := os.ReadFile("testdata/validation/hapi-extensible-binding-warning.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "d04aee8348346db38a0fe28216a22970dff084886e6ab9778aba34b95be4b642" {
		t.Fatal("captured response changed")
	}

	const (
		id     = "Terminology_TX_NoValid_2_CC"
		system = "http://hl7.org/fhir/java-core-messageId"
	)
	entry := func(s, c string) any { return map[string]any{"system": s, "code": c} }
	mutate := func(f func([]any)) string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		issues := doc["issue"].([]any)
		f(issues)
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mutateAdvisory := func(f func(map[string]any)) string {
		return mutate(func(issues []any) { f(issues[0].(map[string]any)) })
	}
	mixed := func(messageIDs ...string) string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		issues := doc["issue"].([]any)
		for _, messageID := range messageIDs {
			issues = append(issues, map[string]any{"severity": "error", "code": "processing", "details": map[string]any{"coding": []any{entry(system, messageID)}}, "diagnostics": "PRIVATE-SENTINEL"})
		}
		doc["issue"] = issues
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	extensible := shnsdk.ValidationIssue{Severity: "warning", Code: "extensible-binding-advisory"}
	narrative := shnsdk.ValidationIssue{Severity: "warning", Code: "narrative-advisory"}
	type row struct {
		name, body string
		status     int
		state      shnsdk.ValidationState
		code       string
		issues     []shnsdk.ValidationIssue
		execution  bool
	}
	rows := []row{
		{"captured", string(raw), 200, shnsdk.ValidationValid, "profile-completed", []shnsdk.ValidationIssue{extensible, narrative}, false},
		{"diagnostics omitted", mutateAdvisory(func(i map[string]any) { delete(i, "diagnostics") }), 200, shnsdk.ValidationValid, "profile-completed", []shnsdk.ValidationIssue{extensible, narrative}, false},
		{"content error", mixed("Extension_EXT_Type"), 200, shnsdk.ValidationInvalid, "profile-invalid", []shnsdk.ValidationIssue{extensible, narrative, {Severity: "error", Code: "extension-type"}}, false},
		{"unsupported profile", mixed("Validation_VAL_Profile_Unknown"), 200, shnsdk.ValidationUnavailable, "profile-support-unproven", []shnsdk.ValidationIssue{extensible, narrative, {Severity: "error", Code: "profile-unsupported"}}, false},
		{"execution error", mixed("SLICING_CANNOT_BE_EVALUATED"), 200, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{extensible, narrative, {Severity: "error", Code: "slicing-unavailable"}}, true},
		{"mixed precedence", mixed("Extension_EXT_Type", "Validation_VAL_Profile_Unknown", "SLICING_CANNOT_BE_EVALUATED"), 200, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{extensible, narrative, {Severity: "error", Code: "extension-type"}, {Severity: "error", Code: "profile-unsupported"}, {Severity: "error", Code: "slicing-unavailable"}}, true},
	}
	for _, near := range []struct {
		name string
		edit func(map[string]any)
		sev  string
	}{
		{"required-binding neighbor", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "Terminology_TX_NoValid_1_CC")}}
		}, "warning"},
		{"unqualified no-service ID", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "TERMINOLOGY_TX_NOSVC_BOUND_EXT")}}
		}, "warning"},
		{"unqualified infrastructure ID", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "Terminology_TX_Confirm_2_CC")}}
		}, "warning"},
		{"unqualified unsupported-code-system ID", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "UNKNOWN_CODESYSTEM")}}
		}, "warning"},
		{"unqualified server-error marker", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "SERVER_ERROR")}}
		}, "warning"},
		{"unknown id", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "PRIVATE-SENTINEL")}}
		}, "warning"},
		{"wrong system", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry("https://example.org/untrusted", id)}}
		}, "warning"},
		{"multiple codings", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry(system, id)}}
		}, "warning"},
		{"wrong category", func(i map[string]any) { i["code"] = "invariant" }, "warning"},
		{"uppercase category", func(i map[string]any) { i["code"] = "PROCESSING" }, "warning"},
		{"information severity", func(i map[string]any) { i["severity"] = "information" }, "information"},
		{"error severity", func(i map[string]any) { i["severity"] = "error" }, "error"},
	} {
		rows = append(rows, row{near.name, mutateAdvisory(near.edit), 200, shnsdk.ValidationUnavailable, "profile-support-unproven", []shnsdk.ValidationIssue{{Severity: near.sev, Code: "unmapped"}, narrative}, false})
	}
	rows = append(rows, row{"missing coding", mutateAdvisory(func(i map[string]any) { i["details"] = map[string]any{} }), 200, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{{Severity: "warning", Code: "processing"}, narrative}, true})
	for _, status := range []int{400, 422} {
		rows = append(rows, row{fmt.Sprintf("HTTP %d warning", status), string(raw), status, shnsdk.ValidationUnavailable, "execution-unavailable", []shnsdk.ValidationIssue{extensible, narrative}, true})
	}
	for _, status := range []int{401, 429, 500} {
		rows = append(rows, row{fmt.Sprintf("HTTP %d warning", status), string(raw), status, shnsdk.ValidationUnavailable, "execution-unavailable", nil, true})
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			v := shnsdk.NewOperationValidator(srv.URL)
			ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
			if !ev.ExecutionAttempted || (err != nil) != tc.execution || ev.Profile.State != tc.state || ev.Profile.Code != tc.code || !reflect.DeepEqual(ev.Profile.Issues, tc.issues) || ev.Terminology.State != shnsdk.ValidationUnavailable || ev.Terminology.Code != "terminology-support-unproven" {
				t.Fatalf("evidence=%+v err=%v", ev, err)
			}
			if strings.Contains(fmt.Sprint(ev, err), "PRIVATE-SENTINEL") {
				t.Fatal("diagnostics or unknown ID leaked")
			}
			result, legacyErr := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
			if (legacyErr != nil) != (tc.state == shnsdk.ValidationUnavailable) || result.Valid != (tc.state == shnsdk.ValidationValid) {
				t.Fatalf("legacy=%+v err=%v", result, legacyErr)
			}
		})
	}
}

func TestHTTPValidatorEvidenceExtensibleBindingAdvisoryRemainsUnqualified(t *testing.T) {
	const raw = `{"outcomes":[{"issues":[{"level":"WARNING","type":"PROCESSING","messageId":"Terminology_TX_NoValid_2_CC"}]}]}`
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
