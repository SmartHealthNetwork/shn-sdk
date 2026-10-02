package shnsdk

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// coveragePartySlots are the Coverage elements that name a party to the
// coverage other than the beneficiary, each a single Reference: a dependent's
// Coverage names the parent there.
var coveragePartySlots = map[string]bool{"subscriber": true, "policyHolder": true}

// CoverageParty reports whether contained, an element of coverage's
// contained list, is a party to the coverage: another person the Coverage
// names (typically a dependent's parent, carrying only an MRN), carried as
// part of the Coverage rather than as the record of the patient the Coverage
// is for.
//
// contained is a party exactly when all of these hold:
//
//   - contained is itself an element of coverage's contained list (the same
//     map, not a copy or a lookalike carrying the same id);
//   - coverage's resourceType is "Coverage" (only a Coverage has a party; a
//     member named like the slots on another resource is no slot);
//   - its resourceType is "Patient" (a RelatedPerson is never a party; its
//     own patient reference says whose it is), and its id is a non-empty
//     string that no other resource in the Coverage's contained list has,
//     under "id" or a member named id in another case (a duplicate id makes
//     neither copy a party: "#<id>" cannot say which it names);
//   - the Coverage's subscriber or policyHolder, or both, is a single
//     Reference whose own reference member is exactly "#" followed by that id
//     ("#" alone names the Coverage itself, never a contained resource, so a
//     party's id is never empty);
//   - nothing else in the Coverage refers to it: not the beneficiary, the
//     payor or any other element, not an extension (including one inside
//     the slot beside the slot's reference), not another contained resource,
//     and not the party itself (a member named "reference" in another case
//     refers to it too);
//   - it holds as a contained resource: it contains nothing (any contained
//     member counts, an empty list or null included), and its identifier, if
//     present, is a list;
//   - every member the rule reads is spelled exactly: no member of the
//     Coverage names resourceType, contained, subscriber or policyHolder in
//     another case, no member of the slot names reference in another case,
//     and no member of the party names resourceType, id, contained or
//     identifier in another case (a reader that ignores case would read
//     something else).
//
// A party's identity is not checked against the patient: it is another
// person. Nothing binds or routes by a party: the beneficiary still binds the
// Coverage.
//
// Both maps are as encoding/json decodes them into map[string]any, which keeps
// only one of two members with the same name, so a caller should refuse a
// document that repeats a member name before asking. This package's builders
// refuse a repeat exactly or in another case; its PAS response check's decoder
// refuses an exact repeat only, and the rule's own case checks above close the
// rest. Member names are matched exactly ("Subscriber" is not a slot), and a
// member that holds no reference (a number, a boolean, a member a reader
// added under a key no JSON document can carry) changes nothing.
//
// This package's PAS request builders and PAS response check decide a
// Coverage's party by it. Its rows are the table in
// testdata/coverageparty/rows.json.
func CoverageParty(coverage, contained map[string]any) bool {
	if rt, _ := coverage["resourceType"].(string); rt != "Coverage" {
		return false
	}
	// The members the rule reads are read by their exact names; a Coverage
	// that also spells one in another case says two things to two readers.
	if caseVariantMember(coverage, "resourceType", "contained", "subscriber", "policyHolder") != "" {
		return false
	}
	if rt, _ := contained["resourceType"].(string); rt != "Patient" {
		return false
	}
	// A party holds as a contained resource: it contains nothing, its
	// identifier, if present, is a list, and it spells the members the rule
	// reads exactly.
	if coveragePartyFault(contained) != "" {
		return false
	}
	id, _ := contained["id"].(string)
	if id == "" {
		// "#" alone names the containing resource, never a contained one.
		return false
	}
	// contained must be the list's own element, and "#<id>" names one
	// contained resource only when no other has that id.
	list, _ := coverage["contained"].([]any)
	self := reflect.ValueOf(contained).UnsafePointer()
	element, same := false, 0
	for _, c := range list {
		cr, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if reflect.ValueOf(cr).UnsafePointer() == self {
			element = true
		}
		// An id spelled in another case ("Id") counts: a reader that
		// ignores case names that resource by it.
		for k, v := range cr {
			if v == id && strings.EqualFold(k, "id") {
				same++
			}
		}
	}
	if !element || same != 1 {
		return false
	}
	ref := "#" + id
	inSlot := false
	for k, v := range coverage {
		if slot, ok := v.(map[string]any); ok && coveragePartySlots[k] && slot["reference"] == ref && caseVariantMember(slot, "reference") == "" {
			// The slot's own reference names the party; a reference to it
			// anywhere else in the slot (an extension) is elsewhere.
			for m, e := range slot {
				if m != "reference" && coverageRefersTo(e, ref) {
					return false
				}
			}
			inSlot = true
			continue
		}
		if coverageRefersTo(v, ref) {
			return false
		}
	}
	return inSlot
}

// coverageRefersTo reports whether v holds a Reference whose reference member,
// under its exact name or in another case ("Reference"), is ref, at any depth.
func coverageRefersTo(v any, ref string) bool {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if coverageRefersTo(e, ref) {
				return true
			}
		}
	case map[string]any:
		for k, e := range t {
			if s, ok := e.(string); ok && s == ref && strings.EqualFold(k, "reference") {
				return true
			}
		}
		for _, e := range t {
			if coverageRefersTo(e, ref) {
				return true
			}
		}
	}
	return false
}

// coveragePartyFault reports why party, a contained Patient, cannot be a
// Coverage's party as a contained resource, or "" when it can: it spells
// resourceType, id, contained and identifier exactly (no member names one of
// them in another case), contains nothing, and its identifier, if present, is
// a list. Its identity is not checked.
func coveragePartyFault(party map[string]any) string {
	if k := caseVariantMember(party, "resourceType", "id", "contained", "identifier"); k != "" {
		return fmt.Sprintf("has a member named %q, a name the rule reads spelled in another case", k)
	}
	if _, ok := party["contained"]; ok {
		return "contains other resources"
	}
	if raw, ok := party["identifier"]; ok {
		if _, ok := raw.([]any); !ok {
			return "has an identifier that is not a list"
		}
	}
	return ""
}

// caseVariantMember returns the first member of m, in name order, whose name
// equals one of names under case folding but not exactly, or "" when none
// does.
func caseVariantMember(m map[string]any, names ...string) string {
	var found []string
	for k := range m {
		for _, n := range names {
			if k != n && strings.EqualFold(k, n) {
				found = append(found, k)
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	slices.Sort(found)
	return found[0]
}
