package shnsdk

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildProvenanceWithIdentifier(t *testing.T) {
	source := ProvenanceIdentifier{System: "http://smarthealth.network/ids/holder", Value: "provider-123"}
	data, err := BuildProvenanceWithIdentifier("DiagnosticReport/report", source, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Agent []struct {
			Who struct {
				Reference  string               `json:"reference"`
				Identifier ProvenanceIdentifier `json:"identifier"`
			} `json:"who"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Agent) != 1 || p.Agent[0].Who.Reference != "" || p.Agent[0].Who.Identifier != source {
		t.Fatalf("attribution: %s", data)
	}
	for _, bad := range []ProvenanceIdentifier{{System: "https://unrecognized.example/identity", Value: "provider"}, {}, {System: "http://smarthealth.network/ids/holder"}, {Value: "provider"}, {System: " ", Value: "provider"}, {System: "http://smarthealth.network/ids/holder", Value: " "}} {
		if _, err := BuildProvenanceWithIdentifier("DiagnosticReport/report", bad, time.Unix(1, 0)); err == nil {
			t.Fatalf("incomplete source accepted: %+v", bad)
		}
	}
}

func TestUpdateEvidenceLogicalAgentGuard(t *testing.T) {
	for _, tc := range []struct {
		system, value string
		want          bool
	}{
		{"http://smarthealth.network/ids/holder", "provider", true},
		{"http://hl7.org/fhir/sid/us-npi", "1234567890", true},
		{"http://hl7.org/fhir/sid/us-npi", "", false},
		{"http://hl7.org/fhir/sid/us-npi", " ", false},
		{"", "provider", false},
		{"https://unrecognized.example/identity", "provider", false},
	} {
		raw, _ := json.Marshal(map[string]any{"resourceType": "Bundle", "entry": []any{map[string]any{"resource": map[string]any{"resourceType": "Claim", "related": []any{map[string]any{"claim": map[string]any{"identifier": map[string]string{"value": "original"}}}}}}, map[string]any{"resource": map[string]any{"resourceType": "Provenance", "agent": []any{map[string]any{"who": map[string]any{"identifier": map[string]string{"system": tc.system, "value": tc.value}}}}}}}})
		facts, ok := parseConformantUpdateFacts(raw)
		if !ok {
			t.Fatal("valid JSON update not parsed")
		}
		if (len(facts.provenanceAgents) > 0) != tc.want {
			t.Fatalf("system=%q value=%q agents=%v", tc.system, tc.value, facts.provenanceAgents)
		}
	}
	for _, who := range []string{`{"identifier":"not-an-Identifier"}`, `{"identifier":{"system":9,"value":"123"}}`, `{"identifier":{"system":"http://hl7.org/fhir/sid/us-npi","value":9}}`} {
		raw := []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim"}},{"resource":{"resourceType":"Provenance","agent":[{"who":` + who + `}]}}]}`)
		if _, ok := parseConformantUpdateFacts(raw); ok {
			t.Fatalf("malformed agent accepted: %s", who)
		}
	}
}
