package shnsdk

import "testing"

// TestResponderTransactionOperations_MatchesEnforcedTable pins the exported
// accessor fresh against the table the Responder actually enforces on inbound
// envelopes (responderReqOp): same keys, same operations. A stale projection
// here would let a network-side lockstep check pass while the enforced table
// had drifted.
func TestResponderTransactionOperations_MatchesEnforcedTable(t *testing.T) {
	ops := ResponderTransactionOperations()
	if len(ops) != len(responderReqOp) {
		t.Fatalf("ResponderTransactionOperations returned %d entries, enforced table has %d", len(ops), len(responderReqOp))
	}
	for tx, op := range responderReqOp {
		got, ok := ops[tx]
		if !ok {
			t.Errorf("ResponderTransactionOperations missing enforced type %q", tx)
			continue
		}
		if got != op {
			t.Errorf("ResponderTransactionOperations[%q] = %q, enforced table has %q", tx, got, op)
		}
	}
}

// TestResponderResponseOperations_MatchesResponseSwitch pins the exported
// response-op accessor fresh against the switch the Responder actually stamps
// response legs with (responseOp): one entry per served transaction type,
// value identical, none empty. A stale projection here would let a
// network-side lockstep check pass while the stamped response op had drifted.
func TestResponderResponseOperations_MatchesResponseSwitch(t *testing.T) {
	ops := ResponderResponseOperations()
	if len(ops) != len(responderReqOp) {
		t.Fatalf("ResponderResponseOperations returned %d entries, responder serves %d types", len(ops), len(responderReqOp))
	}
	for tx := range responderReqOp {
		got, ok := ops[tx]
		if !ok {
			t.Errorf("ResponderResponseOperations missing served type %q", tx)
			continue
		}
		if want := responseOp(tx); got != want || got == "" {
			t.Errorf("ResponderResponseOperations[%q] = %q, response switch stamps %q", tx, got, want)
		}
	}
}

// TestResponderResponseOperations_ReturnsFreshCopy rejects the aliasing
// failure mode for the response-op accessor.
func TestResponderResponseOperations_ReturnsFreshCopy(t *testing.T) {
	first := ResponderResponseOperations()
	first["pas-claim"] = "tampered"
	delete(first, "crd-order-select")
	fresh := ResponderResponseOperations()
	if fresh["pas-claim"] != responseOp("pas-claim") {
		t.Errorf("mutation through a returned map reached a later call: %q", fresh["pas-claim"])
	}
	if _, ok := fresh["crd-order-select"]; !ok {
		t.Error("deletion through a returned map reached a later call")
	}
}

// TestResponderTransactionOperations_ReturnsFreshCopy rejects the aliasing
// failure mode: mutating the returned map must never reach the enforced table
// or a later caller's view of it.
func TestResponderTransactionOperations_ReturnsFreshCopy(t *testing.T) {
	first := ResponderTransactionOperations()
	first["pas-claim"] = "tampered"
	delete(first, "coverage-eligibility")
	fresh := ResponderTransactionOperations()
	if fresh["pas-claim"] != responderReqOp["pas-claim"] {
		t.Errorf("mutation through a returned map reached a later call: %q", fresh["pas-claim"])
	}
	if _, ok := fresh["coverage-eligibility"]; !ok {
		t.Error("deletion through a returned map reached a later call")
	}
}
