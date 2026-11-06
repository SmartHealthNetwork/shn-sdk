package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	fhir "github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// ProvenanceInputs are what BuildProvenanceRecord attributes: a record
// (Target), its source (Agent or AgentIdentifier, exactly one), when it was
// recorded and, optionally, the policy and purpose it was disclosed under.
type ProvenanceInputs struct {
	// ID is the Provenance's own id. When empty it is "prov-<target id>", cut
	// to a valid FHIR id with a digest of the target when it would not be one.
	// A Provenance records one activity: callers building more than one
	// Provenance for the same target should set their own id. An ID that is
	// not a valid FHIR id is refused.
	ID string
	// Target references the record the Provenance attributes, such as
	// "DiagnosticReport/<id>". Required.
	Target string
	// Agent names the source by a literal reference ("Organization/<id>",
	// "Patient/<id>").
	Agent string
	// AgentIdentifier names the source by a holder id or an NPI, without
	// asserting that a resource for it exists.
	AgentIdentifier *ProvenanceIdentifier
	// Policy, when set, cites the authorizing policy ("Consent/<id>") in
	// Provenance.policy.
	Policy string
	// PurposeOfUse, when set, is the v3-ActReason code in Provenance.reason.
	PurposeOfUse string
	// Recorded is when the activity was recorded (injected; deterministic).
	Recorded time.Time
}

// BuildProvenanceRecord builds a Provenance attributing supplemental data to
// its source (FR-32), with an id, so it can ride a PAS update under its own id
// (BuildConformantClaimUpdateBundleCarried). It refuses inputs with no target,
// with an ID that is not a valid FHIR id, or naming the source by neither or by
// both of Agent and AgentIdentifier.
func BuildProvenanceRecord(in ProvenanceInputs) ([]byte, error) {
	if strings.TrimSpace(in.Target) == "" {
		return nil, errors.New("shnsdk: BuildProvenanceRecord: Target is required")
	}
	id := in.ID
	if id == "" {
		id = provenanceIDFor(in.Target)
	} else if !fhirIDPattern.MatchString(id) {
		return nil, fmt.Errorf("shnsdk: BuildProvenanceRecord: ID %q is not a valid FHIR id", id)
	}
	var who fhir.Reference
	switch {
	case in.Agent != "" && in.AgentIdentifier != nil, in.Agent == "" && in.AgentIdentifier == nil:
		return nil, errors.New("shnsdk: BuildProvenanceRecord: name the source by exactly one of Agent and AgentIdentifier")
	case in.Agent != "":
		who = fhir.Reference{Reference: strPtr(in.Agent)}
	default:
		if err := in.AgentIdentifier.validate(); err != nil {
			return nil, fmt.Errorf("shnsdk: BuildProvenanceRecord: %w", err)
		}
		who = fhir.Reference{Identifier: &fhir.Identifier{System: strPtr(in.AgentIdentifier.System), Value: strPtr(in.AgentIdentifier.Value)}}
	}
	prov := fhir.Provenance{
		Id:       strPtr(id),
		Target:   []fhir.Reference{{Reference: strPtr(in.Target)}},
		Recorded: in.Recorded.UTC().Format(time.RFC3339),
		Agent:    []fhir.ProvenanceAgent{{Who: who}},
	}
	if in.Policy != "" {
		prov.Policy = []string{in.Policy}
	}
	if in.PurposeOfUse != "" {
		prov.Reason = []fhir.CodeableConcept{{
			Coding: []fhir.Coding{{
				System: strPtr("http://terminology.hl7.org/CodeSystem/v3-ActReason"),
				Code:   strPtr(in.PurposeOfUse),
			}},
		}}
	}
	raw, err := json.Marshal(prov)
	if err != nil {
		return nil, fmt.Errorf("shnsdk: marshal Provenance: %w", err)
	}
	return pasInjectResourceType(raw, "Provenance")
}
