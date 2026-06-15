package shnsdk

import (
	"testing"
	"time"
)

func rgT0() time.Time { return time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC) }

// TestReplayGuard_ReplayWithinWindow: first sight records (false); a second sight of
// the same key within the window is a replay (true).
func TestReplayGuard_ReplayWithinWindow(t *testing.T) {
	g := NewReplayGuard(time.Hour, 1<<16)
	if g.CheckAndRecord("k", rgT0()) {
		t.Fatal("first sight reported as replay")
	}
	if !g.CheckAndRecord("k", rgT0().Add(30*time.Minute)) {
		t.Fatal("second sight within window not reported as replay")
	}
}

// TestReplayGuard_RecordableAfterWindow: once a key is older than the window it is no
// longer a replay (the entry has aged out of relevance).
func TestReplayGuard_RecordableAfterWindow(t *testing.T) {
	g := NewReplayGuard(time.Hour, 1<<16)
	if g.CheckAndRecord("k", rgT0()) {
		t.Fatal("first sight reported as replay")
	}
	if g.CheckAndRecord("k", rgT0().Add(time.Hour+time.Second)) {
		t.Fatal("sight past the window wrongly reported as replay")
	}
}

// TestReplayGuard_DistinctKeysIndependent: different keys do not interfere.
func TestReplayGuard_DistinctKeysIndependent(t *testing.T) {
	g := NewReplayGuard(time.Hour, 1<<16)
	if g.CheckAndRecord("a", rgT0()) {
		t.Fatal("key a first sight reported as replay")
	}
	if g.CheckAndRecord("b", rgT0()) {
		t.Fatal("key b first sight reported as replay")
	}
}

// TestReplayGuard_CapBounded: under a flood of distinct keys the set never exceeds cap,
// and the guard keeps functioning. A still-in-window key MAY be shed under flood —
// assert the size bound, not which key survives. Exercises the flood-shed branch.
func TestReplayGuard_CapBounded(t *testing.T) {
	const capN = 4
	g := NewReplayGuard(time.Hour, capN)
	now := rgT0()
	for i := 0; i < capN*10; i++ {
		g.CheckAndRecord(string(rune('A'+i)), now) // distinct, all in-window → forces flood-shed
		if n := g.Len(); n > capN {
			t.Fatalf("set exceeded cap: len=%d cap=%d", n, capN)
		}
	}
	if g.CheckAndRecord("fresh", now) {
		t.Fatal("guard non-functional after flood")
	}
}

// TestReplayGuard_EvictsExpiredAtCap: at cap, expired entries are swept so a long-running
// guard does not shed live entries when old ones can be dropped instead.
func TestReplayGuard_EvictsExpiredAtCap(t *testing.T) {
	const capN = 3
	g := NewReplayGuard(time.Hour, capN)
	old := rgT0()
	g.CheckAndRecord("x", old)
	g.CheckAndRecord("y", old)
	later := old.Add(2 * time.Hour) // past the window
	g.CheckAndRecord("z1", later)
	g.CheckAndRecord("z2", later)
	if g.Len() > capN {
		t.Fatalf("len=%d exceeds cap=%d", g.Len(), capN)
	}
	if !g.CheckAndRecord("z2", later) {
		t.Fatal("recent key z2 was not retained (should be a replay)")
	}
}

// TestReplayGuard_ZeroCapClamped: a cap below 1 is clamped to 1 — the guard must not
// hang (a zero cap would spin the flood-shed loop forever) and stays functional.
func TestReplayGuard_ZeroCapClamped(t *testing.T) {
	g := NewReplayGuard(time.Hour, 0)
	now := rgT0()
	if g.CheckAndRecord("a", now) {
		t.Fatal("first sight reported as replay")
	}
	// A second distinct key forces eviction at the clamped cap of 1 — must terminate.
	if g.CheckAndRecord("b", now) {
		t.Fatal("distinct key reported as replay")
	}
	if g.Len() > 1 {
		t.Fatalf("clamped cap not honored: len=%d", g.Len())
	}
}
