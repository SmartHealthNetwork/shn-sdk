package shnsdk

// Wire protocol 1.1.0 names each leg's envelope transaction type flat
// ("pas-claim"). Wire protocol 2.0.0 and version 2 audit records name it with
// its workstream's prefix ("pa.pas-claim"), the prefix plus the flat name.
// The table holds every leg of the network's taxonomy that has a wire name;
// the platform's SDK parity suite checks it against the network's leg taxonomy.
var prefixedTransactionTypes = map[string]string{
	"coverage-eligibility":    "pa.coverage-eligibility",
	"crd-order-select":        "pa.crd-order-select",
	"crd-order-dispatch":      "pa.crd-order-dispatch",
	"dtr-questionnaire-fetch": "pa.dtr-questionnaire-fetch",
	"pas-claim":               "pa.pas-claim",
	"pas-claim-update":        "pa.pas-claim-update",
	"pas-claim-inquire":       "pa.pas-claim-inquire",
	"patient-dtr":             "pa.patient-dtr",
	"cdex-attachment-submit":  "pa.cdex-attachment-submit",
	"federated-query":         "fqs.federated-query",
}

// transactionTypeSince is the release each leg added after shn-sdk v1.0.0
// carries, by flat name: the taxonomy row's since. A recipient that declares
// no legs list (RegistryEntry.Legs absent) predates every leg here, so the
// network does not route one to it.
var transactionTypeSince = map[string]string{
	"cdex-attachment-submit": "1.1.0",
}

// TransactionTypeSince is the release that brings the leg named, in either
// form, and whether it is marked: false for a leg shn-sdk v1.0.0 published,
// which every recipient takes as before, and for a name no leg has.
func TransactionTypeSince(name string) (string, bool) {
	flat, ok := FlatTransactionType(name)
	if !ok {
		return "", false
	}
	since, ok := transactionTypeSince[flat]
	return since, ok
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

// sameLeg reports whether two transaction type names, each in either form,
// name the same leg of the taxonomy.
func sameLeg(a, b string) bool {
	fa, ok := FlatTransactionType(a)
	if !ok {
		return false
	}
	fb, ok := FlatTransactionType(b)
	return ok && fa == fb
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
