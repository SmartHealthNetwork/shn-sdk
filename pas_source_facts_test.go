package shnsdk

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPAS20DefaultPriorityRetainsHistoricalWireShape(t *testing.T) {
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, build := range []func() ([]byte, error){
		func() ([]byte, error) {
			return buildPASClaim("Patient/m", "Coverage/c", "Organization/p", "submit", created)
		},
		func() ([]byte, error) {
			return buildPASUpdateClaim("Patient/m", "Coverage/c", "Organization/p", "update", "submit", created)
		},
	} {
		claim, err := build()
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Priority struct {
				Coding []struct{ System, Code string }
			}
		}
		if err := json.Unmarshal(claim, &parsed); err != nil {
			t.Fatal(err)
		}
		if len(parsed.Priority.Coding) != 1 || parsed.Priority.Coding[0].System != "" || parsed.Priority.Coding[0].Code != "normal" {
			t.Fatalf("PAS 2.0 Claim.priority wire shape changed: %+v", parsed.Priority.Coding)
		}
	}
}

func TestPAS21AuthoredItemRefusesMissingParticipantFacts(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.ItemFacts = nil
	for _, line := range []string{"2.1", "2.2"} {
		_, err := BuildConformantClaimBundleAtLine(line, in)
		if err == nil || !strings.Contains(err.Error(), "PAS item facts") {
			t.Errorf("line %s without item facts: %v, want explicit source refusal", line, err)
		}
	}
	if _, err := BuildConformantClaimBundleAtLine("2.0", in); err != nil {
		t.Fatalf("2.0 request acquired a new item-facts dependency: %v", err)
	}
	in.ItemFacts = syntheticPASLineItemFacts()
	in.ItemFacts.Priority = nil
	if _, err := BuildConformantClaimBundleAtLine("2.2", in); err == nil || !strings.Contains(err.Error(), "priority") {
		t.Fatalf("2.2 missing priority source: %v, want refusal", err)
	}
	in.ItemFacts = syntheticPASLineItemFacts()
	in.ItemFacts.Priority = json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"normal","code":"stat"}]}`)
	if _, err := BuildConformantClaimBundleAtLine("2.2", in); err == nil {
		t.Fatal("ambiguous priority source accepted")
	}
}

func TestPASClaimFactsFromSourceBindsOrderAndPatient(t *testing.T) {
	order := []byte(`{"resourceType":"ServiceRequest","id":"order-1","subject":{"reference":"Patient/member-1"},"code":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"L8000"}]}}`)
	claim := []byte(`{"resourceType":"Claim","patient":{"reference":"Patient/member-1"},"priority":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"stat"}]},"item":[{"productOrService":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"L8000"}]},"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-requestedService","valueReference":{"reference":"ServiceRequest/order-1"}},{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-certificationType","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/1322","code":"R"}]}},{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-serviceItemRequestType","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/1525","code":"IN"}]}}],"locationCodeableConcept":{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set","code":"12"}]}}]}`)
	claim = []byte(strings.Replace(string(claim), `"resourceType":"Claim",`, `"resourceType":"Claim","status":"draft","use":"preauthorization",`, 1))
	facts, err := PASClaimFactsFromSource(claim, order)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(facts.Priority), `"code":"stat"`) || !strings.Contains(string(facts.LocationCodeableConcept), `"code":"12"`) {
		t.Fatalf("source facts lost: %+v", facts)
	}
	for _, row := range []struct {
		name         string
		claim, order []byte
	}{
		{"wrong patient", []byte(strings.Replace(string(claim), "Patient/member-1", "Patient/other", 1)), order},
		{"wrong order", []byte(strings.Replace(string(claim), "ServiceRequest/order-1", "ServiceRequest/other", 1)), order},
		{"duplicate item", []byte(strings.Replace(string(claim), `"item":[{`, `"item":[{}, {`, 1)), order},
		{"two requested services", []byte(strings.Replace(string(claim), `"extension":[{`, `"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-requestedService","valueReference":{"reference":"ServiceRequest/other"}}, {`, 1)), order},
		{"missing priority", []byte(strings.Replace(string(claim), `"priority":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"stat"}]},`, "", 1)), order},
		{"operative claim", []byte(strings.Replace(string(claim), `"status":"draft"`, `"status":"active"`, 1)), order},
		{"other use", []byte(strings.Replace(string(claim), `"use":"preauthorization"`, `"use":"claim"`, 1)), order},
		{"duplicate patient field", []byte(strings.Replace(string(claim), `"patient":`, `"patient":{"reference":"Patient/other"},"patient":`, 1)), order},
		{"wrong item product", []byte(strings.Replace(string(claim), `"code":"L8000"`, `"code":"E0424"`, 1)), order},
		{"missing item product", []byte(strings.Replace(string(claim), `"productOrService":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"L8000"}]},`, "", 1)), order},
		{"ambiguous item product", []byte(strings.Replace(string(claim), `"code":"L8000"}]`, `"code":"L8000"},{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0424"}]`, 1)), order},
	} {
		t.Run(row.name, func(t *testing.T) {
			if _, err := PASClaimFactsFromSource(row.claim, row.order); err == nil {
				t.Fatal("mismatched or missing source accepted")
			}
		})
	}
	wrongPatient := []byte(strings.Replace(string(claim), "Patient/member-1", "Patient/other", 1))
	if _, err := PASClaimFactsFromSource(wrongPatient, order); err == nil || errors.Is(err, ErrPASSourceMismatch) {
		t.Fatalf("Claim names exact requestedService but wrong patient: %v, want contradictory-source error", err)
	}
}

func TestPAS21AuthoredItemUsesExactParticipantFacts(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.ItemFacts = &PASLineItemFacts{
		Priority:                json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"stat"}]}`),
		CertificationType:       json.RawMessage(`{"coding":[{"system":"https://codesystem.x12.org/005010/1322","code":"R","display":"participant supplied"}]}`),
		ServiceItemRequestType:  json.RawMessage(`{"coding":[{"system":"https://codesystem.x12.org/005010/1525","code":"IN","display":"source label"}]}`),
		LocationCodeableConcept: json.RawMessage(`{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set","code":"12"}]}`),
	}
	for _, line := range []string{"2.1", "2.2"} {
		bundle, err := BuildConformantClaimBundleAtLine(line, in)
		if err != nil {
			t.Fatal(err)
		}
		claim := claimEntryForAmendmentTest(t, bundle, "convergence-claim")
		var got struct {
			Priority json.RawMessage `json:"priority"`
			Item     []struct {
				Extension []struct {
					URL                  string          `json:"url"`
					ValueCodeableConcept json.RawMessage `json:"valueCodeableConcept"`
				} `json:"extension"`
				LocationCodeableConcept json.RawMessage `json:"locationCodeableConcept"`
			} `json:"item"`
		}
		if err := json.Unmarshal(claim, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Item) != 1 {
			t.Fatalf("line %s: %d items", line, len(got.Item))
		}
		if !samePASConcept(got.Priority, in.ItemFacts.Priority) {
			t.Errorf("line %s priority = %s, want participant source %s", line, got.Priority, in.ItemFacts.Priority)
		}
		facts := map[string]json.RawMessage{}
		for _, ext := range got.Item[0].Extension {
			facts[ext.URL] = ext.ValueCodeableConcept
		}
		for url, want := range map[string]json.RawMessage{
			pasExtCertificationType:      in.ItemFacts.CertificationType,
			pasExtServiceItemRequestType: in.ItemFacts.ServiceItemRequestType,
		} {
			if !samePASConcept(facts[url], want) {
				t.Errorf("line %s %s = %s, want source %s", line, url, facts[url], want)
			}
		}
		if !samePASConcept(got.Item[0].LocationCodeableConcept, in.ItemFacts.LocationCodeableConcept) {
			t.Errorf("line %s location = %s, want source %s", line, got.Item[0].LocationCodeableConcept, in.ItemFacts.LocationCodeableConcept)
		}
	}
}

func samePASConcept(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func TestPAS22AmendmentKeepsPriorItemFacts(t *testing.T) {
	sub := conformantSubmitInputs(t)
	sub.ItemFacts = &PASLineItemFacts{
		Priority:                json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"deferred"}]}`),
		CertificationType:       json.RawMessage(`{"coding":[{"system":"https://codesystem.x12.org/005010/1322","code":"R"}]}`),
		ServiceItemRequestType:  json.RawMessage(`{"coding":[{"system":"https://codesystem.x12.org/005010/1525","code":"IN"}]}`),
		LocationCodeableConcept: json.RawMessage(`{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set","code":"12"}]}`),
	}
	initial, err := BuildConformantClaimBundleAtLine("2.2", sub)
	if err != nil {
		t.Fatal(err)
	}
	update := conformantUpdateInputsFromGolden(t)
	update.ItemFacts = nil
	update.OriginalCorr = sub.Corr
	update.PriorClaim = claimEntryForAmendmentTest(t, initial, "convergence-claim")
	amended, err := BuildConformantClaimUpdateBundleAtLine("2.2", update)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimEntryForAmendmentTest(t, amended, "convergence-claim-update")
	if !strings.Contains(string(claim), `"code":"12"`) || !strings.Contains(string(claim), `"code":"R"`) || !strings.Contains(string(claim), `"code":"deferred"`) {
		t.Fatalf("amendment lost prior source item facts: %s", claim)
	}
}

// The synthetic SDK test participant explicitly states the three facts used
// by its PAS 2.1+ example order. This is fixture data, never a builder default.
func syntheticPASLineItemFacts() *PASLineItemFacts {
	return &PASLineItemFacts{
		Priority:                json.RawMessage(`{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/processpriority","code":"normal"}]}`),
		CertificationType:       json.RawMessage(`{"coding":[{"system":"https://codesystem.x12.org/005010/1322","code":"I","display":"Initial"}]}`),
		ServiceItemRequestType:  json.RawMessage(`{"coding":[{"system":"https://codesystem.x12.org/005010/1525","code":"IN","display":"Initial Medical Services Reservation"}]}`),
		LocationCodeableConcept: json.RawMessage(`{"coding":[{"system":"https://www.cms.gov/Medicare/Coding/place-of-service-codes/Place_of_Service_Code_Set","code":"11"}]}`),
	}
}
