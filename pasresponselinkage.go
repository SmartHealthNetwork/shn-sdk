package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// sentClaimLinkage keeps only references explicitly available in the sent
// request. The first Claim is the operative one, as in the PAS request reader.
// Later Claims cannot add authority for the operative Claim; only a duplicate
// of its own id or fullUrl makes that selected identity ambiguous. Empty refs
// are valid when the sent Claim has only a business identifier; they cannot
// authorize a response that asserts an unknown request URL.
func sentClaimLinkage(request []byte) (json.RawMessage, []string, error) {
	var bundle struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(request, &bundle); err != nil || bundle.ResourceType != "Bundle" {
		return nil, nil, errors.New("PAS sent request unavailable")
	}
	type claimIdentity struct {
		ID      string `json:"id"`
		Related []struct {
			Claim struct {
				Reference string `json:"reference"`
			} `json:"claim"`
		} `json:"related"`
	}
	type candidate struct {
		raw      json.RawMessage
		fullURL  string
		identity claimIdentity
	}
	var claims []candidate
	for _, entry := range bundle.Entry {
		var head struct {
			ResourceType string `json:"resourceType"`
		}
		if json.Unmarshal(entry.Resource, &head) != nil || head.ResourceType != "Claim" {
			continue
		}
		var identity claimIdentity
		if err := json.Unmarshal(entry.Resource, &identity); err != nil && len(claims) == 0 {
			return nil, nil, fmt.Errorf("PAS sent Claim unreadable: %w", err)
		}
		claims = append(claims, candidate{raw: entry.Resource, fullURL: entry.FullURL, identity: identity})
	}
	if len(claims) == 0 {
		return nil, nil, errors.New("PAS sent request has no Claim")
	}
	claim := claims[0]
	for _, extra := range claims[1:] {
		if claim.identity.ID != "" && extra.identity.ID == claim.identity.ID ||
			claim.fullURL != "" && extra.fullURL == claim.fullURL {
			return nil, nil, errors.New("PAS sent request has ambiguous Claims")
		}
	}
	refs := make([]string, 0, 2+len(claim.identity.Related))
	var refBytes int
	var tooLarge bool
	add := func(ref string) {
		if ref != "" && !slices.Contains(refs, ref) {
			refBytes += len(ref)
			if len(ref) > 2048 || refBytes > 64<<10 {
				tooLarge = true
			}
			refs = append(refs, ref)
		}
	}
	if claim.identity.ID != "" {
		add("Claim/" + claim.identity.ID)
	}
	add(claim.fullURL)
	for _, related := range claim.identity.Related {
		add(related.Claim.Reference)
	}
	if len(refs) > 256 {
		return nil, nil, errors.New("PAS sent Claim has too many reference identities")
	}
	if tooLarge {
		return nil, nil, errors.New("PAS sent Claim reference identity too large")
	}
	return claim.raw, refs, nil
}

// ValidatePASResponseLinkage checks request linkage asserted by a submit or
// update answer against the actual sent request. An absent linkage is allowed:
// the caller must separately verify the authenticated exchange correlation and
// the payload's patient before any local clinical use. The payer's own response
// identifiers and newly issued authorization number are not request identifiers.
// This does not validate conformance or authorize a local action.
func ValidatePASResponseLinkage(request, response []byte) error {
	claim, refs, err := sentClaimLinkage(request)
	if err != nil {
		return errors.New("PAS sent request unavailable")
	}
	var sent struct {
		ID         string          `json:"id"`
		Identifier []PASIdentifier `json:"identifier"`
		Related    []struct {
			Claim struct {
				Reference  string         `json:"reference"`
				Identifier *PASIdentifier `json:"identifier"`
			} `json:"claim"`
		} `json:"related"`
		Item []struct {
			Extension []map[string]json.RawMessage `json:"extension"`
		} `json:"item"`
	}
	if json.Unmarshal(claim, &sent) != nil {
		return errors.New("PAS sent request unreadable")
	}
	facts := PriorAuthContinuation{ClaimIdentifiers: sent.Identifier}
	for _, rel := range sent.Related {
		if rel.Claim.Identifier != nil {
			facts.ClaimIdentifiers = append(facts.ClaimIdentifiers, *rel.Claim.Identifier)
		}
	}
	for _, it := range sent.Item {
		for _, e := range it.Extension {
			var url string
			_ = json.Unmarshal(e["url"], &url)
			if url == pasExtItemTraceNumber {
				var id PASIdentifier
				if json.Unmarshal(e["valueIdentifier"], &id) == nil && validIdentifier(id) {
					facts.Items = append(facts.Items, PASInquiryItem{TraceNumber: id})
				}
			}
		}
	}
	responses, err := claimResponsesIn(response)
	if err != nil || len(responses) != 1 {
		return errors.New("PAS answer has no unambiguous response")
	}
	return facts.validateResponseLinkage(responses[0], refs)
}

// ValidateResponseLinkage checks a selected answer's asserted request identifiers
// and item trace numbers against previously recorded continuation facts. Exact
// request references are retained only when the submitted Bundle stated them;
// an older continuation without them cannot infer a URL from an identifier. A
// new payer authorization number is not the original request's identity.
func (c PriorAuthContinuation) ValidateResponseLinkage(response []byte) error {
	responses, err := claimResponsesIn(response)
	if err != nil || len(responses) != 1 {
		return errors.New("PAS answer has no unambiguous response")
	}
	return c.validateResponseLinkage(responses[0], c.ClaimReferences)
}

func (facts PriorAuthContinuation) validateResponseLinkage(response []byte, refs []string) error {
	var cr struct {
		Request json.RawMessage `json:"request"`
		Item    []struct {
			Extension []map[string]json.RawMessage `json:"extension"`
		} `json:"item"`
	}
	if json.Unmarshal(response, &cr) != nil {
		return errors.New("PAS response linkage unreadable")
	}
	if len(cr.Request) > 0 {
		var ref struct {
			Reference  string         `json:"reference"`
			Identifier *PASIdentifier `json:"identifier"`
		}
		if json.Unmarshal(cr.Request, &ref) != nil || (ref.Reference == "" && ref.Identifier == nil) {
			return errors.New("PAS response request linkage unresolved")
		}
		if ref.Reference != "" && !slices.Contains(refs, ref.Reference) {
			return errors.New("PAS response request linkage conflicts")
		}
		if ref.Identifier != nil && !facts.matchesRequestIdentifier(*ref.Identifier) {
			return errors.New("PAS response request linkage conflicts")
		}
	}
	for _, it := range cr.Item {
		for _, e := range it.Extension {
			var url string
			_ = json.Unmarshal(e["url"], &url)
			if url != pasExtItemTraceNumber {
				continue
			}
			var id PASIdentifier
			if json.Unmarshal(e["valueIdentifier"], &id) != nil || !facts.matchesItemTrace(id) {
				return errors.New("PAS response item linkage conflicts")
			}
		}
	}
	return nil
}

func (c PriorAuthContinuation) matchesRequestIdentifier(id PASIdentifier) bool {
	return validIdentifier(id) && slices.Contains(c.ClaimIdentifiers, id)
}
func (c PriorAuthContinuation) matchesItemTrace(id PASIdentifier) bool {
	if !validIdentifier(id) {
		return false
	}
	for _, own := range c.Items {
		if own.TraceNumber == id {
			return true
		}
	}
	return false
}
