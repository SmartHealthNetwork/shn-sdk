package shnsdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// testNPISystem and testNPI are a synthetic organization NPI: the public
// documentation example, which passes the NPI check digit and names no one.
const (
	testNPISystem = "http://hl7.org/fhir/sid/us-npi"
	testNPI       = "1234567893"
)

// A provider row's operator-attested identifiers ride the feed as providerIds,
// in the same {system, value} shape as a payer's payerIds, and a holder with
// none omits the field.
func TestHolderProviderIDsRoundTrip(t *testing.T) {
	h := Holder{
		ID: "acme-provider", Role: "provider", EncPub: "ZW5j", SignPub: "c2ln", BaseURL: "https://acme.example",
		ProviderIDs: []ProviderIdentifier{{System: testNPISystem, Value: testNPI}},
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"providerIds":[{"system":"`+testNPISystem+`","value":"`+testNPI+`"}]`) {
		t.Fatalf("providerIds not emitted in canonical shape: %s", b)
	}
	if strings.Contains(string(b), "payerIds") {
		t.Fatalf("a provider row with no payer identities must omit payerIds: %s", b)
	}
	var got Holder
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.ProviderIDs, h.ProviderIDs) {
		t.Fatalf("round-trip providerIds: got %+v want %+v", got.ProviderIDs, h.ProviderIDs)
	}
	bare, _ := json.Marshal(Holder{ID: "p", Role: "provider"})
	if strings.Contains(string(bare), "providerIds") {
		t.Fatalf("a holder with no provider identities must omit providerIds: %s", bare)
	}
}

// A feed row from a registrar that serves no providerIds (the field absent)
// reads as a holder attested for no provider identity: the field is absent,
// not unknown, so the holder names no provider identity.
func TestHolderProviderIDs_AnOldFeedReadsAsNone(t *testing.T) {
	var got Holder
	row := `{"id":"acme-provider","role":"provider","encPub":"ZW5j","signPub":"c2ln","baseURL":"https://acme.example"}`
	if err := json.Unmarshal([]byte(row), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ProviderIDs != nil {
		t.Fatalf("providerIds = %+v, want none", got.ProviderIDs)
	}
}

// A reader that knows only payer identities is unaffected by providerIds: a
// feed carrying both decodes into a payer-only shape (an SDK older than this
// field) without error and with the payer identities intact, and this SDK
// reads the two sets apart, never one as the other.
func TestHolderProviderIDs_APayerOnlyReaderIsUnaffected(t *testing.T) {
	payer := PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00078"}
	feed := `[` +
		`{"id":"acme-payer","role":"payer","encPub":"ZW5j","signPub":"c2ln","baseURL":"https://payer.example","payerIds":[{"system":"` + payer.System + `","value":"` + payer.Value + `"}]},` +
		`{"id":"acme-provider","role":"provider","encPub":"ZW5j","signPub":"c2ln","baseURL":"https://acme.example","providerIds":[{"system":"` + testNPISystem + `","value":"` + testNPI + `"}]}` +
		`]`

	// The payer-only shape: the feed row as an SDK without providerIds reads it.
	type payerOnlyHolder struct {
		ID       string            `json:"id"`
		Role     string            `json:"role"`
		PayerIDs []PayerIdentifier `json:"payerIds,omitempty"`
	}
	var old []payerOnlyHolder
	dec := json.NewDecoder(strings.NewReader(feed))
	if err := dec.Decode(&old); err != nil {
		t.Fatalf("a payer-only reader failed on a feed with providerIds: %v", err)
	}
	if len(old) != 2 || !reflect.DeepEqual(old[0].PayerIDs, []PayerIdentifier{payer}) || old[1].PayerIDs != nil {
		t.Fatalf("payer-only read = %+v, want the payer's identity on its row only", old)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()
	hs, err := FetchHolders(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchHolders: %v", err)
	}
	if len(hs) != 2 {
		t.Fatalf("FetchHolders = %d holders, want 2", len(hs))
	}
	if !reflect.DeepEqual(hs[0].PayerIDs, []PayerIdentifier{payer}) || hs[0].ProviderIDs != nil {
		t.Fatalf("payer row = %+v, want its payer identity and no provider identity", hs[0])
	}
	if hs[1].PayerIDs != nil || !reflect.DeepEqual(hs[1].ProviderIDs, []ProviderIdentifier{{System: testNPISystem, Value: testNPI}}) {
		t.Fatalf("provider row = %+v, want its provider identity and no payer identity", hs[1])
	}
}

// The runtime peer-cache entry carries a provider's identifiers beside its
// payer identities, as the registry holds and returns them.
func TestRegistryEntryCarriesProviderIDs(t *testing.T) {
	reg := NewRegistry()
	want := []ProviderIdentifier{{System: testNPISystem, Value: testNPI}}
	reg.Set("acme-provider", RegistryEntry{ID: "acme-provider", Role: "provider", ProviderIDs: want})
	e, ok := reg.Lookup("acme-provider")
	if !ok || !reflect.DeepEqual(e.ProviderIDs, want) || e.PayerIDs != nil {
		t.Fatalf("Lookup = %+v, %v; want the provider identity and no payer identity", e, ok)
	}
}
