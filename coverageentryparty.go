package shnsdk

import (
	"net/url"
	"regexp"
	"strings"
)

// A relative literal reference in a Bundle is "<type>/<id>": a FHIR resource
// type and a FHIR id, nothing else.
var bundleRelativeReference = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}/[A-Za-z0-9\-.]{1,64}$`)

// CoverageEntryParties returns the fullUrl of each entry of bundle that is a
// party to a Coverage in that Bundle: another person a Coverage names
// (typically a dependent's parent), carried as a Patient entry of its own
// rather than contained in the Coverage (CoverageParty is that rule).
//
// An entry is a party exactly when all of these hold:
//
//   - bundle's resourceType is "Bundle";
//   - the entry's resource is a Patient that holds as a party (see
//     CoverageParty: it contains nothing, its identifier, if present, is a
//     list, and it spells the members the rule reads exactly);
//   - no other entry has its fullUrl, which carries no query, fragment or
//     trailing slash and is either a urn: or an http or https URL ending
//     "Patient/<its id>"; any id it has is a string;
//   - the subscriber or policyHolder of at least one entry-level Coverage is a
//     single Reference whose own reference member names the entry
//     (BundleReferenceURL; a reference to one version names it only when the
//     entry's meta.versionId is that version);
//   - every such Coverage has a beneficiary, a single Reference naming a
//     Patient entry of the Bundle;
//   - nothing else in the Bundle refers to the entry: not the Claim, not a
//     beneficiary (so the beneficiary is another Patient), not an extension
//     inside the slot, not a resource contained in any entry (a Coverage
//     carried inside another resource is no Coverage the Bundle names), not
//     the Bundle's own elements, a list element of the entry list, a resource
//     member that is no single resource, and not the entry itself ("#" names
//     it too). This reads wider than naming does, so a reference that could
//     mean the entry counts: its fullUrl in any case, or a path holding
//     "Patient/<its id>" under any base, with any version, query, fragment or
//     trailing slash, resolvable from where it stands or not, and in a member
//     named "reference" in any case. A logical reference counts by the
//     identifier it holds, when the entry carries that identifier (an
//     identifier the entry carries, beside the reference of a slot naming that
//     entry, is the slot's), and a search reference to Patient counts against
//     every entry;
//   - every member the rule reads is spelled exactly: no member of the Bundle
//     names resourceType or entry in another case, no entry names fullUrl or
//     resource in another case, no Coverage naming it names resourceType, id,
//     beneficiary, subscriber or policyHolder in another case, no such slot or
//     beneficiary names reference in another case, and no party names meta in
//     another case (nor its meta versionId).
//
// A party's identity is not checked against the patient, and whether the
// beneficiary is the member is the caller's to check: nothing binds or routes
// by a party. A member who is their own subscriber is no party, since the
// beneficiary names that entry too.
//
// A caller decides a Patient entry by its fullUrl in the result, and a slot by
// the fullUrl BundleReferenceURL gives for its reference. One call costs a
// single walk of bundle, however many entries it has.
//
// bundle is as encoding/json decodes it into map[string]any, so as with
// CoverageParty a caller should refuse a document that repeats a member name
// before asking.
func CoverageEntryParties(bundle map[string]any) map[string]bool {
	if rt, _ := bundle["resourceType"].(string); rt != "Bundle" {
		return nil
	}
	if caseVariantMember(bundle, "resourceType", "entry") != "" {
		return nil
	}
	refs := &bundleReferences{urls: map[string]bool{}, ids: map[string]bool{}, identifiers: map[[2]string]bool{}}
	list, isList := bundle["entry"].([]any)
	byURL := make(map[string][]map[string]any, len(list))
	for _, e := range list {
		em, ok := e.(map[string]any)
		if !ok {
			refs.read(e)
			continue
		}
		if caseVariantMember(em, "fullUrl", "resource") != "" {
			return nil
		}
		if fullURL, _ := em["fullUrl"].(string); fullURL != "" {
			byURL[fullURL] = append(byURL[fullURL], em)
		}
	}
	for k, v := range bundle {
		if k != "entry" || !isList {
			refs.member(k, v)
		}
	}
	named := map[string]bool{}
	var beside []slotIdentifier
	for _, e := range list {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		res, single := em["resource"].(map[string]any)
		for k, v := range em {
			if k != "resource" || !single {
				refs.member(k, v)
			}
		}
		slots := coverageNamesParties(byURL, em)
		for k, v := range res {
			if slot, ok := v.(map[string]any); ok && slots && coveragePartySlots[k] {
				ref, _ := slot["reference"].(string)
				if target := bundleEntryNamed(byURL, em, ref); target != nil && caseVariantMember(slot, "reference") == "" {
					// The slot's own reference names the entry; a reference to
					// it anywhere else in the slot (an extension) is elsewhere.
					fullURL, _ := target["fullUrl"].(string)
					for m, c := range slot {
						id, single := c.(map[string]any)
						switch {
						case m == "reference":
						case m == "identifier" && single:
							beside = append(beside, slotIdentifier{fullURL, identifierKey(id)})
							refs.read(c)
						default:
							refs.member(m, c)
						}
					}
					named[fullURL] = true
					continue
				}
			}
			refs.member(k, v)
		}
	}
	if refs.search {
		return nil
	}
	carried := make(map[string]map[[2]string]bool, len(named))
	for fullURL := range named {
		patient, _ := byURL[fullURL][0]["resource"].(map[string]any)
		own, _ := patient["identifier"].([]any)
		carried[fullURL] = make(map[[2]string]bool, len(own))
		for _, e := range own {
			if id, ok := e.(map[string]any); ok {
				carried[fullURL][identifierKey(id)] = true
			}
		}
	}
	for _, b := range beside {
		if !carried[b.fullURL][b.key] {
			refs.identifiers[b.key] = true
		}
	}
	parties := map[string]bool{}
	for fullURL := range named {
		entry := byURL[fullURL][0]
		patient, _ := entry["resource"].(map[string]any)
		if rt, _ := patient["resourceType"].(string); rt != "Patient" || coveragePartyFault(patient) != "" {
			continue
		}
		meta, _ := patient["meta"].(map[string]any)
		if caseVariantMember(patient, "meta") != "" || caseVariantMember(meta, "versionId") != "" {
			continue
		}
		id, isString := patient["id"].(string)
		if _, has := patient["id"]; has && !isString {
			continue
		}
		if bareReference(fullURL) != strings.ToLower(fullURL) || (!strings.HasPrefix(fullURL, "urn:") && bundleEntryBase(entry) == "") {
			continue
		}
		if !coverageRefersTo(patient, "#") && !refs.urls[strings.ToLower(fullURL)] && (id == "" || !refs.ids[id]) && !refs.carriedBy(carried[fullURL]) {
			parties[fullURL] = true
		}
	}
	return parties
}

// bundleReferences collects what the reference members of a Bundle could mean,
// reading wider than BundleReferenceURL does: the rule uses it to exclude.
type bundleReferences struct {
	urls map[string]bool // each reference as bareReference gives it
	ids  map[string]bool // each <id> a reference's path holds as "Patient/<id>"
	// identifiers holds the system and value of each single identifier: a
	// logical reference's.
	identifiers map[[2]string]bool
	search      bool // a reference searches for a Patient ("Patient?...")
}

// carriedBy reports whether own, an entry's identifiers, holds one a logical
// reference holds.
func (r *bundleReferences) carriedBy(own map[[2]string]bool) bool {
	for key := range own {
		if r.identifiers[key] {
			return true
		}
	}
	return false
}

// slotIdentifier is an identifier beside the reference of a slot naming the
// entry at fullURL.
type slotIdentifier struct {
	fullURL string
	key     [2]string
}

// identifierKey is the system and value of an identifier.
func identifierKey(id map[string]any) [2]string {
	system, _ := id["system"].(string)
	value, _ := id["value"].(string)
	return [2]string{system, value}
}

// read records every reference in v at any depth.
func (r *bundleReferences) read(v any) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			r.read(e)
		}
	case map[string]any:
		for k, e := range t {
			r.member(k, e)
		}
	}
}

// member records the member k of an object, with value v: a reference under
// the exact name or in another case, and every reference in v.
func (r *bundleReferences) member(k string, v any) {
	if s, ok := v.(string); ok && strings.EqualFold(k, "reference") {
		r.urls[bareReference(s)] = true
		path, _, _ := strings.Cut(s, "#")
		path, _, query := strings.Cut(path, "?")
		segments := strings.Split(path, "/")
		if query && segments[len(segments)-1] == "Patient" {
			r.search = true
		}
		for i, seg := range segments[:len(segments)-1] {
			if seg == "Patient" {
				r.ids[segments[i+1]] = true
			}
		}
	}
	if id, ok := v.(map[string]any); ok && strings.EqualFold(k, "identifier") {
		r.identifiers[identifierKey(id)] = true
	}
	r.read(v)
}

// bareReference is reference in lower case, with no version, query, fragment
// or trailing slash.
func bareReference(reference string) string {
	s := strings.ToLower(reference)
	if at := strings.IndexAny(s, "#?"); at >= 0 {
		s = s[:at]
	}
	if at := strings.Index(s, "/_history"); at >= 0 {
		s = s[:at]
	}
	return strings.TrimRight(s, "/")
}

// coverageNamesParties reports whether entry's resource is a Coverage whose
// subscriber and policyHolder can name a party: it spells the members the rule
// reads exactly, and its beneficiary is a single Reference naming a Patient
// entry (never the party: that would be a reference elsewhere).
func coverageNamesParties(byURL map[string][]map[string]any, entry map[string]any) bool {
	cov, _ := entry["resource"].(map[string]any)
	if rt, _ := cov["resourceType"].(string); rt != "Coverage" {
		return false
	}
	if caseVariantMember(cov, "resourceType", "id", "beneficiary", "subscriber", "policyHolder") != "" {
		return false
	}
	beneficiary, _ := cov["beneficiary"].(map[string]any)
	if caseVariantMember(beneficiary, "reference") != "" {
		return false
	}
	ref, _ := beneficiary["reference"].(string)
	res, _ := bundleEntryNamed(byURL, entry, ref)["resource"].(map[string]any)
	rt, _ := res["resourceType"].(string)
	return rt == "Patient"
}

// bundleEntryNamed returns the one entry reference, found in entry from,
// names, or nil: the entry at the fullUrl BundleReferenceURL gives, which no
// other entry shares and which, for a reference to a version, is that version.
func bundleEntryNamed(byURL map[string][]map[string]any, from map[string]any, reference string) map[string]any {
	target := BundleReferenceURL(from, reference)
	if len(byURL[target]) != 1 {
		return nil
	}
	found := byURL[target][0]
	at := strings.LastIndex(reference, "/_history/")
	if at < 0 {
		return found
	}
	res, _ := found["resource"].(map[string]any)
	meta, _ := res["meta"].(map[string]any)
	if meta["versionId"] != reference[at+len("/_history/"):] {
		return nil
	}
	return found
}

// BundleReferenceURL returns the fullUrl reference, a literal reference found
// in from (an element of a Bundle's entry list, or nil for the Bundle's own
// elements), points at under FHIR's rules for resolving references in a
// Bundle, or "" when it can point at none:
//
//   - an absolute reference (a URL or a urn:uuid:) is that fullUrl as written;
//   - a relative reference is exactly "<type>/<id>", made absolute with the
//     base of from's fullUrl, which must be an http or https URL ending in
//     from's own resource type and id; it never resolves by id alone;
//   - a trailing "/_history/<version>" is left out of the result: whether the
//     entry is that version is the entry's to say;
//   - a local reference ("#id") points at no entry.
//
// Whether an entry holds that fullUrl, and only one does, is the caller's to
// check.
func BundleReferenceURL(from map[string]any, reference string) string {
	ref := reference
	if at := strings.LastIndex(ref, "/_history/"); at >= 0 {
		if at+len("/_history/") == len(ref) {
			return ""
		}
		ref = ref[:at]
	}
	if u, err := url.Parse(ref); err == nil && u.IsAbs() {
		return ref
	}
	if !bundleRelativeReference.MatchString(ref) {
		return ""
	}
	base := bundleEntryBase(from)
	if base == "" {
		return ""
	}
	return base + "/" + ref
}

// bundleEntryBase returns the base of entry's fullUrl: what precedes
// "/<type>/<id>" of its own resource in an http or https URL, or "" when
// fullUrl is no such URL.
func bundleEntryBase(entry map[string]any) string {
	fullURL, _ := entry["fullUrl"].(string)
	res, _ := entry["resource"].(map[string]any)
	if caseVariantMember(res, "resourceType", "id") != "" {
		return ""
	}
	typ, _ := res["resourceType"].(string)
	id, _ := res["id"].(string)
	if typ == "" || id == "" || !(strings.HasPrefix(fullURL, "http://") || strings.HasPrefix(fullURL, "https://")) {
		return ""
	}
	base, ok := strings.CutSuffix(fullURL, "/"+typ+"/"+id)
	if !ok {
		return ""
	}
	return base
}
