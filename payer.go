package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
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

// ErrNoCoveragePayer: the Coverage value names no payer this SDK can resolve.
var ErrNoCoveragePayer = errors.New("shnsdk: no payer can be resolved from the coverage")

// ErrAmbiguousCoveragePayer: the Coverage Bundle's Coverages name more than one payer.
var ErrAmbiguousCoveragePayer = errors.New("shnsdk: the coverage names more than one payer")

// ParsePayerIdentifier extracts the payer identity from a Coverage's payor for ROUTING. It honors
// all three payor forms: a CONTAINED Organization (payor ref "#id"), an EXTERNAL Organization
// (payor ref "Organization/id", resolved via resolveRef), and an INLINE Coverage.payor[0].identifier.
// ok=false when no (system,value) can be found. resolveRef may be nil (contained + inline only).
//
// coverageJSON may also be a Bundle (a search result or a collection) of Coverages: it routes
// only when every Coverage entry resolves to the same payer, an external payor reference being
// looked up first among the Bundle's own entries and then through resolveRef.
// ParseCoveragePayer reports why a value does not route.
func ParsePayerIdentifier(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, bool) {
	pid, err := ParseCoveragePayer(coverageJSON, resolveRef)
	return pid, err == nil
}

// ParseCoveragePayer is ParsePayerIdentifier with the reason a value does not route:
// ErrNoCoveragePayer (no Coverage, or a Coverage whose payer cannot be resolved) or
// ErrAmbiguousCoveragePayer (a Bundle whose Coverages name different payers). It only parses.
func ParseCoveragePayer(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, error) {
	entries, isBundle, err := coverageBundleEntries(coverageJSON)
	if err != nil {
		return PayerIdentifier{}, fmt.Errorf("%w: %v", ErrNoCoveragePayer, err)
	}
	if !isBundle {
		if pid, ok := parseCoveragePayor(coverageJSON, resolveRef); ok {
			return pid, nil
		}
		return PayerIdentifier{}, ErrNoCoveragePayer
	}
	inBundle := func(ref string) ([]byte, bool) {
		// An entry resolves a reference equal to its fullUrl, or a relative
		// reference equal to its Type/id; an absolute reference to another
		// server never resolves to an entry.
		for _, e := range entries {
			if e.FullURL != "" && e.FullURL == ref {
				return e.Resource, true
			}
			if e.head.ResourceType != "" && e.head.ID != "" && ref == e.head.ResourceType+"/"+e.head.ID {
				return e.Resource, true
			}
		}
		if resolveRef != nil {
			return resolveRef(ref)
		}
		return nil, false
	}
	var found *PayerIdentifier
	for _, e := range entries {
		if e.head.ResourceType != "Coverage" {
			continue
		}
		pid, ok := parseCoveragePayor(e.Resource, inBundle)
		if !ok {
			return PayerIdentifier{}, fmt.Errorf("%w: Coverage %q", ErrNoCoveragePayer, e.head.ID)
		}
		if found != nil && *found != pid {
			return PayerIdentifier{}, ErrAmbiguousCoveragePayer
		}
		found = &pid
	}
	if found == nil {
		return PayerIdentifier{}, fmt.Errorf("%w: the Bundle holds no Coverage", ErrNoCoveragePayer)
	}
	return *found, nil
}

type coverageBundleEntry struct {
	FullURL  string          `json:"fullUrl"`
	Resource json.RawMessage `json:"resource"`
	head     struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
}

// coverageBundleEntries returns the entries of a Bundle value (isBundle false for any other
// value). A Bundle that does not decode is an error.
func coverageBundleEntries(v []byte) (entries []coverageBundleEntry, isBundle bool, err error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if json.Unmarshal(v, &probe) != nil || probe.ResourceType != "Bundle" {
		return nil, false, nil
	}
	var b struct {
		Entry []coverageBundleEntry `json:"entry"`
	}
	if err := json.Unmarshal(v, &b); err != nil {
		return nil, true, err
	}
	for i := range b.Entry {
		_ = json.Unmarshal(b.Entry[i].Resource, &b.Entry[i].head)
	}
	return b.Entry, true, nil
}

// parseCoveragePayor resolves one Coverage's first payor.
func parseCoveragePayor(coverageJSON []byte, resolveRef func(ref string) ([]byte, bool)) (PayerIdentifier, bool) {
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

// ParseOrganizationIdentifier extracts the first (system,value) identifier from a
// standalone Organization resource — a payer's SELF-read of its own well-known
// Organization (e.g. Organization/payer), as opposed to ParsePayerIdentifier's
// Coverage.payor resolution. Used when a member has NO Coverage record at all: there is
// no payor to parse, so the insurer instead names the payer's own identity. ok=false when
// the resource isn't an Organization or carries no identifier.
func ParseOrganizationIdentifier(orgJSON []byte) (PayerIdentifier, bool) {
	return orgIdentifier(orgJSON, "")
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
