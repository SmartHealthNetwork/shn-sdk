package shnsdk

import (
	"bytes"
	"encoding/json"
	"testing"
)

// carryFixtures are the elements exercised by the round-trip property test:
// a plain object, an object carrying its own nested "extension" array (the
// case that most risks double-escaping), an array-shaped element, a value
// with unicode + escape-worthy characters, and a compact (no-whitespace)
// element — each must come back byte-identical (Restore(Carry(x)) == x).
var carryFixtures = []struct {
	name string
	elem string
}{
	{"plain-object", `{"resourceType":"Extension","valueString":"authorizationNumber-9981"}`},
	{"nested-extension", `{"url":"http://smarthealth.network/fhir/StructureDefinition/example","extension":[{"url":"inner","valueString":"x"}]}`},
	{"array-shaped", `[{"linkId":"a","answer":[{"valueBoolean":true}]},{"linkId":"b"}]`},
	{"unicode-and-escapes", `{"text":"quote\" backslash\\ unicode café newline\n tab\t"}`},
	{"compact", `{"a":1,"b":[1,2,3],"c":null}`},
}

// TestCarryRestoreRoundTrip pins the byte-faithful round-trip: for every
// fixture element, RestoreCarried(CarryElement(path, elem, line)) must return
// exactly (path, elem, line) — elem byte-for-byte, not merely
// JSON-equivalent (spec's carry policy requires the ORIGINAL bytes back).
func TestCarryRestoreRoundTrip(t *testing.T) {
	for _, tc := range carryFixtures {
		t.Run(tc.name, func(t *testing.T) {
			const path = "ClaimResponse.extension[authorizationNumber]"
			const sourceLine = "2.2"

			ext, err := CarryElement(path, json.RawMessage(tc.elem), sourceLine)
			if err != nil {
				t.Fatalf("CarryElement: %v", err)
			}

			gotPath, gotElem, gotLine, err := RestoreCarried(ext)
			if err != nil {
				t.Fatalf("RestoreCarried: %v", err)
			}
			if gotPath != path {
				t.Errorf("path = %q, want %q", gotPath, path)
			}
			if gotLine != sourceLine {
				t.Errorf("sourceLine = %q, want %q", gotLine, sourceLine)
			}
			if !bytes.Equal(gotElem, []byte(tc.elem)) {
				t.Errorf("element round-trip drift:\n got  %s\n want %s", gotElem, tc.elem)
			}
		})
	}
}

// TestCarryElementShape pins the wire shape: a complex extension with url
// CarriedContentExtURL and exactly three sub-extensions (path/sourceLine/
// content), each a plain valueString — the minimal shape the IG package
// profiles.
func TestCarryElementShape(t *testing.T) {
	ext, err := CarryElement("Claim.item[0]", json.RawMessage(`{"x":1}`), "2.1")
	if err != nil {
		t.Fatalf("CarryElement: %v", err)
	}

	var probe struct {
		URL       string `json:"url"`
		Extension []struct {
			URL         string `json:"url"`
			ValueString string `json:"valueString"`
		} `json:"extension"`
	}
	if err := json.Unmarshal(ext, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.URL != CarriedContentExtURL {
		t.Errorf("top-level url = %q, want %q", probe.URL, CarriedContentExtURL)
	}
	if len(probe.Extension) != 3 {
		t.Fatalf("len(extension) = %d, want 3", len(probe.Extension))
	}
	want := map[string]string{"path": "Claim.item[0]", "sourceLine": "2.1", "content": `{"x":1}`}
	got := map[string]string{}
	for _, sub := range probe.Extension {
		got[sub.URL] = sub.ValueString
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("sub-extension %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestCarryElementRejectsEmptyInputs: fail-closed on missing required inputs
// (no silent empty extension).
func TestCarryElementRejectsEmptyInputs(t *testing.T) {
	if _, err := CarryElement("", json.RawMessage(`{"a":1}`), "2.0"); err == nil {
		t.Error("CarryElement(empty path) = nil error")
	}
	if _, err := CarryElement("path", json.RawMessage(`{"a":1}`), ""); err == nil {
		t.Error("CarryElement(empty sourceLine) = nil error")
	}
	if _, err := CarryElement("path", nil, "2.0"); err == nil {
		t.Error("CarryElement(nil element) = nil error")
	}
}

// TestRestoreCarriedRejectsMalformed: RestoreCarried fails loudly (never
// returns a zero-value success) on a wrong top-level url or missing
// sub-extensions.
func TestRestoreCarriedRejectsMalformed(t *testing.T) {
	if _, _, _, err := RestoreCarried(json.RawMessage(`{"url":"http://example.org/not-the-carry-extension","extension":[]}`)); err == nil {
		t.Error("RestoreCarried(wrong url) = nil error")
	}
	missingContent, _ := json.Marshal(map[string]any{
		"url": CarriedContentExtURL,
		"extension": []map[string]any{
			{"url": "path", "valueString": "p"},
			{"url": "sourceLine", "valueString": "2.0"},
		},
	})
	if _, _, _, err := RestoreCarried(missingContent); err == nil {
		t.Error("RestoreCarried(missing content sub-extension) = nil error")
	}
	if _, _, _, err := RestoreCarried(json.RawMessage(`not json`)); err == nil {
		t.Error("RestoreCarried(invalid json) = nil error")
	}
}

// TestExtURLConstants pins the two canonical URLs verbatim (the wire
// contract every participant resolves against).
func TestExtURLConstants(t *testing.T) {
	if CarriedContentExtURL != "http://smarthealth.network/fhir/StructureDefinition/shn-carried-content" {
		t.Errorf("CarriedContentExtURL = %q", CarriedContentExtURL)
	}
	if LossReportExtURL != "http://smarthealth.network/fhir/StructureDefinition/shn-loss-report" {
		t.Errorf("LossReportExtURL = %q", LossReportExtURL)
	}
}
