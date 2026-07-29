package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestCreateWithPayerIDsWireBody verifies CreateWithPayerIDs puts payerIds on the
// wire, and that plain Create omits the key entirely (byte-compatible with old
// servers — the dtr-package-widening lesson: never break a published wire body).
func TestCreateWithPayerIDsWireBody(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clients" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"holder-1"}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok").WithHTTP(srv.Client())

	id, err := c.CreateWithPayerIDs(context.Background(), "acme", "payer", "e", "s", "https://acme.gw.example",
		[]shnsdk.PayerIdentifier{{System: "https://example.org/payer", Value: "ACME"}})
	if err != nil || id != "holder-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	pids, ok := got["payerIds"].([]any)
	if !ok || len(pids) != 1 {
		t.Fatalf("payerIds not on the wire: %v", got)
	}
	// Plain Create must NOT send the key at all (wire-compatible with old servers).
	got = nil
	if _, err := c.Create(context.Background(), "acme", "provider", "e", "s", "https://x"); err != nil {
		t.Fatal(err)
	}
	if _, present := got["payerIds"]; present {
		t.Fatalf("Create must omit payerIds: %v", got)
	}
}
