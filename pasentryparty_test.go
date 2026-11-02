package shnsdk

import (
	"encoding/json"
	"testing"
)

// A dependent's Coverage may name the parent, its subscriber or policyHolder,
// as a Patient entry of the request Bundle rather than a Patient contained in
// the Coverage. That entry is the Coverage's party (CoverageEntryParties) when
// only the subscriber or policyHolder of Coverage entries name it, by its
// exact fullUrl or a relative reference resolving to it from the Coverage
// entry: the PAS response check (consistentPASGraphSubjects) does not read it
// as the patient, as it does not read a contained party, and still refuses
// every other Patient. The request walk's rows are TestPASCarriesAnotherPatientGuard
// and the twin-fence corpus.

// entryParentURL is the parent's fullUrl under the request's base.
const entryParentURL = pasBundleBaseURL + "/Patient/parent"

// entryParentSubmission is a dependent's submission whose parent rides as a
// Patient entry of its own at entryParentURL, the Coverage containing nothing,
// changed by mutate.
func entryParentSubmission(t *testing.T, mutate func(t *testing.T, b map[string]any)) (request, decision []byte, in ConformantClaimInputs) {
	t.Helper()
	request, decision, in = dependentPASRequest(t, []string{"subscriber"})
	var b map[string]any
	if err := json.Unmarshal(request, &b); err != nil {
		t.Fatal(err)
	}
	cov := coverageResourceIn(t, b)
	parent := cov["contained"].([]any)[0]
	delete(cov, "contained")
	cov["subscriber"] = map[string]any{"reference": "Patient/parent"}
	b["entry"] = append(b["entry"].([]any), map[string]any{"fullUrl": entryParentURL, "resource": parent})
	if mutate != nil {
		mutate(t, b)
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out, decision, in
}

// entryParentResponseAccepts are the dependent's submissions whose response
// the check accepts: the parent's entry is the Coverage's party.
var entryParentResponseAccepts = map[string]func(t *testing.T, b map[string]any){
	"the subscriber names it by a relative reference that resolves": nil,
	"both slots name it by its fullUrl, typed Patient": func(t *testing.T, b map[string]any) {
		cov := coverageResourceIn(t, b)
		cov["subscriber"] = map[string]any{"reference": entryParentURL, "type": "Patient"}
		cov["policyHolder"] = map[string]any{"reference": entryParentURL, "type": "http://hl7.org/fhir/StructureDefinition/Patient"}
	},
}

// entryParentResponseRefusals are the changes to such a submission whose
// response the check refuses: the parent's entry is no party, so it is a
// second patient, or the request names another patient.
var entryParentResponseRefusals = map[string]func(t *testing.T, b map[string]any){
	"no Coverage names the parent's entry": func(t *testing.T, b map[string]any) {
		delete(coverageResourceIn(t, b), "subscriber")
	},
	"the parent's entry is also named from the Coverage's extension": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["extension"] = []any{map[string]any{"url": "http://example.org/x", "valueReference": map[string]any{"reference": entryParentURL}}}
	},
	"only another member's Coverage names the parent's entry": func(t *testing.T, b map[string]any) {
		delete(coverageResourceIn(t, b), "subscriber")
		b["entry"] = append(b["entry"].([]any),
			map[string]any{"fullUrl": pasBundleBaseURL + "/Patient/MBR-OTHER", "resource": map[string]any{"resourceType": "Patient", "id": "MBR-OTHER"}},
			map[string]any{"fullUrl": pasBundleBaseURL + "/Coverage/cov-other", "resource": map[string]any{"resourceType": "Coverage", "id": "cov-other", "status": "active",
				"beneficiary": map[string]any{"reference": "Patient/MBR-OTHER"}, "subscriber": map[string]any{"reference": "Patient/parent"},
				"payor": []any{map[string]any{"reference": "Organization/org-cms-payer"}}}})
	},
	// The other slot, typed Patient, names another member by identifier
	// alone: only a slot naming the party is exempt.
	"the other slot a typed Patient naming another member by identifier": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["policyHolder"] = map[string]any{"type": "Patient", "identifier": map[string]any{"system": MemberSystem, "value": "MBR-OTHER"}}
	},
	// The other slot, typed Patient, names another patient by a literal
	// reference, relative or absolute, or names an entry that is no Patient:
	// only a slot naming the party is exempt.
	"the other slot a typed Patient naming another patient by a relative reference": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["policyHolder"] = map[string]any{"type": "Patient", "reference": "Patient/Other"}
	},
	"the other slot a typed Patient naming another patient by an absolute reference": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["policyHolder"] = map[string]any{"type": "Patient", "reference": "https://elsewhere.test/fhir/Patient/Other"}
	},
	"the other slot a typed Patient naming an entry that is no Patient": func(t *testing.T, b map[string]any) {
		coverageResourceIn(t, b)["policyHolder"] = map[string]any{"type": "Patient", "reference": "Organization/org-cms-payer"}
	},
}

// The PAS response a Responder builds retains a dependent's request, the
// parent's entry with it: the parent is the Coverage's party, so the response
// is built. Each refusal row is refused with the one assembly refusal, and is
// the subject check's own (graph validation does not stand in for it).
func TestPASResponseCarriesACoveragesEntryParty(t *testing.T) {
	for name, mutate := range entryParentResponseAccepts {
		t.Run("accepts/"+name, func(t *testing.T) {
			request, decision, in := entryParentSubmission(t, mutate)
			raw, err := buildPASOperationResponse(request, decision, in.Created)
			if err != nil {
				t.Fatalf("refused a dependent's parent carried as an entry: %v", err)
			}
			g, err := readPASGraph(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !consistentPASGraphSubjects(g, pasBundleBaseURL+"/"+in.PatientRef) {
				t.Fatal("the subject check refused the party")
			}
			if !CoverageEntryParties(g.bundle)[entryParentURL] {
				t.Fatal("the response's parent entry is no party")
			}
		})
	}
	for name, mutate := range entryParentResponseRefusals {
		t.Run("refuses/"+name, func(t *testing.T) {
			request, decision, in := entryParentSubmission(t, mutate)
			if _, err := buildPASOperationResponse(request, decision, in.Created); err == nil || err.Error() != pasAssemblyError().Error() {
				t.Fatalf("refusal %v, want %q", err, pasAssemblyError())
			}
			raw, _, _ := entryParentSubmission(t, nil)
			if !subjectsHold(t, responseOf(t, raw, decision, in), pasBundleBaseURL+"/"+in.PatientRef, func(*testing.T, map[string]any) {}) {
				t.Fatal("the control was refused")
			}
			if subjectsHold(t, responseOf(t, raw, decision, in), pasBundleBaseURL+"/"+in.PatientRef, mutate) {
				t.Fatal("the subject check accepted")
			}
		})
	}
	// A relative reference that does not resolve (the parent's entry under
	// another base) is refused by the response graph before the subject
	// check, which itself reads the parent's entry as a second patient.
	t.Run("refuses/a relative reference that does not resolve", func(t *testing.T) {
		request, decision, in := entryParentSubmission(t, func(t *testing.T, b map[string]any) {
			for _, e := range b["entry"].([]any) {
				if em := e.(map[string]any); em["fullUrl"] == entryParentURL {
					em["fullUrl"] = "https://elsewhere.example/fhir/Patient/parent"
				}
			}
		})
		if _, err := buildPASOperationResponse(request, decision, in.Created); err == nil {
			t.Fatal("accepted")
		}
		raw, _, _ := entryParentSubmission(t, nil)
		if subjectsHold(t, responseOf(t, raw, decision, in), pasBundleBaseURL+"/"+in.PatientRef, func(t *testing.T, b map[string]any) {
			for _, e := range b["entry"].([]any) {
				if em := e.(map[string]any); em["fullUrl"] == entryParentURL {
					em["fullUrl"] = "https://elsewhere.example/fhir/Patient/parent"
				}
			}
		}) {
			t.Fatal("the subject check accepted")
		}
	})
}

// responseOf is the response a Responder builds for request.
func responseOf(t *testing.T, request, decision []byte, in ConformantClaimInputs) []byte {
	t.Helper()
	raw, err := buildPASOperationResponse(request, decision, in.Created)
	if err != nil {
		t.Fatalf("refused the control: %v", err)
	}
	return raw
}
