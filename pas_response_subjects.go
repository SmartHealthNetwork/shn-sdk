package shnsdk

import (
	"net/url"
	"strings"
)

func consistentPASGraphSubjects(g *pasGraph, expected string) bool {
	if expected == "" {
		return false
	}
	// R4 primary subject references use these field names across resources:
	// patient/subject, Coverage.beneficiary, Task.for and subject[x]'s Reference
	// choice. ResearchSubject.individual and EnrollmentRequest.candidate are
	// resource-specific: Encounter.participant.individual identifies a participant. Apply the
	// rule to every object, including contained resources and nested elements;
	// an unfamiliar resource type cannot silently exempt an explicit subject.
	subjectFields := map[string]bool{"patient": true, "subject": true, "beneficiary": true, "for": true, "subjectReference": true, "patientReference": true}
	var boundReference func(any, *pasGraphEntry) bool
	boundReference = func(value any, owner *pasGraphEntry) bool {
		if refs, ok := value.([]any); ok {
			if len(refs) == 0 {
				return false
			}
			for _, ref := range refs {
				if !boundReference(ref, owner) {
					return false
				}
			}
			return true
		}
		ref, ok := value.(map[string]any)
		if !ok {
			return false
		}
		literal, ok := ref["reference"].(string)
		// Identifier-only subjects have no exact graph identity to bind.
		return ok && literal != "" && pasSubjectIdentity(owner, literal) == expected
	}

	found := false
	var visit func(any, *pasGraphEntry, int) bool
	visit = func(v any, owner *pasGraphEntry, depth int) bool {
		switch x := v.(type) {
		case map[string]any:
			if typ, ok := x["resourceType"].(string); ok {
				if typ == "Patient" {
					identity := owner.fullURL
					if depth > 0 {
						id, ok := x["id"].(string)
						if !ok {
							return false
						}
						identity += "#" + id
					}
					if identity != expected {
						return false
					}
					found = true
				}

			}
			for field, value := range x {
				resourceType, _ := x["resourceType"].(string)
				isSubject := subjectFields[field] || (resourceType == "ResearchSubject" && field == "individual") || (resourceType == "EnrollmentRequest" && field == "candidate")
				if isSubject && !boundReference(value, owner) {
					return false
				}
			}
			// A typed Patient Reference is also subject-bearing in polymorphic
			// paths (e.g. actor or extension.valueReference). Do not let an
			// identifier-only form bypass binding simply because its role differs.
			if typ, ok := x["type"].(string); ok && (typ == "Patient" || typ == "http://hl7.org/fhir/StructureDefinition/Patient") {
				if !boundReference(x, owner) {
					return false
				}
			}
			for _, child := range x {
				if !visit(child, owner, depth+1) {
					return false
				}
			}
		case []any:
			for _, child := range x {
				if !visit(child, owner, depth+1) {
					return false
				}
			}
		}
		return true
	}
	for _, entry := range g.byURL {
		if !visit(entry.resource, entry, 0) {
			return false
		}
	}
	return found
}

func pasSubjectIdentity(owner *pasGraphEntry, ref string) string {
	if ref == "#" {
		return owner.fullURL
	}
	if strings.HasPrefix(ref, "#") {
		return owner.fullURL + ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		base, err := url.Parse(owner.fullURL)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") {
			return ""
		}
		typ := owner.resource["resourceType"].(string)
		id := owner.resource["id"].(string)
		base.Path = strings.TrimSuffix(base.Path, "/"+typ+"/"+id) + "/" + u.Path
		ref = base.String()
	}
	if pos := strings.Index(ref, "/_history/"); pos >= 0 {
		ref = ref[:pos]
	}
	return ref
}
