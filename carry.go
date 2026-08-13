package shnsdk

import (
	"encoding/json"
	"fmt"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// CarriedContentExtURL / LossReportExtURL are the two canonical extension
// URLs the SHN IG package profiles and every validator lane loads (the
// cross-version transform loss policy). CarriedContentExtURL is the
// "shn-carried-content" complex extension: when a downcast drops an element
// with no honest byte-level target-line equivalent, the ORIGINAL element is
// carried verbatim so a later upcast restores it byte-faithfully
// (Restore(Carry(x)) == x). LossReportExtURL ("shn-loss-report") carries the
// machine-readable LossReport JSON (see provenance.go) on the Provenance a
// transform emits — these two URLs are the vocabulary + IG package that lets
// both resolve cleanly in every lane.
const (
	CarriedContentExtURL = "http://smarthealth.network/fhir/StructureDefinition/shn-carried-content"
	LossReportExtURL     = "http://smarthealth.network/fhir/StructureDefinition/shn-loss-report"
)

// carrySubExt is the fixed shape of shn-carried-content's three
// sub-extensions — see the shn-carried-content StructureDefinition:
// path/sourceLine/content, each a plain valueString.
const (
	carrySubPath       = "path"
	carrySubSourceLine = "sourceLine"
	carrySubContent    = "content"
)

// CarryElement builds one shn-carried-content extension (spec §5): path
// locates the element within the payload (a FHIRPath-ish string, caller-
// supplied — this function does not validate it), element is the ORIGINAL
// element's JSON exactly as it appeared on the source-line payload (stored
// verbatim, byte-for-byte, so RestoreCarried can hand it back unchanged),
// and sourceLine names the contract line the element came from (e.g.
// "2.2"). All three are required — a carry-extension with a blank field is
// not a meaningful record of what was lost.
func CarryElement(path string, element json.RawMessage, sourceLine string) (json.RawMessage, error) {
	if path == "" {
		return nil, fmt.Errorf("shnsdk: CarryElement: path is empty")
	}
	if sourceLine == "" {
		return nil, fmt.Errorf("shnsdk: CarryElement: sourceLine is empty")
	}
	if len(element) == 0 {
		return nil, fmt.Errorf("shnsdk: CarryElement: element is empty")
	}

	ext := fhir.Extension{
		Url: CarriedContentExtURL,
		Extension: []fhir.Extension{
			{Url: carrySubPath, ValueString: strPtr(path)},
			{Url: carrySubSourceLine, ValueString: strPtr(sourceLine)},
			{Url: carrySubContent, ValueString: strPtr(string(element))},
		},
	}
	raw, err := json.Marshal(ext)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: CarryElement: marshal: %w", err)
	}
	return raw, nil
}

// RestoreCarried parses a shn-carried-content extension (as produced by
// CarryElement, from this module or the network's own internal twin — the
// two are byte-parity partners) back into its three fields. element comes
// back byte-identical to what CarryElement
// was given (Restore(Carry(x)) == x, pinned by TestCarryRestoreRoundTrip).
// Fails loudly — never returns a zero-value success — on a wrong top-level
// url, invalid JSON, or a missing/malformed sub-extension.
func RestoreCarried(extension json.RawMessage) (path string, element json.RawMessage, sourceLine string, err error) {
	var ext fhir.Extension
	if uerr := json.Unmarshal(extension, &ext); uerr != nil {
		return "", nil, "", fmt.Errorf("shnsdk: RestoreCarried: unmarshal: %w", uerr)
	}
	if ext.Url != CarriedContentExtURL {
		return "", nil, "", fmt.Errorf("shnsdk: RestoreCarried: url = %q, want %q", ext.Url, CarriedContentExtURL)
	}

	var haveContent bool
	for _, sub := range ext.Extension {
		switch sub.Url {
		case carrySubPath:
			if sub.ValueString != nil {
				path = *sub.ValueString
			}
		case carrySubSourceLine:
			if sub.ValueString != nil {
				sourceLine = *sub.ValueString
			}
		case carrySubContent:
			if sub.ValueString != nil {
				element = json.RawMessage(*sub.ValueString)
				haveContent = true
			}
		}
	}
	if path == "" {
		return "", nil, "", fmt.Errorf("shnsdk: RestoreCarried: missing %q sub-extension", carrySubPath)
	}
	if sourceLine == "" {
		return "", nil, "", fmt.Errorf("shnsdk: RestoreCarried: missing %q sub-extension", carrySubSourceLine)
	}
	if !haveContent {
		return "", nil, "", fmt.Errorf("shnsdk: RestoreCarried: missing %q sub-extension", carrySubContent)
	}
	return path, element, sourceLine, nil
}
