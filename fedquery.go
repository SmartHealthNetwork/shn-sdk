// Package shnsdk — fedquery provides the shared US-Core searchset assembler
// (BuildRecordsBundle), the AllowedTypes narrowness guard, and the unexported
// dateKey/extractEvidenceFromBundle helpers shared with the CDex content layer
// in cdex.go (FR-24/FR-26, AI-1). The bespoke Parameters-era wire (BuildQuery/
// ParseQuery/Query/ExtractOperativeEvidence) was removed when all consumers
// migrated to the CDex Task Data Request wire (BuildCDexTaskDataRequest et al.).
package shnsdk

import (
	"encoding/json"
	"fmt"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// AllowedTypes is the closed set of resource types a federated query may name.
// A request naming anything else, or nothing, is rejected (no bulk, no sweep).
var AllowedTypes = map[string]bool{"DiagnosticReport": true, "DocumentReference": true}

// dateKey normalizes a FHIR date/dateTime to its YYYY-MM-DD prefix for comparison.
func dateKey(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// BuildRecordsBundle assembles the searchset Bundle response from the named
// resource JSONs (already built by the caller, in query order) plus the source
// Provenance. Each entry carries a deterministic fullUrl.
func BuildRecordsBundle(resources [][]byte) ([]byte, error) {
	entries := make([]fhir.BundleEntry, 0, len(resources))
	for i, res := range resources {
		entries = append(entries, fhir.BundleEntry{
			FullUrl:  strPtr(fmt.Sprintf("urn:shn:fedquery:%d", i)),
			Resource: json.RawMessage(res),
		})
	}
	bundle := fhir.Bundle{Type: fhir.BundleTypeSearchset, Entry: entries}
	// fhir.Bundle.MarshalJSON injects "resourceType":"Bundle" automatically
	// (confirmed in samply v0.3.2).
	return json.Marshal(bundle)
}

// extractEvidenceFromBundle is the shared searchset-Bundle extractor: it pulls the
// DiagnosticReport and Provenance out of a US-Core searchset Bundle. ExtractCDexEvidence
// (CDex completed-Task wire) delegates here so the DR+Provenance extraction logic lives
// in exactly one place.
// Errors keep the fedquery: prefix (bundle-layer attribution); ExtractCDexEvidence callers
// therefore see mixed cdex:/fedquery: prefixes — intentional, do not "normalize".
func extractEvidenceFromBundle(bundleJSON []byte) (drJSON, provJSON []byte, err error) {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if e := json.Unmarshal(bundleJSON, &b); e != nil {
		return nil, nil, fmt.Errorf("fedquery: parse response: %w", e)
	}
	for _, e := range b.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			return nil, nil, fmt.Errorf("fedquery: parse entry: %w", err)
		}
		switch rt.ResourceType {
		case "DiagnosticReport":
			drJSON = []byte(e.Resource)
		case "Provenance":
			provJSON = []byte(e.Resource)
		}
	}
	if drJSON == nil {
		return nil, nil, fmt.Errorf("fedquery: response missing DiagnosticReport")
	}
	if provJSON == nil {
		return nil, nil, fmt.Errorf("fedquery: response missing Provenance")
	}
	return drJSON, provJSON, nil
}
