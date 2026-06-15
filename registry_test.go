package shnsdk

import (
	"crypto/ed25519"
	"sync"
	"testing"
)

// TestLookupByRole_Deterministic verifies LookupByRole returns the smallest-id
// holder for a role, deterministically, when multiple same-role holders exist.
func TestLookupByRole_Deterministic(t *testing.T) {
	r := NewRegistry()
	mk := func() (*[32]byte, ed25519.PublicKey) {
		var enc [32]byte
		pub, _, _ := ed25519.GenerateKey(nil)
		return &enc, pub
	}
	e1, s1 := mk()
	e2, s2 := mk()
	r.Set("metro-spine", RegistryEntry{ID: "metro-spine", Role: "facility", EncPub: e1, SignPub: s1})
	r.Set("aaa-clinic", RegistryEntry{ID: "aaa-clinic", Role: "facility", EncPub: e2, SignPub: s2})
	for i := 0; i < 20; i++ {
		h, ok := r.LookupByRole("facility")
		if !ok || h.ID != "aaa-clinic" {
			t.Fatalf("LookupByRole not deterministic: got %q ok=%v (want aaa-clinic)", h.ID, ok)
		}
	}
	if _, ok := r.LookupByRole("nonexistent"); ok {
		t.Fatal("LookupByRole(nonexistent) should be ok=false")
	}
}

// TestRegistry_SetLookup verifies basic set-then-lookup round-trip, including
// absent-key behaviour.
func TestRegistry_SetLookup(t *testing.T) {
	reg := NewRegistry()
	reg.Set("payer", RegistryEntry{ID: "payer", BaseURL: "http://x"})

	holder, ok := reg.Lookup("payer")
	if !ok {
		t.Fatal("expected ok=true for existing key 'payer', got false")
	}
	if holder.BaseURL != "http://x" {
		t.Errorf("BaseURL mismatch: got %q, want %q", holder.BaseURL, "http://x")
	}
	if holder.ID != "payer" {
		t.Errorf("ID mismatch: got %q, want %q", holder.ID, "payer")
	}

	_, ok = reg.Lookup("nope")
	if ok {
		t.Fatal("expected ok=false for missing key 'nope', got true")
	}
}

// TestRegistry_ConcurrentSetLookup stress-tests the registry under concurrent
// Set and Lookup calls. Run with -race to verify no data races.
func TestRegistry_ConcurrentSetLookup(t *testing.T) {
	const goroutines = 50
	const ops = 200

	reg := NewRegistry()

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers: each goroutine sets a handful of keys repeatedly.
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				key := "holder-" + string(rune('A'+i%26))
				reg.Set(key, RegistryEntry{ID: key, BaseURL: "http://example.com"})
			}
		}()
	}

	// Readers: concurrent Lookups on the same keys.
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				key := "holder-" + string(rune('A'+i%26))
				reg.Lookup(key) // return value ignored; race detector catches bad access
			}
		}()
	}

	wg.Wait()
}

// TestRegistry_IDs verifies that IDs returns all holder ids in sorted order.
func TestRegistry_IDs(t *testing.T) {
	r := NewRegistry()
	r.Set("zebra", RegistryEntry{ID: "zebra"})
	r.Set("alpha", RegistryEntry{ID: "alpha"})

	ids := r.IDs()
	if len(ids) != 2 {
		t.Fatalf("IDs() len = %d; want 2", len(ids))
	}
	if ids[0] != "alpha" || ids[1] != "zebra" {
		t.Fatalf("IDs() = %v; want [alpha zebra]", ids)
	}
}

// TestLookupByRole verifies that LookupByRole returns the first holder with the
// given role, and ok=false for an unknown role.
func TestLookupByRole(t *testing.T) {
	r := NewRegistry()
	r.Set("payer", RegistryEntry{ID: "payer", Role: "payer"})
	r.Set("metro-spine", RegistryEntry{ID: "metro-spine", Role: "facility"})

	h, ok := r.LookupByRole("facility")
	if !ok || h.ID != "metro-spine" {
		t.Fatalf("LookupByRole(facility) = %v, %v; want metro-spine, true", h.ID, ok)
	}
	if _, ok := r.LookupByRole("nobody"); ok {
		t.Fatalf("LookupByRole(nobody) = true; want false")
	}
}

// TestRegistry_Delete verifies Delete removes a holder and is a no-op for an
// absent id. Used by the registrar poller's converge-to-feed (sub-slice c).
func TestRegistry_Delete(t *testing.T) {
	r := NewRegistry()
	r.Set("alpha", RegistryEntry{ID: "alpha"})
	r.Set("beta", RegistryEntry{ID: "beta"})

	r.Delete("alpha")
	if _, ok := r.Lookup("alpha"); ok {
		t.Fatal("Delete did not remove 'alpha'")
	}
	if _, ok := r.Lookup("beta"); !ok {
		t.Fatal("Delete removed the wrong holder ('beta' missing)")
	}
	r.Delete("absent") // no-op, must not panic
}
