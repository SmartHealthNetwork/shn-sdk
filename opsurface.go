// opsurface.go — read-only export of the Responder's inbound operation table.
package shnsdk

import "slices"

// ResponderTransactionOperations returns a fresh copy of the TransactionType →
// request-operation table the Responder enforces on inbound envelopes (an
// envelope whose transaction type has no entry is rejected 400 before any
// token work). It lets an embedder — or a network-side lockstep conformance
// check that pins this published surface against the network's own operation
// catalog — enumerate the transaction types this build serves without probing
// a live endpoint. Mutating the returned map never touches the enforced table.
func ResponderTransactionOperations() map[string]string {
	out := make(map[string]string, len(responderReqOp))
	for tx, op := range responderReqOp {
		out[tx] = op
	}
	return out
}

// Legs are the legs this Responder answers as a recipient, by flat name and
// sorted: what its system declares with Identity.DeclareCapabilities. A leg it has no
// handler for is not among them. The network refuses to route a leg a
// recipient does not take only for a leg TransactionTypeSince marks; an
// unmarked leg is routed to every recipient as before, declared or not.
func (r *Responder) Legs() []string {
	legs := make([]string, 0, len(responderReqOp))
	for tx := range responderReqOp {
		legs = append(legs, tx)
	}
	slices.Sort(legs)
	return legs
}

// ResponderResponseOperations returns a fresh copy of the TransactionType →
// response-operation table the Responder stamps on the answer leg (the
// responseOp switch), one entry per served transaction type. Together with
// ResponderTransactionOperations it makes both halves of the Responder's wire
// contract enumerable — and pinnable by a network-side lockstep conformance
// check — without probing a live endpoint. Mutating the returned map never
// touches the stamped values.
func ResponderResponseOperations() map[string]string {
	out := make(map[string]string, len(responderReqOp))
	for tx := range responderReqOp {
		out[tx] = responseOp(tx)
	}
	return out
}
