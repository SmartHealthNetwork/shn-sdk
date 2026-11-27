package shnsdk

// patientref.go — a patient reference read the same way in every form FHIR
// allows, as a payer's Smart Gateway reads it. A relative ("Patient/<id>") or
// absolute ("<base>/Patient/<id>", with or without "/_history/<version>")
// reference names the member by its Patient id; a contained one ("#<id>")
// names the contained Patient, whose member is its member identifier (its id
// is local to the resource carrying it). Only a well-formed reference names a
// patient this way: anything else (a trailing path, an operation, an escaped
// character, a reference that resolves to no patient or to more than one)
// names none, and the caller reads or refuses it as it did before.

import (
	"net/url"
	"regexp"
	"strings"
)

// A FHIR id: letters, digits, "-" and ".", at most 64.
const fhirID = `[A-Za-z0-9\-.]{1,64}`

// localRefShape is a well-formed contained reference.
var localRefShape = regexp.MustCompile(`^#(` + fhirID + `)$`)

// fhirIDShape is a FHIR id alone.
var fhirIDShape = regexp.MustCompile(`^` + fhirID + `$`)

// readPatientReference reads a literal reference by FHIR's reference grammar,
// by its structure, as a payer's Smart Gateway reads it, never by finding
// "Patient/" in its text. It names a Patient when it is the relative
// "Patient/<id>", or an http or https URL (a host, no user, query or
// fragment, no empty path segment) whose path ends in "Patient/<id>", either
// with an optional trailing "/_history/<version>". ok is false for anything
// else: another type, another number of segments, an empty id or version, an
// operation ("$<name>") where the id goes, a URN or another scheme, a "#" (a
// contained reference, which its caller resolves), a query, an escaped
// character in the id or version (a "%" escape can hide a "/", so the
// reference would read two ways; a base's own segments may carry one), or a
// URL that does not parse. Such a reference names no Patient.
//
// id is the Patient id as written. conforms reports whether it, and the
// version when there is one, are FHIR ids: an id that names one Patient by
// structure but is not a FHIR id still names that Patient.
func readPatientReference(ref string) (id string, conforms, ok bool) {
	typ, id, conforms, ok := readTypedReference(ref)
	if !ok || typ != "Patient" {
		return "", false, false
	}
	return id, conforms, true
}

// resourceTypeName is a FHIR resource type's name, as a reference's type
// segment spells it.
var resourceTypeName = regexp.MustCompile(`^[A-Z][A-Za-z]{0,63}$`)

// readTypedReference reads a literal reference to a resource of any type by
// the grammar readPatientReference reads a Patient's: typ is its type segment
// (a resource type's name), id its id as written, and conforms and ok as there.
func readTypedReference(ref string) (typ, id string, conforms, ok bool) {
	if ref == "" || strings.ContainsAny(ref, "#?") {
		return "", "", false, false
	}
	path := ref
	absolute := false
	if i := strings.IndexByte(ref, ':'); i >= 0 && !strings.Contains(ref[:i], "/") {
		// A scheme: only an http or https URL is read.
		u, err := url.Parse(ref)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", "", false, false
		}
		// The path as sent, taken from the reference's own text after its
		// authority, never a path rebuilt from the decoded one, where an
		// escaped "/" would read as a separator. A base may carry escapes.
		rest, found := strings.CutPrefix(ref[i+1:], "//")
		j := strings.IndexByte(rest, '/')
		if !found || j < 0 {
			return "", "", false, false
		}
		path, absolute = rest[j+1:], true
	}
	seg := strings.Split(path, "/")
	version := ""
	if n := len(seg); n >= 4 && seg[n-2] == "_history" {
		version, seg = seg[n-1], seg[:n-2]
		if version == "" {
			return "", "", false, false
		}
	}
	n := len(seg)
	if n < 2 || (!absolute && n != 2) || !resourceTypeName.MatchString(seg[n-2]) || seg[n-1] == "" || strings.HasPrefix(seg[n-1], "$") {
		return "", "", false, false
	}
	for _, s := range seg {
		if s == "" {
			return "", "", false, false
		}
	}
	// The segments that name the Patient carry no escape: one can hide a "/",
	// so the reference would read two ways. A base's own segments may.
	if strings.Contains(seg[n-1], "%") || strings.Contains(version, "%") {
		return "", "", false, false
	}
	id = seg[n-1]
	conforms = fhirIDShape.MatchString(id) && (version == "" || fhirIDShape.MatchString(version))
	return seg[n-2], id, conforms, true
}

// patientRefCompared is how a patient-consistency check reads a literal
// reference: the Patient id it names by FHIR's reference grammar
// (readPatientReference), else, for a reference to a patient that names no
// Patient so (patientLike), the reference as written, which is no member the
// request binds. names is false for any other reference (another type, a URN,
// a "#<id>"), which the check resolves or leaves to its own type.
func patientRefCompared(ref string) (member string, names bool) {
	if id, _, ok := readPatientReference(ref); ok {
		return id, true
	}
	if patientLike(ref) {
		return ref, true
	}
	return "", false
}

// patientLike reports whether a reference carries a "Patient" segment, its
// segments split at "/", ":" and "#" and read without surrounding white
// space, as written or with its escapes decoded: a reference to a patient,
// whether or not it names one by FHIR's reference grammar. A "#<id>" and a
// well-formed reference to another type (readTypedReference) are not.
func patientLike(ref string) bool {
	if contained := strings.TrimSpace(ref); strings.HasPrefix(contained, "#") && len(contained) > 1 && !strings.ContainsAny(contained[1:], "/:#") {
		// A "#<id>" names a contained resource by its id, which its caller
		// resolves: never a reference to a patient by its text.
		return false
	}
	if typ, _, _, ok := readTypedReference(ref); ok && typ != "Patient" {
		// A well-formed reference to another type names that type, whatever
		// its base or id spells.
		return false
	}
	if patientSegment(ref) {
		return true
	}
	decoded, err := url.PathUnescape(ref)
	return err == nil && decoded != ref && patientSegment(decoded)
}

// patientSegment reports a "Patient" segment of ref (patientLike).
func patientSegment(ref string) bool {
	for _, s := range strings.FieldsFunc(ref, func(r rune) bool { return r == '/' || r == ':' || r == '#' }) {
		if strings.TrimSpace(s) == "Patient" {
			return true
		}
	}
	return false
}

// claimPatientTarget is the Patient a PAS request's Claim.patient names when
// readPatientReference does not read it: a "#<id>" names the one resource
// the Claim contains with that id, and a URN ("urn:uuid:…", "urn:oid:…")
// names the one entry of the Bundle whose fullUrl it is. doc is the Bundle
// and claim the Claim, each read as a JSON object. contained reports a
// contained Patient. nil is a reference that names no Patient: no such
// resource, more than one, or one of another type. Nothing else is resolved:
// a relative or http reference to another type names that type, never a
// Patient, whatever an entry under it holds.
func claimPatientTarget(doc, claim map[string]any, ref string) (patient map[string]any, contained bool) {
	var found []map[string]any
	switch {
	case strings.HasPrefix(ref, "#"):
		// A contained resource's id is one path segment: "#Patient/1" names
		// no contained resource, whatever one carries as its id.
		id := ref[1:]
		if id == "" || strings.Contains(id, "/") {
			return nil, false
		}
		list, _ := claim["contained"].([]any)
		for _, c := range containedObjects(list) {
			if cid, _ := c["id"].(string); cid == id {
				found = append(found, c)
			}
		}
		contained = true
	case strings.HasPrefix(ref, "urn:"):
		entries, _ := doc["entry"].([]any)
		for _, e := range entries {
			em, _ := e.(map[string]any)
			if u, _ := em["fullUrl"].(string); u == ref {
				r, _ := em["resource"].(map[string]any)
				found = append(found, r)
			}
		}
	}
	if len(found) != 1 || found[0] == nil || found[0]["resourceType"] != "Patient" {
		return nil, false
	}
	return found[0], contained
}

// firstClaim is a PAS Bundle's operative Claim, its first Claim entry's
// resource, read as a JSON object; nil when it carries none.
func firstClaim(doc map[string]any) map[string]any {
	entries, _ := doc["entry"].([]any)
	for _, e := range entries {
		em, _ := e.(map[string]any)
		if r, _ := em["resource"].(map[string]any); r != nil && r["resourceType"] == "Claim" {
			return r
		}
	}
	return nil
}

// patientRefMember returns the member a patient reference names, among the
// resource's contained resources; ok is false when it names none. A "#<id>"
// names the one contained Patient with that id, by its member identifier;
// any other reference is read by FHIR's reference grammar
// (readPatientReference), and names the Patient id it writes, whether or not
// that id is a FHIR id (whether to refuse one that is not is the
// participant's conformance choice, never the network's).
func patientRefMember(ref string, contained []map[string]any) (string, bool) {
	if m := localRefShape.FindStringSubmatch(ref); m != nil {
		id := m[1]
		var found map[string]any
		for _, c := range contained {
			if cid, _ := c["id"].(string); cid != id {
				continue
			}
			if found != nil {
				return "", false // one id naming two contained resources
			}
			found = c
		}
		if found == nil || found["resourceType"] != "Patient" {
			return "", false
		}
		return containedPatientMember(found)
	}
	if id, _, ok := readPatientReference(ref); ok {
		return id, true
	}
	return "", false
}

// containedPatientMember is a contained Patient's member: its member
// identifier, which it must carry exactly once (a contained resource's id is
// local to the resource that carries it, never a member id). The value is an
// identifier's, not limited to a FHIR id's shape.
func containedPatientMember(patient map[string]any) (string, bool) {
	member := ""
	ids, _ := patient["identifier"].([]any)
	for _, id := range ids {
		m, ok := id.(map[string]any)
		if !ok || m["system"] != MemberSystem {
			continue
		}
		v, _ := m["value"].(string)
		if v == "" {
			continue // a blank value names no one
		}
		if member != "" && v != member {
			return "", false // two members: more than one patient
		}
		member = v
	}
	return member, member != ""
}

// containedObjects is a contained list's resources: its elements that are
// JSON objects. Anything else in it names no resource, and reading the
// request does not depend on it.
func containedObjects(list []any) []map[string]any {
	var out []map[string]any
	for _, c := range list {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// pasPatientRefMember is the member a PAS request's patient reference names, read by
// structure as a payer's Smart Gateway reads it: the one reading the inquiry, the
// submission and the update share, for Claim.patient and for each other subject
// they compare with it. holder is the resource that carries ref, whose contained
// list a "#<id>" names into.
//
// A Patient reference (readPatientReference) names its id. A "#<id>" or URN that
// names one Patient (claimPatientTarget: one the holder contains, or the one
// Bundle entry under that fullUrl) names that Patient's member: a contained Patient
// its member identifier (a contained id is local to its holder), an entry its id,
// else its member identifier. Such a Patient that holds no member key names no one
// (noMember), never the reference's text. Any other reference names no Patient:
// "", which names no one.
func pasPatientRefMember(doc, holder map[string]any, ref string) (member string, conforms, noMember bool) {
	if id, conforms, ok := readPatientReference(ref); ok {
		return id, conforms, false
	}
	p, contained := claimPatientTarget(doc, holder, ref)
	if p == nil {
		return "", false, false
	}
	if !contained {
		if id, _ := p["id"].(string); id != "" {
			return id, true, false
		}
	}
	if m, ok := containedPatientMember(p); ok {
		return m, true, false
	}
	return "", true, true
}

// inquiryClaimPatient is the member a PAS inquiry names by its Claim.patient ref
// (pasPatientRefMember), and, for a "#<id>", the contained Patient it names.
func inquiryClaimPatient(doc map[string]any, ref string) (member string, contained map[string]any, noMember bool) {
	member, _, noMember = pasPatientRefMember(doc, firstClaim(doc), ref)
	if p, isContained := claimPatientTarget(doc, firstClaim(doc), ref); isContained {
		contained = p
	}
	return member, contained, noMember
}

// inquiryReferenceKey is the Patient a reference in a PAS inquiry names, as
// "Patient/<member>": a Patient reference by FHIR's grammar names its id, and
// a URN naming the one Patient entry with that fullUrl names that Patient by
// its id, else by its member identifier. "" for a reference that names no
// Patient so.
func inquiryReferenceKey(doc map[string]any, ref string) string {
	if id, _, ok := readPatientReference(ref); ok {
		return "Patient/" + id
	}
	if !strings.HasPrefix(ref, "urn:") {
		return ""
	}
	member, _, _ := inquiryClaimPatient(doc, ref)
	if member == "" {
		return ""
	}
	return "Patient/" + member
}
