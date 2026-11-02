package shnsdk

import (
	"bytes"
	"encoding/json"
	"strings"
)

// pasSubjectElements are the elements whose declared target is the patient a
// resource is about: a reference there names a patient whether or not its
// type says so, so one that names it only by identifier names a patient.
var pasSubjectElements = map[string]bool{"patient": true, "subject": true, "beneficiary": true, "for": true, "subjectReference": true, "patientReference": true}

// pasCarriesIdentifier reports whether p carries the identifier id (a
// Reference's identifier) exactly: its system and value.
func pasCarriesIdentifier(p map[string]any, id any) bool {
	want, ok := id.(map[string]any)
	if !ok {
		return false
	}
	ids, _ := p["identifier"].([]any)
	for _, have := range ids {
		if m, ok := have.(map[string]any); ok && m["system"] == want["system"] && m["value"] == want["value"] {
			return true
		}
	}
	return false
}

// pasPartySlotMark is the member pasPartySlotsRead adds to its read of a
// slot naming an entry party, by which the walk knows that slot's reference
// names the party. Its name holds a byte that is not UTF-8, so no decoded
// document carries it: a decoder refuses or replaces such a byte.
const pasPartySlotMark = "\xffpas-party-slot"

// pasEntryParties is each Patient entry of doc that is a Coverage's party
// (CoverageEntryParties: a dependent's parent carried as an entry of its
// own), by its fullUrl. The rule is read once per Bundle.
func pasEntryParties(doc map[string]any) map[string]map[string]any {
	set := CoverageEntryParties(doc)
	if len(set) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(set))
	entries, _ := doc["entry"].([]any)
	for _, e := range entries {
		em, _ := e.(map[string]any)
		fullURL, _ := em["fullUrl"].(string)
		if res, ok := em["resource"].(map[string]any); ok && set[fullURL] {
			out[fullURL] = res
		}
	}
	return out
}

// pasPartySlotsRead is cov, the resource of entry (a Coverage entry), as the
// walk reads it: a subscriber or policyHolder naming the Coverage's party
// names that person, not the member, so an identifier beside the slot's
// reference that the party carries is the party's and is not read; the rest
// of the slot is. The party is a Patient contained in the Coverage
// (CoverageParty), named by a local reference, which names no patient; or a
// Patient entry of the Bundle (one of entryParties), named by a reference
// that resolves to its fullUrl from entry (BundleReferenceURL): each such
// slot, as read, carries pasPartySlotMark, and its reference names the party,
// not a patient.
func pasPartySlotsRead(entry, cov map[string]any, entryParties map[string]map[string]any) map[string]any {
	out := make(map[string]any, len(cov))
	for k, v := range cov {
		out[k] = v
	}
	contained, _ := cov["contained"].([]any)
	for _, k := range []string{"subscriber", "policyHolder"} {
		slot, _ := cov[k].(map[string]any)
		ref, _ := slot["reference"].(string)
		var party map[string]any
		for _, c := range contained {
			if cr, ok := c.(map[string]any); ok && strings.HasPrefix(ref, "#") && cr["id"] == ref[1:] && CoverageParty(cov, cr) {
				party = cr
			}
		}
		entryParty := false
		if party == nil && ref != "" {
			party = entryParties[BundleReferenceURL(entry, ref)]
			entryParty = party != nil
		}
		if party == nil {
			continue
		}
		read := make(map[string]any, len(slot))
		for m, v := range slot {
			if m == "identifier" && pasCarriesIdentifier(party, v) {
				continue
			}
			read[m] = v
		}
		if entryParty {
			read[pasPartySlotMark] = true
		}
		out[k] = read
	}
	return out
}

// pasMemberIdentifiers reports whether p, a Patient, carries the member
// identifier for member (mine) and whether it carries one for anyone else
// (another).
func pasMemberIdentifiers(p map[string]any, member string) (mine, another bool) {
	ids, _ := p["identifier"].([]any)
	for _, id := range ids {
		m, ok := id.(map[string]any)
		if !ok || m["system"] != MemberSystem {
			continue
		}
		if m["value"] == member {
			mine = true
		} else {
			another = true
		}
	}
	return mine, another
}

// pasCarriesAnotherPatient reports whether a PAS request Bundle is, carries or
// names a patient other than member (its Claim.patient's), anywhere in it: an
// entry, a contained resource or an element nested in either. It is the twin
// of the gateway's own read (pasCarriesAnotherPatient in its engine), and the
// twin-fence corpus holds the two in lockstep:
//   - a Patient is the member when it carries no member identifier for anyone
//     else and is the member by its own identity: an entry by its id or the
//     member identifier (its fullUrl alone does not make it the member), a
//     contained one by the member identifier (its id is local).
//   - a Coverage's party is another person the Coverage names (a dependent's
//     parent), so its own identity is not checked; what it names is. It is a
//     Patient contained in a Coverage entry that the Coverage's subscriber or
//     policyHolder names (CoverageParty; a Coverage carried inside another resource
//     has none), or a Patient entry of its own that only such slots name
//     (CoverageEntryParties, by its exact fullUrl or a relative reference
//     resolving to it from the Coverage entry, never by id alone). That
//     slot's reference, and an identifier the party carries beside it, name
//     the party; any other reference to the party names another patient.
//   - a reference whose identifier is in the member system names that member,
//     in any element, of any type and beside a literal reference or not;
//   - a reference whose reference names "Patient/<id>" names that member
//     (pasMemberFromRef: in either spelling, under any base, versioned or
//     not);
//   - any other reference with no reference that names a patient, by an
//     element whose target is the patient (pasSubjectElements) or by its type,
//     names the member only by one of the member's identifiers: the member
//     identifier, or one the member's Patient entry carries.
//
// A urn:uuid: or #id reference names the entry or contained resource it
// points at, which is read in its own right. The Bundle's own elements are
// read as well. A bundle this cannot read is reported as naming another
// patient: never read as naming no one.
func pasCarriesAnotherPatient(bundleJSON []byte, member string) bool {
	// Numbers are read as written (UseNumber), as the gateway reads them: a
	// decimal outside float64's range is valid FHIR and must not stop the read.
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(bundleJSON))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return true
	}
	entries, _ := doc["entry"].([]any)
	isMemberEntry := func(p map[string]any) bool {
		mine, another := pasMemberIdentifiers(p, member)
		return !another && (mine || p["id"] == member)
	}
	isMemberContained := func(p map[string]any) bool {
		mine, another := pasMemberIdentifiers(p, member)
		return mine && !another
	}
	// A Coverage's party carried as an entry of its own is another person:
	// neither its identity nor its identifiers are the member's.
	entryParties := pasEntryParties(doc)
	own := map[[2]string]bool{{MemberSystem, member}: true}
	for _, e := range entries {
		em, _ := e.(map[string]any)
		res, _ := em["resource"].(map[string]any)
		fullURL, _ := em["fullUrl"].(string)
		if res["resourceType"] != "Patient" || entryParties[fullURL] != nil || !isMemberEntry(res) {
			continue
		}
		ids, _ := res["identifier"].([]any)
		for _, id := range ids {
			if m, ok := id.(map[string]any); ok {
				system, _ := m["system"].(string)
				value, _ := m["value"].(string)
				own[[2]string{system, value}] = true
			}
		}
	}
	namesAnother := func(ref map[string]any, element string) bool {
		// A resource's own identifier (QuestionnaireResponse.identifier is a
		// single Identifier) is not a reference.
		if _, isResource := ref["resourceType"]; isResource {
			return false
		}
		// A member identifier names that member, beside a literal reference
		// or not.
		if id, ok := ref["identifier"].(map[string]any); ok && id["system"] == MemberSystem && id["value"] != member {
			return true
		}
		if literal, ok := ref["reference"].(string); ok && literal != "" {
			// A slot's reference to the Coverage's entry party names that
			// person, not a patient.
			if _, party := ref[pasPartySlotMark]; party {
				return false
			}
			return strings.Contains(literal, "Patient/") && pasMemberFromRef(literal) != member
		}
		id, ok := ref["identifier"].(map[string]any)
		if !ok || id["system"] == MemberSystem {
			return false
		}
		typ, _ := ref["type"].(string)
		if !pasSubjectElements[element] && typ != "Patient" && typ != "http://hl7.org/fhir/StructureDefinition/Patient" {
			return false
		}
		system, _ := id["system"].(string)
		value, _ := id["value"].(string)
		return !own[[2]string{system, value}]
	}
	// visit reads v, found under element; patient says whether a Patient there
	// is checked (not for an entry's own, read below, nor a party's).
	var visit func(v any, element string, patient bool) bool
	visit = func(v any, element string, patient bool) bool {
		switch x := v.(type) {
		case []any:
			for _, c := range x {
				if visit(c, element, patient) {
					return true
				}
			}
		case map[string]any:
			if patient && x["resourceType"] == "Patient" && !isMemberContained(x) {
				return true
			}
			if namesAnother(x, element) {
				return true
			}
			for k, c := range x {
				if visit(c, k, true) {
					return true
				}
			}
		}
		return false
	}
	// The Bundle's own elements (its signature's who and onBehalfOf among
	// them) are read as any other element is.
	for k, v := range doc {
		if k != "entry" && visit(v, k, true) {
			return true
		}
	}
	for _, e := range entries {
		em, _ := e.(map[string]any)
		res, _ := em["resource"].(map[string]any)
		fullURL, _ := em["fullUrl"].(string)
		// A Coverage's party carried as an entry of its own is another person,
		// so its identity is not checked; what it names is, below.
		if res["resourceType"] == "Patient" && entryParties[fullURL] == nil && !isMemberEntry(res) {
			return true
		}
		if res["resourceType"] == "Coverage" {
			// Only a Bundle entry's own Coverage has a party (CoverageParty),
			// as the answer's check reads it: a Coverage carried inside another
			// resource has none, so every Patient it contains is read.
			res = pasPartySlotsRead(em, res, entryParties)
			if list, ok := res["contained"].([]any); ok {
				for _, cr := range list {
					crm, _ := cr.(map[string]any)
					if visit(cr, "contained", crm == nil || !CoverageParty(res, crm)) {
						return true
					}
				}
				rest := make(map[string]any, len(res))
				for k, v := range res {
					if k != "contained" {
						rest[k] = v
					}
				}
				res = rest
			}
		}
		if visit(res, "", false) {
			return true
		}
	}
	return false
}
