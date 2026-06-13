package shnsdk

import (
	"testing"
)

// TestPendedLedger_RecordThenBegin: record then begin → begin returns true (claimed);
// a second begin on the same key → false (only one in flight at a time).
func TestPendedLedger_RecordThenBegin(t *testing.T) {
	l := newPendedLedger()
	const (
		subj = "pci-alice"
		corr = "corr-001"
	)

	l.record(subj, corr)

	if !l.begin(subj, corr) {
		t.Fatal("begin after record: want true, got false")
	}
	// Second begin while in-progress → false (serialization guard).
	if l.begin(subj, corr) {
		t.Fatal("second begin while in-progress: want false, got true")
	}
}

// TestPendedLedger_BeginWithoutRecord: begin with no prior record → false.
func TestPendedLedger_BeginWithoutRecord(t *testing.T) {
	l := newPendedLedger()
	if l.begin("pci-nobody", "corr-ghost") {
		t.Fatal("begin on absent key: want false, got true")
	}
}

// TestPendedLedger_ReleaseResetsToAvailable: record → begin (→ in-progress) →
// release → a subsequent begin returns true again (released back to pended).
func TestPendedLedger_ReleaseResetsToAvailable(t *testing.T) {
	l := newPendedLedger()
	const (
		subj = "pci-bob"
		corr = "corr-002"
	)

	l.record(subj, corr)

	if !l.begin(subj, corr) {
		t.Fatal("first begin: want true")
	}

	l.release(subj, corr)

	// After release the claim should be pended again.
	if !l.begin(subj, corr) {
		t.Fatal("begin after release: want true (back to pended), got false")
	}
}

// TestPendedLedger_FinalizeBlocksReplay: record → begin → finalize → a subsequent
// begin → false (entry removed; replay protection).
func TestPendedLedger_FinalizeBlocksReplay(t *testing.T) {
	l := newPendedLedger()
	const (
		subj = "pci-carol"
		corr = "corr-003"
	)

	l.record(subj, corr)

	if !l.begin(subj, corr) {
		t.Fatal("begin before finalize: want true")
	}

	l.finalize(subj, corr)

	// Replayed update finds nothing.
	if l.begin(subj, corr) {
		t.Fatal("begin after finalize: want false (replay protection), got true")
	}
}

// TestPendedLedger_UnknownKeySafeNoOps: finalize, release, and begin on a key
// that was never recorded must not panic and must return the documented value.
func TestPendedLedger_UnknownKeySafeNoOps(t *testing.T) {
	l := newPendedLedger()
	const (
		subj = "pci-unknown"
		corr = "corr-never"
	)

	// None of these should panic.
	l.finalize(subj, corr)
	l.release(subj, corr)

	if l.begin(subj, corr) {
		t.Fatal("begin on unknown key after no-op finalize/release: want false, got true")
	}
}

// TestPendedLedger_DistinctKeysAreIndependent: two different (subject, correlation)
// pairs are fully independent — one being in-progress does not block the other.
func TestPendedLedger_DistinctKeysAreIndependent(t *testing.T) {
	l := newPendedLedger()

	// Same correlation, different subject.
	l.record("pci-alice", "corr-shared")
	l.record("pci-bob", "corr-shared")

	if !l.begin("pci-alice", "corr-shared") {
		t.Fatal("begin alice: want true")
	}
	// Bob's entry is independent; alice in-progress must not block bob.
	if !l.begin("pci-bob", "corr-shared") {
		t.Fatal("begin bob (independent key): want true, got false")
	}

	// Same subject, different correlation.
	l.record("pci-carol", "corr-A")
	l.record("pci-carol", "corr-B")

	if !l.begin("pci-carol", "corr-A") {
		t.Fatal("begin carol/corrA: want true")
	}
	if !l.begin("pci-carol", "corr-B") {
		t.Fatal("begin carol/corrB (different correlation): want true, got false")
	}
}
