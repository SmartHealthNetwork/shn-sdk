package shnsdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestResponderPASOperationsReturnCompleteBundle(t *testing.T) {
	h, ident, _ := newPAHarness(t)
	_, srv := h.makeResponderSrv(t, ident, &paTestAdjudicator{now: h.now})
	for _, tc := range []struct {
		name     string
		clinical ClinicalContext
	}{
		{"approve", ClinicalContext{ConservativeTherapyWeeks: 8}},
		{"deny", ClinicalContext{ConservativeTherapyWeeks: 4}},
		{"pend", ClinicalContext{ConservativeTherapyWeeks: 8, PriorSurgery: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qr := answeredQR(t, "MBR-001", tc.clinical, h.now)
			request := buildConformantClaim(t, "MBR-001", "bundle-"+tc.name, qr, h.now)
			env, hdr := h.buildForwardEnv(t, "pas-claim", "pas-submit", "bundle-"+tc.name, request)
			resp := postInbound(t, srv, env, hdr)
			body := readBody(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
			raw := h.openResponse(t, body)
			var b struct {
				ResourceType string `json:"resourceType"`
				Entry        []struct {
					Resource struct {
						ResourceType string `json:"resourceType"`
					} `json:"resource"`
				} `json:"entry"`
			}
			if err := json.Unmarshal(raw, &b); err != nil {
				t.Fatal(err)
			}
			if b.ResourceType != "Bundle" {
				t.Fatalf("operation emitted %s, want complete Bundle", b.ResourceType)
			}
			types := map[string]bool{}
			for _, e := range b.Entry {
				types[e.Resource.ResourceType] = true
			}
			for _, typ := range []string{"ClaimResponse", "Claim", "Patient", "Coverage", "ServiceRequest", "QuestionnaireResponse", "Organization"} {
				if !types[typ] {
					t.Errorf("missing %s", typ)
				}
			}
		})
	}
}

func TestPASOperationGraphRetainsInputsAndRejectsMutations(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := buildPASOperationResponse(request, decision, in.Created)
	if err != nil {
		t.Fatal(err)
	}
	var req, out map[string]any
	if err := json.Unmarshal(request, &req); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := out["entry"].([]any)
	for i, entry := range req["entry"].([]any) {
		before, _ := json.Marshal(entry)
		after, _ := json.Marshal(got[i+1])
		if string(before) != string(after) {
			t.Fatalf("entry %d changed", i)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing insurer": func(b map[string]any) {
			a := b["entry"].([]any)
			for i, v := range a {
				if v.(map[string]any)["resource"].(map[string]any)["resourceType"] == "Organization" {
					b["entry"] = append(a[:i], a[i+1:]...)
					return
				}
			}
		},
		"duplicate entry": func(b map[string]any) { a := b["entry"].([]any); b["entry"] = append(a, a[0]) },
		"foreign base patient": func(b map[string]any) {
			b["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)["patient"] = map[string]any{"reference": "https://foreign.example/Patient/MBR-COVERED"}
		},
		"foreign contained patient": func(b map[string]any) {
			b["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)["contained"] = []any{map[string]any{"resourceType": "Patient", "id": "other"}}
		},
		"dangling provenance": func(b map[string]any) {
			b["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)["extension"] = []any{map[string]any{"url": "https://example.org/source", "valueReference": map[string]any{"reference": "Organization/missing"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var b map[string]any
			_ = json.Unmarshal(request, &b)
			mutate(b)
			bad, _ := json.Marshal(b)
			if _, err := buildPASOperationResponse(bad, decision, in.Created); err == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
}

func TestPASOperationGraphBoundsAndDuplicateKeys(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, _ := BuildConformantClaimBundle(in)
	decision, _ := BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	if _, err := buildPASOperationResponse([]byte(`{"resourceType":"Bundle","resourceType":"Bundle","entry":[]}`), decision, in.Created); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
	var req map[string]any
	_ = json.Unmarshal(request, &req)
	var extras []any
	for i := 0; i < pasGraphMaxResources; i++ {
		extras = append(extras, map[string]any{"fullUrl": fmt.Sprintf("https://shn.example/fhir/Observation/o%d", i), "resource": map[string]any{"resourceType": "Observation", "id": fmt.Sprintf("o%d", i)}})
	}
	req["entry"] = append(req["entry"].([]any), extras...)
	bad, _ := json.Marshal(req)
	if _, err := buildPASOperationResponse(bad, decision, in.Created); err == nil {
		t.Fatal("resource budget exceeded")
	}
}

func TestPASOperationRejectsForeignSubjectForms(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, _ := BuildConformantClaimBundle(in)
	decision, _ := BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	for _, field := range []string{"subjectReference", "patientReference", "subject"} {
		for _, array := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-array-%v", field, array), func(t *testing.T) {
				var b map[string]any
				_ = json.Unmarshal(request, &b)
				var ref any = map[string]any{"reference": "https://shn.example/fhir/Organization/cms-payer"}
				if array {
					ref = []any{ref}
				}
				first := b["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
				first["extension"] = []any{map[string]any{"url": "https://example.org/context", field: ref}}
				bad, _ := json.Marshal(b)
				if _, err := buildPASOperationResponse(bad, decision, in.Created); err == nil {
					t.Fatal("foreign subject accepted")
				}
			})
		}
	}
	var b map[string]any
	_ = json.Unmarshal(request, &b)
	b["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)["extension"] = []any{map[string]any{"url": "https://example.org/context", "valueReference": map[string]any{"type": "Patient", "identifier": map[string]any{"system": "urn:test", "value": "other"}}}}
	bad, _ := json.Marshal(b)
	if _, err := buildPASOperationResponse(bad, decision, in.Created); err == nil {
		t.Fatal("typed logical Patient bypass accepted")
	}
}

func TestResponderPASUpdateGraphFailureReleasesPendingClaim(t *testing.T) {
	h, ident, _ := newPAHarness(t)
	r, _ := h.makeResponderSrv(t, ident, &paTestAdjudicator{now: h.now})
	qr := answeredQR(t, "MBR-001", ClinicalContext{ConservativeTherapyWeeks: 8, PriorSurgery: true}, h.now)
	const subject = "pci:operation-graph"
	const original = "original-operation-graph"
	r.ledger.record(subject, original)
	request := buildConformantUpdate(t, "MBR-001", "amend-operation-graph", original, qr, h.now)
	var b map[string]any
	_ = json.Unmarshal(request, &b)
	entries := b["entry"].([]any)
	for i, e := range entries {
		if e.(map[string]any)["resource"].(map[string]any)["resourceType"] == "Patient" {
			b["entry"] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	bad, _ := json.Marshal(b)
	failed := r.handlePASUpdate(bad, Token{Subject: subject}, "failed-amend", h.now, "")
	if failed.appStatus != http.StatusBadRequest || failed.commit != nil || failed.rollback != nil {
		t.Fatalf("graph failure: %+v", failed)
	}
	accepted := r.handlePASUpdate(request, Token{Subject: subject}, "accepted-amend", h.now, "")
	if accepted.appStatus != 0 || accepted.commit == nil || accepted.rollback == nil {
		t.Fatalf("failed graph stranded pending claim: %+v", accepted)
	}
	if err := validatePASBundleGraph(accepted.payload); err != nil {
		t.Fatal(err)
	}
	g, _ := readPASGraph(accepted.payload)
	claimRef := g.response.resource["request"].(map[string]any)["reference"]
	if claimRef != "https://shn.example/fhir/Claim/"+conformantPASClaimUpdateID {
		t.Fatalf("response request=%v", claimRef)
	}
	accepted.rollback()
	retry := r.handlePASUpdate(request, Token{Subject: subject}, "retry-amend", h.now, "")
	if retry.appStatus != 0 {
		t.Fatal("response failure stranded claim")
	}
	retry.commit()
	if r.ledger.begin(subject, original) {
		t.Fatal("committed claim remains pending")
	}
}

func TestPASOperationEncounterParticipantIsNotPatientSubject(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, _ := BuildConformantClaimBundle(in)
	decision, _ := BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	for _, role := range []string{"Practitioner", "PractitionerRole", "RelatedPerson"} {
		t.Run(role, func(t *testing.T) {
			var b map[string]any
			_ = json.Unmarshal(request, &b)
			entries := b["entry"].([]any)
			entries = append(entries, map[string]any{"fullUrl": "https://shn.example/fhir/" + role + "/participant", "resource": map[string]any{"resourceType": role, "id": "participant"}}, map[string]any{"fullUrl": "https://shn.example/fhir/Encounter/visit", "resource": map[string]any{"resourceType": "Encounter", "id": "visit", "subject": map[string]any{"reference": in.PatientRef}, "participant": []any{map[string]any{"individual": map[string]any{"reference": role + "/participant"}}}}})
			b["entry"] = entries
			raw, _ := json.Marshal(b)
			if _, err := buildPASOperationResponse(raw, decision, in.Created); err != nil {
				t.Fatalf("legitimate %s participant rejected: %v", role, err)
			}
		})
	}
}

func TestPASOperationResourceSpecificSubjectsRemainBound(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, _ := BuildConformantClaimBundle(in)
	decision, _ := BuildClaimResponse("AUTH", "", in.PatientRef, in.Corr, in.Created)
	for _, tc := range []struct{ resource, field string }{{"ResearchSubject", "individual"}, {"EnrollmentRequest", "candidate"}} {
		for _, reference := range []string{in.PatientRef, "Organization/cms-payer"} {
			t.Run(tc.resource+reference, func(t *testing.T) {
				var b map[string]any
				_ = json.Unmarshal(request, &b)
				b["entry"] = append(b["entry"].([]any), map[string]any{"fullUrl": "https://shn.example/fhir/" + tc.resource + "/record", "resource": map[string]any{"resourceType": tc.resource, "id": "record", tc.field: map[string]any{"reference": reference}}})
				raw, _ := json.Marshal(b)
				_, err := buildPASOperationResponse(raw, decision, in.Created)
				if (err == nil) != (reference == in.PatientRef) {
					t.Fatalf("reference=%s err=%v", reference, err)
				}
			})
		}
	}
}
