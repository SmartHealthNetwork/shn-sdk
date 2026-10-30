package shnsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The code systems of the request facts a PAS Claim states. Each is the
// system a requester's coding must name for the element it fills.
const (
	// PASSystemClaimType is the HL7 claim-type code system (Claim.type; PAS 2.2
	// binds Claim.type to its ClaimTypes value set, drawn from this system).
	PASSystemClaimType = "http://terminology.hl7.org/CodeSystem/claim-type"
	// PASSystemProcessPriority is the HL7 process priority code system
	// (Claim.priority, example binding process-priority; the system the PAS
	// examples and the Da Vinci reference provider state priority in).
	PASSystemProcessPriority = "http://terminology.hl7.org/CodeSystem/processpriority"
	// PASSystemX12ServiceType is the X12 1365 service type code system
	// (Claim.item.category, a required binding at every PAS line).
	PASSystemX12ServiceType = "https://codesystem.x12.org/005010/1365"
	// PASSystemPlaceOfService is the CMS place-of-service code set, one of the
	// two X12278LocationType draws on (Claim.item.location[x]).
	PASSystemPlaceOfService = pasSystemCMSPlaceOfService
	// PASSystemNUBCTypeOfBill is the NUBC type-of-bill code system, the other
	// code system X12278LocationType draws on (Claim.item.location[x]).
	PASSystemNUBCTypeOfBill = "https://www.nubc.org/CodeSystem/TypeOfBill"
	// PASSystemX12RequestCategory is the X12 1525 request category code system
	// (Claim.item extension requestType, required from PAS 2.1).
	PASSystemX12RequestCategory = pasSystemX12ServiceItemRequestType
	// PASSystemX12CertificationType is the X12 1322 certification type code
	// system (Claim.item extension certificationType, required from PAS 2.1).
	PASSystemX12CertificationType = pasSystemX12CertificationType
)

// PASClaimFacts are the facts a prior-authorization request states that only
// the requester can state: what kind of claim it is, how urgent, what category
// of service, where it is delivered, and what kind of review is asked for. Each
// is one FHIR Coding as JSON, carried onto the Claim as given — every member and
// value (the bundle is assembled through JSON objects, which order an object's
// members; FHIR JSON gives member order no meaning). Nothing here is ever
// normalised, re-derived, read from another resource or filled with a default.
//
// No published mapping derives any of them from the order or an Encounter (the
// PAS IG defers the X12 278 mapping to X12; its profiles carry no ConceptMap),
// so the builder reads them only from the requester's statement here. Earlier
// releases wrote the same constants on every request — a "professional" claim,
// priority "normal" with no system, X12 1365 "1" Medical Care, place of service
// "11" Office and request type "IN". Required by the PAS Claim profile snapshot
// at the line (Claim.type, Claim.priority and Claim.item.category at every line;
// Claim.item.location[x] and the requestType extension from 2.1) and absent, the
// builder refuses before anything is assembled (ErrPASClaimFactAbsent); optional
// at the line (location and request type at 2.0) and absent, it is omitted.
type PASClaimFacts struct {
	// ClaimType is Claim.type (1..1 at every line; 2.2 binds the ClaimTypes
	// value set, drawn from PASSystemClaimType).
	ClaimType json.RawMessage `json:"claimType,omitempty"`
	// Priority is Claim.priority (1..1 at every line).
	Priority json.RawMessage `json:"priority,omitempty"`
	// ServiceCategory is Claim.item.category (1..1 at every line, an X12 1365
	// coding).
	ServiceCategory json.RawMessage `json:"serviceCategory,omitempty"`
	// PlaceOfService is Claim.item.location[x] (0..1 at 2.0, 1..1 from 2.1): a
	// CMS place-of-service or NUBC type-of-bill coding, the two code systems
	// X12278LocationType includes. Absent at 2.0 it is omitted.
	PlaceOfService json.RawMessage `json:"placeOfService,omitempty"`
	// RequestType is the Claim.item requestType extension
	// (extension-serviceItemRequestType, an X12 1525 coding; 0..1 at 2.0,
	// 1..1 from 2.1). Stated, it is carried at every line; absent at 2.0 it is
	// omitted.
	RequestType json.RawMessage `json:"requestType,omitempty"`
}

// PASPriorClaim is what the participant recorded of the submission an
// amendment continues, read off the bytes the submission sent
// (PASPriorClaimOf). Claim is the submitted Claim resource exactly as the
// submission's bundle carried it: from PAS 2.1 the amendment carries it as its
// prior-Claim entry, byte for byte, and at every line the amendment's items
// are compared with its items to say which of them carry the PAS infoChanged
// marker (see ConformantClaimUpdateInputs). Claim is REQUIRED: a record that
// lacks it — every record written before it existed — is refused
// (ErrPASPriorClaimNotCarried), never rebuilt from the fields beside it.
type PASPriorClaim struct {
	Claim json.RawMessage `json:"claim,omitempty"`
	// ClaimType, Priority, Created and Items are the submitted Claim's type,
	// priority, created and items, each as sent. PASPriorClaimOf still fills
	// them; the update builder reads none of them.
	//
	// Deprecated: the update builder carries Claim as sent and reads its items
	// from it. A later SDK release removes these fields, named in that release's notes.
	ClaimType json.RawMessage   `json:"claimType"`
	Priority  json.RawMessage   `json:"priority"`
	Created   string            `json:"created"`
	Items     []json.RawMessage `json:"items"`
}

// PASPriorClaimOf reads the record an amendment needs off a submitted PAS
// request Bundle: its first Claim entry's resource, exactly as the bundle's
// bytes hold it. A record the bytes cannot supply is refused by the update
// builder, never filled.
func PASPriorClaimOf(submittedBundle []byte) (PASPriorClaim, error) {
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(submittedBundle, &b); err != nil {
		return PASPriorClaim{}, fmt.Errorf("shnsdk: record the submission: %w", err)
	}
	for _, e := range b.Entry {
		var cl struct {
			ResourceType string            `json:"resourceType"`
			Created      string            `json:"created"`
			Type         json.RawMessage   `json:"type"`
			Priority     json.RawMessage   `json:"priority"`
			Item         []json.RawMessage `json:"item"`
		}
		if json.Unmarshal(e.Resource, &cl) != nil || cl.ResourceType != "Claim" {
			continue
		}
		return PASPriorClaim{Claim: e.Resource, ClaimType: cl.Type, Priority: cl.Priority, Created: cl.Created, Items: cl.Item}, nil
	}
	return PASPriorClaim{}, errors.New("shnsdk: record the submission: the submitted bundle carries no Claim")
}

// ErrPASClaimFactAbsent is the refusal of a request builder that cannot read a
// required Claim element from the requester's own system or the request it was
// given. It is the fidelity of SHN's own build: SHN building a participant's
// wire message never supplies a value that participant's system did not state,
// so the request is not built and nothing is sealed or sent. It is never a
// judgement of anyone's payload.
var ErrPASClaimFactAbsent = errors.New("absent at its source, so the request is not built: SHN's build never supplies a value the requester's own system does not state (fidelity of SHN's own build)")

// ErrPASClaimFactInvalid refuses a supplied fact that is not a coding the
// element can carry (no code, no system, or a system the element's binding does
// not draw from). Like ErrPASClaimFactAbsent it is about SHN's own build.
var ErrPASClaimFactInvalid = errors.New("not a coding this Claim element can carry")

// ErrPASPriorClaimNotCarried is the refusal of an amendment whose recorded
// submission cannot be carried as its prior Claim exactly as the requester
// submitted it: SHN never rebuilds, restates or rewrites a participant's
// submitted Claim, so the amendment is not built and nothing is sealed or
// sent. It is the fidelity of SHN's own edit, never a judgement of anyone's
// payload.
var ErrPASPriorClaimNotCarried = errors.New("the prior Claim cannot be carried as the requester submitted it, so the amendment is not built: SHN never rebuilds or rewrites a participant's submitted Claim (fidelity of SHN's own edit)")

func pasPriorClaimNotCarried(why string) error {
	return fmt.Errorf("%w: %s", ErrPASPriorClaimNotCarried, why)
}

func pasFactAbsent(element, source string) error {
	return fmt.Errorf("%s %w (expected from %s)", element, ErrPASClaimFactAbsent, source)
}

// pasResolvedFacts are the Claim's request facts, each a CodeableConcept JSON
// wrapping exactly the coding its source stated; nil means absent.
type pasResolvedFacts struct {
	claimType, priority, category, location, requestType, certificationType json.RawMessage
}

// pasFactIsAbsent reports whether a fact is absent: not stated, or stated as
// JSON null (FHIR JSON has no null value; a null member states nothing).
func pasFactIsAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

// pasCodingOf checks a supplied coding: a JSON object with a non-empty code
// and system, and — when systems is non-empty — one of those systems.
func pasCodingOf(element string, raw json.RawMessage, systems ...string) (json.RawMessage, error) {
	var c struct {
		System string `json:"system"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w: %v", element, ErrPASClaimFactInvalid, err)
	}
	if strings.TrimSpace(c.Code) == "" || strings.TrimSpace(c.System) == "" {
		return nil, fmt.Errorf("%s: %w: a coding needs both a system and a code", element, ErrPASClaimFactInvalid)
	}
	if len(systems) > 0 {
		ok := false
		for _, s := range systems {
			ok = ok || c.System == s
		}
		if !ok {
			return nil, fmt.Errorf("%s: %w: system %q is not %s", element, ErrPASClaimFactInvalid, c.System, strings.Join(systems, " or "))
		}
	}
	return pasConcept(raw)
}

// pasConcept wraps one coding, exactly as given, as a CodeableConcept.
func pasConcept(coding json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string][]json.RawMessage{"coding": {coding}})
}

// resolvePASClaimFacts reads every request fact the Claim states from the
// requester's statement only, and refuses — before anything is built — a fact
// required at the line (PAS Claim profile snapshot) that the requester did not
// state. There is no fallback: no constant, no other resource.
func resolvePASClaimFacts(def PASDef, f PASClaimFacts) (pasResolvedFacts, error) {
	var r pasResolvedFacts
	var err error
	required := []struct {
		element, field string
		raw            json.RawMessage
		out            *json.RawMessage
		systems        []string
	}{
		{"Claim.type", "ClaimType", f.ClaimType, &r.claimType, nil},
		{"Claim.priority", "Priority", f.Priority, &r.priority, nil},
		{"Claim.item.category", "ServiceCategory", f.ServiceCategory, &r.category, []string{PASSystemX12ServiceType}},
	}
	for _, q := range required {
		if pasFactIsAbsent(q.raw) {
			return r, pasFactAbsent(q.element, "the requester's "+q.field)
		}
		if *q.out, err = pasCodingOf(q.element, q.raw, q.systems...); err != nil {
			return r, err
		}
	}
	switch {
	case !pasFactIsAbsent(f.PlaceOfService):
		if r.location, err = pasCodingOf("Claim.item.location[x]", f.PlaceOfService, PASSystemPlaceOfService, PASSystemNUBCTypeOfBill); err != nil {
			return r, err
		}
	case def.ClaimItemLineDetailRequired:
		return r, pasFactAbsent("Claim.item.location[x] (required from PAS 2.1)", "the requester's PlaceOfService")
	}
	switch {
	case !pasFactIsAbsent(f.RequestType):
		if r.requestType, err = pasCodingOf("Claim.item requestType", f.RequestType, PASSystemX12RequestCategory); err != nil {
			return r, err
		}
	case def.ClaimItemLineDetailRequired:
		return r, pasFactAbsent("Claim.item requestType (required from PAS 2.1)", "the requester's RequestType")
	}
	return r, nil
}

// resolvePASUpdateFacts is resolvePASClaimFacts for an amendment: the same
// facts, plus the certificationType only the requester can state (the kind of
// amendment this is). Stated, it is carried at every line (the PAS 2.0.1
// Claim allows Claim.item.extension:certificationType 0..1); absent, it is
// omitted at 2.0 and refused from PAS 2.1, where it is required.
func resolvePASUpdateFacts(def PASDef, f PASClaimFacts, certificationType json.RawMessage) (pasResolvedFacts, error) {
	facts, err := resolvePASClaimFacts(def, f)
	if err != nil {
		return facts, err
	}
	switch {
	case !pasFactIsAbsent(certificationType):
		if facts.certificationType, err = pasCodingOf("Claim.item certificationType", certificationType, PASSystemX12CertificationType); err != nil {
			return facts, err
		}
	case def.ClaimItemLineDetailRequired:
		return facts, pasFactAbsent("Claim.item certificationType (required from PAS 2.1)", "the requester's CertificationType (an X12 1322 coding naming the kind of amendment)")
	}
	return facts, nil
}

// CheckPASClaimFactsAtLine reports, without building or sending anything, the
// refusal the PAS submission builder at line (BuildConformantClaimBundleAtLineExact)
// makes for these facts — the same rule, read by the same resolver, with the
// same text — or nil when it would build them. A caller that sends other legs
// before the submission (a coverage check, a questionnaire fetch) checks here
// first, so a request the build will refuse sends nothing at all.
func CheckPASClaimFactsAtLine(line string, f PASClaimFacts) error {
	def, ok := PASLineDef(line)
	if !ok {
		return fmt.Errorf("shnsdk: CheckPASClaimFactsAtLine: unknown PAS line %q", line)
	}
	if _, err := resolvePASClaimFacts(def, f); err != nil {
		return fmt.Errorf("shnsdk: conformant submit: %w", err)
	}
	return nil
}

// CheckPASUpdateFactsAtLine is CheckPASClaimFactsAtLine for an amendment
// (BuildConformantClaimUpdateBundleAtLineExact): its facts and its
// certificationType, refused exactly as that builder refuses them.
func CheckPASUpdateFactsAtLine(line string, f PASClaimFacts, certificationType json.RawMessage) error {
	def, ok := PASLineDef(line)
	if !ok {
		return fmt.Errorf("shnsdk: CheckPASUpdateFactsAtLine: unknown PAS line %q", line)
	}
	if _, err := resolvePASUpdateFacts(def, f, certificationType); err != nil {
		return fmt.Errorf("shnsdk: conformant update: %w", err)
	}
	return nil
}

// pasSubmitCertificationType is the certificationType of a submission: X12
// 1322 "I" Initial. It is a fact of the operation — a submission is a new
// request — not a value read or assumed about the request's content. The code
// and display are the PAS 2.2.1 example instance's (Claim-MedicalServicesAuthorizationExample).
var pasSubmitCertificationType = json.RawMessage(`{"coding":[{"system":"` + pasSystemX12CertificationType + `","code":"I","display":"Initial"}]}`)

// spliceClaimFacts writes the resolved facts onto a built Claim: type,
// priority, item[0].category and — when stated — item[0].locationCodeableConcept.
func spliceClaimFacts(claimJSON []byte, r pasResolvedFacts) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(claimJSON, &m); err != nil {
		return nil, fmt.Errorf("claim facts: parse claim: %w", err)
	}
	if r.claimType == nil || r.priority == nil || r.category == nil {
		return nil, errors.New("claim facts: type, priority and category must be resolved before the claim is built")
	}
	m["type"], m["priority"] = r.claimType, r.priority
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(m["item"], &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("claim facts: claim has no item")
	}
	items[0]["category"] = r.category
	if r.location != nil {
		items[0]["locationCodeableConcept"] = r.location
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	m["item"] = itemsJSON
	return json.Marshal(m)
}
