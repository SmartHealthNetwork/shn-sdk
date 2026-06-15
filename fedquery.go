// Package shnsdk — fedquery builds and parses the federated-query messages used in
// prior auth with external supplemental evidence: a narrow FHIR Parameters request
// (named patient + resource types, NO bulk export) and a searchset Bundle response
// carrying only the named records (FR-24/FR-26, AI-1).
package shnsdk

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// AllowedTypes is the closed set of resource types a federated query may name.
// A request naming anything else, or nothing, is rejected (no bulk, no sweep).
var AllowedTypes = map[string]bool{"DiagnosticReport": true, "DocumentReference": true}

// Query is a parsed federated-query request.
type Query struct {
	PatientRef string
	Types      []string
	Start      string // FHIR date, inclusive — FR-24 "stated date range"
	End        string
}

// BuildQuery builds the Parameters: one 'patient' valueReference, one 'start'
// valueDate, one 'end' valueDate, and one 'type' valueString per requested
// resource type (FR-24: named types within a stated date range — never bulk).
func BuildQuery(patientRef string, types []string, start, end string) ([]byte, error) {
	params := []map[string]any{
		{"name": "patient", "valueReference": map[string]string{"reference": patientRef}},
		{"name": "start", "valueDate": start},
		{"name": "end", "valueDate": end},
	}
	for _, t := range types {
		params = append(params, map[string]any{"name": "type", "valueString": t})
	}
	return json.Marshal(map[string]any{"resourceType": "Parameters", "parameter": params})
}

// dateKey normalizes a FHIR date/dateTime to its YYYY-MM-DD prefix for comparison.
func dateKey(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// InRange reports whether date falls within the query's inclusive [Start, End].
func (q Query) InRange(date string) bool {
	d := dateKey(date)
	return d != "" && dateKey(q.Start) <= d && d <= dateKey(q.End)
}

// ParseQuery parses and VALIDATES narrowness: patient present, ≥1 type, every
// type in AllowedTypes, and a valid non-inverted date range (FR-24). Any
// violation is an error (the facility returns 403).
func ParseQuery(data []byte) (Query, error) {
	var p struct {
		ResourceType string `json:"resourceType"`
		Parameter    []struct {
			Name           string `json:"name"`
			ValueString    string `json:"valueString"`
			ValueDate      string `json:"valueDate"`
			ValueReference struct {
				Reference string `json:"reference"`
			} `json:"valueReference"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Query{}, fmt.Errorf("fedquery: parse: %w", err)
	}
	if p.ResourceType != "Parameters" {
		return Query{}, fmt.Errorf("fedquery: expected Parameters, got %q", p.ResourceType)
	}
	var q Query
	for _, prm := range p.Parameter {
		switch prm.Name {
		case "patient":
			q.PatientRef = prm.ValueReference.Reference
		case "start":
			q.Start = prm.ValueDate
		case "end":
			q.End = prm.ValueDate
		case "type":
			if !AllowedTypes[prm.ValueString] {
				return Query{}, fmt.Errorf("fedquery: disallowed type %q (no bulk export)", prm.ValueString)
			}
			q.Types = append(q.Types, prm.ValueString)
		}
	}
	if q.PatientRef == "" {
		return Query{}, fmt.Errorf("fedquery: query missing patient")
	}
	if len(q.Types) == 0 {
		return Query{}, fmt.Errorf("fedquery: query names no resource types (no bulk export)")
	}
	if q.Start == "" || q.End == "" {
		return Query{}, fmt.Errorf("fedquery: missing date range (FR-24: a stated date range is required)")
	}
	// Validate the WINDOW endpoints as real calendar dates (FHIR `date`), not just
	// lexically — an impossible date (2024-13-45, 2024-02-30) must be rejected.
	// time.Parse with this layout is strict (rejects month>12, day-out-of-range).
	const dateLayout = "2006-01-02"
	startT, err := time.Parse(dateLayout, q.Start)
	if err != nil {
		return Query{}, fmt.Errorf("fedquery: invalid start date %q: %w", q.Start, err)
	}
	endT, err := time.Parse(dateLayout, q.End)
	if err != nil {
		return Query{}, fmt.Errorf("fedquery: invalid end date %q: %w", q.End, err)
	}
	if startT.After(endT) {
		return Query{}, fmt.Errorf("fedquery: inverted date range %s..%s", q.Start, q.End)
	}
	return q, nil
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

// ExtractOperativeEvidence pulls the DiagnosticReport and the Provenance out of a
// facility searchset response, for attachment to the ClaimUpdate. The
// DocumentReference rode along to demonstrate the named-types query but the claim
// evidence the payer re-adjudicates on is the DiagnosticReport (R1). Errors if
// either required resource is absent.
func ExtractOperativeEvidence(bundleJSON []byte) (drJSON, provJSON []byte, err error) {
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
