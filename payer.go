package shnsdk

import (
	"encoding/json"
	"strings"
)

// PayerIdentifier is a Coverage.payor Organization identifier (system|value) used for routing.
type PayerIdentifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// CMSPayerIdentity is the historical hardcoded payer identity (CMS, urn:oid:…300|00001).
// Kept as a named constant so behavior-preserving call sites can pass it explicitly during the
// migration to coverage-derived payer identity.
var CMSPayerIdentity = PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00001"}

// ParsePayerIdentifier extracts the payer identity from a Coverage's payor for ROUTING. It honors
// all three payor forms: a CONTAINED Organization (payor ref "#id"), an EXTERNAL Organization
// (payor ref "Organization/id", resolved via resolveRef), and an INLINE Coverage.payor[0].identifier.
// ok=false when no (system,value) can be found. resolveRef may be nil (contained + inline only).
func ParsePayerIdentifier(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, bool) {
	var cov struct {
		Payor []struct {
			Reference  string      `json:"reference"`
			Identifier *identifier `json:"identifier"`
		} `json:"payor"`
		Contained []json.RawMessage `json:"contained"`
	}
	if err := json.Unmarshal(coverageJSON, &cov); err != nil || len(cov.Payor) == 0 {
		return PayerIdentifier{}, false
	}
	p := cov.Payor[0]
	// Inline form.
	if p.Identifier != nil && p.Identifier.System != "" && p.Identifier.Value != "" {
		return PayerIdentifier{p.Identifier.System, p.Identifier.Value}, true
	}
	switch {
	case strings.HasPrefix(p.Reference, "#"): // contained
		id := strings.TrimPrefix(p.Reference, "#")
		for _, raw := range cov.Contained {
			if pid, ok := orgIdentifier(raw, id); ok {
				return pid, true
			}
		}
	case p.Reference != "" && resolveRef != nil: // external
		if orgJSON, found := resolveRef(p.Reference); found {
			if pid, ok := orgIdentifier(orgJSON, ""); ok {
				return pid, true
			}
		}
	}
	return PayerIdentifier{}, false
}

type identifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

// orgIdentifier returns the first (system,value) of an Organization's identifier. When wantID is
// non-empty the Organization's id must match (contained-resolution); empty wantID accepts any.
func orgIdentifier(orgJSON []byte, wantID string) (PayerIdentifier, bool) {
	var org struct {
		ResourceType string       `json:"resourceType"`
		ID           string       `json:"id"`
		Identifier   []identifier `json:"identifier"`
	}
	if err := json.Unmarshal(orgJSON, &org); err != nil || org.ResourceType != "Organization" {
		return PayerIdentifier{}, false
	}
	if wantID != "" && org.ID != wantID {
		return PayerIdentifier{}, false
	}
	for _, id := range org.Identifier {
		if id.System != "" && id.Value != "" {
			return PayerIdentifier{id.System, id.Value}, true
		}
	}
	return PayerIdentifier{}, false
}
