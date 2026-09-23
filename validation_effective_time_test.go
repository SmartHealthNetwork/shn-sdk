package shnsdk_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// The full four-warning response was captured from one real profile request.
// Every edit to it below is an isolated synthetic control, not a server receipt.
func TestOperationValidatorExactEffectiveTimeAdvisory(t *testing.T) {
	request, err := os.ReadFile("testdata/validation/hapi-observation-effective-request.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/validation/hapi-observation-effective-response.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(request)) != "6441e7ee60146f5673437165d2d29bd066c2e42fdef33a3dfd433aa4f6deb0e4" || fmt.Sprintf("%x", sha256.Sum256(raw)) != "e2a2e08203b03e219554b2e0f4c04e5ca4784b8cef4aad7e76d42f3b3ee79832" {
		t.Fatal("captured request or response changed")
	}
	const id = "All_observations_should_have_an_effectiveDateTime_or_an_effectivePeriod"
	const system = "http://hl7.org/fhir/java-core-messageId"
	const profile = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-observation-clinical-result"
	entry := func(s, c string) any { return map[string]any{"system": s, "code": c} }
	isolated := func(edit func(map[string]any)) string {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		issue := doc["issue"].([]any)[3].(map[string]any)
		doc["issue"] = []any{issue}
		if edit != nil {
			edit(issue)
		}
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
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
	problem := func(code, messageID string) any {
		return map[string]any{"severity": "error", "code": code, "details": map[string]any{"coding": []any{entry(system, messageID)}}, "diagnostics": "PRIVATE-SENTINEL"}
	}
	advice := shnsdk.ValidationIssue{Severity: "warning", Code: "observation-effective-time-advisory"}
	type row struct {
		name, body, profileCode string
		status                  int
		state                   shnsdk.ValidationState
		issues                  []shnsdk.ValidationIssue
		execution               bool
		legacyErrors            int
	}
	rows := []row{
		{"captured four warnings", string(raw), "profile-completed", 200, shnsdk.ValidationValid, []shnsdk.ValidationIssue{{Severity: "warning", Code: "extensible-binding-advisory"}, {Severity: "warning", Code: "narrative-advisory"}, {Severity: "warning", Code: "observation-performer-advisory"}, advice}, false, 0},
		{"synthetic isolated effective time", isolated(nil), "profile-completed", 200, shnsdk.ValidationValid, []shnsdk.ValidationIssue{advice}, false, 0},
		{"synthetic content error", mixed(problem("processing", "Extension_EXT_Type")), "profile-invalid", 200, shnsdk.ValidationInvalid, []shnsdk.ValidationIssue{advice, {Severity: "error", Code: "extension-type"}}, false, 1},
		{"synthetic unknown profile", mixed(problem("processing", "Validation_VAL_Profile_Unknown")), "profile-support-unproven", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{advice, {Severity: "error", Code: "profile-unsupported"}}, false, 0},
		{"synthetic slicing failure", mixed(problem("processing", "SLICING_CANNOT_BE_EVALUATED")), "execution-unavailable", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{advice, {Severity: "error", Code: "slicing-unavailable"}}, true, 0},
		{"synthetic generic terminology", mixed(problem("processing", "Terminology_PassThrough_TX_Message")), "profile-support-unproven", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{advice, {Severity: "error", Code: "unmapped"}}, false, 0},
		{"synthetic combined precedence", mixed(problem("processing", "Extension_EXT_Type"), problem("processing", "Validation_VAL_Profile_Unknown"), problem("processing", "SLICING_CANNOT_BE_EVALUATED")), "execution-unavailable", 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{advice, {Severity: "error", Code: "extension-type"}, {Severity: "error", Code: "profile-unsupported"}, {Severity: "error", Code: "slicing-unavailable"}}, true, 0},
		{"synthetic HTTP 400 warning", isolated(nil), "execution-unavailable", 400, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{advice}, true, 0},
		{"synthetic HTTP 422 warning", isolated(nil), "execution-unavailable", 422, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{advice}, true, 0},
		{"synthetic HTTP 401", isolated(nil), "execution-unavailable", 401, shnsdk.ValidationUnavailable, nil, true, 0},
		{"synthetic HTTP 429", isolated(nil), "execution-unavailable", 429, shnsdk.ValidationUnavailable, nil, true, 0},
		{"synthetic HTTP 500", isolated(nil), "execution-unavailable", 500, shnsdk.ValidationUnavailable, nil, true, 0},
	}
	for _, near := range []struct {
		name                        string
		edit                        func(map[string]any)
		severity, code, profileCode string
		execution                   bool
	}{
		{"error severity", func(i map[string]any) { i["severity"] = "error" }, "error", "unmapped", "profile-support-unproven", false},
		{"fatal severity", func(i map[string]any) { i["severity"] = "fatal" }, "fatal", "unmapped", "profile-support-unproven", false},
		{"information severity", func(i map[string]any) { i["severity"] = "information" }, "information", "unmapped", "profile-support-unproven", false},
		{"wrong category", func(i map[string]any) { i["code"] = "invariant" }, "warning", "unmapped", "profile-support-unproven", false},
		{"case category", func(i map[string]any) { i["code"] = "PROCESSING" }, "warning", "unmapped", "profile-support-unproven", false},
		{"wrong ID case", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, strings.ToLower(id))}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"subject neighbor", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "All_observations_should_have_a_subject")}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"made-up adjacent ID", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, "All_observations_should_have_an_effective_time")}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"foreign system", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry("https://example.org/untrusted", id)}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"empty identity", func(i map[string]any) { i["details"] = map[string]any{"coding": []any{entry(system, "")}} }, "warning", "unmapped", "profile-support-unproven", false},
		{"extra foreign coding", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry("https://example.org/untrusted", id)}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"duplicate coding", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry(system, id)}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"conflicting coding", func(i map[string]any) {
			i["details"] = map[string]any{"coding": []any{entry(system, id), entry(system, "Validation_VAL_Profile_Unknown")}}
		}, "warning", "unmapped", "profile-support-unproven", false},
		{"extension only", func(i map[string]any) { delete(i, "details") }, "warning", "processing", "execution-unavailable", true},
	} {
		rows = append(rows, row{near.name, isolated(near.edit), near.profileCode, 200, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: near.severity, Code: near.code}}, near.execution, 0})
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/Observation/$validate" || r.URL.Query().Get("profile") != profile {
					t.Errorf("request target: %s %s", r.Method, r.URL.String())
				}
				got, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(got, request) {
					t.Errorf("request bytes changed: err=%v", err)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			v := shnsdk.NewOperationValidator(srv.URL)
			ev, err := v.ValidateEvidence(context.Background(), request, profile)
			if calls != 1 || !ev.ExecutionAttempted || (err != nil) != tc.execution || ev.Profile.State != tc.state || ev.Profile.Code != tc.profileCode || !reflect.DeepEqual(ev.Profile.Issues, tc.issues) || ev.Terminology.State != shnsdk.ValidationUnavailable || ev.Terminology.Code != "terminology-support-unproven" {
				t.Fatalf("calls=%d evidence=%+v err=%v", calls, ev, err)
			}
			if strings.Contains(fmt.Sprint(ev, err), "PRIVATE-SENTINEL") {
				t.Fatal("raw diagnostic leaked")
			}
			legacy, legacyErr := v.Validate(context.Background(), request, profile)
			if calls != 2 || (legacyErr != nil) != (tc.state == shnsdk.ValidationUnavailable) || legacy.Valid != (tc.state == shnsdk.ValidationValid) || len(legacy.Issues) != tc.legacyErrors {
				t.Fatalf("calls=%d legacy=%+v err=%v", calls, legacy, legacyErr)
			}
			for _, msg := range legacy.Issues {
				if msg != "PRIVATE-SENTINEL" && tc.legacyErrors != 0 {
					t.Fatalf("legacy error diagnostic lost: %q", msg)
				}
			}
		})
	}
}

func TestHTTPValidatorEffectiveTimeAdvisoryRemainsUnqualified(t *testing.T) {
	const raw = `{"outcomes":[{"issues":[{"level":"WARNING","type":"PROCESSING","messageId":"All_observations_should_have_an_effectiveDateTime_or_an_effectivePeriod"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, raw) }))
	defer srv.Close()
	v := shnsdk.NewHTTPValidator(srv.URL)
	ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Observation"}`), "")
	if err != nil || !ev.ExecutionAttempted || ev.Profile.State != shnsdk.ValidationUnavailable || ev.Profile.Code != "profile-support-unproven" || !reflect.DeepEqual(ev.Profile.Issues, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}}) || ev.Terminology.State != shnsdk.ValidationUnavailable {
		t.Fatalf("unqualified wrapper evidence=%+v err=%v", ev, err)
	}
}
