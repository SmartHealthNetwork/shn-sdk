package shnsdk

import (
	"bytes"
	"testing"
)

func TestBuildConformantOrderDispatchRequest(t *testing.T) {
	drJSON := []byte(`{"resourceType":"DeviceRequest","id":"dr-ox","status":"active","intent":"order","subject":{"reference":"Patient/MBR-OX"},"codeCodeableConcept":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0431"}]}}`)
	orgJSON := []byte(`{"resourceType":"Organization","id":"dme-1","identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1922334455"}],"name":"Acme Home Medical (Non-Contracted)"}`)
	covJSON := []byte(`{"resourceType":"Coverage","id":"MBR-OX-cov","beneficiary":{"reference":"Patient/MBR-OX"},"payor":[{"reference":"Organization/cms-ext"}]}`)
	out, err := BuildConformantOrderDispatchRequest(OrderDispatchInputs{
		PatientID: "MBR-OX", OrderRef: "DeviceRequest/dr-ox", PerformerRef: "Organization/dme-1",
		DeviceRequest: drJSON, Supplier: orgJSON, Coverage: covJSON, PatientRef: "Patient/MBR-OX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"hook":"order-dispatch"`)) ||
		!bytes.Contains(out, []byte(`"dispatchedOrders"`)) ||
		!bytes.Contains(out, []byte(`"performer"`)) {
		t.Fatalf("bad order-dispatch request: %s", out)
	}
	// Self-containment: the dispatched DeviceRequest AND the supplier Organization
	// must ride in prefetch, and there must be NO fhirServer field (non-aggregation floor).
	if !bytes.Contains(out, []byte(`"resourceType":"DeviceRequest"`)) ||
		!bytes.Contains(out, []byte(`"resourceType":"Organization"`)) {
		t.Fatalf("DeviceRequest+Organization not prefetch-self-contained: %s", out)
	}
	if bytes.Contains(out, []byte(`"fhirServer"`)) {
		t.Fatalf("fhirServer must be absent: %s", out)
	}
	// Exact performer ref-match: the performer string must equal the seeded supplier ref.
	if !bytes.Contains(out, []byte(`"performer":"Organization/dme-1"`)) {
		t.Fatalf("performer ref not exact: %s", out)
	}
	// Prefetch entry fullUrls must be "<Type>/<id>" (the reference form br-payer resolves by) —
	// a urn:shn: fullUrl leaves the order unresolvable ("dispatchedOrders ... could not be
	// resolved"). Verified live against br-payer a8bece4.
	if !bytes.Contains(out, []byte(`"fullUrl":"DeviceRequest/dr-ox"`)) {
		t.Fatalf("deviceHistory entry fullUrl must be DeviceRequest/dr-ox: %s", out)
	}
	if !bytes.Contains(out, []byte(`"fullUrl":"Organization/dme-1"`)) {
		t.Fatalf("serviceHistory entry fullUrl must be Organization/dme-1: %s", out)
	}
	if bytes.Contains(out, []byte(`urn:shn:`)) {
		t.Fatalf("prefetch fullUrls must be Type/id, not urn:shn: (br-payer resolves by Type/id): %s", out)
	}
	// The coverage bundle must carry an EXTERNAL payer Organization (Organization/cms-payer) with
	// the CMS payor identifier so br-payer's dispatch payor gate resolves it (a #contained payer
	// or inline identifier is rejected — verified live against a8bece4).
	if !bytes.Contains(out, []byte(`"reference":"Organization/cms-payer"`)) {
		t.Fatalf("coverage payor must be rewritten to the external Organization/cms-payer ref: %s", out)
	}
	if !bytes.Contains(out, []byte(`"fullUrl":"Organization/cms-payer"`)) ||
		!bytes.Contains(out, []byte(`"value":"00001"`)) {
		t.Fatalf("coverage bundle must carry the external CMS payer Org (payor-id 00001): %s", out)
	}
}
