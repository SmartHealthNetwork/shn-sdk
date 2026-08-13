package shnsdk

import (
	"encoding/json"
	"fmt"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// LossReport / LossEntry are the CANONICAL WIRE SCHEMA for the
// shn-loss-report extension (the shn-loss-report StructureDefinition):
// what a cross-version transform module carried, synthesized, or otherwise
// changed bridging one leg from Source to Target (the network's
// cross-version loss policy).
//
// Layering note: an SHN gateway's own internal transform machinery defines
// its own equivalent LossReport/LossEntry types — deliberately NOT these
// types, since that machinery has no published-SDK surface — and the sdk
// module never imports gateway-internal packages (only the reverse holds:
// a gateway already imports shnsdk). These sdk types are instead the single
// canonical JSON encoding: field-for-field identical (same names, same
// order, same json tags) to the gateway's own internal type, so
// json.Marshal of either produces byte-identical output for equivalent
// values. A gateway converts its own internal loss rows to these sdk types,
// element by element, before calling BuildTransformProvenance below — a
// type conversion, not a re-derivation, so the two can never encode a loss
// report two different ways.
type LossReport struct {
	Module      string      `json:"module"` // "pa.pas 2.1->2.2"
	Source      string      `json:"source"`
	Target      string      `json:"target"`
	Carried     []LossEntry `json:"carried,omitempty"`     // moved into shn-carried-content
	Synthesized []LossEntry `json:"synthesized,omitempty"` // deterministically minted (upcast-mandatory)
}

// LossEntry is one carried or synthesized element within a LossReport — see
// LossReport's doc comment.
type LossEntry struct {
	Path   string `json:"path"`             // FHIRPath-ish locator of the element
	Detail string `json:"detail,omitempty"` // e.g. "authorizationNumber carried; source line 2.2"
}

// transformActivitySystem is the SHN-owned CodeSystem for the
// Provenance.activity coding BuildTransformProvenance stamps: Code = the
// module id (e.g. "pa.pas 2.1->2.2"), Display = "<sourceLine>-><targetLine>".
// Same "urn:shn:*" idiom as pasCorrelationSystem / pasBundleIdentifierSystem.
const transformActivitySystem = "urn:shn:transform:module"

// BuildTransformProvenance builds the Provenance a cross-version transform
// leg attaches to its output payload:
//
//   - agent.who = agentWho, the gateway's own self-attribution reference —
//     the SAME "Organization/"+HolderID idiom every other self-attributed
//     Provenance on a leg already uses; caller-supplied, like
//     BuildProvenanceWithPolicy's agentWho, because only the caller (the
//     running gateway instance) knows its own HolderID.
//   - activity names the module: both a Coding (System=transformActivitySystem,
//     Code=moduleID, Display="sourceLine->targetLine") and Text=moduleID, so
//     the resource is both machine- and human-readable without a
//     terminology lookup.
//   - the shn-loss-report extension (LossReportExtURL) carries loss marshaled
//     as a single JSON array in Extension.valueString — round-trippable back
//     into []LossReport via RestoreTransformLoss. A nil loss still produces
//     a well-formed "[]" array, never an absent extension or null, so a
//     downstream reader never special-cases "no extension" vs "empty loss".
//
// recorded is a PARAMETER, not sampled from a clock — this builder stays
// pure like BuildProvenance/BuildProvenanceWithPolicy (no I/O, no clock).
//
// Hard rule: this is a payload-internal FHIR resource ONLY. It carries no
// envelope/Metadata field and is never visible to the Hub by construction —
// the Hub only ever sees the opaque envelope, never payload content.
func BuildTransformProvenance(targetRef, agentWho, moduleID, sourceLine, targetLine string, loss []LossReport, recorded time.Time) ([]byte, error) {
	if loss == nil {
		loss = []LossReport{}
	}
	lossJSON, err := json.Marshal(loss)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: BuildTransformProvenance: marshal loss reports: %w", err)
	}

	prov := fhir.Provenance{
		Target:   []fhir.Reference{{Reference: strPtr(targetRef)}},
		Recorded: recorded.UTC().Format(time.RFC3339),
		Agent:    []fhir.ProvenanceAgent{{Who: fhir.Reference{Reference: strPtr(agentWho)}}},
		Activity: &fhir.CodeableConcept{
			Coding: []fhir.Coding{{
				System:  strPtr(transformActivitySystem),
				Code:    strPtr(moduleID),
				Display: strPtr(sourceLine + "->" + targetLine),
			}},
			Text: strPtr(moduleID),
		},
		Extension: []fhir.Extension{{
			Url:         LossReportExtURL,
			ValueString: strPtr(string(lossJSON)),
		}},
	}
	raw, err := json.Marshal(prov)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal transform Provenance: %w", err)
	}
	return pasInjectResourceType(raw, "Provenance")
}

// RestoreTransformLoss parses a shn-loss-report extension (as produced by
// BuildTransformProvenance, from this module or the substrate-internal twin
// — the two are byte-parity partners, test/sdkparity) back into
// []LossReport, exact (RestoreTransformLoss(...) after BuildTransformProvenance
// returns the original slice — pinned by TestBuildTransformProvenanceLossRoundTrip).
// Fails loudly — never returns a zero-value success — on a wrong top-level
// url, a missing value, or invalid JSON.
func RestoreTransformLoss(extension json.RawMessage) ([]LossReport, error) {
	var ext fhir.Extension
	if err := json.Unmarshal(extension, &ext); err != nil {
		return nil, fmt.Errorf("shnsdk: RestoreTransformLoss: unmarshal: %w", err)
	}
	if ext.Url != LossReportExtURL {
		return nil, fmt.Errorf("shnsdk: RestoreTransformLoss: url = %q, want %q", ext.Url, LossReportExtURL)
	}
	if ext.ValueString == nil {
		return nil, fmt.Errorf("shnsdk: RestoreTransformLoss: missing value")
	}
	var loss []LossReport
	if err := json.Unmarshal([]byte(*ext.ValueString), &loss); err != nil {
		return nil, fmt.Errorf("shnsdk: RestoreTransformLoss: unmarshal loss JSON: %w", err)
	}
	return loss, nil
}
