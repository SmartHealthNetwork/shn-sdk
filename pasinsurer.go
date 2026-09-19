package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The payer a prior-authorization request names, and why it is the
// participant's own record rather than one this package makes.
//
// It is the third element of the same rule the provider and the coverage
// already follow. Da Vinci PAS says a payer matches an inquiry on the member
// and the provider; the reference payer additionally SCOPES that search by the
// insurer, and it resolves an Organization to one it holds only through an NPI.
// A payer Organization carries a plan identifier, not an NPI, so the one a
// request names is never re-homed and never stored: whatever string the
// submission wrote is the string a later inquiry has to write too.
//
// A request that named a payer Organization this package minted, while the
// inquiry about it named the Organization the participant's own records hold,
// therefore matched nothing — measured live, and isolated to exactly that one
// id. So the request carries the participant's OWN record for the payer: the
// Organization its member's Coverage names as payor, read from its own system.
// Nothing here invents one.

var errPASInsurerRequired = errors.New(
	"the payer's own Organization record is required: this request names the payer, a later inquiry names it from your own records, and the payer scopes its inquiry search by the insurer — so a request naming a payer organization this package made up is one your inquiry can never match")

// pasPayerOrgRecord is the participant's own Organization record for the payer.
type pasPayerOrgRecord struct {
	id  string
	raw []byte
}

// pasPayerOrgEntry prepares the participant's own payer Organization to ride a
// prior-authorization request as the entry Claim.insurer and Coverage.payor
// both name.
//
// It refuses a record that is not one Organization with a FHIR id, and one that
// does not carry the payer identity this exchange routed on — a request whose
// insurer entry named a different payer than the leg was routed to would be two
// payers in one message.
func pasPayerOrgEntry(record []byte, payer PayerIdentifier) (pasPayerOrgRecord, error) {
	if len(strings.TrimSpace(string(record))) == 0 {
		return pasPayerOrgRecord{}, errPASInsurerRequired
	}
	var head struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
		Identifier   []struct {
			System string `json:"system"`
			Value  string `json:"value"`
		} `json:"identifier"`
	}
	if json.Unmarshal(record, &head) != nil {
		return pasPayerOrgRecord{}, errors.New("your payer organization record is not one JSON object")
	}
	if head.ResourceType != "Organization" {
		return pasPayerOrgRecord{}, fmt.Errorf("the payer record is a %q, and a prior authorization names the payer as an Organization", PASResourceTypeOf(record))
	}
	if !fhirIDPattern.MatchString(head.ID) {
		return pasPayerOrgRecord{}, errors.New("your payer organization record has no valid id, so it cannot ride the request as the resolvable entry the Claim names")
	}
	if strings.TrimSpace(payer.System) == "" || strings.TrimSpace(payer.Value) == "" {
		return pasPayerOrgRecord{}, errors.New("the payer identity this exchange routed on is required")
	}
	carries := false
	for _, id := range head.Identifier {
		if id.System == payer.System && id.Value == payer.Value {
			carries = true
		}
	}
	if !carries {
		return pasPayerOrgRecord{}, fmt.Errorf("your payer organization record carries no %s|%s identifier, and this exchange routed to that payer — a request whose insurer names one payer while the leg went to another names two",
			payer.System, payer.Value)
	}
	out, err := stripMetaProfile(record)
	if err != nil {
		return pasPayerOrgRecord{}, fmt.Errorf("strip payer organization meta: %w", err)
	}
	return pasPayerOrgRecord{id: head.ID, raw: out}, nil
}

// checkPASInsurerResolves refuses a built request that CARRIES a payer
// organization whose Claim does not name it.
//
// It is the guard, and it is the provider guard's sibling for the same reason:
// a Reference satisfies Claim.insurer whether or not the payer can read
// anything at it, so a request naming an insurer that resolves to nothing
// passes every structural check and is then stored under a payer identity the
// requester cannot name again.
//
// KNOWN HOLE, older than this guard and deliberately not hidden by it: the
// SHN-native lane (neither PayerOrgEntry nor ContainedInsurer) carries NO payer
// organization at all and leaves Claim.insurer at buildPASClaim's generic
// `Organization/payer`, which nothing resolves. That is the same defect this
// guard exists for, one lane over, and closing it moves the byte-frozen 2.0
// golden and every parity fence pinned to it — so the builders run this guard on
// the lanes that DO carry a payer organization, and the native lane's dangling
// insurer is written down rather than quietly passed.
func checkPASInsurerResolves(bundle []byte) error {
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return fmt.Errorf("read the built request: %w", err)
	}
	organizations := map[string]bool{}
	named, stated := "", false
	for _, e := range b.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Contained    []struct {
				ResourceType string `json:"resourceType"`
				ID           string `json:"id"`
			} `json:"contained"`
			Insurer struct {
				Reference string `json:"reference"`
			} `json:"insurer"`
		}
		if json.Unmarshal(e.Resource, &r) != nil {
			continue
		}
		if r.ResourceType == "Organization" {
			if e.FullURL != "" {
				organizations[e.FullURL] = true
			}
			if r.ID != "" {
				organizations["Organization/"+r.ID] = true
			}
		}
		if r.ResourceType == "Claim" && !stated {
			stated = true
			named = r.Insurer.Reference
			// A contained payer organization is resolvable inside the Claim that
			// contains it — that is the shape the non-entry lanes put on the wire.
			for _, c := range r.Contained {
				if c.ResourceType == "Organization" && c.ID != "" {
					organizations["#"+c.ID] = true
				}
			}
		}
	}
	if !stated {
		return errors.New("the request carries no Claim")
	}
	if named == "" {
		return errors.New("the request's Claim names no insurer by reference, so the payer has no payer identity to store the authorization under")
	}
	if !organizations[named] {
		return fmt.Errorf("the request's Claim names insurer %q, which the Bundle does not resolve to an Organization", named)
	}
	return nil
}

// payerOrgFromCoverageSearch reads the payer Organization out of a Coverage
// search result: the record the member's Coverage names as payor, included in
// the same searchset.
//
// It is how the orchestrator gets what pasPayerOrgEntry requires without asking
// a caller for the same fact twice — PriorAuthRequest.Coverage is documented as
// "the member's Coverage and the payor Organization it names", and this is that
// Organization. A search result that holds no such record is a refusal naming
// what is missing, not a minted payer.
func payerOrgFromCoverageSearch(coverageSearch []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(coverageSearch))) == 0 {
		return nil, errPASInsurerRequired
	}
	entries, isBundle, err := coverageBundleEntries(coverageSearch)
	if err != nil {
		return nil, fmt.Errorf("your Coverage search result: %w", err)
	}
	if !isBundle {
		return nil, errors.New("your Coverage search result is a bare Coverage, so it names no payer organization for this request to carry")
	}
	payorRef := ""
	for _, e := range entries {
		if e.head.ResourceType != "Coverage" {
			continue
		}
		var cov struct {
			Payor []struct {
				Reference string `json:"reference"`
			} `json:"payor"`
		}
		if json.Unmarshal(e.Resource, &cov) == nil && len(cov.Payor) > 0 {
			payorRef = cov.Payor[0].Reference
		}
		break
	}
	if payorRef == "" {
		return nil, errors.New("your Coverage names no payer organization by reference, and a prior authorization names the payer")
	}
	want := payorRef
	if i := strings.LastIndex(want, "/Organization/"); i >= 0 {
		want = want[i+1:]
	}
	for _, e := range entries {
		if e.head.ResourceType != "Organization" {
			continue
		}
		var org struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(e.Resource, &org) == nil && "Organization/"+org.ID == want {
			return e.Resource, nil
		}
	}
	return nil, fmt.Errorf("your Coverage names payer organization %q and your search result does not include it", payorRef)
}
