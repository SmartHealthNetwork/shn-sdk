package shnsdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A dependent's Bundle: Claim.patient and Coverage.beneficiary name the
// member, and the Coverage's subscriber and policyHolder name the parent, a
// Patient entry of its own.
const entryPartyBundle = `{"resourceType":"Bundle","type":"collection","entry":[
 {"fullUrl":"https://payer.example/fhir/Claim/c1","resource":{"resourceType":"Claim","id":"c1",
  "patient":{"reference":"Patient/member"},"insurance":[{"coverage":{"reference":"Coverage/cov"}}]}},
 {"fullUrl":"https://payer.example/fhir/Coverage/cov","resource":{"resourceType":"Coverage","id":"cov",
  "beneficiary":{"reference":"Patient/member"},"subscriber":{"reference":"Patient/parent"},
  "policyHolder":{"reference":"https://payer.example/fhir/Patient/parent"},
  "payor":[{"reference":"Organization/payer"}]}},
 {"fullUrl":"https://payer.example/fhir/Patient/member","resource":{"resourceType":"Patient","id":"member"}},
 {"fullUrl":"https://payer.example/fhir/Patient/parent","resource":{"resourceType":"Patient","id":"parent",
  "identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"name":[{"family":"Parent"}]}},
 {"fullUrl":"https://payer.example/fhir/Organization/payer","resource":{"resourceType":"Organization","id":"payer"}}
]}`

func entryPartyDoc(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(entryPartyBundle), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// entryOf returns the entry of doc whose resource has the id.
func entryOf(t *testing.T, doc map[string]any, id string) map[string]any {
	t.Helper()
	list, _ := doc["entry"].([]any)
	for _, e := range list {
		em, _ := e.(map[string]any)
		if res, _ := em["resource"].(map[string]any); res != nil && res["id"] == id {
			return em
		}
	}
	t.Fatalf("fixture: no entry %q", id)
	return nil
}

func resourceOf(t *testing.T, doc map[string]any, id string) map[string]any {
	t.Helper()
	res, _ := entryOf(t, doc, id)["resource"].(map[string]any)
	return res
}

func coverageSlot(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	slot, _ := resourceOf(t, doc, "cov")[name].(map[string]any)
	return slot
}

// urnBundle moves every entry to a urn:uuid fullUrl, its references with it.
func urnBundle(t *testing.T, doc map[string]any) {
	t.Helper()
	for _, id := range []string{"c1", "cov", "member", "parent", "payer"} {
		entryOf(t, doc, id)["fullUrl"] = "urn:uuid:00000000-0000-4000-8000-0000000" + id
	}
	set := func(m map[string]any, id string) { m["reference"] = "urn:uuid:00000000-0000-4000-8000-0000000" + id }
	set(coverageSlot(t, doc, "beneficiary"), "member")
	set(coverageSlot(t, doc, "subscriber"), "parent")
	set(coverageSlot(t, doc, "policyHolder"), "parent")
}

func TestCoverageEntryParties(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(t *testing.T, doc map[string]any)
		party bool
	}{
		{name: "relative subscriber and absolute policyHolder", party: true},
		{name: "subscriber only", party: true, edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
		}},
		{name: "policyHolder only", party: true, edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "subscriber")
		}},
		{name: "urn:uuid entries", party: true, edit: urnBundle},
		{name: "a parent with no identifier", party: true, edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "parent"), "identifier")
		}},
		{name: "a versioned reference to the entry's version", party: true, edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["reference"] = "Patient/parent/_history/2"
			resourceOf(t, doc, "parent")["meta"] = map[string]any{"versionId": "2"}
		}},
		{name: "two Coverages of the member name one parent", party: true, edit: func(t *testing.T, doc map[string]any) {
			var second map[string]any
			raw, _ := json.Marshal(entryOf(t, doc, "cov"))
			if err := json.Unmarshal(raw, &second); err != nil {
				t.Fatal(err)
			}
			second["fullUrl"] = "https://payer.example/fhir/Coverage/cov2"
			second["resource"].(map[string]any)["id"] = "cov2"
			doc["entry"] = append(doc["entry"].([]any), second)
		}},

		{name: "a versioned reference to another version", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["reference"] = "Patient/parent/_history/1"
			resourceOf(t, doc, "parent")["meta"] = map[string]any{"versionId": "2"}
		}},
		{name: "no Coverage names it", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "subscriber")
			delete(resourceOf(t, doc, "cov"), "policyHolder")
		}},
		{name: "a relative reference that does not resolve", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			coverageSlot(t, doc, "subscriber")["reference"] = "Patient/nobody"
		}},
		{name: "matching by id alone: the Coverage's fullUrl has no base", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "cov")["fullUrl"] = "urn:uuid:00000000-0000-4000-8000-00000000c0f0"
		}},
		{name: "matching by id alone: another base", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "cov")["fullUrl"] = "https://other.example/fhir/Coverage/cov"
		}},
		{name: "the Coverage's fullUrl does not end in its own type and id", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "cov")["fullUrl"] = "https://payer.example/fhir/Coverage/other"
		}},
		{name: "a relative reference with extra path", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			coverageSlot(t, doc, "subscriber")["reference"] = "fhir/Patient/parent"
		}},
		{name: "two entries share the fullUrl", edit: func(t *testing.T, doc map[string]any) {
			var twin map[string]any
			raw, _ := json.Marshal(entryOf(t, doc, "parent"))
			if err := json.Unmarshal(raw, &twin); err != nil {
				t.Fatal(err)
			}
			doc["entry"] = append(doc["entry"].([]any), twin)
		}},
		{name: "the entry has no fullUrl", edit: func(t *testing.T, doc map[string]any) {
			delete(entryOf(t, doc, "parent"), "fullUrl")
		}},
		{name: "the beneficiary is the entry too", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "beneficiary")["reference"] = "Patient/parent"
		}},
		{name: "the Coverage has no beneficiary", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "beneficiary")
		}},
		{name: "the beneficiary resolves to no entry", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "beneficiary")["reference"] = "Patient/nobody"
		}},
		{name: "the beneficiary names no Patient", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "beneficiary")["reference"] = "Organization/payer"
		}},
		{name: "the Claim names it", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["patient"].(map[string]any)["reference"] = "Patient/parent"
		}},
		{name: "another element names it by a spelled-out URL", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://payer.example/fhir/Patient/parent"}
		}},
		{name: "a member named in another case names it", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"Reference": "Patient/parent"}
		}},
		{name: "the Coverage names it elsewhere", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "cov")["payor"] = []any{map[string]any{"reference": "Patient/parent"}}
		}},
		{name: "an extension in the slot names it", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["extension"] = []any{map[string]any{
				"url": "https://x.example", "valueReference": map[string]any{"reference": "Patient/parent"}}}
		}},
		{name: "another entry's contained resource names it", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["contained"] = []any{map[string]any{"resourceType": "Coverage", "id": "in",
				"beneficiary": map[string]any{"reference": "Patient/member"},
				"subscriber":  map[string]any{"reference": "Patient/parent"}}}
		}},
		{name: "the Bundle's own element names it", edit: func(t *testing.T, doc map[string]any) {
			doc["signature"] = map[string]any{"who": map[string]any{"reference": "https://payer.example/fhir/Patient/parent"}}
		}},
		{name: "the entry names itself", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["link"] = []any{map[string]any{"other": map[string]any{"reference": "Patient/parent"}}}
		}},
		{name: "a second Coverage with no beneficiary names it", edit: func(t *testing.T, doc map[string]any) {
			doc["entry"] = append(doc["entry"].([]any), map[string]any{
				"fullUrl": "https://payer.example/fhir/Coverage/cov2",
				"resource": map[string]any{"resourceType": "Coverage", "id": "cov2",
					"subscriber": map[string]any{"reference": "Patient/parent"}}})
		}},
		{name: "the slot is a list", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			cov := resourceOf(t, doc, "cov")
			cov["subscriber"] = []any{cov["subscriber"]}
		}},
		{name: "the slot's reference is spelled in another case", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			slot := coverageSlot(t, doc, "subscriber")
			slot["Reference"] = slot["reference"]
			delete(slot, "reference")
		}},
		{name: "the slot is spelled in another case", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			cov := resourceOf(t, doc, "cov")
			cov["Subscriber"] = cov["subscriber"]
			delete(cov, "subscriber")
		}},
		{name: "a slot's reference beside one spelled in another case", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			coverageSlot(t, doc, "subscriber")["Reference"] = "Patient/member"
		}},
		{name: "a beneficiary's reference beside one spelled in another case", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "beneficiary")["Reference"] = "Organization/payer"
		}},
		{name: "a slot on another resource type names it", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "subscriber")
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			doc["entry"] = append(doc["entry"].([]any), map[string]any{
				"fullUrl": "https://payer.example/fhir/ServiceRequest/sr",
				"resource": map[string]any{"resourceType": "ServiceRequest", "id": "sr",
					"beneficiary": map[string]any{"reference": "Patient/member"},
					"subscriber":  map[string]any{"reference": "Patient/parent"}}})
		}},
		{name: "an entry's own member names it", edit: func(t *testing.T, doc map[string]any) {
			entryOf(t, doc, "payer")["link"] = []any{map[string]any{"reference": "Patient/parent"}}
		}},
		{name: "a base that is no http or https URL", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "cov")["fullUrl"] = "urn:shn:/fhir/Coverage/cov"
			entryOf(t, doc, "member")["fullUrl"] = "urn:shn:/fhir/Patient/member"
			entryOf(t, doc, "parent")["fullUrl"] = "urn:shn:/fhir/Patient/parent"
		}},
		{name: "a shallower base and a reference with path to the entry", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "cov")["fullUrl"] = "https://payer.example/Coverage/cov"
			coverageSlot(t, doc, "beneficiary")["reference"] = "https://payer.example/fhir/Patient/member"
			coverageSlot(t, doc, "subscriber")["reference"] = "fhir/Patient/parent"
		}},
		{name: "a slot with no reference and an entry at the base", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "parent")["fullUrl"] = "https://payer.example/fhir/"
			resourceOf(t, doc, "cov")["subscriber"] = map[string]any{"display": "Parent"}
		}},
		{name: "a beneficiary with no reference and an entry at the base", edit: func(t *testing.T, doc map[string]any) {
			entryOf(t, doc, "member")["fullUrl"] = "https://payer.example/fhir/"
			resourceOf(t, doc, "cov")["beneficiary"] = []any{map[string]any{"reference": "Patient/member"}}
		}},
		{name: "an identifier beside the slot's reference, the entry's", party: true, edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["identifier"] = map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}
		}},
		{name: "an identifier no entry carries beside a slot's reference", party: true, edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["identifier"] = map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "OTHER"}
		}},
		{name: "another Coverage slot's identifier, naming another entry, is the entry's", edit: func(t *testing.T, doc map[string]any) {
			doc["entry"] = append(doc["entry"].([]any),
				map[string]any{"fullUrl": "https://payer.example/fhir/Patient/q",
					"resource": map[string]any{"resourceType": "Patient", "id": "q"}},
				map[string]any{"fullUrl": "https://payer.example/fhir/Coverage/cov2",
					"resource": map[string]any{"resourceType": "Coverage", "id": "cov2",
						"beneficiary": map[string]any{"reference": "Patient/member"},
						"subscriber": map[string]any{"reference": "Patient/q",
							"identifier": map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}}}})
		}},
		{name: "a search reference to Patient", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["patient"] = map[string]any{"reference": "Patient?identifier=urn:oid:1.2.3.4.5|MRN-9"}
		}},
		{name: "a search reference to Patient under a base", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://payer.example/fhir/Patient?_id=parent"}
		}},
		{name: "a reference that is only the type, no search", party: true, edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "Patient"}
		}},
		{name: "a search reference to another type", party: true, edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["provider"] = map[string]any{"reference": "Organization?identifier=x"}
		}},
		{name: "a logical reference elsewhere holds the entry's identifier", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["patient"] = map[string]any{"identifier": map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}}
		}},
		{name: "a logical reference spelled in another case", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"Identifier": map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}}
		}},
		{name: "another Coverage's logical beneficiary holds the entry's identifier", edit: func(t *testing.T, doc map[string]any) {
			doc["entry"] = append(doc["entry"].([]any), map[string]any{
				"fullUrl": "https://payer.example/fhir/Coverage/cov2",
				"resource": map[string]any{"resourceType": "Coverage", "id": "cov2",
					"beneficiary": map[string]any{"identifier": map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}}}})
		}},
		{name: "a logical reference with no identifier of the entry's", party: true, edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"identifier": map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "OTHER"}}
		}},
		{name: "an identifier in an assigner beside the slot's reference", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["identifier"] = map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9",
				"assigner": map[string]any{"identifier": map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}}}
		}},
		{name: "a reference elsewhere with an empty version", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://payer.example/fhir/Patient/parent/_history/"}
		}},
		{name: "a reference elsewhere with a fragment", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://payer.example/fhir/Patient/parent#x"}
		}},
		{name: "a reference elsewhere with a trailing slash", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://payer.example/fhir/Patient/parent/"}
		}},
		{name: "a urn:uuid reference elsewhere", edit: func(t *testing.T, doc map[string]any) {
			urnBundle(t, doc)
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": entryOf(t, doc, "parent")["fullUrl"]}
		}},
		{name: "a reference elsewhere with a query", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "Patient/parent?x=1"}
		}},
		{name: "a reference elsewhere with two versions", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "Patient/parent/_history/1/_history/2"}
		}},
		{name: "a reference elsewhere to the same id under another base", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://other.example/fhir/Patient/parent"}
		}},
		{name: "a relative reference from an entry with no base", edit: func(t *testing.T, doc map[string]any) {
			entryOf(t, doc, "c1")["fullUrl"] = "urn:uuid:00000000-0000-4000-8000-0000000000c1"
			resourceOf(t, doc, "c1")["patient"].(map[string]any)["reference"] = "Patient/parent"
		}},
		{name: "a relative reference in the Bundle's own element", edit: func(t *testing.T, doc map[string]any) {
			doc["signature"] = map[string]any{"who": map[string]any{"reference": "Patient/parent"}}
		}},
		{name: "the entry names itself by a bare #", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["generalPractitioner"] = []any{map[string]any{"reference": "#"}}
		}},
		{name: "the entry spells meta in another case", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["Meta"] = map[string]any{"versionId": "3"}
		}},
		{name: "the entry spells versionId in another case", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["meta"] = map[string]any{"VersionId": "3"}
		}},
		{name: "the Coverage spells id in another case", edit: func(t *testing.T, doc map[string]any) {
			urnBundle(t, doc)
			resourceOf(t, doc, "cov")["ID"] = "cov"
		}},
		{name: "a urn:uuid entry that is no Patient", edit: func(t *testing.T, doc map[string]any) {
			urnBundle(t, doc)
			resourceOf(t, doc, "parent")["resourceType"] = "RelatedPerson"
		}},
		{name: "an http entry with no id", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "parent"), "id")
		}},
		{name: "an upper-case scheme whose id is not its fullUrl's", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "parent")["fullUrl"] = "HTTP://payer.example/fhir/Patient/other"
			coverageSlot(t, doc, "subscriber")["reference"] = "HTTP://payer.example/fhir/Patient/other"
		}},
		{name: "a urn:uuid entry whose id is no string", edit: func(t *testing.T, doc map[string]any) {
			urnBundle(t, doc)
			resourceOf(t, doc, "parent")["id"] = 5
		}},
		{name: "a reference elsewhere with history before the id", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "https://other.example/_history/Patient/parent"}
		}},
		{name: "a reference elsewhere with an operation after the id", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "Patient/parent/$everything"}
		}},
		{name: "a relative reference elsewhere with a fragment", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": "Patient/parent#x"}
		}},
		{name: "a urn:uuid reference elsewhere with a version", edit: func(t *testing.T, doc map[string]any) {
			urnBundle(t, doc)
			fullURL, _ := entryOf(t, doc, "parent")["fullUrl"].(string)
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": fullURL + "/_history/1"}
		}},
		{name: "a urn:uuid reference elsewhere in upper case", edit: func(t *testing.T, doc map[string]any) {
			urnBundle(t, doc)
			fullURL, _ := entryOf(t, doc, "parent")["fullUrl"].(string)
			resourceOf(t, doc, "c1")["enterer"] = map[string]any{"reference": strings.ToUpper(fullURL)}
		}},
		{name: "an http entry whose id is not its fullUrl's", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["id"] = "other"
		}},
		{name: "a fullUrl with a fragment", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "parent")["fullUrl"] = "urn:shn:parent#f"
			coverageSlot(t, doc, "subscriber")["reference"] = "urn:shn:parent#f"
		}},
		{name: "a fullUrl with a query", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "parent")["fullUrl"] = "urn:shn:parent?q"
			coverageSlot(t, doc, "subscriber")["reference"] = "urn:shn:parent?q"
		}},
		{name: "a fullUrl with a trailing slash", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "policyHolder")
			entryOf(t, doc, "parent")["fullUrl"] = "urn:shn:parent/"
			coverageSlot(t, doc, "subscriber")["reference"] = "urn:shn:parent/"
		}},
		{name: "an entry whose resource member is a list names it", edit: func(t *testing.T, doc map[string]any) {
			doc["entry"] = append(doc["entry"].([]any), map[string]any{"fullUrl": "urn:shn:x",
				"resource": []any{map[string]any{"resourceType": "Claim", "patient": map[string]any{"reference": "Patient/parent"}}}})
		}},
		{name: "a list in the entry list names it", edit: func(t *testing.T, doc map[string]any) {
			doc["entry"] = append(doc["entry"].([]any), []any{map[string]any{"reference": "Patient/parent"}})
		}},
		{name: "a reference member of the Bundle itself", edit: func(t *testing.T, doc map[string]any) {
			doc["reference"] = "Patient/parent"
		}},
		{name: "a reference member of an entry itself", edit: func(t *testing.T, doc map[string]any) {
			entryOf(t, doc, "payer")["reference"] = "Patient/parent"
		}},
		{name: "a reference member of a resource itself", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "payer")["reference"] = "Patient/parent"
		}},
		{name: "a reference member of the Coverage itself", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "cov")["reference"] = "Patient/parent"
		}},
		{name: "a reference member beside a slot's", edit: func(t *testing.T, doc map[string]any) {
			coverageSlot(t, doc, "subscriber")["identifier"] = map[string]any{"reference": "Patient/parent"}
		}},
		{name: "the Coverage spells a member it reads in another case", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "cov")["Beneficiary"] = map[string]any{"display": "x"}
		}},
		{name: "an entry spells fullUrl in another case", edit: func(t *testing.T, doc map[string]any) {
			entryOf(t, doc, "payer")["FullUrl"] = "https://payer.example/fhir/Patient/parent"
		}},
		{name: "the Bundle spells entry in another case", edit: func(t *testing.T, doc map[string]any) {
			doc["Entry"] = []any{}
		}},
		{name: "the entry is a RelatedPerson", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["resourceType"] = "RelatedPerson"
		}},
		{name: "the entry contains a resource", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["contained"] = []any{}
		}},
		{name: "the entry's identifier is not a list", edit: func(t *testing.T, doc map[string]any) {
			resourceOf(t, doc, "parent")["identifier"] = map[string]any{"value": "MRN-9"}
		}},
		{name: "the document is no Bundle", edit: func(t *testing.T, doc map[string]any) {
			doc["resourceType"] = "Parameters"
		}},
		{name: "a Patient the Bundle's Coverage does not name", edit: func(t *testing.T, doc map[string]any) {
			delete(resourceOf(t, doc, "cov"), "subscriber")
			coverageSlot(t, doc, "policyHolder")["reference"] = "Organization/payer"
		}},
	}
	parties, others := 0, 0
	for _, c := range cases {
		if c.party {
			parties++
		} else {
			others++
		}
		t.Run(c.name, func(t *testing.T) {
			doc := entryPartyDoc(t)
			parent := entryOf(t, doc, "parent")
			if c.edit != nil {
				c.edit(t, doc)
			}
			fullURL, _ := parent["fullUrl"].(string)
			if got := CoverageEntryParties(doc)[fullURL]; got != c.party {
				t.Fatalf("CoverageEntryParties names the entry: %t, want %t", got, c.party)
			}
		})
	}
	if parties < 5 || others < 20 {
		t.Fatalf("the table holds %d parties and %d others", parties, others)
	}
}

// The member's own entry is read as the member, never as a party, even when
// the Coverage's slot names it: a self-subscriber is the beneficiary.
func TestCoverageEntryParties_TheMemberIsNoParty(t *testing.T) {
	doc := entryPartyDoc(t)
	coverageSlot(t, doc, "subscriber")["reference"] = "Patient/member"
	coverageSlot(t, doc, "policyHolder")["reference"] = "Patient/member"
	if parties := CoverageEntryParties(doc); len(parties) != 0 {
		t.Fatalf("parties %v", parties)
	}
}

// One walk of the Bundle: a request of the size a gateway accepts, with every
// entry a Coverage or a parent, decides without a per-entry search of it.
func TestCoverageEntryParties_ManyEntries(t *testing.T) {
	const n = 40000
	list := make([]any, 0, 2*n+1)
	list = append(list, map[string]any{"fullUrl": "https://payer.example/fhir/Patient/member",
		"resource": map[string]any{"resourceType": "Patient", "id": "member"}})
	for i := range n {
		id := fmt.Sprintf("p%d", i)
		list = append(list,
			map[string]any{"fullUrl": "https://payer.example/fhir/Patient/" + id,
				"resource": map[string]any{"resourceType": "Patient", "id": id}},
			map[string]any{"fullUrl": "https://payer.example/fhir/Coverage/c" + id,
				"resource": map[string]any{"resourceType": "Coverage", "id": "c" + id,
					"beneficiary": map[string]any{"reference": "Patient/member"},
					"subscriber":  map[string]any{"reference": "Patient/" + id}}})
	}
	doc := map[string]any{"resourceType": "Bundle", "entry": list}
	if got := len(CoverageEntryParties(doc)); got != n {
		t.Fatalf("%d parties, want %d", got, n)
	}
}

// A Coverage's versioned reference reads the entry it names once, whatever the
// entry's size.
func TestCoverageEntryParties_ManyVersionedReferences(t *testing.T) {
	const n = 20000
	parent := map[string]any{"resourceType": "Patient", "id": "parent", "meta": map[string]any{"versionId": "2"}}
	for i := range n {
		parent[fmt.Sprintf("m%d", i)] = true
	}
	list := []any{
		map[string]any{"fullUrl": "https://payer.example/fhir/Patient/member",
			"resource": map[string]any{"resourceType": "Patient", "id": "member"}},
		map[string]any{"fullUrl": "https://payer.example/fhir/Patient/parent", "resource": parent},
	}
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		list = append(list, map[string]any{"fullUrl": "https://payer.example/fhir/Coverage/" + id,
			"resource": map[string]any{"resourceType": "Coverage", "id": id,
				"beneficiary": map[string]any{"reference": "Patient/member"},
				"subscriber":  map[string]any{"reference": "Patient/parent/_history/2"}}})
	}
	if got := CoverageEntryParties(map[string]any{"resourceType": "Bundle", "entry": list}); !got["https://payer.example/fhir/Patient/parent"] {
		t.Fatalf("parties %v", got)
	}
}

func TestBundleReferenceURL(t *testing.T) {
	doc := entryPartyDoc(t)
	cov := entryOf(t, doc, "cov")
	const parent = "https://payer.example/fhir/Patient/parent"
	for ref, want := range map[string]string{
		"Patient/parent":              parent,
		"Patient/parent/_history/2":   parent,
		parent:                        parent,
		parent + "/_history/2":        parent,
		"urn:uuid:00000000-0000-4000": "urn:uuid:00000000-0000-4000",
		"":                            "",
		"#":                           "",
		"#parent":                     "",
		"parent":                      "",
		"Patient/":                    "",
		"/Patient/parent":             "",
		"fhir/Patient/parent":         "",
		"../fhir/Patient/parent":      "",
		"Patient/parent?x=1":          "",
		"Patient/parent#y":            "",
		"Patient/parent/_history/":    "",
		parent + "/_history/":         "",
	} {
		if got := BundleReferenceURL(cov, ref); got != want {
			t.Errorf("%q points at %q, want %q", ref, got, want)
		}
	}
	// With no entry to resolve against, only an absolute reference points at
	// one.
	if got := BundleReferenceURL(nil, "Patient/parent"); got != "" {
		t.Errorf("a relative reference with no base points at %q", got)
	}
	if got := BundleReferenceURL(nil, parent); got != parent {
		t.Errorf("an absolute reference with no base points at %q", got)
	}
	// A base is what its own resource's type and id leave of the fullUrl.
	res := cov["resource"].(map[string]any)
	res["Id"] = "cov"
	if got := BundleReferenceURL(cov, "Patient/parent"); got != "" {
		t.Errorf("a resource spelling id in another case gives base %q", got)
	}
}
