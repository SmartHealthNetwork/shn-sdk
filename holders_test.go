package shnsdk

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// feedJSON builds a minimal /holders feed JSON with one entry whose encPub is a
// std-base64 encoding of key (must be 32 bytes).
func feedJSON(id, role string, key [32]byte) string {
	enc := base64.StdEncoding.EncodeToString(key[:])
	sign := base64.StdEncoding.EncodeToString(key[:]) // reuse for signPub in tests
	return fmt.Sprintf(`[{"id":%q,"role":%q,"encPub":%q,"signPub":%q,"baseURL":"http://x"}]`,
		id, role, enc, sign)
}

// TestFetchHolders covers the basic success path, non-200 error, garbage JSON, and
// trailing-slash normalisation.
func TestFetchHolders(t *testing.T) {
	var key32 [32]byte
	for i := range key32 {
		key32[i] = byte(i + 1)
	}

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/holders" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, feedJSON("payer", "payer", key32))
		}))
		defer srv.Close()

		hs, err := FetchHolders(t.Context(), srv.Client(), srv.URL)
		if err != nil {
			t.Fatalf("FetchHolders: %v", err)
		}
		if len(hs) != 1 {
			t.Fatalf("want 1 holder, got %d", len(hs))
		}
		h := hs[0]
		if h.ID != "payer" || h.Role != "payer" || h.BaseURL != "http://x" {
			t.Errorf("unexpected holder fields: %+v", h)
		}
		want := base64.StdEncoding.EncodeToString(key32[:])
		if h.EncPub != want {
			t.Errorf("encPub = %q, want %q", h.EncPub, want)
		}
	})

	t.Run("trailing slash normalised", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/holders" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, feedJSON("x", "provider", key32))
		}))
		defer srv.Close()

		// Pass the URL with a trailing slash; FetchHolders must normalise it.
		hs, err := FetchHolders(t.Context(), srv.Client(), srv.URL+"/")
		if err != nil {
			t.Fatalf("FetchHolders with trailing slash: %v", err)
		}
		if len(hs) != 1 {
			t.Fatalf("want 1 holder, got %d", len(hs))
		}
	})

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "gone", http.StatusGone)
		}))
		defer srv.Close()

		_, err := FetchHolders(t.Context(), srv.Client(), srv.URL)
		if err == nil {
			t.Fatal("expected error for non-200 response, got nil")
		}
	})

	t.Run("garbage JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not json {{{")
		}))
		defer srv.Close()

		_, err := FetchHolders(t.Context(), srv.Client(), srv.URL)
		if err == nil {
			t.Fatal("expected error for garbage JSON, got nil")
		}
	})
}

// TestHolderEncKey covers the EncKey decode: valid 32-byte b64, short key, non-base64.
func TestHolderEncKey(t *testing.T) {
	var key32 [32]byte
	for i := range key32 {
		key32[i] = byte(i + 7)
	}
	valid := base64.StdEncoding.EncodeToString(key32[:])

	t.Run("valid 32-byte b64 round-trips", func(t *testing.T) {
		h := Holder{EncPub: valid}
		got, err := h.EncKey()
		if err != nil {
			t.Fatalf("EncKey: %v", err)
		}
		if *got != key32 {
			t.Errorf("key mismatch: got %v want %v", *got, key32)
		}
	})

	t.Run("short key (31 bytes) → error", func(t *testing.T) {
		short := base64.StdEncoding.EncodeToString(key32[:31])
		h := Holder{EncPub: short}
		if _, err := h.EncKey(); err == nil {
			t.Fatal("expected error for 31-byte key, got nil")
		}
	})

	t.Run("non-base64 → error", func(t *testing.T) {
		h := Holder{EncPub: "not!base64!!!"}
		if _, err := h.EncKey(); err == nil {
			t.Fatal("expected error for non-base64, got nil")
		}
	})
}

// TestNewFeedEncResolver verifies resolver success, absent id, server error, and the
// no-cache contract (re-fetches per call).
func TestNewFeedEncResolver(t *testing.T) {
	var keyA, keyB [32]byte
	for i := range keyA {
		keyA[i] = byte(i + 10)
		keyB[i] = byte(i + 50)
	}

	t.Run("finds present holder and returns its key", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, feedJSON("payer", "payer", keyA))
		}))
		defer srv.Close()

		resolve := NewFeedEncResolver(srv.Client(), srv.URL)
		got, ok := resolve("payer")
		if !ok {
			t.Fatal("expected ok=true for present holder")
		}
		if *got != keyA {
			t.Errorf("key mismatch: got %v want %v", *got, keyA)
		}
	})

	t.Run("absent holder id → ok=false", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, feedJSON("payer", "payer", keyA))
		}))
		defer srv.Close()

		resolve := NewFeedEncResolver(srv.Client(), srv.URL)
		_, ok := resolve("nonexistent")
		if ok {
			t.Fatal("expected ok=false for absent holder")
		}
	})

	t.Run("server 500 → ok=false", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}))
		defer srv.Close()

		resolve := NewFeedEncResolver(srv.Client(), srv.URL)
		_, ok := resolve("payer")
		if ok {
			t.Fatal("expected ok=false when server returns 500")
		}
	})

	t.Run("re-fetches per call (no-cache contract)", func(t *testing.T) {
		// Serve two different feeds across calls using an atomic counter.
		var callCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := callCount.Add(1)
			if n == 1 {
				fmt.Fprint(w, feedJSON("holder-a", "provider", keyA))
			} else {
				fmt.Fprint(w, feedJSON("holder-b", "provider", keyB))
			}
		}))
		defer srv.Close()

		resolve := NewFeedEncResolver(srv.Client(), srv.URL)

		// First call: should see holder-a (keyA).
		got1, ok1 := resolve("holder-a")
		if !ok1 {
			t.Fatal("call 1: expected ok=true for holder-a")
		}
		if *got1 != keyA {
			t.Errorf("call 1: key mismatch: got %v want %v", *got1, keyA)
		}

		// Second call: feed has changed — should see holder-b, not holder-a.
		_, ok2 := resolve("holder-a")
		if ok2 {
			t.Fatal("call 2: expected ok=false for holder-a (second feed has only holder-b)")
		}
		got2, ok2b := resolve("holder-b")
		// This is call 3 — server returns feed with holder-b again.
		if !ok2b {
			t.Fatal("call 3: expected ok=true for holder-b")
		}
		if *got2 != keyB {
			t.Errorf("call 3: key mismatch: got %v want %v", *got2, keyB)
		}
	})
}

func TestHolderPayerIDsRoundTrip(t *testing.T) {
	h := Holder{
		ID: "acme-payer", Role: "payer", EncPub: "ZW5j", SignPub: "c2ln", BaseURL: "https://acme.example",
		PayerIDs: []PayerIdentifier{{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00078"}},
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"payerIds":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00078"}]`) {
		t.Fatalf("payerIds not emitted in canonical shape: %s", b)
	}
	var got Holder
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.PayerIDs, h.PayerIDs) {
		t.Fatalf("round-trip payerIds: got %+v want %+v", got.PayerIDs, h.PayerIDs)
	}
	// A holder with no payer-ids omits the field entirely (omitempty — provider/facility/phg).
	bare, _ := json.Marshal(Holder{ID: "p", Role: "provider"})
	if strings.Contains(string(bare), "payerIds") {
		t.Fatalf("bare holder must omit payerIds: %s", bare)
	}
}
