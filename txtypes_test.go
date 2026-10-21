package shnsdk

import (
	"slices"
	"strings"
	"testing"
)

func TestTransactionTypeNames(t *testing.T) {
	all := PrefixedTransactionTypes()
	if len(all) != 9 {
		t.Fatalf("%d legs, want 9", len(all))
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
