package shnsdk

import (
	"slices"
	"strings"
	"testing"
)

func TestTransactionTypeNames(t *testing.T) {
	all := PrefixedTransactionTypes()
	if len(all) != 12 {
		t.Fatalf("%d legs, want 12", len(all))
	}
	for flat, prefixed := range all {
		ws, name, ok := strings.Cut(prefixed, ".")
		if !ok || name != flat || (ws != "pa" && ws != "fqs") {
			t.Errorf("%s: prefixed name %q is not a workstream prefix plus the flat name", flat, prefixed)
		}
		for _, n := range []string{flat, prefixed} {
			if p, ok := PrefixedTransactionType(n); !ok || p != prefixed {
				t.Errorf("PrefixedTransactionType(%q) = %q, %v; want %q", n, p, ok, prefixed)
			}
			if f, ok := FlatTransactionType(n); !ok || f != flat {
				t.Errorf("FlatTransactionType(%q) = %q, %v; want %q", n, f, ok, flat)
			}
		}
	}
	if all["federated-query"] != "fqs.federated-query" {
		t.Errorf("federated-query takes the fqs prefix, got %q", all["federated-query"])
	}
	for _, n := range []string{"", "patient-access-read", "pa.patient-access-read", "pa.federated-query", "fqs.pas-claim", "pa."} {
		if _, ok := PrefixedTransactionType(n); ok {
			t.Errorf("PrefixedTransactionType(%q) accepted a name that is no leg's", n)
		}
		if _, ok := FlatTransactionType(n); ok {
			t.Errorf("FlatTransactionType(%q) accepted a name that is no leg's", n)
		}
	}
	all["pas-claim"] = "x"
	if p, _ := PrefixedTransactionType("pas-claim"); p != "pa.pas-claim" {
		t.Error("PrefixedTransactionTypes returned the table itself, not a copy")
	}
}

func TestWireProtocolVersionsNamesTheVersionSpoken(t *testing.T) {
	if !slices.Contains(WireProtocolVersions(), WireProtocolVersion) {
		t.Fatalf("WireProtocolVersions() = %v lacks WireProtocolVersion %q", WireProtocolVersions(), WireProtocolVersion)
	}
}

// TestTransactionTypeAnswerEnvelope: every leg in the table has an answer
// envelope, read by either of its names; a name no leg has has none. The
// platform's parity suite holds each value to the taxonomy's row.
func TestTransactionTypeAnswerEnvelope(t *testing.T) {
	for flat, prefixed := range PrefixedTransactionTypes() {
		a, okA := TransactionTypeAnswerEnvelope(flat)
		b, okB := TransactionTypeAnswerEnvelope(prefixed)
		if !okA || !okB || a != b || (a != AnswerEnvelopeFHIR && a != AnswerEnvelopeJSON) {
			t.Errorf("%s / %s: answer envelope %q,%v / %q,%v, want one closed value by either name", flat, prefixed, a, okA, b, okB)
		}
	}
	for flat, want := range map[string]string{"pas-claim": AnswerEnvelopeFHIR, "crd-order-select": AnswerEnvelopeJSON, "patient-dtr": AnswerEnvelopeJSON, "federated-query": AnswerEnvelopeFHIR} {
		if got, _ := TransactionTypeAnswerEnvelope(flat); got != want {
			t.Errorf("%s: answer envelope %q, want %q", flat, got, want)
		}
	}
	for _, name := range []string{"", "pa.", "toy-echo", "PAS-CLAIM", "pa.pas-claim.x"} {
		if got, ok := TransactionTypeAnswerEnvelope(name); ok || got != "" {
			t.Errorf("%q: answer envelope %q, %v, want none", name, got, ok)
		}
	}
}
