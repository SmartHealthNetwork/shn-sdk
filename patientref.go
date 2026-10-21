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

import "regexp"

// A FHIR id: letters, digits, "-" and ".", at most 64.
const fhirID = `[A-Za-z0-9\-.]{1,64}`

// patientRefShape is a well-formed relative or absolute Patient reference: an
// optional http(s) base of plain path segments, then Patient/<id>, then an
// optional /_history/<version>, and nothing else.
var patientRefShape = regexp.MustCompile(`^(?:https?://[A-Za-z0-9.\-]+(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~\-]+)*/)?Patient/(` + fhirID + `)(?:/_history/` + fhirID + `)?$`)

// localRefShape is a well-formed contained reference.
var localRefShape = regexp.MustCompile(`^#(` + fhirID + `)$`)

// fhirIDShape is a FHIR id alone.
var fhirIDShape = regexp.MustCompile(`^` + fhirID + `$`)

// patientRefMember returns the member a patient reference names, among the
// resource's contained resources; ok is false when it names none.
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
	if m := patientRefShape.FindStringSubmatch(ref); m != nil {
		return m[1], true
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
