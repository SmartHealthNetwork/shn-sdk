package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The party a prior-authorization request names, and the one rule that picks it.
//
// Da Vinci PAS says a payer SHALL match an inquiry on the patient's member or
// subscriber id PLUS the ordering and/or rendering provider identifier. The
// party is therefore not decoration on a submitted request: it is half of what
// makes that request findable again. Claim.provider is a PAS Requestor, which
// the published packages type as an Organization or a PractitionerRole; a bare
// Practitioner is a real ordering clinician and a perfectly good FHIR
// requester, but it is not what that element accepts, so the request names the
// clinician's ROLE or ORGANIZATION instead.
//
// THE SUBMIT AND THE INQUIRY MUST PICK THE SAME PARTY. If a submission named
// the ordering PractitionerRole while the inquiry about it named the rendering
// Organization, the payer's match would fail for exactly the reason a
// party-less submission fails — just later, and harder to find. That is why
// this rule lives here, in the one module both builders are in, rather than in
// each caller.

// PASProviderTypes are the resource types a Da Vinci PAS Claim.provider
// accepts, per the published PAS packages: the PAS Requestor is an
// Organization or a PractitionerRole.
var PASProviderTypes = []string{"Organization", "PractitionerRole"}

// pasNPISystem is the NPI identifier system a payer matches a request on.
const pasNPISystem = "http://hl7.org/fhir/sid/us-npi"

// PASProviderResolver reads a record out of a participant's own system by the
// reference an order states. found is false when the system holds no such
// record, which is a different fact from a read that failed.
type PASProviderResolver func(reference string) (resource []byte, found bool, err error)

// SelectPASProvider picks the party a prior-authorization request about this
// order names, and returns the reference the order stated together with the
// record the participant's own system holds for it.
//
// It walks the references the order states, in the order the order states them
// — the requester first, because that is who asked for the service — and takes
// the first that resolves to a type a prior-authorization request can carry. An
// order whose requester is a bare Practitioner therefore falls through to its
// performer rather than failing, and an order that names nobody a request can
// carry is refused HERE, naming the reference and the type that was found.
//
// Nothing is invented. A party the order does not name, or one the
// participant's system cannot supply, is a refusal — never a minted identity.
func SelectPASProvider(order []byte, resolve PASProviderResolver) (reference string, resource []byte, err error) {
	refs := pasProviderRefs(order)
	if len(refs) == 0 {
		return "", nil, errors.New("the order names no requesting provider, and a prior-authorization request must name one")
	}
	if resolve == nil {
		return "", nil, errors.New("no reader was given for the participant's own records")
	}
	var found []string
	for _, r := range refs {
		res, ok, err := resolve(r)
		if err != nil {
			return "", nil, fmt.Errorf("read the requesting provider from the system of record: %w", err)
		}
		if !ok {
			return "", nil, errors.New("the system of record has no requesting provider " + r)
		}
		rt := PASResourceTypeOf(res)
		if slices.Contains(PASProviderTypes, rt) {
			return r, res, nil
		}
		found = append(found, r+" is a "+rt)
	}
	return "", nil, errors.New(
		"the order names no provider a prior-authorization request can carry (it names an Organization or a PractitionerRole): " +
			strings.Join(found, "; "))
}

// PASResourceTypeOf reads a resource's own type, or "an unreadable resource"
// when it states none — which is a fact worth reporting rather than an empty
// string in the middle of a sentence.
func PASResourceTypeOf(resource []byte) string {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if json.Unmarshal(resource, &probe) != nil || probe.ResourceType == "" {
		return "an unreadable resource"
	}
	return probe.ResourceType
}

// PASProviderNPI reads the NPI a provider record carries, or "" when it carries
// none. A payer matches on the ordering or rendering provider identifier, so
// this is the identifier a requester keeps beside the party it sent.
func PASProviderNPI(resource []byte) string {
	var probe struct {
		Identifier []struct {
			System string `json:"system"`
			Value  string `json:"value"`
		} `json:"identifier"`
	}
	if json.Unmarshal(resource, &probe) != nil {
		return ""
	}
	for _, id := range probe.Identifier {
		if id.System == pasNPISystem && strings.TrimSpace(id.Value) != "" {
			return id.Value
		}
	}
	return ""
}

// pasProviderEntry checks the provider record a prior-authorization request is
// about to name and returns the bundle-local reference and the fullUrl it rides
// at. A request whose provider the Bundle does not resolve is not a request a
// Da Vinci payer can adjudicate — the reference payer's response-graph walk
// refuses it — so the party the Claim names always travels with it.
func pasProviderEntry(provider []byte) (ref, fullURL string, err error) {
	if len(strings.TrimSpace(string(provider))) == 0 {
		return "", "", errors.New("the requesting provider record is required: a payer matches a request on the member id plus the ordering or rendering provider identifier")
	}
	var head struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if json.Unmarshal(provider, &head) != nil {
		return "", "", errors.New("the requesting provider record is not one JSON object")
	}
	if !slices.Contains(PASProviderTypes, head.ResourceType) {
		return "", "", fmt.Errorf("the requesting provider is a %q, and a prior-authorization request names an Organization or a PractitionerRole",
			PASResourceTypeOf(provider))
	}
	if !fhirIDPattern.MatchString(head.ID) {
		return "", "", errors.New("the requesting provider record has no valid id, so it cannot ride the request as a resolvable entry")
	}
	return head.ResourceType + "/" + head.ID, pasBundleBaseURL + "/" + head.ResourceType + "/" + head.ID, nil
}

// checkPASProviderResolves refuses a built request whose Claim names a provider
// the Bundle does not resolve.
//
// It is the guard, not a restatement of the builders: a Reference satisfies
// Claim.provider 1..1 at every PAS line whether or not it identifies anybody
// reachable, so $validate certifies such a request while the payer that stores
// it has nothing to match a later inquiry against. The check reads the assembled
// bytes and resolves the reference the way a payer does — against the entries'
// own fullUrls, and against the relative form of each entry's identity.
func checkPASProviderResolves(bundle []byte) error {
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return fmt.Errorf("read the built request: %w", err)
	}
	resolvable := map[string]bool{}
	claimProvider := ""
	for _, e := range b.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Provider     struct {
				Reference string `json:"reference"`
			} `json:"provider"`
		}
		if json.Unmarshal(e.Resource, &r) != nil {
			continue
		}
		if e.FullURL != "" {
			resolvable[e.FullURL] = true
		}
		if r.ResourceType != "" && r.ID != "" {
			resolvable[r.ResourceType+"/"+r.ID] = true
		}
		if r.ResourceType == "Claim" && claimProvider == "" {
			claimProvider = r.Provider.Reference
		}
	}
	if claimProvider == "" {
		return errors.New("the request's Claim names no provider by reference, so a payer has no provider identity to match a later inquiry against")
	}
	if !resolvable[claimProvider] {
		return fmt.Errorf("the request's Claim names provider %q, which the Bundle does not resolve", claimProvider)
	}
	return nil
}

// pasMemberIdentifierType is the v2-0203 type a member identifier carries. PAS
// 2.1.0 slices the Patient's member identifier as this type, and the Da Vinci
// reference payer requires it on an inquiry at EVERY line — measured 2026-09-18,
// which the published 2.0.1 profile does not say.
const (
	pasV20203System         = "http://terminology.hl7.org/CodeSystem/v2-0203"
	pasMemberIdentifierType = "MB"
)

// pasMemberPatient builds the Patient a prior-authorization request carries: the
// member, named by the identifier the participant's OWN system names them
// under, and nothing else.
//
// The identifier is not decoration either. A payer SHALL match an inquiry on the
// member or subscriber id plus the provider identifier, so a request whose
// Patient carries no identifier is stored under a member the payer cannot key
// on — the same defect as a Claim.provider carrying display text alone, one
// element over, and equally invisible: `Patient.id` satisfies the reference the
// Claim makes whether or not anything identifies the person.
//
// Demographics stay out. The member identifier is what the payer matches on; the
// name and birth date are not, and this request carries the minimum that makes
// it answerable.
func pasMemberPatient(patientRef, memberSystem, memberID string) ([]byte, error) {
	if strings.TrimSpace(memberSystem) == "" {
		return nil, errors.New("the member identifier system is required: it is the namespace the participant's own system names this member under, and a payer matches a request on the member id")
	}
	if strings.TrimSpace(memberID) == "" {
		return nil, errors.New("the member id is required")
	}
	id := strings.TrimPrefix(patientRef, "Patient/")
	if !fhirIDPattern.MatchString(id) {
		return nil, fmt.Errorf("the patient reference %q names no FHIR id", patientRef)
	}
	return json.Marshal(map[string]any{
		"resourceType": "Patient",
		"id":           id,
		"identifier": []any{map[string]any{
			"type":   map[string]any{"coding": []any{map[string]string{"system": pasV20203System, "code": pasMemberIdentifierType}}},
			"system": memberSystem,
			"value":  memberID,
		}},
	})
}

// memberIdentifierSystemOf reads the namespace a participant's OWN Patient
// record names a member under. A record that names the member under no system is
// a refusal, not a default: a request that guessed the namespace would identify
// somebody else's member, and one that omitted it identifies nobody.
var errMemberIDSystemRequired = errors.New("the namespace your own records name this member under is required (PriorAuthRequest.MemberIDSystem, or the member identifier on the Patient you send): a payer matches a prior authorization on the member id")

func memberIdentifierSystemOf(patient []byte, memberID string) (string, error) {
	if len(strings.TrimSpace(string(patient))) == 0 {
		return "", errMemberIDSystemRequired
	}
	var probe struct {
		Identifier []struct {
			System string `json:"system"`
			Value  string `json:"value"`
		} `json:"identifier"`
	}
	if json.Unmarshal(patient, &probe) != nil {
		return "", errors.New("your Patient record is not one JSON object")
	}
	for _, id := range probe.Identifier {
		if id.Value == memberID && strings.TrimSpace(id.System) != "" {
			return id.System, nil
		}
	}
	return "", fmt.Errorf("your Patient record carries no identifier naming member %q, so a payer has nothing to match a prior authorization on", memberID)
}

// checkPASMemberIdentified refuses a built request whose Patient identifies
// nobody.
//
// It is the guard, and it reads the assembled bytes: a Patient entry satisfies
// every structural check a PAS submit runs with an id alone, so $validate
// certifies a request the payer then stores under a member it cannot find
// again. Nothing downstream would have said so — which is exactly how the
// provider half of the same rule shipped.
func checkPASMemberIdentified(bundle []byte, memberID string) error {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return fmt.Errorf("read the built request: %w", err)
	}
	for _, e := range b.Entry {
		var p struct {
			ResourceType string `json:"resourceType"`
			Identifier   []struct {
				System string `json:"system"`
				Value  string `json:"value"`
				Type   struct {
					Coding []PASCoding `json:"coding"`
				} `json:"type"`
			} `json:"identifier"`
		}
		if json.Unmarshal(e.Resource, &p) != nil || p.ResourceType != "Patient" {
			continue
		}
		for _, id := range p.Identifier {
			if id.Value != memberID || strings.TrimSpace(id.System) == "" {
				continue
			}
			for _, c := range id.Type.Coding {
				if c.System == pasV20203System && c.Code == pasMemberIdentifierType {
					return nil
				}
			}
		}
		return errors.New("the request's Patient carries no member identifier typed MB with a system and the member id, so a payer has nothing to match a later inquiry on")
	}
	return errors.New("the request carries no Patient")
}

// pasProviderRefs lists the provider references an order states, requester
// first. All of them are references INTO the participant's own system.
//
// BOTH order shapes are read, because both are orders: a DeviceRequest states
// ONE performer and a ServiceRequest states a LIST of them, so a reader that
// knew only one shape would silently find no provider on the other — and this
// list being empty is a refusal, which would have read as "the order names
// nobody" when the order named several.
func pasProviderRefs(order []byte) []string {
	var probe struct {
		Requester struct {
			Reference string `json:"reference"`
		} `json:"requester"`
		Performer json.RawMessage `json:"performer"`
	}
	if json.Unmarshal(order, &probe) != nil {
		return nil
	}
	var out []string
	if probe.Requester.Reference != "" {
		out = append(out, probe.Requester.Reference)
	}
	type reference struct {
		Reference string `json:"reference"`
	}
	var one reference
	var many []reference
	switch {
	case len(probe.Performer) == 0:
	case json.Unmarshal(probe.Performer, &many) == nil:
	case json.Unmarshal(probe.Performer, &one) == nil:
		many = []reference{one}
	}
	for _, p := range many {
		if p.Reference != "" {
			out = append(out, p.Reference)
		}
	}
	return out
}
