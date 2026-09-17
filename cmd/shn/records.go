package main

import (
	"encoding/json"
	"errors"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// demoOrderingNPI is the ordering practitioner shn's prior-auth checks name:
// the demonstration NPI the network's test flows order for.
const demoOrderingNPI = "1234567890"

// testPersonaRecords returns the records shn sends for a network test persona
// in a prior-auth check: a Patient (id = the member id, the member identifier,
// name and birth date) and the Coverage search result for it (the member's
// Coverage naming the persona's payer, with the payer Organization included).
// They are demonstration records for the persona the network advertises; a
// participant sends the records its own system holds.
func testPersonaRecords(p shnsdk.DiscoveryPersona) (patient, coverage []byte, err error) {
	if p.PayerID == nil || p.PayerID.System == "" || p.PayerID.Value == "" {
		return nil, nil, errors.New("the persona names no payer identity")
	}
	patient, err = json.Marshal(map[string]any{
		"resourceType": "Patient",
		"id":           p.MemberID,
		"identifier":   []map[string]string{{"system": shnsdk.MemberSystem, "value": p.MemberID}},
		"name":         []map[string]string{{"family": p.Family}},
		"birthDate":    p.DOB,
	})
	if err != nil {
		return nil, nil, err
	}
	const base = "https://shn-cli.invalid/fhir"
	orgID := "payer-" + strings.ToLower(strings.NewReplacer(".", "-", ":", "-", "|", "-").Replace(p.PayerID.Value))
	coverage, err = json.Marshal(map[string]any{
		"resourceType": "Bundle",
		"type":         "searchset",
		"entry": []map[string]any{
			{"fullUrl": base + "/Coverage/cov-" + p.MemberID, "search": map[string]string{"mode": "match"}, "resource": map[string]any{
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
				"payor":        []map[string]string{{"reference": "Organization/" + orgID}},
			}},
			{"fullUrl": base + "/Organization/" + orgID, "search": map[string]string{"mode": "include"}, "resource": map[string]any{
				"resourceType": "Organization",
				"id":           orgID,
				"identifier":   []map[string]string{{"system": p.PayerID.System, "value": p.PayerID.Value}},
				"name":         "Payer " + p.PayerID.Value,
			}},
		},
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
	if p.PayerID == nil {
		return req, nil
	}
	patient, coverage, err := testPersonaRecords(p)
	if err != nil {
		return req, err
	}
	req.Patient, req.Coverage, req.NPI = patient, coverage, demoOrderingNPI
	return req, nil
}
