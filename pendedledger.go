package shnsdk

import "sync"

// pendedLedger mirrors the substrate payer's pended-claim ledger semantics
// (RecordPendedClaim / BeginClaimUpdate / ReleaseClaimUpdate /
// FinalizeClaimUpdate — see internal/gateway holderdata): one atomic
// test-and-set per (subjectPCI, correlationID) so exactly one ClaimUpdate is in
// flight, a replay of an approved update finds nothing, and a failed or
// insufficient update releases the claim back to pended. In-memory ⇒
// PER-PROCESS (same scope note as the jti guard); a partner needing durable
// pends fronts this with their own store (additive seam).
type pendedLedger struct {
	mu    sync.Mutex
	state map[string]ledgerStatus
}

type ledgerStatus int

const (
	ledgerPended     ledgerStatus = iota + 1 // awaiting supplemental data
	ledgerInProgress                         // a ClaimUpdate is mid-adjudication
)

func newPendedLedger() *pendedLedger {
	return &pendedLedger{state: make(map[string]ledgerStatus)}
}

func ledgerKey(subjectPCI, correlationID string) string {
	return subjectPCI + "|" + correlationID
}

// record records a pended claim for (subjectPCI, correlationID).
func (l *pendedLedger) record(subjectPCI, correlationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state[ledgerKey(subjectPCI, correlationID)] = ledgerPended
}

// begin atomically claims a pended claim for a ClaimUpdate: returns true only if
// the claim was in the pended state (not in-progress, not absent). This single
// test-and-set is the FR-6 current-state authority check AND the serialization
// point — only one update can be in flight.
func (l *pendedLedger) begin(subjectPCI, correlationID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey(subjectPCI, correlationID)
	if l.state[k] != ledgerPended {
		return false
	}
	l.state[k] = ledgerInProgress
	return true
}

// release returns an in-progress claim to pended (a ClaimUpdate did NOT approve).
func (l *pendedLedger) release(subjectPCI, correlationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey(subjectPCI, correlationID)
	if l.state[k] == ledgerInProgress {
		l.state[k] = ledgerPended
	}
}

// finalize completes the pended→approved transition: removes the entry so a
// replayed update finds nothing (replay protection).
func (l *pendedLedger) finalize(subjectPCI, correlationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, ledgerKey(subjectPCI, correlationID))
}
