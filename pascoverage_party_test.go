package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
)

// A dependent's Coverage names the parent, its subscriber or policyHolder, as
// a contained Patient carrying only an MRN: the Coverage's party
// (CoverageParty). The PAS builders carry it as the participant's record has
// it, and the PAS response check accepts it without reading it as the patient.

// dependentParent is the parent, contained, carrying only an MRN.
const dependentParent = `{"resourceType":"Patient","id":"parent","identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"name":[{"family":"Parent"}]}`

// dependentSlotSets are the party slot sets a dependent's Coverage may use.
var dependentSlotSets = map[string][]string{
	"subscriber":                  {"subscriber"},
	"policyHolder":                {"policyHolder"},
	"subscriber and policyHolder": {"subscriber", "policyHolder"},
}

// dependentCoverage is member's own Coverage as a dependent: contained holds
// the contained resources, each slot names "#parent", and extra adds members.
func dependentCoverage(member, contained string, slots []string, extra string) []byte {
	c := `{"resourceType":"Coverage","id":"cov-` + strings.ToLower(member) + `","status":"active",` +
		`"identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},` +
		`"system":"urn:shn:coverage","value":"` + member + `"}],` +
		`"contained":[` + contained + `],`
	for _, s := range slots {
		c += `"` + s + `":{"reference":"#parent"},`
	}
	return []byte(c + `"beneficiary":{"reference":"Patient/` + member + `"},` +
		`"relationship":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/subscriber-relationship","code":"child"}]},` +
		`"payor":[{"reference":"Organization/org-cms-payer"}]` + extra + `}`)
}

// coverageEntryOf returns the members of the one Coverage entry in a built
// Bundle, each as the bytes the Bundle holds.
func coverageEntryOf(t *testing.T, bundle []byte) map[string]json.RawMessage {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	var found map[string]json.RawMessage
	for _, e := range b.Entry {
		var m map[string]json.RawMessage
		if json.Unmarshal(e.Resource, &m) != nil || string(m["resourceType"]) != `"Coverage"` {
			continue
		}
		if found != nil {
			t.Fatal("the Bundle carries two Coverage entries")
		}
		found = m
	}
	if found == nil {
		t.Fatal("the Bundle carries no Coverage entry")
	}
	return found
}

// carriesTheParty asserts the Coverage carries its contained parent and the
// slots naming it as the record has them (the builder re-encodes the Coverage,
// and this record, compact with nothing to escape, keeps its bytes), and that
// the slots it does not name are absent.
func carriesTheParty(t *testing.T, cov map[string]json.RawMessage, slots []string) {
	t.Helper()
	if got := string(cov["contained"]); got != `[`+dependentParent+`]` {
		t.Fatalf("contained %s, want the record's own bytes %s", got, `[`+dependentParent+`]`)
	}
	for _, s := range []string{"subscriber", "policyHolder"} {
		got, ok := cov[s]
		named := false
		for _, n := range slots {
			named = named || n == s
		}
		if !named {
			if ok {
				t.Fatalf("%s %s appeared", s, got)
			}
			continue
		}
		if string(got) != `{"reference":"#parent"}` {
			t.Fatalf("%s %s, want the record's own bytes", s, got)
		}
	}
}

// The builder carries a dependent's Coverage: the slots naming the parent and
// the contained parent ride with the participant's record's JSON values, on
// the submission and on the amendment, while the beneficiary is re-homed to
// the request's Patient as before.
func TestPASBuildersCarryACoveragesParty(t *testing.T) {
	const member = "MBR-COVERED"
	for name, slots := range dependentSlotSets {
		record := dependentCoverage(member, dependentParent, slots, "")
		t.Run(name+"/entry", func(t *testing.T) {
			in := conformantSubmitInputs(t)
			payerOrg, err := pasPayerOrgEntry(in.Insurer, in.Payer)
			if err != nil {
				t.Fatal(err)
			}
			cov, err := pasCoverageEntry(record, in.PatientRef, payerOrg)
			if err != nil {
				t.Fatalf("refused a dependent's Coverage: %v", err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(cov.raw, &m); err != nil {
				t.Fatal(err)
			}
			carriesTheParty(t, m, slots)
			if string(m["beneficiary"]) != `{"reference":"`+in.PatientRef+`"}` {
				t.Fatalf("beneficiary %s", m["beneficiary"])
			}
		})
		t.Run(name+"/submit", func(t *testing.T) {
			in := conformantSubmitInputs(t)
			in.Coverage = record
			b, err := BuildConformantClaimBundle(in)
			if err != nil {
				t.Fatalf("refused a dependent's Coverage: %v", err)
			}
			carriesTheParty(t, coverageEntryOf(t, b), slots)
		})
		t.Run(name+"/update", func(t *testing.T) {
			in := conformantUpdateInputsFromGolden(t)
			in.Coverage = record
			b, err := BuildConformantClaimUpdateBundle(in)
			if err != nil {
				t.Fatalf("refused a dependent's Coverage: %v", err)
			}
			carriesTheParty(t, coverageEntryOf(t, b), slots)
		})
		// With AbsoluteRefs the Coverage, whose payor reference is made
		// absolute, is re-encoded as a whole (its members' order is not kept,
		// as for every entry whose references change): the party and its
		// slots carry the same JSON, and the slots' local references stay
		// local.
		t.Run(name+"/absolute refs", func(t *testing.T) {
			in := payerOrgEntryInputs(t)
			in.Coverage = record
			b, err := BuildConformantClaimBundle(in)
			if err != nil {
				t.Fatalf("refused a dependent's Coverage: %v", err)
			}
			cov := coverageEntryOf(t, b)
			if !jsonEqual(t, cov["contained"], []byte(`[`+dependentParent+`]`)) {
				t.Fatalf("contained %s", cov["contained"])
			}
			for _, s := range slots {
				if !jsonEqual(t, cov[s], []byte(`{"reference":"#parent"}`)) {
					t.Fatalf("%s %s", s, cov[s])
				}
			}
		})
	}
}

// The builder still refuses a subscriber or policyHolder naming anyone other
// than the member that is no party: a RelatedPerson, an Organization, another
// Patient by reference, and a contained parent that something else in the
// Coverage also names.
func TestPASBuildersRefuseWhatIsNoParty(t *testing.T) {
	const member = "MBR-COVERED"
	// Each refusal names the slot it is about.
	const other = "subscriber names someone other than the member"
	for _, tc := range []struct{ name, coverage, want string }{
		{"a contained RelatedPerson subscriber", string(dependentCoverage(member, `{"resourceType":"RelatedPerson","id":"parent","patient":{"reference":"Patient/MBR-COVERED"}}`, []string{"subscriber"}, "")), other},
		{"an Organization policyHolder", string(dependentCoverage(member, dependentParent, []string{"subscriber"}, `,"policyHolder":{"reference":"Organization/employer"}`)), "policyHolder names someone other than the member"},
		{"another Patient by reference", string(dependentCoverage(member, dependentParent, []string{"policyHolder"}, `,"subscriber":{"reference":"Patient/other"}`)), other},
		{"the parent also named from an extension", string(dependentCoverage(member, dependentParent, []string{"subscriber"}, `,"extension":[{"url":"http://example.org/x","valueReference":{"reference":"#parent"}}]`)), other},
		{"the parent also named inside its slot", strings.Replace(string(dependentCoverage(member, dependentParent, nil, "")), `"beneficiary"`, `"subscriber":{"reference":"#parent","extension":[{"url":"http://example.org/x","valueReference":{"reference":"#parent"}}]},"beneficiary"`, 1), other},
		{"the parent also named from another contained resource", string(dependentCoverage(member, dependentParent+`,{"resourceType":"RelatedPerson","id":"rp","patient":{"reference":"#parent"}}`, []string{"subscriber"}, `,"contract":[{"reference":"#rp"}]`)), other},
		{"an id that differs from the slot's", string(dependentCoverage(member, strings.Replace(dependentParent, `"id":"parent"`, `"id":"parent2"`, 1), []string{"subscriber"}, "")), other},
		// The party is a contained resource: it contains nothing, its
		// identifier is a list, and every reference it makes follows the
		// Coverage's own rule (refused unless the request carries it).
		{"a party containing a resource", string(dependentCoverage(member, strings.Replace(dependentParent, `"name":`, `"contained":[{"resourceType":"Organization","id":"o"}],"name":`, 1), []string{"subscriber"}, "")), "contains other resources"},
		{"a party with a contained list named in another case", string(dependentCoverage(member, strings.Replace(dependentParent, `"name":`, `"Contained":[{"resourceType":"Organization","id":"o"}],"name":`, 1), []string{"subscriber"}, "")), `has a member named "Contained"`},
		{"a party with an identifier named in another case", string(dependentCoverage(member, strings.Replace(dependentParent, `"identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}]`, `"Identifier":{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}`, 1), []string{"subscriber"}, "")), `has a member named "Identifier"`},
		{"a party whose identifier is not a list", string(dependentCoverage(member, strings.Replace(dependentParent, `"identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}]`, `"identifier":{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}`, 1), []string{"subscriber"}, "")), "has an identifier that is not a list"},
		{"a party referencing an outside record", string(dependentCoverage(member, strings.Replace(dependentParent, `"name":`, `"managingOrganization":{"reference":"Organization/clinic"},"name":`, 1), []string{"subscriber"}, "")), "Coverage.contained[0].managingOrganization -> Organization/clinic"},
		// A member named "reference" in another case is read as a reference.
		{"a party referencing an outside record under a case-variant reference member", string(dependentCoverage(member, strings.Replace(dependentParent, `"name":`, `"managingOrganization":{"Reference":"Organization/clinic"},"name":`, 1), []string{"subscriber"}, "")), "Coverage.contained[0].managingOrganization.Reference -> Organization/clinic"},
		// Before this change: built (the variant not read); now: refused.
		{"a Coverage extension naming an outside record under a case-variant reference member", string(dependentCoverage(member, dependentParent, []string{"subscriber"}, `,"extension":[{"url":"http://example.org/x","valueReference":{"Reference":"Organization/x"}}]`)), "Coverage.extension[0].valueReference.Reference -> Organization/x"},
		{"a party referencing an outside record from an extension", string(dependentCoverage(member, strings.Replace(dependentParent, `"name":`, `"extension":[{"url":"http://example.org/x","valueReference":{"reference":"Practitioner/dr"}}],"name":`, 1), []string{"policyHolder"}, "")), "Coverage.contained[0].extension[0].valueReference -> Practitioner/dr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := conformantSubmitInputs(t)
			in.Coverage = []byte(tc.coverage)
			_, err := BuildConformantClaimBundle(in)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// dependentPASRequest is a submission for a dependent whose Coverage names the
// parent in slots.
func dependentPASRequest(t *testing.T, slots []string) (request, decision []byte, in ConformantClaimInputs) {
	t.Helper()
	in = conformantSubmitInputs(t)
	in.Coverage = dependentCoverage(in.MemberID, dependentParent, slots, "")
	request, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	decision, err = BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision, in
}

// coverageResourceIn returns the Coverage resource of a decoded Bundle.
func coverageResourceIn(t *testing.T, b map[string]any) map[string]any {
	t.Helper()
	for _, e := range b["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		if r["resourceType"] == "Coverage" {
			return r
		}
	}
	t.Fatal("no Coverage entry")
	return nil
}

// pasResponsePartyRefusals are the changes to a dependent's submission (or
// to the response retaining it) that the PAS response check refuses.
var pasResponsePartyRefusals = map[string]func(t *testing.T, b map[string]any){
	// The party is fenced as a contained resource.
	"a party containing a resource": func(t *testing.T, b map[string]any) {
		party := coverageResourceIn(t, b)["contained"].([]any)[0].(map[string]any)
		party["contained"] = []any{map[string]any{"resourceType": "Organization", "id": "o"}}
	},
	"a party whose identifier is not a list": func(t *testing.T, b map[string]any) {
		party := coverageResourceIn(t, b)["contained"].([]any)[0].(map[string]any)
		party["identifier"] = map[string]any{"system": "urn:oid:1.2.3.4.5", "value": "MRN-9"}
	},
	// A contained Patient something else names is no party.
	"the parent also named from an extension": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueReference": map[string]any{"reference": "#parent"}}}
	},
	"the parent also named inside its slot": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["subscriber"] = map[string]any{"reference": "#parent", "extension": []any{map[string]any{"url": "http://example.org/x", "valueReference": map[string]any{"reference": "#parent"}}}}
	},
	"a typed Patient reference to the parent elsewhere": func(t *testing.T, b map[string]any) {
		cov := coverageResourceIn(t, b)
		cov["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueReference": map[string]any{"reference": "#parent", "type": "Patient"}}}
	},
	"a contained RelatedPerson in the slot": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["contained"] = []any{map[string]any{"resourceType": "RelatedPerson", "id": "parent", "patient": map[string]any{"reference": "Patient/other"}}}
	},
	"a contained parent on a resource that is not a Coverage": func(t *testing.T, b map[string]any) {
		for _, e := range b["entry"].([]any) {
			r := e.(map[string]any)["resource"].(map[string]any)
			if r["resourceType"] == "ServiceRequest" {
				var p map[string]any
				_ = json.Unmarshal([]byte(dependentParent), &p)
				r["contained"] = []any{p}
				r["subscriber"] = map[string]any{"reference": "#parent"}
			}
		}
	},
	// A contained id two contained resources share names neither copy.
	"a duplicate id, the first copy containing another member": func(t *testing.T, b map[string]any) {
		cov := coverageResourceIn(t, b)
		first := map[string]any{"resourceType": "Patient", "id": "parent", "contained": []any{map[string]any{"resourceType": "Patient", "id": "m", "identifier": []any{map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}}}}
		cov["contained"] = []any{first, map[string]any{"resourceType": "Patient", "id": "parent"}}
	},
	"a duplicate id, another member's Patient beside the parent": func(t *testing.T, b map[string]any) {
		cov := coverageResourceIn(t, b)
		cov["contained"] = append(cov["contained"].([]any), map[string]any{"resourceType": "Patient", "id": "parent", "identifier": []any{map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}})
	},
	// A slot carrying a reference member in another case names no party.
	"a party slot that also carries a case-variant reference": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["subscriber"] = map[string]any{"reference": "#parent", "type": "Patient", "Reference": "Patient/MBR-OTHER"}
	},
	// A party containing a resource in another case is no party.
	"a party with a contained list named in another case": func(t *testing.T, b map[string]any) {
		party := coverageResourceIn(t, b)["contained"].([]any)[0].(map[string]any)
		party["Contained"] = []any{map[string]any{"resourceType": "Organization", "id": "o"}}
	},
	// A standalone entry for the parent is another patient.
	"a standalone parent entry": func(t *testing.T, b map[string]any) {
		var p map[string]any
		_ = json.Unmarshal([]byte(dependentParent), &p)
		b["entry"] = append(b["entry"].([]any), map[string]any{"fullUrl": pasBundleBaseURL + "/Patient/parent", "resource": p})
	},
	// The beneficiary still binds the Coverage.
	"the beneficiary naming the parent": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["beneficiary"] = map[string]any{"reference": "#parent"}
	},
}

// The PAS response a Responder builds retains a dependent's Coverage with its
// contained parent: the parent is the Coverage's party, so it is not read as
// the patient. Everything else the check refuses, it still refuses.
func TestPASResponseCarriesACoveragesParty(t *testing.T) {
	for name, slots := range dependentSlotSets {
		t.Run(name, func(t *testing.T) {
			request, decision, in := dependentPASRequest(t, slots)
			raw, err := buildPASOperationResponse(request, decision, in.Created)
			if err != nil {
				t.Fatalf("refused a dependent's Coverage: %v", err)
			}
			// The response retains the request's entries as their JSON, as it
			// retains every entry.
			cov := coverageEntryOf(t, raw)
			if !jsonEqual(t, cov["contained"], []byte(`[`+dependentParent+`]`)) {
				t.Fatalf("the parent was not retained: %s", cov["contained"])
			}
			for _, s := range slots {
				if !jsonEqual(t, cov[s], []byte(`{"reference":"#parent"}`)) {
					t.Fatalf("%s %s", s, cov[s])
				}
			}
		})
	}
	t.Run("a slot typed Patient", func(t *testing.T) {
		request, decision, in := dependentPASRequest(t, []string{"subscriber"})
		var b map[string]any
		_ = json.Unmarshal(request, &b)
		coverageResourceIn(t, b)["subscriber"] = map[string]any{"reference": "#parent", "type": "Patient"}
		typed, _ := json.Marshal(b)
		if _, err := buildPASOperationResponse(typed, decision, in.Created); err != nil {
			t.Fatalf("refused a party slot typed Patient: %v", err)
		}
	})

	for name, mutate := range pasResponsePartyRefusals {
		t.Run("refuses/"+name, func(t *testing.T) {
			request, decision, in := dependentPASRequest(t, []string{"subscriber", "policyHolder"})
			var b map[string]any
			_ = json.Unmarshal(request, &b)
			mutate(t, b)
			bad, _ := json.Marshal(b)
			// The Responder refuses with its one assembly refusal.
			if _, err := buildPASOperationResponse(bad, decision, in.Created); err == nil || err.Error() != pasAssemblyError().Error() {
				t.Fatalf("refusal %v, want %q", err, pasAssemblyError())
			}
		})
	}

	// A response for another patient is refused whatever the party: the
	// Claim names a second Patient entry, so the member's own Patient entry,
	// the Coverage's beneficiary and the rest name someone else.
	t.Run("refuses/a response naming another patient", func(t *testing.T) {
		request, _, in := dependentPASRequest(t, []string{"subscriber", "policyHolder"})
		var b map[string]any
		_ = json.Unmarshal(request, &b)
		other := map[string]any{"resourceType": "Patient", "id": "MBR-OTHER", "identifier": []any{map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}}
		b["entry"] = append(b["entry"].([]any), map[string]any{"fullUrl": pasBundleBaseURL + "/Patient/MBR-OTHER", "resource": other})
		for _, e := range b["entry"].([]any) {
			r := e.(map[string]any)["resource"].(map[string]any)
			if r["resourceType"] == "Claim" {
				r["patient"] = map[string]any{"reference": "Patient/MBR-OTHER"}
			}
		}
		bad, _ := json.Marshal(b)
		decision, err := BuildClaimResponse("AUTH", "", "Patient/MBR-OTHER", in.Corr, in.Created)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := buildPASOperationResponse(bad, decision, in.Created); err == nil || err.Error() != pasAssemblyError().Error() {
			t.Fatalf("refusal %v, want %q", err, pasAssemblyError())
		}
	})
}

// The subject check alone: a party is skipped only as the Coverage's own
// contained resource, and its identifier must be a list.
func TestPASGraphSubjectsParty(t *testing.T) {
	request, decision, in := dependentPASRequest(t, []string{"subscriber"})
	raw, err := buildPASOperationResponse(request, decision, in.Created)
	if err != nil {
		t.Fatalf("refused a dependent's Coverage: %v", err)
	}
	patientURL := pasBundleBaseURL + "/" + in.PatientRef
	check := func(t *testing.T, mutate func(b map[string]any)) bool {
		t.Helper()
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		mutate(b)
		out, _ := json.Marshal(b)
		g, err := readPASGraph(out)
		if err != nil {
			t.Fatal(err)
		}
		return consistentPASGraphSubjects(g, patientURL)
	}
	if !check(t, func(map[string]any) {}) {
		t.Fatal("refused the party")
	}
	if check(t, func(b map[string]any) {
		coverageResourceIn(t, b)["contained"].([]any)[0].(map[string]any)["identifier"] = "MRN-9"
	}) {
		t.Fatal("accepted a party whose identifier is not a list")
	}
	if check(t, func(b map[string]any) {
		coverageResourceIn(t, b)["contained"].([]any)[0].(map[string]any)["contained"] = []any{map[string]any{"resourceType": "Organization", "id": "o"}}
	}) {
		t.Fatal("accepted a party containing a resource")
	}
	// A Patient under the party's id carried anywhere but the Coverage's
	// contained list is no party, beside the party or without it.
	if check(t, func(b map[string]any) {
		cov := coverageResourceIn(t, b)
		cov["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueResource": map[string]any{"resourceType": "Patient", "id": "parent"}}}
	}) {
		t.Fatal("accepted a Patient under the party's id outside the Coverage's contained list")
	}
	if check(t, func(b map[string]any) {
		cov := coverageResourceIn(t, b)
		party := cov["contained"].([]any)[0]
		cov["contained"] = []any{}
		cov["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueResource": party}}
	}) {
		t.Fatal("accepted a Patient carried outside the Coverage's contained list")
	}
	// Only a slot naming its party is spared the typed Patient reference
	// binding: a typed Patient reference to another contained resource
	// still binds.
	if check(t, func(b map[string]any) {
		cov := coverageResourceIn(t, b)
		cov["contained"] = append(cov["contained"].([]any), map[string]any{"resourceType": "RelatedPerson", "id": "rp", "patient": map[string]any{"reference": in.PatientRef}})
		cov["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueReference": map[string]any{"reference": "#rp", "type": "Patient"}}}
	}) {
		t.Fatal("accepted a typed Patient reference naming a contained RelatedPerson")
	}
	// The graph itself refuses a contained resource that contains another,
	// the party included.
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	coverageResourceIn(t, b)["contained"].([]any)[0].(map[string]any)["contained"] = []any{map[string]any{"resourceType": "Organization", "id": "o"}}
	nested, _ := json.Marshal(b)
	if err := validatePASBundleGraph(nested); err == nil {
		t.Fatal("the graph accepted a party containing a resource")
	}
}

// partyRecord is a dependent's Coverage whose contained parent the slots
// name, with slots written as given (each a JSON member, comma-joined).
func partyRecord(member, contained, slots string) []byte {
	return []byte(strings.Replace(string(dependentCoverage(member, contained, nil, "")), `"beneficiary"`, slots+`,"beneficiary"`, 1))
}

// buildEveryWay builds record through the entry, submit and update paths,
// returning each path's error.
func buildEveryWay(t *testing.T, record []byte) map[string]error {
	t.Helper()
	errs := map[string]error{}
	in := conformantSubmitInputs(t)
	payerOrg, err := pasPayerOrgEntry(in.Insurer, in.Payer)
	if err != nil {
		t.Fatal(err)
	}
	_, errs["entry"] = pasCoverageEntry(record, in.PatientRef, payerOrg)
	in.Coverage = record
	_, errs["submit"] = BuildConformantClaimBundle(in)
	up := conformantUpdateInputsFromGolden(t)
	up.Coverage = record
	_, errs["update"] = BuildConformantClaimUpdateBundle(up)
	return errs
}

// A slot naming the party is carried as written, so every other reference in
// it is held to the Coverage's own rule: a reference to a record the request
// does not carry is refused, naming the element, on every path.
func TestPASBuildersRefuseOutsideReferencesInAPartySlot(t *testing.T) {
	const member = "MBR-COVERED"
	for _, tc := range []struct {
		name, slots string
		want        []string
	}{
		{"an extension in the subscriber",
			`"subscriber":{"reference":"#parent","extension":[{"url":"http://example.org/x","valueReference":{"reference":"Practitioner/dr"}}]}`,
			[]string{"Coverage.subscriber.extension[0].valueReference -> Practitioner/dr"}},
		{"an identifier assigner in the policyHolder",
			`"policyHolder":{"reference":"#parent","identifier":{"system":"s","value":"v","assigner":{"reference":"Organization/other"}}}`,
			[]string{"Coverage.policyHolder.identifier.assigner -> Organization/other"}},
		{"both in one slot",
			`"subscriber":{"reference":"#parent","extension":[{"url":"http://example.org/x","valueReference":{"reference":"Practitioner/dr"}}],"identifier":{"system":"s","value":"v","assigner":{"reference":"Organization/other"}}}`,
			[]string{"Coverage.subscriber.extension[0].valueReference -> Practitioner/dr", "Coverage.subscriber.identifier.assigner -> Organization/other"}},
	} {
		for path, err := range buildEveryWay(t, partyRecord(member, dependentParent, tc.slots)) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				for _, w := range tc.want {
					if !strings.Contains(err.Error(), w) {
						t.Fatalf("refusal %q does not name %q", err, w)
					}
				}
			})
		}
	}
	// The guard reads only the party slot's other members: a slot carrying a
	// type and a display beside its reference is carried with the same JSON.
	slot := `{"reference":"#parent","type":"Patient","display":"Parent"}`
	record := partyRecord(member, dependentParent, `"subscriber":`+slot)
	for path, err := range buildEveryWay(t, record) {
		t.Run("a type and a display/"+path, func(t *testing.T) {
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
	in := conformantSubmitInputs(t)
	in.Coverage = record
	b, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := coverageEntryOf(t, b)["subscriber"]; !jsonEqual(t, got, []byte(slot)) {
		t.Fatalf("subscriber %s, want %s", got, slot)
	}
}

// A slot is a party slot only by its exact reference member, as
// CoverageParty reads it: a member named "Reference" names nothing. Through
// the builders such a slot is refused before it is read (a member named
// "reference" in another case is refused on every element this package
// reads), so the read itself is pinned at the guard too.
func TestPASBuildersReadAPartySlotByItsExactReference(t *testing.T) {
	const member = "MBR-COVERED"
	const want = `subscriber carries a member named "Reference"`
	for _, tc := range []struct{ name, slots, want string }{
		// Both names in one object are a repeated name (case-folded).
		{"a subscriber naming another patient, with the party under Reference",
			`"policyHolder":{"reference":"#parent"},"subscriber":{"reference":"Patient/MBR-OTHER","Reference":"#parent"}`,
			`repeats the member name "Reference" in Coverage.subscriber`},
		{"a subscriber naming the party only under Reference",
			`"subscriber":{"Reference":"#parent"}`, want},
		{"a subscriber naming the policyHolder's party only under Reference",
			`"policyHolder":{"reference":"#parent"},"subscriber":{"Reference":"#parent"}`, want},
	} {
		for path, err := range buildEveryWay(t, partyRecord(member, dependentParent, tc.slots)) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("refusal %q does not say %q", err, tc.want)
				}
			})
		}
	}
	var party map[string]any
	if err := json.Unmarshal([]byte(dependentParent), &party); err != nil {
		t.Fatal(err)
	}
	parties := map[string]map[string]any{"#parent": party}
	for slot, named := range map[string]bool{
		`{"reference":"#parent"}`:                                 true,
		`{"Reference":"#parent"}`:                                 false,
		`{"REFERENCE":"#parent"}`:                                 false,
		`{"reference":"Patient/MBR-OTHER","Reference":"#parent"}`: false,
		`{"reference":7}`:                                         false,
	} {
		if _, ok := coveragePartyNamed(json.RawMessage(slot), parties); ok != named {
			t.Errorf("coveragePartyNamed(%s) = %t, want %t", slot, ok, named)
		}
	}
	for elem, ref := range map[string]string{
		`{"reference":"Patient/A"}`:                         "Patient/A",
		`{"Reference":"Patient/A"}`:                         "",
		`{"reference":"Patient/A","Reference":"Patient/B"}`: "Patient/A",
		`{"Reference":"Patient/B","reference":"Patient/A"}`: "Patient/A",
		`{"reference":7}`:                                   "",
	} {
		if got := coverageReferenceOf(json.RawMessage(elem)); got != ref {
			t.Errorf("coverageReferenceOf(%s) = %q, want %q", elem, got, ref)
		}
	}
}

// Before this change, a subscriber, policyHolder, beneficiary or payor whose
// reference member was also written in another case ("Reference") was read
// case-insensitively, the last member winning, and re-pointed (or replaced)
// on that reading: what the record asserted under the exact name could be
// dropped. Now such an element is refused, naming it and the member: both
// names in one object are a repeated name (case-folded), and a variant alone
// is not a reference this package reads.
func TestPASBuildersRefuseCaseVariantReferenceMembers(t *testing.T) {
	const member = "MBR-COVERED"
	for _, tc := range []struct{ name, record, want string }{
		// before this change: re-pointed to the member (MBR-OTHER dropped); now: refused.
		{"subscriber: conflicting, the variant naming the member",
			string(partyRecord(member, dependentParent, `"policyHolder":{"reference":"#parent"},"subscriber":{"reference":"Patient/MBR-OTHER","Reference":"Patient/MBR-COVERED"}`)),
			`repeats the member name "Reference" in Coverage.subscriber`},
		// before this change: refused as naming someone else; now: refused as a repeated name.
		{"subscriber: conflicting, the variant naming another",
			string(partyRecord(member, dependentParent, `"policyHolder":{"reference":"#parent"},"subscriber":{"reference":"Patient/MBR-COVERED","Reference":"Patient/MBR-OTHER"}`)),
			`repeats the member name "Reference" in Coverage.subscriber`},
		// before this change: re-pointed to the member; now: refused.
		{"subscriber: only a variant",
			string(partyRecord(member, dependentParent, `"policyHolder":{"reference":"#parent"},"subscriber":{"Reference":"Patient/MBR-COVERED"}`)),
			`subscriber carries a member named "Reference"`},
		// before this change: re-pointed to the member (MBR-OTHER dropped); now: refused.
		{"policyHolder: conflicting, the variant naming the member",
			string(partyRecord(member, dependentParent, `"subscriber":{"reference":"#parent"},"policyHolder":{"reference":"Patient/MBR-OTHER","REFERENCE":"Patient/MBR-COVERED"}`)),
			`repeats the member name "REFERENCE" in Coverage.policyHolder`},
		// before this change: re-pointed to the member; now: refused.
		{"policyHolder: only an upper-case variant",
			string(partyRecord(member, dependentParent, `"subscriber":{"reference":"#parent"},"policyHolder":{"REFERENCE":"Patient/MBR-COVERED"}`)),
			`policyHolder carries a member named "REFERENCE"`},
		// before this change: re-pointed to the member; now: refused.
		{"policyHolder: only a variant",
			string(partyRecord(member, dependentParent, `"subscriber":{"reference":"#parent"},"policyHolder":{"Reference":"Patient/MBR-COVERED"}`)),
			`policyHolder carries a member named "Reference"`},
		// before this change: resolved (the beneficiary replaced by the member's Patient entry); now: refused.
		{"beneficiary: conflicting",
			strings.Replace(string(dependentCoverage(member, dependentParent, []string{"subscriber"}, "")), `"beneficiary":{"reference":"Patient/MBR-COVERED"}`, `"beneficiary":{"reference":"Patient/MBR-COVERED","Reference":"Patient/MBR-OTHER"}`, 1),
			`repeats the member name "Reference" in Coverage.beneficiary`},
		// before this change: resolved (replaced by the member's Patient entry); now: refused.
		{"beneficiary: only a variant",
			strings.Replace(string(dependentCoverage(member, dependentParent, []string{"subscriber"}, "")), `"beneficiary":{"reference":"Patient/MBR-COVERED"}`, `"beneficiary":{"Reference":"Patient/MBR-COVERED"}`, 1),
			`beneficiary carries a member named "Reference"`},
		// before this change: resolved (re-pointed to the payer Organization entry); now: refused.
		{"payor: conflicting",
			strings.Replace(string(dependentCoverage(member, dependentParent, []string{"subscriber"}, "")), `"payor":[{"reference":"Organization/org-cms-payer"}]`, `"payor":[{"reference":"Organization/org-cms-payer","Reference":"Organization/other"}]`, 1),
			`repeats the member name "Reference" in Coverage.payor[0]`},
		// before this change: resolved (re-pointed to the payer Organization entry); now: refused.
		{"payor: only a variant",
			strings.Replace(string(dependentCoverage(member, dependentParent, []string{"subscriber"}, "")), `"payor":[{"reference":"Organization/org-cms-payer"}]`, `"payor":[{"Reference":"Organization/org-cms-payer"}]`, 1),
			`payor[0] carries a member named "Reference"`},
	} {
		for path, err := range buildEveryWay(t, []byte(tc.record)) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("refusal %q does not say %q", err, tc.want)
				}
			})
		}
	}
	// An ordinary slot naming the member is still re-homed to the request's
	// Patient entry, and the party slot beside it carried.
	for slot, other := range map[string]string{"subscriber": "policyHolder", "policyHolder": "subscriber"} {
		record := partyRecord(member, dependentParent, `"`+other+`":{"reference":"#parent"},"`+slot+`":{"reference":"Patient/MBR-COVERED"}`)
		for path, err := range buildEveryWay(t, record) {
			t.Run("an ordinary "+slot+"/"+path, func(t *testing.T) {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
			})
		}
		in := conformantSubmitInputs(t)
		in.Coverage = record
		b, err := BuildConformantClaimBundle(in)
		if err != nil {
			t.Fatal(err)
		}
		cov := coverageEntryOf(t, b)
		if !jsonEqual(t, cov[slot], []byte(`{"reference":"`+in.PatientRef+`"}`)) {
			t.Fatalf("%s %s, want the request's Patient", slot, cov[slot])
		}
		if !jsonEqual(t, cov[other], []byte(`{"reference":"#parent"}`)) {
			t.Fatalf("%s %s", other, cov[other])
		}
	}
}

// A party carrying a case-variant "Id" member naming another id, referenced
// elsewhere, cannot hide its outside reference: the record repeats a member
// name (case-folded) and is refused before anything is read. (The party is
// walked at its own index in the contained list, the element CoverageParty
// decided on.)
func TestPASBuildersWalkThePartyByItsExactID(t *testing.T) {
	const member = "MBR-COVERED"
	record := dependentCoverage(member,
		`{"resourceType":"Patient","id":"parent","managingOrganization":{"reference":"Organization/outside"},"Id":"zzz"}`,
		[]string{"subscriber"}, `,"extension":[{"url":"http://example.org/x","valueReference":{"reference":"#zzz"}}]`)
	for path, err := range buildEveryWay(t, record) {
		t.Run(path, func(t *testing.T) {
			if err == nil {
				t.Fatal("accepted")
			}
			if want := `repeats the member name "Id" in Coverage.contained[0]`; !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not say %q", err, want)
			}
		})
	}
}

// The payer Organization a re-point displaces is dropped from contained by its
// exact resourceType and id, at the guard: a contained resource that spells
// them only in another case is not that Organization, and stays (the
// contained-reference check then refuses it for its "Id" member).
func TestDropContainedPayerOrgReadsExactNames(t *testing.T) {
	for name, tc := range map[string]struct {
		contained string
		dropped   bool
	}{
		"the payer Organization":      {`{"resourceType":"Organization","id":"org-cms-payer"}`, true},
		"only case-variant names":     {`{"ResourceType":"Organization","Id":"org-cms-payer"}`, false},
		"a case-variant resourceType": {`{"ResourceType":"Organization","id":"org-cms-payer"}`, false},
		"a case-variant id":           {`{"resourceType":"Organization","Id":"org-cms-payer"}`, false},
		"a Patient with a variant id": {`{"resourceType":"Patient","Id":"org-cms-payer"}`, false},
		"another Organization":        {`{"resourceType":"Organization","id":"other"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]json.RawMessage{
				"payor":     json.RawMessage(`[{"reference":"Organization/org-cms-payer"}]`),
				"contained": json.RawMessage(`[` + tc.contained + `]`),
			}
			if err := dropContainedPayerOrg(m, "org-cms-payer"); err != nil {
				t.Fatal(err)
			}
			if _, kept := m["contained"]; kept == tc.dropped {
				t.Fatalf("contained %s after the drop, want dropped=%t", m["contained"], tc.dropped)
			}
		})
	}
}

// Before this change a Coverage record that repeated a member name (exactly,
// or in another case) was read keeping the last occurrence, and built. Now it
// is refused on every path, naming the element and the member: the network
// refuses a request carrying a repeated name at every level.
func TestPASBuildersRefuseRepeatedMemberNames(t *testing.T) {
	const member = "MBR-COVERED"
	for _, tc := range []struct{ name, record, want string }{
		// before this change: built, carrying Patient/MBR-OTHER; now: refused.
		{"a repeated reference in a party slot",
			string(partyRecord(member, dependentParent, `"subscriber":{"reference":"Patient/MBR-OTHER","reference":"#parent"}`)),
			`repeats the member name "reference" in Coverage.subscriber`},
		// before this change: built, carrying Organization/outside; now: refused.
		{"a repeated member in the party",
			string(dependentCoverage(member, `{"resourceType":"Patient","id":"parent","managingOrganization":{"reference":"Organization/outside"},"managingOrganization":{"display":"x"}}`, []string{"subscriber"}, "")),
			`repeats the member name "managingOrganization" in Coverage.contained[0]`},
		// before this change: built, the RelatedPerson carried as a party; now: refused.
		{"a repeated resourceType in the party",
			string(dependentCoverage(member, `{"resourceType":"RelatedPerson","resourceType":"Patient","id":"parent"}`, []string{"subscriber"}, "")),
			`repeats the member name "resourceType" in Coverage.contained[0]`},
		// before this change: built, the party read as the payer
		// Organization and dropped; now: refused.
		{"a case-folded repeat in the party",
			string(dependentCoverage(member, `{"resourceType":"Patient","id":"parent","ResourceType":"Organization","Id":"org-cms-payer"}`, []string{"subscriber"}, "")),
			`repeats the member name "ResourceType" in Coverage.contained[0]`},
		// A Coverage with no party. Before this change: built, the later
		// start read; now: refused.
		{"a case-folded repeat in a Coverage that is no dependent's",
			strings.Replace(string(testMemberCoverage(member)), `"resourceType":"Coverage"`, `"resourceType":"Coverage","period":{"start":"2026-01-01","Start":"2025-01-01"}`, 1),
			`repeats the member name "Start" in Coverage.period`},
	} {
		for path, err := range buildEveryWay(t, []byte(tc.record)) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("refusal %q does not say %q", err, tc.want)
				}
			})
		}
	}
}

// A Coverage may name two parties, one in each slot: both ride, each is
// walked, and the response check carries both.
func TestPASBuildersCarryTwoParties(t *testing.T) {
	const member = "MBR-COVERED"
	a := `{"resourceType":"Patient","id":"a","identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-A"}]}`
	b := `{"resourceType":"Patient","id":"b","identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-B"}],"managingOrganization":{"reference":"Organization/outside"}}`
	slots := `"subscriber":{"reference":"#a"},"policyHolder":{"reference":"#b"}`
	for path, err := range buildEveryWay(t, partyRecord(member, a+","+b, slots)) {
		t.Run("the second party's outside reference/"+path, func(t *testing.T) {
			if err == nil {
				t.Fatal("accepted")
			}
			if want := "Coverage.contained[1].managingOrganization -> Organization/outside"; !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not name %q", err, want)
			}
		})
	}
	b = strings.Replace(b, `,"managingOrganization":{"reference":"Organization/outside"}`, "", 1)
	record := partyRecord(member, a+","+b, slots)
	for path, err := range buildEveryWay(t, record) {
		t.Run(path, func(t *testing.T) {
			if err != nil {
				t.Fatalf("refused two parties: %v", err)
			}
		})
	}
	in := conformantSubmitInputs(t)
	in.Coverage = record
	request, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := coverageEntryOf(t, request)["contained"]; !jsonEqual(t, got, []byte(`[`+a+`,`+b+`]`)) {
		t.Fatalf("contained %s", got)
	}
	decision, err := BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := buildPASOperationResponse(request, decision, in.Created)
	if err != nil {
		t.Fatalf("the response refused two parties: %v", err)
	}
	// At the guard, each slot typed Patient so the party exemption is what
	// lets each through.
	if !subjectsHold(t, raw, pasBundleBaseURL+"/"+in.PatientRef, func(t *testing.T, bb map[string]any) {
		c := coverageResourceIn(t, bb)
		c["subscriber"] = map[string]any{"reference": "#a", "type": "Patient"}
		c["policyHolder"] = map[string]any{"reference": "#b", "type": "Patient"}
	}) {
		t.Fatal("the subject check refused two parties")
	}
}

// A contained id two contained resources share names neither: the builder
// refuses the slot naming it, whichever copy holds.
func TestPASBuildersRefuseADuplicatePartyID(t *testing.T) {
	const member = "MBR-COVERED"
	want := map[string]string{
		"the first copy contains another member": "subscriber names a contained Patient that contains other resources",
		"two clean copies":                       "subscriber names someone other than the member",
	}
	for name, contained := range map[string]string{
		"the first copy contains another member": `{"resourceType":"Patient","id":"parent","contained":[{"resourceType":"Patient","id":"m","identifier":[{"system":"urn:shn:member","value":"MBR-OTHER"}]}]},{"resourceType":"Patient","id":"parent"}`,
		"two clean copies":                       dependentParent + `,{"resourceType":"Patient","id":"parent","identifier":[{"system":"urn:shn:member","value":"MBR-OTHER"}]}`,
	} {
		for path, err := range buildEveryWay(t, dependentCoverage(member, contained, []string{"subscriber"}, "")) {
			t.Run(name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				// A copy that does not hold as a contained resource is named
				// as such; two clean copies name someone other than the member.
				if !strings.Contains(err.Error(), want[name]) {
					t.Fatalf("refusal %q does not say %q", err, want[name])
				}
			})
		}
	}
}

// acceptedDependentResponse is the response a Responder builds for a
// dependent's submission, with the patient identity its subjects bind to.
func acceptedDependentResponse(t *testing.T) (raw []byte, patientURL string) {
	t.Helper()
	request, decision, in := dependentPASRequest(t, []string{"subscriber", "policyHolder"})
	raw, err := buildPASOperationResponse(request, decision, in.Created)
	if err != nil {
		t.Fatalf("refused a dependent's Coverage: %v", err)
	}
	return raw, pasBundleBaseURL + "/" + in.PatientRef
}

// subjectsHold runs the subject check alone over raw changed by mutate.
func subjectsHold(t *testing.T, raw []byte, patientURL string, mutate func(t *testing.T, b map[string]any)) bool {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	mutate(t, b)
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	g, err := readPASGraph(out)
	if err != nil {
		t.Fatal(err)
	}
	return consistentPASGraphSubjects(g, patientURL)
}

// The subject check alone refuses a Coverage carried inside another resource:
// only a Bundle entry's own Coverage has a party.
func TestPASGraphSubjectsPartyAtTheGuard(t *testing.T) {
	raw, patientURL := acceptedDependentResponse(t)
	if !subjectsHold(t, raw, patientURL, func(*testing.T, map[string]any) {}) {
		t.Fatal("refused the party")
	}
	other := func() map[string]any {
		return map[string]any{"resourceType": "Patient", "id": "parent", "identifier": []any{map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}}
	}
	for name, mutate := range map[string]func(t *testing.T, b map[string]any){
		"a Coverage inside a ServiceRequest extension naming another member's Patient": func(t *testing.T, b map[string]any) {
			for _, e := range b["entry"].([]any) {
				r := e.(map[string]any)["resource"].(map[string]any)
				if r["resourceType"] != "ServiceRequest" {
					continue
				}
				q := other()
				q["id"] = "q"
				nested := map[string]any{
					"resourceType": "Coverage", "id": "nested", "status": "active",
					"contained":   []any{q},
					"subscriber":  map[string]any{"reference": "#q"},
					"beneficiary": map[string]any{"reference": strings.TrimPrefix(patientURL, pasBundleBaseURL+"/")},
					"payor":       []any{map[string]any{"reference": "Organization/org-cms-payer"}},
				}
				r["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueResource": nested}}
				return
			}
			t.Fatal("no ServiceRequest entry")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if subjectsHold(t, raw, patientURL, mutate) {
				t.Fatal("accepted")
			}
		})
	}
}

// Every refusal TestPASResponseCarriesACoveragesParty asserts end to end is
// also the subject check's own: graph validation does not stand in for it.
func TestPASResponsePartyRefusalsAtTheGuard(t *testing.T) {
	raw, patientURL := acceptedDependentResponse(t)
	for name, mutate := range pasResponsePartyRefusals {
		t.Run(name, func(t *testing.T) {
			if subjectsHold(t, raw, patientURL, mutate) {
				t.Fatal("accepted")
			}
		})
	}
	// A response for another patient: the check bound to that patient refuses
	// the member's Coverage and Patient entry.
	t.Run("a response naming another patient", func(t *testing.T) {
		otherURL := pasBundleBaseURL + "/Patient/MBR-OTHER"
		if subjectsHold(t, raw, otherURL, func(t *testing.T, b map[string]any) {
			other := map[string]any{"resourceType": "Patient", "id": "MBR-OTHER", "identifier": []any{map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}}
			b["entry"] = append(b["entry"].([]any), map[string]any{"fullUrl": otherURL, "resource": other})
			for _, e := range b["entry"].([]any) {
				r := e.(map[string]any)["resource"].(map[string]any)
				if r["resourceType"] == "Claim" || r["resourceType"] == "ClaimResponse" {
					r["patient"] = map[string]any{"reference": "Patient/MBR-OTHER"}
				}
			}
		}) {
			t.Fatal("accepted")
		}
	})
}

// A contained resource with both "id" and "Id", the Coverage referencing only
// the variant: before this change the request was built (the variant read as
// its id); now the Coverage is refused for the repeated name, and the
// contained-reference check refuses the variant on its own (at the guard).
func TestPASContainedCheckReadsTheExactID(t *testing.T) {
	const member = "MBR-COVERED"
	record := dependentCoverage(member,
		dependentParent+`,{"resourceType":"Organization","id":"x","Id":"y"}`,
		[]string{"subscriber"}, `,"contract":[{"reference":"#y"}]`)
	for path, err := range buildEveryWay(t, record) {
		if path == "entry" {
			continue // the check runs over the assembled request
		}
		t.Run(path, func(t *testing.T) {
			if err == nil {
				t.Fatal("accepted")
			}
			if want := `repeats the member name "Id" in Coverage.contained[1]`; !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not say %q", err, want)
			}
		})
	}
	bundle := `{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Coverage","id":"c","contained":[{"resourceType":"Organization","id":"x","Id":"y"}],"contract":[{"reference":"#y"}]}}]}`
	if err := checkPASContainedReferenced([]byte(bundle)); err == nil || !strings.Contains(err.Error(), `contains Organization with a member named "Id"`) {
		t.Fatalf("checkPASContainedReferenced = %v", err)
	}
}

// A contained resource whose id member is written in another case ("Id") is
// refused, as a reference member in another case is: a local reference names
// a contained resource by its exact id. Before this change (in the last
// release) such a resource was read by the variant: refused when nothing
// named it, accepted when "#<variant>" did. Now both are refused, naming the
// resource and the member.
func TestPASContainedCheckRefusesACaseVariantID(t *testing.T) {
	const member = "MBR-COVERED"
	for name, extra := range map[string]string{
		// before this change: refused as unreferenced; now: refused for the variant.
		"named by nothing": ``,
		// before this change: accepted; now: refused.
		"named by the variant": `,"contract":[{"reference":"#y"}]`,
	} {
		record := dependentCoverage(member, dependentParent+`,{"resourceType":"Organization","Id":"y"}`, []string{"subscriber"}, extra)
		for path, err := range buildEveryWay(t, record) {
			if path == "entry" {
				continue // the check runs over the assembled request
			}
			t.Run(name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				if want := `contains Organization with a member named "Id"`; !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not say %q", err, want)
				}
			})
		}
	}
	// At the guard, on a Claim's contained list as on a Coverage's.
	bundle := `{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim","id":"c","contained":[{"resourceType":"Organization","ID":"o"}],"insurer":{"reference":"#o"}}}]}`
	if err := checkPASContainedReferenced([]byte(bundle)); err == nil || !strings.Contains(err.Error(), `contains Organization with a member named "ID"`) {
		t.Fatalf("checkPASContainedReferenced = %v", err)
	}
	if err := checkPASContainedReferenced([]byte(strings.Replace(bundle, `"ID":"o"`, `"id":"o"`, 1))); err != nil {
		t.Fatalf("refused an exact id: %v", err)
	}
}

// The payer Organization a re-point displaces is dropped only when nothing
// else still names it: a reference the Coverage's party (or any other
// contained resource) makes to it keeps it, so the party's reference still
// resolves inside the Coverage. Its own references to itself do not count.
func TestDropContainedPayerOrgKeepsWhatAContainedResourceNames(t *testing.T) {
	const org = `{"resourceType":"Organization","id":"org-cms-payer"}`
	for name, tc := range map[string]struct {
		contained string
		dropped   bool
	}{
		"named by the party":                   {`{"resourceType":"Patient","id":"parent","managingOrganization":{"reference":"#org-cms-payer"}},` + org, false},
		"named by another contained resource":  {`{"resourceType":"Observation","id":"o","performer":[{"reference":"#org-cms-payer"}]},` + org, false},
		"named by the party in another case":   {`{"resourceType":"Patient","id":"parent","managingOrganization":{"Reference":"#org-cms-payer"}},` + org, false},
		"named only by another copy of itself": {`{"resourceType":"Organization","id":"org-cms-payer","partOf":{"reference":"#org-cms-payer"}},` + org, true},
		"named only by itself":                 {`{"resourceType":"Organization","id":"org-cms-payer","partOf":{"reference":"#org-cms-payer"}}`, true},
		"named by nothing":                     {org, true},
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]json.RawMessage{
				"payor":     json.RawMessage(`[{"reference":"Organization/org-cms-payer"}]`),
				"contained": json.RawMessage(`[` + tc.contained + `]`),
			}
			if err := dropContainedPayerOrg(m, "org-cms-payer"); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(m["contained"]), `"resourceType":"Organization"`); got == tc.dropped {
				t.Fatalf("contained %s after the drop, want dropped=%t", m["contained"], tc.dropped)
			}
		})
	}
	// Through every builder path: a dependent's Coverage whose party names
	// the contained payer Organization builds, and the Organization stays in
	// the carried Coverage, so the party's reference resolves.
	const member = "MBR-COVERED"
	parent := `{"resourceType":"Patient","id":"parent","identifier":[{"system":"urn:oid:1.2.3.4.5","value":"MRN-9"}],"managingOrganization":{"reference":"#org-cms-payer"}}`
	record := []byte(strings.Replace(string(dependentCoverage(member, parent+","+org, []string{"subscriber"}, "")),
		`"payor":[{"reference":"Organization/org-cms-payer"}]`, `"payor":[{"reference":"#org-cms-payer"}]`, 1))
	for path, err := range buildEveryWay(t, record) {
		t.Run("built/"+path, func(t *testing.T) {
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
	in := conformantSubmitInputs(t)
	in.Coverage = record
	request, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := coverageEntryOf(t, request)["contained"]; !strings.Contains(string(got), `"id":"org-cms-payer"`) {
		t.Fatalf("the Organization the party names was dropped: contained %s", got)
	}
	// A party naming it under "Reference" keeps it too, and the request is
	// refused (a reference is read by its exact name) rather than built with
	// a reference left pointing at nothing.
	variant := []byte(strings.Replace(string(record), `"managingOrganization":{"reference":"#org-cms-payer"}`, `"managingOrganization":{"Reference":"#org-cms-payer"}`, 1))
	for path, err := range buildEveryWay(t, variant) {
		if path == "entry" {
			continue // the entry path drops nothing; the bundle paths do
		}
		t.Run("named in another case/"+path, func(t *testing.T) {
			if err == nil {
				t.Fatal("built a request whose party names a dropped Organization")
			}
			if want := `Organization "org-cms-payer"`; !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not name %s", err, want)
			}
		})
	}
	// The Coverage itself naming it under "Reference" (an extension) keeps
	// it too, and the request is refused for it.
	outside := []byte(strings.Replace(string(dependentCoverage(member, dependentParent+","+org, []string{"subscriber"},
		`,"extension":[{"url":"http://example.org/x","valueReference":{"Reference":"#org-cms-payer"}}]`)),
		`"payor":[{"reference":"Organization/org-cms-payer"}]`, `"payor":[{"reference":"#org-cms-payer"}]`, 1))
	for path, err := range buildEveryWay(t, outside) {
		if path == "entry" {
			continue // the entry path drops nothing; the bundle paths do
		}
		t.Run("named by the Coverage in another case/"+path, func(t *testing.T) {
			if err == nil {
				t.Fatal("built a request whose Coverage names a dropped Organization")
			}
			if want := `Organization "org-cms-payer"`; !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not name %s", err, want)
			}
		})
	}
}

// The Claim's insurer re-point drops its displaced payer Organization by the
// same rule: one the Claim itself still names, under a member named
// reference in any case, stays.
func TestDropContainedPayerOrgKeepsWhatTheClaimNames(t *testing.T) {
	for name, tc := range map[string]struct {
		extension string
		dropped   bool
	}{
		"named by nothing":         {``, true},
		"named by an extension":    {`,"extension":[{"url":"http://example.org/x","valueReference":{"reference":"#org-cms-payer"}}]`, false},
		"named in another case":    {`,"extension":[{"url":"http://example.org/x","valueReference":{"Reference":"#org-cms-payer"}}]`, false},
		"named by another id only": {`,"extension":[{"url":"http://example.org/x","valueReference":{"reference":"#other"}}]`, true},
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]json.RawMessage
			claim := `{"resourceType":"Claim","insurer":{"reference":"Organization/org-cms-payer"},"contained":[{"resourceType":"Organization","id":"org-cms-payer"}]` + tc.extension + `}`
			if err := json.Unmarshal([]byte(claim), &m); err != nil {
				t.Fatal(err)
			}
			if err := dropContainedPayerOrg(m, "org-cms-payer"); err != nil {
				t.Fatal(err)
			}
			if _, kept := m["contained"]; kept == tc.dropped {
				t.Fatalf("contained %s after the drop, want dropped=%t", m["contained"], tc.dropped)
			}
		})
	}
}

// A subscriber or policyHolder is re-pointed to the request's Patient only
// when it and the beneficiary name the same Patient by a literal reference.
// When the beneficiary names no literal Patient (an identifier only, or none),
// earlier releases read both as naming nobody and re-pointed the slot to the
// member, dropping what the record asserted; now the slot is refused, naming
// it.
func TestPASBuildersRefuseASlotWhenTheBeneficiaryNamesNoPatient(t *testing.T) {
	const member = "MBR-COVERED"
	const literal = `"beneficiary":{"reference":"Patient/MBR-COVERED"}`
	identifierOnly := `"beneficiary":{"identifier":{"system":"urn:shn:member","value":"MBR-COVERED"}}`
	record := func(contained, slots, extra, beneficiary string) string {
		r := string(partyRecord(member, contained, slots))
		if extra != "" {
			r = strings.Replace(r, `"beneficiary"`, extra+`,"beneficiary"`, 1)
		}
		if beneficiary == "" {
			return strings.Replace(r, literal+",", "", 1)
		}
		return strings.Replace(r, literal, beneficiary, 1)
	}
	other := `{"resourceType":"Patient","id":"parent","identifier":[{"system":"urn:shn:member","value":"MBR-OTHER"}]}`
	for _, tc := range []struct{ name, record, want string }{
		// before this change: re-pointed to the member (the RelatedPerson dropped); now: refused.
		{"an identifier-only beneficiary and a RelatedPerson policyHolder",
			record(dependentParent, `"subscriber":{"reference":"#parent"},"policyHolder":{"reference":"RelatedPerson/rp"}`, "", identifierOnly),
			"policyHolder names someone other than the member"},
		// before this change: re-pointed to the member (the employer dropped); now: refused.
		{"no beneficiary and an Organization subscriber",
			strings.Replace(record(dependentParent, `"subscriber":{"reference":"Organization/employer"}`, "", ""), `"contained":[`+dependentParent+`],`, "", 1),
			"subscriber names someone other than the member"},
		// before this change: re-pointed to the member (the other member's
		// Patient still riding through the extension); now: refused.
		{"no beneficiary and a subscriber naming a contained Patient that is no party",
			record(other, `"subscriber":{"reference":"#parent"}`, `"extension":[{"url":"http://example.org/x","valueReference":{"reference":"#parent"}}]`, ""),
			"subscriber names someone other than the member"},
		// before this change: re-pointed to the member (the identifier dropped); now: refused.
		{"an identifier-only beneficiary and an identifier-only subscriber",
			strings.Replace(record(dependentParent, `"subscriber":{"identifier":{"system":"urn:shn:member","value":"MBR-COVERED"}}`, "", identifierOnly), `"contained":[`+dependentParent+`],`, "", 1),
			"subscriber names someone other than the member"},
		// before this change: re-pointed to the member (both keys empty); now: refused.
		{"a urn:uuid beneficiary and the same urn:uuid subscriber",
			strings.Replace(record(dependentParent, `"subscriber":{"reference":"urn:uuid:3f1e5a52-6c9b-4f7e-9d2a-1b8c7e4f0a11"}`, "", `"beneficiary":{"reference":"urn:uuid:3f1e5a52-6c9b-4f7e-9d2a-1b8c7e4f0a11"}`), `"contained":[`+dependentParent+`],`, "", 1),
			"subscriber names someone other than the member"},
		// before this change: re-pointed to the member (a null slot rewritten
		// into a reference); now: refused.
		{"an identifier-only beneficiary and a null subscriber",
			strings.Replace(record(dependentParent, `"subscriber":null`, "", identifierOnly), `"contained":[`+dependentParent+`],`, "", 1),
			"subscriber names someone other than the member"},
	} {
		for path, err := range buildEveryWay(t, []byte(tc.record)) {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				if err == nil {
					t.Fatal("accepted")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("refusal %q does not say %q", err, tc.want)
				}
			})
		}
	}
}
