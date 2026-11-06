package shnsdk

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// provenanceIDFor is the id BuildProvenanceRecord gives the Provenance it
// builds for targetRef when the caller sets none: "prov-<target id>", the target id being the last
// segment of the reference (its version, if any, left off as splitTargetVersion
// reads one). When that is not a
// valid FHIR id (longer than 64 characters, or the target id holds a character
// a FHIR id cannot), it is "prov-", at most the first 50 FHIR-id characters of
// the target id, "-" and the first 8 hex digits of the SHA-256 of the whole
// target reference: deterministic, valid, and never the target's own id. Two
// Provenances built for one target without an ID get one id, so a caller
// building more than one sets its own.
func provenanceIDFor(targetRef string) string {
	// The version is read off as ProvenanceTargetNames reads it: only a
	// "/_history/<FHIR id>" ending the reference is one.
	ref, _, _ := splitTargetVersion(targetRef)
	tid := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		tid = ref[i+1:]
	}
	tid = strings.TrimPrefix(tid, "#")
	if id := "prov-" + tid; tid != "" && fhirIDPattern.MatchString(id) {
		return id
	}
	kept := make([]byte, 0, 50)
	for i := 0; i < len(tid) && len(kept) < 50; i++ {
		if c := tid[i]; strings.IndexByte(fhirIDChars, c) >= 0 {
			kept = append(kept, c)
		}
	}
	sum := sha256.Sum256([]byte(targetRef))
	return "prov-" + string(kept) + "-" + hex.EncodeToString(sum[:4])
}

// fhirIDChars are the characters a FHIR id may hold.
const fhirIDChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-."
