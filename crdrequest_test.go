package shnsdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const (
	crdReqPatient  = "{ \"resourceType\": \"Patient\", \"id\": \"pat-1\", \"name\": [{\"family\": \"O'Brien <Jr>\"}], \"extension\": [{\"url\": \"http://x\", \"valueDecimal\": 1.50}] }"
	crdReqCoverage = `{"resourceType":"Bundle","type":"searchset","total":1,"entry":[{"fullUrl":"https://ehr.example/fhir/Coverage/cov-1","resource":{"resourceType":"Coverage","id":"cov-1","status":"active","beneficiary":{"reference":"Patient/pat-1"},"payor":[{"reference":"Organization/payer-1"}]},"search":{"mode":"match"}}]}`
	crdReqOrders   = `{"resourceType":"Bundle","type":"collection","entry":[{"fullUrl":"https://ehr.example/fhir/ServiceRequest/sr-1","resource":{"resourceType":"ServiceRequest","id":"sr-1","status":"draft","intent":"order","subject":{"reference":"Patient/pat-1"}}}]}`
	crdReqHookInst = "d1577c69-dfbe-44ad-ba6d-3e05e953b2ea"
)

func orderSelectInputs() CRDRequestInputs {
	return CRDRequestInputs{
		Hook:         "order-select",
		HookInstance: crdReqHookInst,
		UserID:       "PractitionerRole/pr-1",
		DraftOrders:  []byte(crdReqOrders),
		Selections:   []string{"ServiceRequest/sr-1"},
		Patient:      []byte(crdReqPatient),
		Coverage:     []byte(crdReqCoverage),
		Prefetch: map[string][]byte{
			"serviceRequestHistory": []byte(`null`),
			"practitionerRole":      []byte(`{"resourceType":"PractitionerRole","id":"pr-1","note":"1e2"}`),
		},
	}
}

func dispatchInputs() CRDRequestInputs {
	return CRDRequestInputs{
		Hook:             "order-dispatch",
		HookInstance:     crdReqHookInst,
		DispatchedOrders: []string{"DeviceRequest/dr-1"},
		Performer:        "Organization/dme-1",
		Patient:          []byte(crdReqPatient),
		Coverage:         []byte(`null`),
	}
}

func topLevelKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestBuildCRDRequest_NoCallbackFields: an authored CRD request names no FHIR
// server and grants no authorization, for every hook; everything the payer
// needs travels in the context and prefetch.
func TestBuildCRDRequest_NoCallbackFields(t *testing.T) {
	sign := orderSelectInputs()
	sign.Hook, sign.Selections = "order-sign", nil
	for name, in := range map[string]CRDRequestInputs{
		"order-select": orderSelectInputs(), "order-sign": sign, "order-dispatch": dispatchInputs(),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := BuildCRDRequest(in)
			if err != nil {
				t.Fatal(err)
			}
			if got := topLevelKeys(t, out); !slices.Equal(got, []string{"context", "hook", "hookInstance", "prefetch"}) {
				t.Fatalf("top-level members %v", got)
			}
			for _, banned := range []string{`"fhirServer"`, `"fhirAuthorization"`} {
				if bytes.Contains(out, []byte(banned)) {
					t.Fatalf("request carries %s: %s", banned, out)
				}
			}
			var req struct {
				Hook    string                     `json:"hook"`
				Context map[string]json.RawMessage `json:"context"`
			}
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatal(err)
			}
			if req.Hook != name || string(req.Context["patientId"]) != `"pat-1"` {
				t.Fatalf("hook %q, patientId %s", req.Hook, req.Context["patientId"])
			}
		})
	}
}

// TestBuildCRDRequest_RequiresPatient: the patient prefetch is the
// participant's own Patient; without one there is no request.
func TestBuildCRDRequest_RequiresPatient(t *testing.T) {
	for name, patient := range map[string]string{
		"absent":         "",
		"null":           "null",
		"not a Patient":  `{"resourceType":"Person","id":"pat-1"}`,
		"no id":          `{"resourceType":"Patient","name":[{"family":"X"}]}`,
		"empty id":       `{"resourceType":"Patient","id":""}`,
		"not JSON":       `{"resourceType":"Patient"`,
		"duplicate name": `{"resourceType":"Patient","id":"pat-1","id":"pat-2"}`,
	} {
		for _, base := range []CRDRequestInputs{orderSelectInputs(), dispatchInputs()} {
			in := base
			in.Patient = []byte(patient)
			if out, err := BuildCRDRequest(in); err == nil {
				t.Errorf("%s (%s): built %s", name, in.Hook, out)
			}
		}
	}
}

// TestBuildCRDRequest_ValuesExact: the Patient, the Coverage search result,
// the draft orders and every other prefetch value travel as the caller's own
// bytes; the context is written from the caller's values.
func TestBuildCRDRequest_ValuesExact(t *testing.T) {
	out, err := BuildCRDRequest(orderSelectInputs())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"hook":"order-select","hookInstance":"` + crdReqHookInst + `","context":{"userId":"PractitionerRole/pr-1","patientId":"pat-1",` +
		`"selections":["ServiceRequest/sr-1"],"draftOrders":` + crdReqOrders + `},"prefetch":{"patient":` + crdReqPatient +
		`,"coverage":` + crdReqCoverage + `,"practitionerRole":{"resourceType":"PractitionerRole","id":"pr-1","note":"1e2"},"serviceRequestHistory":null}}`
	if string(out) != want {
		t.Fatalf("request:\n%s\nwant\n%s", out, want)
	}
	in := dispatchInputs()
	in.FulfillmentTasks = [][]byte{[]byte(`{"resourceType":"Task","id":"t-1","status":"requested","intent":"order"}`)}
	out, err = BuildCRDRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"hook":"order-dispatch","hookInstance":"` + crdReqHookInst + `","context":{"patientId":"pat-1","dispatchedOrders":["DeviceRequest/dr-1"],` +
		`"performer":"Organization/dme-1","fulfillmentTasks":[{"resourceType":"Task","id":"t-1","status":"requested","intent":"order"}]},` +
		`"prefetch":{"patient":` + crdReqPatient + `,"coverage":null}}`
	if string(out) != want {
		t.Fatalf("request:\n%s\nwant\n%s", out, want)
	}
	sign := orderSelectInputs()
	sign.Hook, sign.Selections, sign.EncounterID, sign.Prefetch = "order-sign", nil, "enc-1", nil
	out, err = BuildCRDRequest(sign)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"hook":"order-sign","hookInstance":"` + crdReqHookInst + `","context":{"userId":"PractitionerRole/pr-1","patientId":"pat-1","encounterId":"enc-1",` +
		`"draftOrders":` + crdReqOrders + `},"prefetch":{"patient":` + crdReqPatient + `,"coverage":` + crdReqCoverage + `}}`
	if string(out) != want {
		t.Fatalf("request:\n%s\nwant\n%s", out, want)
	}
}

// TestBuildCRDRequest_Refusals: one mutation of valid inputs, one refusal.
func TestBuildCRDRequest_Refusals(t *testing.T) {
	rows := []struct {
		name string
		base func() CRDRequestInputs
		m    func(*CRDRequestInputs)
	}{
		{"unsupported hook", orderSelectInputs, func(in *CRDRequestInputs) { in.Hook = "appointment-book" }},
		{"no hook", orderSelectInputs, func(in *CRDRequestInputs) { in.Hook = "" }},
		{"hookInstance not a UUID", orderSelectInputs, func(in *CRDRequestInputs) { in.HookInstance = "hi-1" }},
		{"no userId", orderSelectInputs, func(in *CRDRequestInputs) { in.UserID = "" }},
		{"no draft orders", orderSelectInputs, func(in *CRDRequestInputs) { in.DraftOrders = nil }},
		{"draft orders not a Bundle", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(`{"resourceType":"ServiceRequest","id":"sr-1","subject":{"reference":"Patient/pat-1"}}`)
		}},
		{"draft orders empty", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(`{"resourceType":"Bundle","type":"collection","entry":[]}`)
			in.Selections = nil
		}},
		{"draft order for another patient", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(strings.Replace(crdReqOrders, "Patient/pat-1", "Patient/pat-2", 1))
		}},
		{"NutritionOrder for another patient", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"NutritionOrder","id":"no-1","status":"draft","intent":"order","patient":{"reference":"Patient/pat-2"}}}]}`)
			in.Selections = []string{"NutritionOrder/no-1"}
		}},
		{"VisionPrescription for another patient", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"VisionPrescription","id":"vp-1","status":"draft","patient":{"reference":"Patient/pat-2"}}}]}`)
			in.Selections = []string{"VisionPrescription/vp-1"}
		}},
		{"draft order whose subject and patient disagree", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(strings.Replace(crdReqOrders, `"subject":{"reference":"Patient/pat-1"}`, `"subject":{"reference":"Patient/pat-1"},"patient":{"reference":"Patient/pat-2"}`, 1))
		}},
		{"draft order without a subject", orderSelectInputs, func(in *CRDRequestInputs) {
			in.DraftOrders = []byte(strings.Replace(crdReqOrders, `,"subject":{"reference":"Patient/pat-1"}`, "", 1))
		}},
		{"no selections", orderSelectInputs, func(in *CRDRequestInputs) { in.Selections = nil }},
		{"selection not among the draft orders", orderSelectInputs, func(in *CRDRequestInputs) { in.Selections = []string{"ServiceRequest/sr-9"} }},
		{"selections on order-sign", orderSelectInputs, func(in *CRDRequestInputs) { in.Hook = "order-sign" }},
		{"no coverage value", orderSelectInputs, func(in *CRDRequestInputs) { in.Coverage = nil }},
		{"bare Coverage", orderSelectInputs, func(in *CRDRequestInputs) {
			in.Coverage = []byte(`{"resourceType":"Coverage","id":"cov-1","beneficiary":{"reference":"Patient/pat-1"}}`)
		}},
		{"coverage not a searchset", orderSelectInputs, func(in *CRDRequestInputs) {
			in.Coverage = []byte(strings.Replace(crdReqCoverage, `"searchset"`, `"collection"`, 1))
		}},
		{"coverage for another patient", orderSelectInputs, func(in *CRDRequestInputs) {
			in.Coverage = []byte(strings.Replace(crdReqCoverage, "Patient/pat-1", "Patient/pat-2", 1))
		}},
		{"coverage not JSON", orderSelectInputs, func(in *CRDRequestInputs) { in.Coverage = []byte(`{"resourceType":"Bundle"`) }},
		{"prefetch key collides", orderSelectInputs, func(in *CRDRequestInputs) { in.Prefetch["patient"] = []byte(`null`) }},
		{"prefetch empty key", orderSelectInputs, func(in *CRDRequestInputs) { in.Prefetch[""] = []byte(`null`) }},
		{"prefetch value missing", orderSelectInputs, func(in *CRDRequestInputs) { in.Prefetch["x"] = nil }},
		{"prefetch value a string", orderSelectInputs, func(in *CRDRequestInputs) { in.Prefetch["x"] = []byte(`"text"`) }},
		{"prefetch value two documents", orderSelectInputs, func(in *CRDRequestInputs) { in.Prefetch["x"] = []byte(`{} {}`) }},
		{"dispatch without orders", dispatchInputs, func(in *CRDRequestInputs) { in.DispatchedOrders = nil }},
		{"dispatch with an empty order", dispatchInputs, func(in *CRDRequestInputs) { in.DispatchedOrders = []string{""} }},
		{"dispatch without performer", dispatchInputs, func(in *CRDRequestInputs) { in.Performer = "" }},
		{"dispatch with draft orders", dispatchInputs, func(in *CRDRequestInputs) { in.DraftOrders = []byte(crdReqOrders) }},
		{"dispatch with a user", dispatchInputs, func(in *CRDRequestInputs) { in.UserID = "Practitioner/p" }},
		{"select with dispatch fields", orderSelectInputs, func(in *CRDRequestInputs) { in.Performer = "Organization/dme-1" }},
		{"fulfillment task not an object", dispatchInputs, func(in *CRDRequestInputs) { in.FulfillmentTasks = [][]byte{[]byte(`[]`)} }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			in := r.base()
			r.m(&in)
			if out, err := BuildCRDRequest(in); err == nil {
				t.Fatalf("built %s", out)
			}
		})
	}
	for _, base := range []CRDRequestInputs{orderSelectInputs(), dispatchInputs()} {
		if _, err := BuildCRDRequest(base); err != nil {
			t.Fatalf("%s base: %v", base.Hook, err)
		}
	}
}

// TestDeprecatedBuildConformantOrderSelect_OutputUnchanged pins both
// deprecated CRD request builders' output byte for byte. They keep their
// earlier behavior (an id-only Patient; a placeholder fhirServer on
// order-select) until they are removed; BuildCRDRequest is the replacement.
func TestDeprecatedBuildConformantOrderSelect_OutputUnchanged(t *testing.T) {
	sr, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", "Patient/MBR-COVERED")
	if err != nil {
		t.Fatal(err)
	}
	cov, err := BuildCoverageWithPayer("Patient/MBR-COVERED", "MBR-COVERED", CMSPayerIdentity)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := BuildConformantOrderSelectRequest(sr, cov, "Patient/MBR-COVERED")
	if err != nil {
		t.Fatal(err)
	}
	dis, err := BuildConformantOrderDispatchRequest(OrderDispatchInputs{
		PatientID: "MBR-OX", PatientRef: "Patient/MBR-OX", OrderRef: "DeviceRequest/dr1", PerformerRef: "Organization/sup1",
		DeviceRequest: []byte(`{"resourceType":"DeviceRequest","id":"dr1","status":"draft","intent":"order","subject":{"reference":"Patient/MBR-OX"}}`),
		Supplier:      []byte(`{"resourceType":"Organization","id":"sup1"}`),
		Coverage:      cov, Payer: CMSPayerIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, row := range map[string]struct {
		out  []byte
		want string
	}{
		"order-select":   {sel, "bfc6666747ba64222c6083c35aafbf43991e838926a0bea6f5bc659789f0d244"},
		"order-dispatch": {dis, "a6dddcaead8b56784c68661debc94673a70145c934e9651d5d28e4070bfa6192"},
	} {
		sum := sha256.Sum256(row.out)
		if got := hex.EncodeToString(sum[:]); got != row.want {
			t.Errorf("%s output changed (sha256 %s): %s", name, got, row.out)
		}
	}
	if !bytes.Contains(sel, []byte(`"fhirServer"`)) || !bytes.Contains(sel, []byte(`"patient":{"id":"MBR-COVERED","resourceType":"Patient"}`)) {
		t.Fatalf("order-select output: %s", sel)
	}
}

// TestBuildCRDRequest_PatientElementOrders: orders whose patient element is
// named patient (NutritionOrder, VisionPrescription) are accepted when they
// are for the request's patient.
func TestBuildCRDRequest_PatientElementOrders(t *testing.T) {
	for _, order := range []string{
		`{"resourceType":"NutritionOrder","id":"no-1","status":"draft","intent":"order","patient":{"reference":"Patient/pat-1"}}`,
		`{"resourceType":"VisionPrescription","id":"no-1","status":"draft","patient":{"reference":"https://ehr.example/fhir/Patient/pat-1"}}`,
	} {
		in := orderSelectInputs()
		in.Hook, in.Selections = "order-sign", nil
		in.DraftOrders = []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":` + order + `}]}`)
		out, err := BuildCRDRequest(in)
		if err != nil {
			t.Fatalf("%s: %v", order, err)
		}
		if !bytes.Contains(out, []byte(order)) {
			t.Fatalf("order not carried exactly: %s", out)
		}
	}
}
