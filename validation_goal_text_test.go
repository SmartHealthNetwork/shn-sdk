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
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// The positive and missing-description responses were captured from one pinned
// 2.2 validator with an explicit US Core Goal profile. The latter is a real
// malformed source neighbor, not a fabricated OperationOutcome.
func TestOperationValidatorGoalTextAdvisory(t *testing.T) {
	request, err := os.ReadFile("testdata/validation/hapi-goal-text-request.json")
	if err != nil {
		t.Fatal(err)
	}
	positive, err := os.ReadFile("testdata/validation/hapi-goal-text-warning.json")
	if err != nil {
		t.Fatal(err)
	}
	negative, err := os.ReadFile("testdata/validation/hapi-goal-minus-description.json")
	if err != nil {
		t.Fatal(err)
	}
	negativeRequest, err := os.ReadFile("testdata/validation/hapi-goal-minus-description-request.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(request)) != "cf692c003ad6bc3f103a01170fce1d4ac9e167ea28c069a9b3a4f1fa9c573e3e" ||
		fmt.Sprintf("%x", sha256.Sum256(negativeRequest)) != "0c4143019a8d8a3651b37949323b8d64f944ea1c263340be07d8e13ea1813b9a" ||
		fmt.Sprintf("%x", sha256.Sum256(positive)) != "e38b11451d2ebdc0a69ed658bfbb4c3d313af7b569b7b26201a1bb3487c1517d" ||
		fmt.Sprintf("%x", sha256.Sum256(negative)) != "290abe944e7fdcde34e3a771b8bb17c95bb6795f2256219b8bf1e2c39b4b8db6" {
		t.Fatal("pinned Goal request or real validator outcome changed")
	}
	const profile = "http://hl7.org/fhir/us/core/StructureDefinition/us-core-goal|7.0.0"
	mutate := func(edit func(map[string]any)) []byte {
		var outcome map[string]any
		if err := json.Unmarshal(positive, &outcome); err != nil {
			t.Fatal(err)
		}
		issue := outcome["issue"].([]any)[0].(map[string]any)
		edit(issue)
		raw, err := json.Marshal(outcome)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	type row struct {
		name    string
		request []byte
		body    []byte
		state   shnsdk.ValidationState
		issues  []shnsdk.ValidationIssue
	}
	advice := shnsdk.ValidationIssue{Severity: "warning", Code: "goal-description-text-advisory"}
	narrative := shnsdk.ValidationIssue{Severity: "warning", Code: "narrative-advisory"}
	rows := []row{
		{"captured text Goal", request, positive, shnsdk.ValidationValid, []shnsdk.ValidationIssue{advice, narrative}},
		{"captured missing required description", negativeRequest, negative, shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "error", Code: "unmapped"}, narrative}},
		{"wrong expression", request, mutate(func(i map[string]any) { i["expression"] = []any{"Observation.code"} }), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
		{"missing expression", request, mutate(func(i map[string]any) { delete(i, "expression") }), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
		{"multiple expressions", request, mutate(func(i map[string]any) { i["expression"] = []any{"Goal.description", "Goal.code"} }), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
		{"error severity", request, mutate(func(i map[string]any) { i["severity"] = "error" }), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "error", Code: "unmapped"}, narrative}},
		{"wrong category", request, mutate(func(i map[string]any) { i["code"] = "invariant" }), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
		{"wrong ID", request, mutate(func(i map[string]any) {
			i["details"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"] = "Terminology_TX_Code_ValueSet_Req"
		}), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
		{"wrong system", request, mutate(func(i map[string]any) {
			i["details"].(map[string]any)["coding"].([]any)[0].(map[string]any)["system"] = "https://example.org/unknown"
		}), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
		{"duplicate coding", request, mutate(func(i map[string]any) {
			coding := i["details"].(map[string]any)["coding"].([]any)
			i["details"].(map[string]any)["coding"] = append(coding, coding[0])
		}), shnsdk.ValidationUnavailable, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}, narrative}},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Goal/$validate" || r.URL.Query().Get("profile") != profile {
					t.Errorf("wrong explicit Goal target: %s", r.URL.String())
				}
				got, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(got, tc.request) {
					t.Errorf("source request changed: %v", err)
				}
				w.Header().Set("Content-Type", "application/fhir+json")
				w.Write(tc.body)
			}))
			defer srv.Close()
			ev, err := shnsdk.NewOperationValidator(srv.URL).ValidateEvidence(context.Background(), tc.request, profile)
			if err != nil || !ev.ExecutionAttempted || ev.Profile.State != tc.state || !reflect.DeepEqual(ev.Profile.Issues, tc.issues) || ev.Terminology.State != shnsdk.ValidationUnavailable {
				t.Fatalf("evidence=%+v err=%v", ev, err)
			}
		})
	}
}

func TestHTTPValidatorGoalTextAdvisoryUnqualified(t *testing.T) {
	const raw = `{"outcomes":[{"issues":[{"level":"WARNING","type":"PROCESSING","messageId":"Terminology_TX_Code_ValueSet_Ext","expression":["Goal.description"]}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, raw) }))
	defer srv.Close()
	ev, err := shnsdk.NewHTTPValidator(srv.URL).ValidateEvidence(context.Background(), []byte(`{"resourceType":"Goal"}`), "")
	if err != nil || ev.Profile.State != shnsdk.ValidationUnavailable || !reflect.DeepEqual(ev.Profile.Issues, []shnsdk.ValidationIssue{{Severity: "warning", Code: "unmapped"}}) {
		t.Fatalf("CLI wrapper incorrectly qualified: evidence=%+v err=%v", ev, err)
	}
}
