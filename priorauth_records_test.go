package shnsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// callerRecords are a participant's own Patient and Coverage search result for
// the demonstration member, in its own layout.
func callerRecords(member string) (patient, coverage []byte) {
	patient = []byte(`{"resourceType" : "Patient", "id" : "` + member + `", "identifier":[{"system":"urn:shn:member","value":"` + member + `"}],"name":[{"family":"Johansson"}],"birthDate":"1975-04-02"}`)
	coverage = []byte(`{"resourceType":"Bundle","type":"searchset","entry":[` +
		`{"fullUrl":"https://ehr.example/fhir/Coverage/cov-9","resource":{"resourceType":"Coverage","id":"cov-9","status":"active","beneficiary":{"reference":"Patient/` + member + `"},"payor":[{"reference":"Organization/pay-1"}]},"search":{"mode":"match"}},` +
		`{"fullUrl":"https://ehr.example/fhir/Organization/pay-1","resource":{"resourceType":"Organization","id":"pay-1","identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00001"}]},"search":{"mode":"include"}}]}`)
	return patient, coverage
}

// TestRunPriorAuth_OrderSignFromCallerRecords: with the caller's own Patient
// and Coverage, the coverage check is an order-sign request that carries those
// records exactly, names the member as the patient, and sends no FHIR server
// or authorization; the flow completes.
func TestRunPriorAuth_OrderSignFromCallerRecords(t *testing.T) {
	_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
	f := &paFakeSubstrate{
		signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
		payerID: "payer", now: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), paRequired: true,
	}
	id, ep, payer, _ := newPATestRig(t, f)
	req := demoPARequest()
	req.Patient, req.Coverage = callerRecords(req.Member)
	res, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, req)
	if err != nil {
		t.Fatalf("RunPriorAuth: %v", err)
	}
	if res.Outcome != "approved" {
		t.Fatalf("outcome %q", res.Outcome)
	}
	var sent struct {
		Hook         string `json:"hook"`
		HookInstance string `json:"hookInstance"`
		Context      struct {
			PatientID   string          `json:"patientId"`
			UserID      string          `json:"userId"`
			Selections  []string        `json:"selections"`
			DraftOrders json.RawMessage `json:"draftOrders"`
		} `json:"context"`
		Prefetch map[string]json.RawMessage `json:"prefetch"`
	}
	if err := json.Unmarshal(f.capturedCRDRequest, &sent); err != nil {
		t.Fatalf("captured CRD request: %v", err)
	}
	if sent.Hook != "order-sign" || sent.Context.PatientID != req.Member || sent.Context.UserID != "Practitioner/"+req.NPI || sent.Context.Selections != nil {
		t.Fatalf("request %+v", sent)
	}
	if !hookInstanceUUID.MatchString(sent.HookInstance) {
		t.Fatalf("hookInstance %q is not a UUID", sent.HookInstance)
	}
	if !bytes.Contains(f.capturedCRDRequest, req.Patient) || !bytes.Contains(f.capturedCRDRequest, req.Coverage) {
		t.Fatal("the caller's records were not carried exactly")
	}
	for _, callback := range []string{`"fhirServer"`, `"fhirAuthorization"`} {
		if bytes.Contains(f.capturedCRDRequest, []byte(callback)) {
			t.Fatalf("request carries %s", callback)
		}
	}
	if !strings.Contains(string(sent.Context.DraftOrders), `"subject":{"reference":"Patient/`+req.Member+`"}`) ||
		!strings.Contains(string(sent.Context.DraftOrders), `"fullUrl":"ServiceRequest/sr-`+req.Member+`"`) {
		t.Fatalf("draft orders %s", sent.Context.DraftOrders)
	}
}

// TestRunPriorAuth_CallerRecordsRefused: records set by halves, records for
// another patient, and a missing ordering practitioner are refused before
// anything is sent.
func TestRunPriorAuth_CallerRecordsRefused(t *testing.T) {
	patient, coverage := callerRecords("MBR-COVERED")
	_, otherCoverage := callerRecords("MBR-OTHER")
	for name, mutate := range map[string]func(*PriorAuthRequest){
		"patient only":             func(r *PriorAuthRequest) { r.Patient, r.Coverage = patient, nil },
		"coverage only":            func(r *PriorAuthRequest) { r.Patient, r.Coverage = nil, coverage },
		"another patient's record": func(r *PriorAuthRequest) { r.Patient, r.Coverage = patient, otherCoverage },
		"no ordering practitioner": func(r *PriorAuthRequest) { r.Patient, r.Coverage, r.NPI = patient, coverage, "" },
		"a hook this flow does not send": func(r *PriorAuthRequest) {
			r.Patient, r.Coverage, r.Hook = patient, coverage, "order-dispatch"
		},
		"a hook without the caller's records": func(r *PriorAuthRequest) {
			r.Patient, r.Coverage, r.Hook = nil, nil, "order-sign"
		},
		"coverage not a searchset": func(r *PriorAuthRequest) {
			r.Patient, r.Coverage = patient, []byte(`{"resourceType":"Coverage","id":"c","beneficiary":{"reference":"Patient/MBR-COVERED"}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
			payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
			f := &paFakeSubstrate{signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub, payerID: "payer", now: time.Now(), paRequired: true}
			id, ep, payer, _ := newPATestRig(t, f)
			req := demoPARequest()
			mutate(&req)
			// A records refusal names the stage that made it: "prior authorization:"
			// when the precondition catches it before anything is sent, and
			// "crd-order-select:" when the coverage check's own builder does. Both
			// send nothing, which is what the check after this one pins.
			_, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, req)
			if err == nil {
				t.Fatal("err = nil, want a refusal")
			}
			if !strings.HasPrefix(err.Error(), "crd-order-select:") && !strings.HasPrefix(err.Error(), "prior authorization:") {
				t.Fatalf("err = %v", err)
			}
			if f.capturedCRDRequest != nil {
				t.Fatal("a refused request was sent")
			}
		})
	}
}

// TestRunPriorAuth_CallerChoosesTheHook: a caller whose order is still being
// chosen sends order-select (the order named in selections); the default for a
// request built from the caller's records is order-sign.
func TestRunPriorAuth_CallerChoosesTheHook(t *testing.T) {
	for hook, want := range map[string]string{"": "order-sign", "order-sign": "order-sign", "order-select": "order-select"} {
		t.Run("hook "+hook, func(t *testing.T) {
			_, signPriv, _ := ed25519.GenerateKey(rand.Reader)
			payerPub, payerPriv, _ := box.GenerateKey(rand.Reader)
			f := &paFakeSubstrate{
				signPriv: signPriv, payerEnc: payerPriv, payerPub: payerPub,
				payerID: "payer", now: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC), paRequired: true,
			}
			id, ep, payer, _ := newPATestRig(t, f)
			req := demoPARequest()
			req.Patient, req.Coverage = callerRecords(req.Member)
			req.Hook = hook
			if _, err := id.RunPriorAuth(context.Background(), http.DefaultClient, ep, payer, req); err != nil {
				t.Fatalf("RunPriorAuth: %v", err)
			}
			var sent struct {
				Hook    string `json:"hook"`
				Context struct {
					Selections []string `json:"selections"`
				} `json:"context"`
			}
			if err := json.Unmarshal(f.capturedCRDRequest, &sent); err != nil {
				t.Fatal(err)
			}
			wantSelections := []string(nil)
			if want == "order-select" {
				wantSelections = []string{"ServiceRequest/sr-" + req.Member}
			}
			if sent.Hook != want || strings.Join(sent.Context.Selections, ",") != strings.Join(wantSelections, ",") {
				t.Fatalf("hook %q selections %v, want %q %v", sent.Hook, sent.Context.Selections, want, wantSelections)
			}
		})
	}
}
