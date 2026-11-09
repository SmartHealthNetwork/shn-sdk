package shnsdk

import (
	"encoding/json"
	"strings"
)

// splitTargetVersion splits a reference into the reference to the resource and
// the version it names, if any: "<ref>/_history/<version>", where the version
// is a FHIR id ending the reference. A "/_history/" followed by anything else
// (a base whose path holds one, say) names no version: the reference is read
// whole. ok is always true; it is kept so a caller reads one shape.
func splitTargetVersion(ref string) (base, version string, ok bool) {
	i := strings.LastIndex(ref, "/_history/")
	if i < 0 {
		return ref, "", true
	}
	if version = ref[i+len("/_history/"):]; !fhirIDPattern.MatchString(version) {
		return ref, "", true
	}
	return ref[:i], version, true
}

// targetVersionAgrees reports whether a target's version agrees with the
// resource's meta.versionId: an unversioned target always does, a versioned one
// only when the resource carries that very version. FHIR R4 matches a
// version-specific reference to a Bundle entry by its meta.versionId, so a
// version the resource does not carry cannot resolve in the Bundle.
func targetVersionAgrees(version, versionID string) bool {
	return version == "" || version == versionID
}

// ProvenanceTargetNames reports whether ref, one of a Provenance's target
// references, names the resource resourceType/id it attributes, which rides
// at the Bundle entry whose fullUrl is fullURL: exactly the relative
// "<type>/<id>", or exactly that fullUrl, either one version specific or not
// ("…/_history/<version>"; FHIR R4 says a Provenance target usually is). A
// version must be versionID, the resource's meta.versionId: a target naming a
// version the resource does not carry, the resource carrying none included,
// cannot resolve in the Bundle (FHIR R4 Bundle resolution), so it names
// nothing there. A reference under any other base names another server's
// resource, whatever its path ends in, and an empty type or id names nothing.
// The match is the reference as a whole, never a part of its text. It is the
// payer side's content rule: the SDK's Responder reads a target by it, and so,
// from shn-gateway v1.8.0, does a payer's Smart Gateway. The
// carried update builder carries a target as written and never judges it.
func ProvenanceTargetNames(ref, fullURL, resourceType, id, versionID string) bool {
	if resourceType == "" || id == "" {
		return false
	}
	base, version, ok := splitTargetVersion(ref)
	if !ok || !targetVersionAgrees(version, versionID) {
		return false
	}
	return base == resourceType+"/"+id || (fullURL != "" && base == fullURL)
}

// resourceVersionID is a resource's meta.versionId, or "" when it carries
// none or cannot be read.
func resourceVersionID(raw []byte) string {
	// Read by exact member name, as FHIR JSON names them.
	var r, meta map[string]json.RawMessage
	var version string
	if json.Unmarshal(raw, &r) != nil || json.Unmarshal(r["meta"], &meta) != nil || json.Unmarshal(meta["versionId"], &version) != nil {
		return ""
	}
	return version
}
