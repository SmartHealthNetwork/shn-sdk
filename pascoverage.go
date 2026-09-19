package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// The coverage a prior-authorization request is made under, and the one rule
// that puts it on the wire.
//
// Da Vinci PAS makes Claim.insurance.coverage a min=1 mustSupport
// Reference(profile-coverage) at every published line, and its definition says
// the insurer uses that Coverage's DETAILS to locate the policy. Every example
// bundle in every published package — submit, update and inquiry alike — names
// an in-bundle Coverage entry by a literal reference, and the inquiry example
// names the same Coverage the submission did. Base FHIR R4 says a receiver
// "should not be expected to be able resolve" an identifier-only reference and
// may "reject" it, so an identifier-only coverage is exactly the form a payer is
// licensed to refuse — and one Da Vinci reference payer does: it stores what it
// was given and matches a later inquiry against what it stored, which it cannot
// do for a Coverage it was never allowed to store.
//
// THE SUBMISSION AND THE INQUIRY MUST NAME THE SAME COVERAGE RECORD, for the
// same reason they must name the same provider. That is why the Coverage this
// request carries is the participant's OWN record — its own id, its own
// identifiers — rather than one this package mints: a minted Coverage is a
// record no later inquiry, built from the participant's own system, can name
// again, and a minted id that is the SAME for every member is one member's
// coverage overwriting another's on a payer that stores by client-assigned id.
//
// Nothing here invents a Coverage. A caller with no Coverage record for the
// member is refused.

// pasCoverageRecord is the participant's own Coverage for the member, read out
// of the record (or the search result) the caller supplied.
type pasCoverageRecord struct {
	id  string
	raw []byte
}

var errPASCoverageRequired = errors.New(
	"the member's own Coverage record is required: a payer locates the policy from the coverage a request names, and matches a later inquiry against the coverage it stored, so a prior authorization cannot carry one this package made up")

// readPASCoverage reads the participant's own Coverage for a member out of
// either a Coverage resource or a Bundle holding it (an OpenCoverage search
// result is the usual shape). It refuses anything that is not exactly one
// Coverage with a FHIR id — the id is load-bearing, because it is what the
// submission and any later inquiry both name.
func readPASCoverage(record []byte) (pasCoverageRecord, error) {
	if len(strings.TrimSpace(string(record))) == 0 {
		return pasCoverageRecord{}, errPASCoverageRequired
	}
	entries, isBundle, err := coverageBundleEntries(record)
	if err != nil {
		return pasCoverageRecord{}, fmt.Errorf("your Coverage search result: %w", err)
	}
	raw := record
	if isBundle {
		var found []byte
		for _, e := range entries {
			if e.head.ResourceType != "Coverage" {
				continue
			}
			if found != nil {
				return pasCoverageRecord{}, errors.New("your Coverage search result holds more than one Coverage, and a prior authorization names one policy")
			}
			found = e.Resource
		}
		if found == nil {
			return pasCoverageRecord{}, errPASCoverageRequired
		}
		raw = found
	}
	var head struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return pasCoverageRecord{}, errors.New("your Coverage record is not one JSON object")
	}
	if head.ResourceType != "Coverage" {
		return pasCoverageRecord{}, fmt.Errorf("the coverage record is a %q, and a prior authorization is made under a Coverage", PASResourceTypeOf(raw))
	}
	if !fhirIDPattern.MatchString(head.ID) {
		return pasCoverageRecord{}, errors.New("your Coverage record has no valid id, so it cannot ride the request as the resolvable entry the Claim names")
	}
	return pasCoverageRecord{id: head.ID, raw: raw}, nil
}

// The closed set of references a Coverage may make on a prior-authorization
// request, and what this package does with each.
//
// Every reference a participant's Coverage makes points into that participant's
// own server, and resolves to nothing at the payer — which refuses the whole
// request graph for it (measured: 422 "unresolved source reference"). So the set
// is closed, and nothing outside it is guessed at:
//
//   - beneficiary and payor are OWNED: they become the Patient entry and the
//     payer Organization entry this request carries.
//   - subscriber and policyHolder are re-homed to that same Patient entry WHEN
//     they name the same person the beneficiary does — the usual "subscriber is
//     the patient" record, re-pointed with nothing lost. When they name anyone
//     else (a RelatedPerson, an employer Organization) the request is refused:
//     that party does not ride the request, and dropping the element would
//     change what the participant's record asserts.
//   - every OTHER reference — a contract, one inside an extension — is refused,
//     naming the element.
var (
	pasCoverageOwnedReferences   = []string{"beneficiary", "payor"}
	pasCoverageSubjectReferences = []string{"subscriber", "policyHolder"}
)

// pasCoverageEntry prepares the participant's own Coverage to ride a
// prior-authorization request: its own id and its own identifiers travel
// unchanged, meta.profile is dropped (the PAS context declares none), the two
// references this package owns are pointed at this Bundle's own entries, and a
// record making any OTHER reference is refused.
func pasCoverageEntry(record []byte, patientRef string, payer PayerIdentifier, payerOrg pasPayerOrgRecord) (pasCoverageRecord, error) {
	cov, err := readPASCoverage(record)
	if err != nil {
		return pasCoverageRecord{}, err
	}
	if err := checkCoverageReferencesOwned(cov.raw); err != nil {
		return pasCoverageRecord{}, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(cov.raw, &m); err != nil {
		return pasCoverageRecord{}, fmt.Errorf("your Coverage record: %w", err)
	}
	beneficiary, err := json.Marshal(map[string]string{"reference": patientRef})
	if err != nil {
		return pasCoverageRecord{}, err
	}
	// The member this coverage is for, and — where the record says they are the
	// same person — the subscriber and policy holder with them.
	beneficiaryKey := patientKey(coverageReferenceOf(m["beneficiary"]))
	for _, field := range pasCoverageSubjectReferences {
		raw, ok := m[field]
		if !ok || len(raw) == 0 {
			continue
		}
		if patientKey(coverageReferenceOf(raw)) != beneficiaryKey {
			return pasCoverageRecord{}, fmt.Errorf("your Coverage record's %s names someone other than the member it covers, and this request carries only the member: a payer refuses a request whose graph names a party it cannot resolve, and this package will not drop what your record asserts",
				field)
		}
		m[field] = beneficiary
	}
	m["beneficiary"] = beneficiary
	out, err := json.Marshal(m)
	if err != nil {
		return pasCoverageRecord{}, err
	}
	out, err = stripMetaProfile(out)
	if err != nil {
		return pasCoverageRecord{}, fmt.Errorf("strip coverage meta: %w", err)
	}
	if payerOrg.id != "" {
		// The payer Organization rides as a resolvable bundle ENTRY on this lane —
		// the participant's own record — and the reference payer's payor lookup
		// reads entries only.
		out, err = repointPayorToEntry(out, payerOrg.id)
		if err != nil {
			return pasCoverageRecord{}, err
		}
		return pasCoverageRecord{id: cov.id, raw: out}, nil
	}
	out, err = containCoveragePayor(out, payer)
	if err != nil {
		return pasCoverageRecord{}, err
	}
	return pasCoverageRecord{id: cov.id, raw: out}, nil
}

// checkCoverageReferencesOwned refuses a Coverage record that references
// anything this request does not carry.
//
// A reference to a resource contained in the record itself ("#id") is kept: it
// resolves inside the resource wherever the resource goes. Everything else is
// named and refused, because the alternative is a request the payer rejects
// whole, or a record quietly stripped of what it asserted.
func checkCoverageReferencesOwned(coverageJSON []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(coverageJSON, &m); err != nil {
		return fmt.Errorf("your Coverage record: %w", err)
	}
	var found []string
	var walk func(path string, raw json.RawMessage)
	walk = func(path string, raw json.RawMessage) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			for k, v := range obj {
				if k == "reference" {
					var ref string
					if json.Unmarshal(v, &ref) == nil && ref != "" && !strings.HasPrefix(ref, "#") {
						found = append(found, path+" -> "+ref)
					}
					continue
				}
				walk(path+"."+k, v)
			}
			return
		}
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			for i, v := range arr {
				walk(fmt.Sprintf("%s[%d]", path, i), v)
			}
		}
	}
	for k, v := range m {
		if k == "contained" || slices.Contains(pasCoverageOwnedReferences, k) || slices.Contains(pasCoverageSubjectReferences, k) {
			continue
		}
		walk("Coverage."+k, v)
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)
	return fmt.Errorf("your Coverage record references records this request does not carry (%s): a prior authorization carries the member, the coverage, the provider and the payer, and a payer refuses a request whose graph names anything it cannot resolve — this package will not quietly drop what your record asserts",
		strings.Join(found, "; "))
}

// containCoveragePayor points a Coverage's payor at a contained payer
// Organization carrying the payer identity the flow read from the member's own
// coverage — the shape every non-reference-payer lane has always put on the
// wire. Any payor the record stated is replaced: it named the participant's own
// server, which the receiver cannot read.
func containCoveragePayor(coverageJSON []byte, payer PayerIdentifier) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(coverageJSON, &m); err != nil {
		return nil, fmt.Errorf("parse coverage: %w", err)
	}
	payorJSON, err := json.Marshal([]map[string]string{{"reference": "#" + conformantPayerOrgID}})
	if err != nil {
		return nil, err
	}
	m["payor"] = payorJSON
	org, err := json.Marshal(map[string]any{
		"resourceType": "Organization",
		"id":           conformantPayerOrgID,
		"name":         conformantPayerOrgName,
		"identifier":   []any{map[string]string{"system": payer.System, "value": payer.Value}},
	})
	if err != nil {
		return nil, err
	}
	// Whatever else the record contained travels with it; only a previous payer
	// organization under this id is displaced.
	kept := []json.RawMessage{json.RawMessage(org)}
	if raw, ok := m["contained"]; ok && len(raw) > 0 {
		var existing []json.RawMessage
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("parse coverage contained: %w", err)
		}
		for _, c := range existing {
			var probe struct {
				ResourceType string `json:"resourceType"`
				ID           string `json:"id"`
			}
			if json.Unmarshal(c, &probe) == nil &&
				probe.ResourceType == "Organization" && probe.ID == conformantPayerOrgID {
				continue
			}
			kept = append(kept, c)
		}
	}
	contained, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	m["contained"] = contained
	return json.Marshal(m)
}

// setInsuranceCoverageEntryRef points a built Claim's insurance[0].coverage at
// the Coverage ENTRY this Bundle carries, by the same bundle-local relative
// reference every other entry is named by (the reference-payer lanes absolutize
// it afterwards, exactly as they do the insurer's). It replaces whatever the
// builders stamped from the caller's CoverageRef, which named a resource no
// receiver holds.
//
// The element stays a FHIR Reference and the change happens bundle-side, the
// same way the two payer-organization repoints do, so the Claim builders'
// signatures are untouched.
func setInsuranceCoverageEntryRef(claimJSON []byte, coverageRef string) ([]byte, error) {
	if strings.TrimSpace(coverageRef) == "" {
		return nil, errors.New("setInsuranceCoverageEntryRef: the coverage reference is empty")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("setInsuranceCoverageEntryRef: parse claim: %w", err)
	}
	// Typed round-trip (not the map surgery the payer-org repoints use):
	// fhir.ClaimInsurance models the element in full, and re-marshalling through
	// it preserves the builders' exact key order for the untouched siblings
	// (sequence, focal).
	var insurance []fhir.ClaimInsurance
	if err := json.Unmarshal(m["insurance"], &insurance); err != nil {
		return nil, fmt.Errorf("setInsuranceCoverageEntryRef: parse insurance: %w", err)
	}
	if len(insurance) == 0 {
		return nil, fmt.Errorf("setInsuranceCoverageEntryRef: Claim.insurance is empty")
	}
	// Whole-value replacement, so nothing of the caller's CoverageRef survives.
	insurance[0].Coverage = fhir.Reference{Reference: strPtr(coverageRef)}
	insuranceJSON, err := json.Marshal(insurance)
	if err != nil {
		return nil, fmt.Errorf("setInsuranceCoverageEntryRef: marshal insurance: %w", err)
	}
	m["insurance"] = insuranceJSON
	return json.Marshal(m)
}

// checkPASCoverageResolves refuses a built request whose Claim names a coverage
// the Bundle does not resolve.
//
// It is the guard, and validation cannot be it: measured against the pinned
// IG-profile validator, a request whose Claim.insurance.coverage is an
// identifier-only reference, a literal to an entry, a literal to NOTHING, or
// both at once all return the same zero errors, because a Reference satisfies
// the element whether or not it names anything reachable. Only the payer knows,
// months later, by locating no policy and matching no inquiry.
//
// The check reads the assembled bytes and resolves the reference the way a
// payer does — against the entries' own fullUrls, and against the relative form
// of each entry's identity — and it requires the thing it lands on to be a
// Coverage.
func checkPASCoverageResolves(bundle []byte) error {
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return fmt.Errorf("read the built request: %w", err)
	}
	coverages := map[string]bool{}
	named, stated := "", false
	for _, e := range b.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Insurance    []struct {
				Coverage struct {
					Reference  string `json:"reference"`
					Identifier *struct {
						System string `json:"system"`
						Value  string `json:"value"`
					} `json:"identifier"`
				} `json:"coverage"`
			} `json:"insurance"`
		}
		if json.Unmarshal(e.Resource, &r) != nil {
			continue
		}
		if r.ResourceType == "Coverage" {
			if e.FullURL != "" {
				coverages[e.FullURL] = true
			}
			if r.ID != "" {
				coverages["Coverage/"+r.ID] = true
			}
		}
		if r.ResourceType == "Claim" && !stated {
			stated = true
			if len(r.Insurance) > 0 {
				named = r.Insurance[0].Coverage.Reference
			}
		}
	}
	if !stated {
		return errors.New("the request carries no Claim")
	}
	if named == "" {
		return errors.New("the request's Claim names no coverage by reference, so the payer has no policy to locate and no coverage to match a later inquiry against")
	}
	if !coverages[named] {
		return fmt.Errorf("the request's Claim names coverage %q, which the Bundle does not resolve to a Coverage", named)
	}
	return nil
}

// coverageReferenceOf reads the reference a Coverage element states, or "" when
// it states none.
func coverageReferenceOf(raw json.RawMessage) string {
	var ref struct {
		Reference string `json:"reference"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &ref) != nil {
		return ""
	}
	return ref.Reference
}
