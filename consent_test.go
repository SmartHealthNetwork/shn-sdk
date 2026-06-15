package shnsdk

import (
	"encoding/json"
	"testing"
)

func TestConsentCheckRequestJSON(t *testing.T) {
	b, err := json.Marshal(ConsentCheckRequest{PCI: "pci:abc", Purpose: PurposeTreatment, Custodian: "metro-spine", Recipient: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pci":"pci:abc","purpose":"TREAT","custodian":"metro-spine","recipient":"provider"}`
	if string(b) != want {
		t.Fatalf("ConsentCheckRequest json = %s, want %s", b, want)
	}
}

func TestConsentCheckResponseDecode(t *testing.T) {
	var r ConsentCheckResponse
	if err := json.Unmarshal([]byte(`{"permit":true,"consentRef":"Consent/c1"}`), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Permit || r.ConsentRef != "Consent/c1" {
		t.Fatalf("decoded %+v", r)
	}
	b, _ := json.Marshal(ConsentCheckResponse{Permit: false})
	if string(b) != `{"permit":false}` {
		t.Fatalf("empty-ref response json = %s, want {\"permit\":false}", b)
	}
	if PurposeTreatment != "TREAT" {
		t.Fatalf("PurposeTreatment = %q, want TREAT", PurposeTreatment)
	}
}
