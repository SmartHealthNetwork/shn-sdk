package shnsdk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPASDecisionRequiresRecognizedEvidence(t *testing.T) {
	approved, err := BuildClaimResponse("AUTH-1", "2030-01-01", "Patient/test", "corr", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	denied, err := BuildDeniedResponseAtLine("2.0", "Patient/test", "corr", "Payer denial", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name string
		body []byte
		want string
	}{
		{"approved", approved, "approved"}, {"denied", denied, "denied"},
		{"queued", []byte(`{"resourceType":"ClaimResponse","outcome":"queued"}`), "pended"},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, err := parsePASClaimDecision(row.body)
			if err != nil || got.Outcome != row.want {
				t.Fatalf("outcome=%s err=%v", got.Outcome, err)
			}
		})
	}
	for _, row := range []struct{ name, from, to string }{
		{"unknown code", `"code":"A1"`, `"code":"ZZ"`},
		{"wrong system", X12ReviewDecisionSystem, "urn:unrelated:review"},
		{"missing decision", `"code":"A1"`, `"display":"Certified"`},
		{"unknown alongside approval", `"code":"A1"`, `"code":"A1"},{"system":"https://codesystem.x12.org/005010/306","code":"ZZ"`},
		{"contradictory pending", `"code":"A1"`, `"code":"A1"},{"system":"https://codesystem.x12.org/005010/306","code":"A4"`},
	} {
		t.Run(row.name, func(t *testing.T) {
			raw := bytes.ReplaceAll(approved, []byte(row.from), []byte(row.to))
			if bytes.Equal(raw, approved) {
				t.Fatal("mutation changed nothing")
			}
			if got, err := parsePASClaimDecision(raw); err == nil {
				t.Fatalf("unsupported evidence consumed as %s", got.Outcome)
			}
		})
	}
	for _, missing := range []string{"concept", "coding"} {
		t.Run("missing "+missing, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(approved, &doc); err != nil {
				t.Fatal(err)
			}
			mutated := false
			var visit func(any)
			visit = func(v any) {
				switch x := v.(type) {
				case map[string]any:
					if x["url"] == reviewActionCodeExtURL {
						mutated = true
						if missing == "concept" {
							delete(x, "valueCodeableConcept")
						} else {
							x["valueCodeableConcept"] = map[string]any{"coding": []any{}}
						}
					}
					for _, child := range x {
						visit(child)
					}
				case []any:
					for _, child := range x {
						visit(child)
					}
				}
			}
			visit(doc)
			if !mutated {
				t.Fatal("mutation changed nothing")
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := parsePASClaimDecision(raw); err == nil {
				t.Fatalf("missing evidence consumed as %s", got.Outcome)
			}
		})
	}
	t.Run("number alone is not approval", func(t *testing.T) {
		if got, err := parsePASClaimDecision([]byte(`{"resourceType":"ClaimResponse","outcome":"complete","preAuthRef":"AUTH-1"}`)); err == nil {
			t.Fatalf("number became %s", got.Outcome)
		}
	})
	t.Run("supported partial unchanged", func(t *testing.T) {
		raw := strings.ReplaceAll(string(approved), `"code":"A1"`, `"code":"A2"`)
		raw = strings.Replace(raw, `"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"`, `"extension":[{"url":"number","valueString":"AUTH-1"},{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode"`, 1)
		got, err := parsePASClaimDecision([]byte(raw))
		if err != nil || got.Outcome != "approved" || !got.Partial {
			t.Fatalf("partial=%+v err=%v", got, err)
		}
	})
}
