package shnsdk

// Wire protocol 1.1.0 names each leg's envelope transaction type flat
// ("pas-claim"). Wire protocol 2.0.0 and version 2 audit records name it with
// its workstream's prefix ("pa.pas-claim"), the prefix plus the flat name.
// The table holds every leg of the network's taxonomy that has a wire name;
// test/sdkparity checks it against internal/legtaxonomy.
var prefixedTransactionTypes = map[string]string{
	"coverage-eligibility":    "pa.coverage-eligibility",
	"crd-order-select":        "pa.crd-order-select",
	"crd-order-dispatch":      "pa.crd-order-dispatch",
	"dtr-questionnaire-fetch": "pa.dtr-questionnaire-fetch",
	"pas-claim":               "pa.pas-claim",
	"pas-claim-update":        "pa.pas-claim-update",
	"pas-claim-inquire":       "pa.pas-claim-inquire",
	"patient-dtr":             "pa.patient-dtr",
	"federated-query":         "fqs.federated-query",
}

var flatTransactionTypes = func() map[string]string {
	m := make(map[string]string, len(prefixedTransactionTypes))
	for flat, prefixed := range prefixedTransactionTypes {
		m[prefixed] = flat
	}
	return m
}()

// PrefixedTransactionType returns the prefixed name of a leg's transaction
// type, given either of its names, and whether the name is a leg's.
func PrefixedTransactionType(name string) (string, bool) {
	if p, ok := prefixedTransactionTypes[name]; ok {
		return p, true
	}
	if _, ok := flatTransactionTypes[name]; ok {
		return name, true
	}
	return "", false
}

// FlatTransactionType returns the flat (wire protocol 1.1.0) name of a leg's
// transaction type, given either of its names, and whether the name is a
// leg's. Two names denote the same leg exactly when their flat names are
// equal.
func FlatTransactionType(name string) (string, bool) {
	if f, ok := flatTransactionTypes[name]; ok {
		return f, true
	}
	if _, ok := prefixedTransactionTypes[name]; ok {
		return name, true
	}
	return "", false
}

// PrefixedTransactionTypes returns every leg's flat name mapped to its
// prefixed name. The map is a copy.
func PrefixedTransactionTypes() map[string]string {
	m := make(map[string]string, len(prefixedTransactionTypes))
	for flat, prefixed := range prefixedTransactionTypes {
		m[flat] = prefixed
	}
	return m
}
