package shnsdk

import (
	"sync"
	"time"
)

// ReplayGuard is a bounded, TTL-evicting one-time-use set: it records keys
// (correlationIDs, assertion jtis) and reports whether a key was already seen within
// a window. It is the shared mechanism behind the Hub's correlation replay guard, the
// gateway's patient-access replay guard, the authz/registrar assertion-jti one-time-use
// guards, and the SDK Responder's hub-assertion replay check. The clock is injected by
// the caller (CheckAndRecord takes now) so the guard owns no time dependency and stays
// hermetic under an injected clock.
//
// ReplayGuard is concurrency-safe. Keys are remembered for window and the set is
// bounded at cap entries with amortized eviction.
type ReplayGuard struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	window time.Duration
	cap    int
}

// NewReplayGuard returns a ReplayGuard that remembers keys for window, bounded at
// maxEntries entries. A maxEntries below 1 is clamped to 1: a zero/negative value would
// make the flood-shed loop in evictLocked spin forever (it can never get the set below
// the cap), so this shared type fails safe to "remember at most one" rather than hang.
// Under a deliberate flood of distinct in-window keys, a shed entry may permit at most
// ONE replay of a still-valid key; size maxEntries generously (a JTI/assertion guard
// uses 1<<16).
func NewReplayGuard(window time.Duration, maxEntries int) *ReplayGuard {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &ReplayGuard{seen: make(map[string]time.Time), window: window, cap: maxEntries}
}

// CheckAndRecord returns true if key was already seen within the window at now (a
// replay). Otherwise it records key at now and returns false. Amortized eviction: the
// O(n) sweep runs only when the set reaches cap, so steady state stays O(1) per call.
func (g *ReplayGuard) CheckAndRecord(key string, now time.Time) (replay bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if seen, ok := g.seen[key]; ok && now.Sub(seen) <= g.window {
		return true
	}
	if len(g.seen) >= g.cap {
		g.evictLocked(now)
	}
	g.seen[key] = now
	return false
}

// Len returns the current number of recorded keys. Primarily for tests/observability.
func (g *ReplayGuard) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.seen)
}

// evictLocked drops expired entries and, if a flood of distinct keys keeps the set at
// the cap, sheds arbitrary entries until bounded. Map iteration is randomized, so the
// flood-shed step drops arbitrary (not strictly oldest) entries; a shed entry can at
// worst permit ONE replay of an item still inside its window — an acceptable bound
// under a deliberate flood by an authenticated caller. Caller must hold mu.
func (g *ReplayGuard) evictLocked(now time.Time) {
	for k, seen := range g.seen {
		if now.Sub(seen) > g.window {
			delete(g.seen, k)
		}
	}
	for len(g.seen) >= g.cap {
		for k := range g.seen {
			delete(g.seen, k)
			break
		}
	}
}
