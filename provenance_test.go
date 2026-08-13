package shnsdk

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// transformLossFixture is the representative loss-report input exercised
// across this file's shape/round-trip tests: one carried entry (a downcast
// dropping an element into shn-carried-content) and one synthesized entry
// (an upcast-mandatory field minted deterministically), covering both
// LossReport slots in a single case plus a second, loss-free step to prove
// a chain summary of more than one module.
var transformLossFixture = []LossReport{
	{
		Module: "pa.pas 2.1->2.2",
		Source: "2.1",
		Target: "2.2",
		Carried: []LossEntry{
			{Path: "Claim.item[0].extension", Detail: "authorizationNumber carried; source line 2.2"},
		},
	},
	{
		Module: "pa.pas 2.0->2.1",
		Source: "2.0",
		Target: "2.1",
		Synthesized: []LossEntry{
			{Path: "Claim.identifier", Detail: "synthesized from correlation id"},
		},
	},
}

// TestBuildTransformProvenanceShape pins the wire shape: agent.who
// carries the caller-supplied gateway self-attribution (the SAME
// "Organization/"+HolderID idiom every other self-attributed Provenance on
// a leg uses), activity names the module (both a coding and Text, so both
// machine- and human-readable), and the ONLY extension present is the
// shn-loss-report one. No stray top-level field (this Provenance is
// payload-internal only, never an envelope/Metadata carrier).
func TestBuildTransformProvenanceShape(t *testing.T) {
	recorded := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	raw, err := BuildTransformProvenance(
		"Bundle/leg-req-1", "Organization/provider",
		"pa.pas 2.1->2.2", "2.1", "2.2",
		transformLossFixture, recorded,
	)
	if err != nil {
		t.Fatalf("BuildTransformProvenance: %v", err)
	}

	allowed := map[string]bool{
		"resourceType": true, "target": true, "recorded": true,
		"agent": true, "activity": true, "extension": true,
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal top-level: %v", err)
	}
	for k := range top {
		if !allowed[k] {
			t.Fatalf("unexpected top-level field %q on transform Provenance (no envelope/metadata leakage)", k)
		}
	}
	if _, ok := top["metadata"]; ok {
		t.Fatalf("transform Provenance must never carry a metadata/envelope field")
	}

	prov, err := fhir.UnmarshalProvenance(raw)
	if err != nil {
		t.Fatalf("UnmarshalProvenance: %v", err)
	}
	if rt := gjsonResourceType(t, raw); rt != "Provenance" {
		t.Fatalf("resourceType = %q, want Provenance", rt)
	}
	if len(prov.Target) != 1 || prov.Target[0].Reference == nil || *prov.Target[0].Reference != "Bundle/leg-req-1" {
		t.Fatalf("target = %+v", prov.Target)
	}
	if prov.Recorded != "2026-08-12T10:00:00Z" {
		t.Fatalf("recorded = %q", prov.Recorded)
	}
	if len(prov.Agent) != 1 || prov.Agent[0].Who.Reference == nil || *prov.Agent[0].Who.Reference != "Organization/provider" {
		t.Fatalf("agent.who = %+v", prov.Agent)
	}
	if prov.Activity == nil || prov.Activity.Text == nil || *prov.Activity.Text != "pa.pas 2.1->2.2" {
		t.Fatalf("activity.text = %+v", prov.Activity)
	}
	if len(prov.Activity.Coding) != 1 || prov.Activity.Coding[0].Code == nil || *prov.Activity.Coding[0].Code != "pa.pas 2.1->2.2" {
		t.Fatalf("activity.coding = %+v", prov.Activity.Coding)
	}
	if prov.Activity.Coding[0].Display == nil || *prov.Activity.Coding[0].Display != "2.1->2.2" {
		t.Fatalf("activity.coding.display = %+v, want 2.1->2.2", prov.Activity.Coding[0].Display)
	}
	if len(prov.Extension) != 1 || prov.Extension[0].Url != LossReportExtURL {
		t.Fatalf("extension = %+v, want exactly one shn-loss-report extension", prov.Extension)
	}
}

// TestBuildTransformProvenanceLossRoundTrip proves the loss-extension
// round-trip: unmarshal the shn-loss-report extension back
// into []LossReport and get the ORIGINAL rows exactly, including nested
// Carried/Synthesized entries.
func TestBuildTransformProvenanceLossRoundTrip(t *testing.T) {
	recorded := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	raw, err := BuildTransformProvenance(
		"Bundle/leg-req-1", "Organization/payer",
		"pa.pas 2.1->2.2", "2.1", "2.2",
		transformLossFixture, recorded,
	)
	if err != nil {
		t.Fatalf("BuildTransformProvenance: %v", err)
	}
	prov, err := fhir.UnmarshalProvenance(raw)
	if err != nil {
		t.Fatalf("UnmarshalProvenance: %v", err)
	}
	extRaw, err := json.Marshal(prov.Extension[0])
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}

	got, err := RestoreTransformLoss(extRaw)
	if err != nil {
		t.Fatalf("RestoreTransformLoss: %v", err)
	}
	if !reflect.DeepEqual(got, transformLossFixture) {
		t.Fatalf("loss round-trip drift:\n got  %+v\n want %+v", got, transformLossFixture)
	}
}

// TestBuildTransformProvenanceEmptyLoss: a leg step chain with NO loss
// (every step class "full", nil-identity or otherwise lossless) still
// carries a well-formed extension — an empty JSON array, not an absent
// extension or null — so a downstream reader never has to special-case
// "no extension" vs "empty loss".
func TestBuildTransformProvenanceEmptyLoss(t *testing.T) {
	recorded := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	raw, err := BuildTransformProvenance(
		"Bundle/leg-req-2", "Organization/provider",
		"pa.dtr 2.1->2.2", "2.1", "2.2",
		nil, recorded,
	)
	if err != nil {
		t.Fatalf("BuildTransformProvenance: %v", err)
	}
	prov, err := fhir.UnmarshalProvenance(raw)
	if err != nil {
		t.Fatalf("UnmarshalProvenance: %v", err)
	}
	if len(prov.Extension) != 1 || prov.Extension[0].ValueString == nil || *prov.Extension[0].ValueString != "[]" {
		t.Fatalf("empty-loss extension = %+v, want valueString \"[]\"", prov.Extension)
	}
	extRaw, err := json.Marshal(prov.Extension[0])
	if err != nil {
		t.Fatalf("marshal extension: %v", err)
	}
	got, err := RestoreTransformLoss(extRaw)
	if err != nil {
		t.Fatalf("RestoreTransformLoss: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty-loss round-trip = %+v, want empty", got)
	}
}

// TestRestoreTransformLossRejectsMalformed: fails loudly (never a
// zero-value success) on a wrong url, missing value, or invalid JSON —
// same posture as RestoreCarried (carry_test.go).
func TestRestoreTransformLossRejectsMalformed(t *testing.T) {
	if _, err := RestoreTransformLoss(json.RawMessage(`{"url":"http://example.org/not-the-loss-extension","valueString":"[]"}`)); err == nil {
		t.Error("RestoreTransformLoss(wrong url) = nil error")
	}
	missingValue, _ := json.Marshal(map[string]any{"url": LossReportExtURL})
	if _, err := RestoreTransformLoss(missingValue); err == nil {
		t.Error("RestoreTransformLoss(missing value) = nil error")
	}
	if _, err := RestoreTransformLoss(json.RawMessage(`not json`)); err == nil {
		t.Error("RestoreTransformLoss(invalid json) = nil error")
	}
	badPayload, _ := json.Marshal(map[string]any{"url": LossReportExtURL, "valueString": "not an array"})
	if _, err := RestoreTransformLoss(badPayload); err == nil {
		t.Error("RestoreTransformLoss(non-JSON-array value) = nil error")
	}
}

func gjsonResourceType(t *testing.T, raw []byte) string {
	t.Helper()
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal resourceType: %v", err)
	}
	return probe.ResourceType
}
