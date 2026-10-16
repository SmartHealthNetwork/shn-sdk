package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrPASAnswerSourceAbsent: a PAS answer builder was not given a value the
// answer restates from the request it answers, so nothing is built. The
// builders never supply one of their own (through shn-sdk v0.61.x they wrote
// the claim type "professional" and the placeholder insurer
// Organization/payer).
var ErrPASAnswerSourceAbsent = errors.New("shnsdk: absent in the request the answer restates, so the answer is not built: an answer builder never supplies a value of its own")

// PASAnswerRequest is what a payer's PAS answer restates from the request it
// answers: the request Claim's type and its insurer, each exactly as the
// request wrote it. ClaimResponse.type and ClaimResponse.insurer are 1..1 at
// every PAS line and no other source states them, so the answer builders
// (BuildClaimResponseExact, BuildPendedClaimResponseAtLineExact, BuildDeniedResponseExact and
// their AtLine forms) carry these bytes and refuse without them
// (ErrPASAnswerSourceAbsent).
type PASAnswerRequest struct {
	// ClaimType is the request Claim.type CodeableConcept, as written.
	ClaimType json.RawMessage `json:"claimType,omitempty"`
	// Insurer is the request Claim.insurer Reference, as written: the payer
	// the request is addressed to, which is the payer answering it.
	Insurer json.RawMessage `json:"insurer,omitempty"`
}

// PASAnswerRequestOf reads a PAS request Bundle's first Claim's type and
// insurer, each as written (an answer restates it JSON-equal: its members and
// values, not its whitespace). A value the bytes do not carry stays
// empty, and the answer builders refuse an answer built from it.
func PASAnswerRequestOf(requestBundle []byte) (PASAnswerRequest, error) {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(requestBundle, &b); err != nil {
		return PASAnswerRequest{}, fmt.Errorf("shnsdk: read the request the answer restates: %w", err)
	}
	for _, e := range b.Entry {
		var cl struct {
			ResourceType string          `json:"resourceType"`
			Type         json.RawMessage `json:"type"`
			Insurer      json.RawMessage `json:"insurer"`
		}
		if json.Unmarshal(e.Resource, &cl) == nil && cl.ResourceType == "Claim" {
			return PASAnswerRequest{ClaimType: cl.Type, Insurer: cl.Insurer}, nil
		}
	}
	return PASAnswerRequest{}, fmt.Errorf("shnsdk: the request the answer restates carries no Claim: %w", ErrPASAnswerSourceAbsent)
}

// check refuses an answer whose restated values are absent or not JSON objects.
func (r PASAnswerRequest) check() error {
	for _, v := range []struct {
		element, source string
		raw             json.RawMessage
	}{
		{"ClaimResponse.type", "the request Claim's type (PASAnswerRequest.ClaimType)", r.ClaimType},
		{"ClaimResponse.insurer", "the request Claim's insurer (PASAnswerRequest.Insurer)", r.Insurer},
	} {
		var obj map[string]json.RawMessage
		if pasFactIsAbsent(v.raw) || json.Unmarshal(v.raw, &obj) != nil || len(obj) == 0 {
			return fmt.Errorf("shnsdk: %s from %s: %w", v.element, v.source, ErrPASAnswerSourceAbsent)
		}
	}
	return nil
}
