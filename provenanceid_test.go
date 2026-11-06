package shnsdk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// The id BuildProvenanceRecord gives a Provenance by default is deterministic,
// "prov-<target id>", so the carried update builder can carry it as written
// under its own id. An id that would be too long, or a target id with characters a FHIR
// id cannot hold, is cut to a valid FHIR id with a digest of the whole target
// reference, deterministically, and the result is never the target's own id.
func TestProvenanceIDFor(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, row := range []struct{ target, want string }{
		{"DiagnosticReport/dr-1", "prov-dr-1"},
		{"DiagnosticReport/dr-1/_history/2", "prov-dr-1"},
		{"https://facility.example/fhir/DiagnosticReport/dr-1", "prov-dr-1"},
		{"QuestionnaireResponse/qr.7", "prov-qr.7"},
		// A "/_history/" that does not end the reference with a version is
		// part of the reference, as ProvenanceTargetNames reads it.
		{"http://h/_history/x/DiagnosticReport/dr-9", "prov-dr-9"},
	} {
		if got := provenanceIDFor(row.target); got != row.want {
			t.Errorf("provenanceIDFor(%q) = %q, want %q", row.target, got, row.want)
		}
	}
	for _, target := range []string{"DiagnosticReport/" + long, "DiagnosticReport/" + "prov-" + strings.Repeat("b", 59), "DiagnosticReport/a_b", "#contained", "DiagnosticReport/"} {
		got := provenanceIDFor(target)
		if !fhirIDPattern.MatchString(got) || !strings.HasPrefix(got, "prov-") {
			t.Errorf("provenanceIDFor(%q) = %q, not a FHIR id starting prov-", target, got)
		}
		if got != provenanceIDFor(target) {
			t.Errorf("provenanceIDFor(%q) is not deterministic", target)
		}
		if i := strings.LastIndex(target, "/"); i >= 0 && got == target[i+1:] {
			t.Errorf("provenanceIDFor(%q) = %q, the target's own id", target, got)
		}
	}
	if provenanceIDFor("DiagnosticReport/"+long) == provenanceIDFor("DiagnosticReport/"+long+"x") {
		t.Error("two targets cut to one id")
	}
}

// BuildProvenanceRecord gives the Provenance an id: the caller's when set,
// else "prov-<target id>" (provenanceIDFor). The Deprecated builders keep
// their bytes, with no id (TestPASExactBuilders_KeepTheirBytes).
func TestBuildProvenanceRecord(t *testing.T) {
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	npi := &ProvenanceIdentifier{System: "http://hl7.org/fhir/sid/us-npi", Value: "1234567893"}
	read := func(t *testing.T, b []byte) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	for name, row := range map[string]struct {
		in     ProvenanceInputs
		wantID string
	}{
		"the default id":         {ProvenanceInputs{Target: "DiagnosticReport/dr-1", Agent: "Organization/o", Recorded: now}, "prov-dr-1"},
		"the default id, cut":    {ProvenanceInputs{Target: "DiagnosticReport/" + strings.Repeat("a", 64), Agent: "Organization/o", Recorded: now}, provenanceIDFor("DiagnosticReport/" + strings.Repeat("a", 64))},
		"a caller's id wins":     {ProvenanceInputs{ID: "amendment-2", Target: "DiagnosticReport/dr-1", Agent: "Organization/o", Recorded: now}, "amendment-2"},
		"an agent by identifier": {ProvenanceInputs{Target: "DiagnosticReport/dr-1", AgentIdentifier: npi, Policy: "Consent/c", PurposeOfUse: "TREAT", Recorded: now}, "prov-dr-1"},
	} {
		b, err := BuildProvenanceRecord(row.in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		m := read(t, b)
		if m["resourceType"] != "Provenance" || m["id"] != row.wantID {
			t.Errorf("%s: %s, want a Provenance with id %q", name, b, row.wantID)
		}
		if got := m["target"].([]any)[0].(map[string]any)["reference"]; got != row.in.Target {
			t.Errorf("%s: target %v, want %q", name, got, row.in.Target)
		}
	}
	// The source by identifier, with the policy and purpose beside it.
	b, err := BuildProvenanceRecord(ProvenanceInputs{Target: "DiagnosticReport/dr-1", AgentIdentifier: npi, Policy: "Consent/c", PurposeOfUse: "TREAT", Recorded: now})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"agent":[{"who":{"identifier":{"system":"http://hl7.org/fhir/sid/us-npi","value":"1234567893"}}}],"id":"prov-dr-1","policy":["Consent/c"],"reason":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v3-ActReason","code":"TREAT"}]}],"recorded":"2026-11-06T00:00:00Z","resourceType":"Provenance","target":[{"reference":"DiagnosticReport/dr-1"}]}`; string(b) != want {
		t.Errorf("BuildProvenanceRecord = %s\nwant %s", b, want)
	}
	for name, in := range map[string]ProvenanceInputs{
		"no target":            {Agent: "Organization/o", Recorded: now},
		"an id FHIR refuses":   {ID: "a_b", Target: "DiagnosticReport/dr-1", Agent: "Organization/o", Recorded: now},
		"no agent":             {Target: "DiagnosticReport/dr-1", Recorded: now},
		"both agents":          {Target: "DiagnosticReport/dr-1", Agent: "Organization/o", AgentIdentifier: npi, Recorded: now},
		"an unknown id system": {Target: "DiagnosticReport/dr-1", AgentIdentifier: &ProvenanceIdentifier{System: "urn:other", Value: "x"}, Recorded: now},
	} {
		if b, err := BuildProvenanceRecord(in); err == nil {
			t.Errorf("%s: built %s, want it refused", name, b)
		}
	}
}

// Two entries of one type that share an id cannot both ride an update bundle
// (FHIR bdl-7): one would silently stand for the other. Two Provenances the
// SDK built for one report share prov-<report id>; the check refuses them, the
// fidelity refusal of SHN's own build. Every pair the carried update builder's inputs
// can produce is refused by a more specific check first (the submitted Claim
// under the amendment's own id, a Coverage party under the member's), so the
// check is exercised directly.
func TestPASUpdate_RefusesTwoEntriesWithOneTypeAndID(t *testing.T) {
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	p1, err := BuildProvenanceRecord(ProvenanceInputs{Target: "DiagnosticReport/dr-1", Agent: "Organization/a", Recorded: now})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := BuildProvenanceRecord(ProvenanceInputs{Target: "DiagnosticReport/dr-1", Agent: "Organization/b", Recorded: now})
	if err != nil {
		t.Fatal(err)
	}
	url := pasBundleBaseURL + "/Provenance/" + provenanceIDFor("DiagnosticReport/dr-1")
	other := pasBundleBaseURL + "/DiagnosticReport/dr-1"
	two := []fhir.BundleEntry{{FullUrl: strPtr(other)}, {FullUrl: strPtr(url), Resource: p1}, {FullUrl: strPtr(url), Resource: p2}}
	err = checkPASEntriesUnique(two)
	if err == nil || !errors.Is(err, ErrPASRecordNotCarried) || !strings.Contains(err.Error(), "bdl-7") || !strings.Contains(err.Error(), url) {
		t.Fatalf("two Provenances of one id: %v, want ErrPASRecordNotCarried naming %s and bdl-7", err, url)
	}
	if err := checkPASEntriesUnique(two[:2]); err != nil {
		t.Fatalf("distinct entries refused: %v", err)
	}
}
