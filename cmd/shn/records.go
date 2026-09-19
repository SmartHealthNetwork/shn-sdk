package main

import (
	"encoding/json"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// demoOrderingNPI is the ordering practitioner shn's prior-auth checks name:
// the demonstration NPI the network's test flows order for.
const demoOrderingNPI = "1234567890"

// demoRequestingProviderNPI is the requesting provider organization's NPI. A
// payer matches a prior-authorization inquiry on the member id PLUS the ordering
// or rendering provider identifier, so a claim identifies the party it comes
// from; this is the demonstration organization the network's test flows request
// under, and a participant sends the record its own system holds.
const demoRequestingProviderNPI = "1417947384"

// testPersonaRequestingProvider is the requesting provider record shn sends for
// a network test persona's prior-auth check.
func testPersonaRequestingProvider() ([]byte, error) {
	return json.Marshal(map[string]any{
		"resourceType": "Organization",
		"id":           "shn-cli-requesting-provider",
		"identifier":   []map[string]string{{"system": "http://hl7.org/fhir/sid/us-npi", "value": demoRequestingProviderNPI}},
		"name":         "SHN CLI Demonstration Provider",
	})
}

// testPersonaRecords returns the records shn sends for a network test persona
// in a prior-auth check: a Patient (id = the member id, the member identifier,
// name and birth date) and the Coverage search result for it (the member's
// Coverage naming the persona's payer, with the payer Organization included).
// They are demonstration records for the persona the network advertises; a
// participant sends the records its own system holds.
func testPersonaRecords(p shnsdk.DiscoveryPersona) (patient, coverage []byte, err error) {
	patient, err = json.Marshal(map[string]any{
		"resourceType": "Patient",
		"id":           p.MemberID,
		// Typed MB (v2-0203): a payer matches a prior authorization and every later
		// inquiry about it on this identifier, and refuses an inquiry whose Patient
		// carries it untyped.
		"identifier": []map[string]any{{
			"type":   map[string]any{"coding": []map[string]string{{"system": "http://terminology.hl7.org/CodeSystem/v2-0203", "code": "MB"}}},
			"system": shnsdk.MemberSystem,
			"value":  p.MemberID,
		}},
		"name":      []map[string]string{{"family": p.Family}},
		"birthDate": p.DOB,
	})
	if err != nil {
		return nil, nil, err
	}
	const base = "https://shn-cli.invalid/fhir"
	cov := map[string]any{
		"resourceType": "Coverage",
		"id":           "cov-" + p.MemberID,
		"status":       "active",
		"identifier": []map[string]any{{
			"type":   map[string]any{"coding": []map[string]string{{"system": "http://terminology.hl7.org/CodeSystem/v2-0203", "code": "MB"}}},
			"system": "urn:shn:coverage",
			"value":  p.MemberID,
		}},
		"beneficiary":  map[string]string{"reference": "Patient/" + p.MemberID},
		"relationship": map[string]any{"coding": []map[string]string{{"system": "http://terminology.hl7.org/CodeSystem/subscriber-relationship", "code": "self"}}},
	}
	// The payer organization the coverage names, and the claim then carries: a
	// prior authorization names the payer, and the payer scopes a later inquiry's
	// search by the organization the claim named.
	//
	// Its identity is the persona's when the network states one. When the network
	// PREDATES persona payerId it states none — and this driver does not invent a
	// payer, it states the one its own prior-authorization leg already asserts
	// (RunPriorAuth sends shnsdk.CMSPayerIdentity for every persona it drives). The
	// record says what the request says; a participant sends what its own system
	// holds.
	payerID := shnsdk.CMSPayerIdentity
	if p.PayerID != nil && p.PayerID.System != "" && p.PayerID.Value != "" {
		payerID = *p.PayerID
	}
	orgID := "org-cms-payer"
	if payerID != shnsdk.CMSPayerIdentity {
		orgID = "payer-" + strings.ToLower(strings.NewReplacer(".", "-", ":", "-", "|", "-").Replace(payerID.Value))
	}
	cov["payor"] = []map[string]string{{"reference": "Organization/" + orgID}}
	entries := []map[string]any{
		{"fullUrl": base + "/Coverage/cov-" + p.MemberID, "search": map[string]string{"mode": "match"}, "resource": cov},
		{"fullUrl": base + "/Organization/" + orgID, "search": map[string]string{"mode": "include"}, "resource": map[string]any{
			"resourceType": "Organization",
			"id":           orgID,
			"identifier":   []map[string]string{{"system": payerID.System, "value": payerID.Value}},
			"name":         "Payer " + payerID.Value,
		}},
	}
	coverage, err = json.Marshal(map[string]any{
		"resourceType": "Bundle",
		"type":         "searchset",
		"entry":        entries,
	})
	if err != nil {
		return nil, nil, err
	}
	return patient, coverage, nil
}

// withTestPersonaRecords sets req's Patient, Coverage and ordering practitioner
// for persona p. A persona from a network that names no payer identity keeps
// the older request.
func withTestPersonaRecords(req shnsdk.PriorAuthRequest, p shnsdk.DiscoveryPersona) (shnsdk.PriorAuthRequest, error) {
	provider, err := testPersonaRequestingProvider()
	if err != nil {
		return req, err
	}
	// The requesting provider is set for EVERY persona, including one from a
	// network that names no payer identity: the prior-authorization claim
	// identifies the party it comes from whichever coverage check preceded it.
	req.Provider = provider
	// The network's test personas are named under the network's own member
	// namespace. A participant states the namespace ITS records use; this one
	// states the namespace the personas it drives are seeded under, and the
	// claim identifies the member by it — a claim whose Patient identifies
	// nobody is stored under a member no later inquiry can find.
	req.MemberIDSystem = shnsdk.MemberSystem
	// The member's own records go on EVERY persona, payer identity or not: the
	// prior-authorization claim is made under a coverage, and a claim carrying a
	// coverage the SDK made up names a policy the payer cannot locate and a record
	// no later inquiry can ask about.
	patient, coverage, err := testPersonaRecords(p)
	if err != nil {
		return req, err
	}
	req.Patient, req.Coverage, req.NPI = patient, coverage, demoOrderingNPI
	return req, nil
}
