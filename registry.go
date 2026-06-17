package shnsdk

import (
	"crypto/ed25519"
	"sort"
	"sync"
)

// RegistryEntry describes a substrate participant (see Role for the allowed
// values) as held in the runtime peer cache. It is DISTINCT from the feed wire
// DTO Holder (string-keyed, base64-encoded, json-tagged): RegistryEntry is the
// decoded, in-memory peer-cache entry keyed by id, carrying the keys as usable
// values.
type RegistryEntry struct {
	ID      string
	Role    string            // "provider" | "payer" | "facility" | "phg" | "partner" (role-based routing / audit-append trust)
	EncPub  *[32]byte         // X25519 public key for envelope encryption
	SignPub ed25519.PublicKey // Ed25519 public key for signature verification
	BaseURL string
}

// Registry is a concurrency-safe holder registry. It is a value type whose
// internals are shared references (a *sync.RWMutex and a map pointer), so all
// copies passed by value operate on the same underlying state and lock. This
// means callers continue to use Registry by value with no signature changes to
// New(reg Registry, ...) anywhere.
//
// The registry is safe for concurrent reads AND writes. Static-after-boot usage
// (populate once, then read-only) remains correct; future dynamic registration
// or rotation (FR-38) is also sound.
type Registry struct {
	mu      *sync.RWMutex
	holders map[string]RegistryEntry
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() Registry {
	return Registry{mu: &sync.RWMutex{}, holders: make(map[string]RegistryEntry)}
}

// Set registers or replaces the RegistryEntry for the given id. Safe for concurrent use.
func (r Registry) Set(id string, h RegistryEntry) {
	r.mu.Lock()
	r.holders[id] = h
	r.mu.Unlock()
}

// Delete removes the RegistryEntry for the given id (no-op if absent). Safe for
// concurrent use. Used by the registrar poller to converge the registry to the
// registrar's live feed (manifest-base holders are never deleted by the poller).
func (r Registry) Delete(id string) {
	r.mu.Lock()
	delete(r.holders, id)
	r.mu.Unlock()
}

// Lookup returns the RegistryEntry for the given id. ok is false if id is not
// present. Safe for concurrent use.
func (r Registry) Lookup(id string) (RegistryEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.holders[id]
	return h, ok
}

// IDs returns the registered holder ids, sorted. Safe for concurrent use.
func (r Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.holders))
	for id := range r.holders {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// LookupByRole returns the role's RegistryEntry with the lexicographically-smallest
// id, deterministically (independent of map iteration order). ok is false if no
// holder has that role. Routing selects the federated-query target by role (the
// external facility) rather than a hardcoded id. The stable smallest-id pick
// keeps routing deterministic once dynamic admission can add a second same-role
// holder; routing-by-explicit-id for genuine multi-holder-per-role deployments is
// tracked future work.
func (r Registry) LookupByRole(role string) (RegistryEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best RegistryEntry
	found := false
	for _, h := range r.holders {
		if h.Role != role {
			continue
		}
		if !found || h.ID < best.ID {
			best = h
			found = true
		}
	}
	return best, found
}
