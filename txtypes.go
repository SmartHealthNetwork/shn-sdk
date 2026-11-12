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
	"cdex-attachment-request": "pa.cdex-attachment-request",
	"cdex-task-status":        "pa.cdex-task-status",
	"federated-query":         "fqs.federated-query",
}

// transactionTypeSince is the release each leg added after shn-sdk v1.0.0
// carries, by flat name: the taxonomy row's since. A recipient that declares
// no legs list (RegistryEntry.Legs absent) predates every leg here, so the
// network does not route one to it.
var transactionTypeSince = map[string]string{
	"cdex-attachment-submit":  "1.1.0",
	"cdex-attachment-request": "1.5.0",
	"cdex-task-status":        "1.12.0",
}

// Answer envelope values (TransactionTypeAnswerEnvelope).
const (
	// AnswerEnvelopeFHIR: a refusal the answering side writes itself is an
	// OperationOutcome, application/fhir+json, as a client of the leg's FHIR
	// operation reads one.
	AnswerEnvelopeFHIR = "fhir"
	// AnswerEnvelopeJSON: such a refusal is {"error": …}, application/json
	// (the CDS Hooks legs and the patient-authored DTR exchange).
	AnswerEnvelopeJSON = "json"
)

// transactionTypeAnswerEnvelope is each leg's answer envelope, by flat name:
// the taxonomy row's answerEnvelope. Every leg in the table has one.
var transactionTypeAnswerEnvelope = map[string]string{
	"coverage-eligibility":    AnswerEnvelopeFHIR,
	"crd-order-select":        AnswerEnvelopeJSON,
	"crd-order-dispatch":      AnswerEnvelopeJSON,
	"dtr-questionnaire-fetch": AnswerEnvelopeFHIR,
	"pas-claim":               AnswerEnvelopeFHIR,
	"pas-claim-update":        AnswerEnvelopeFHIR,
	"pas-claim-inquire":       AnswerEnvelopeFHIR,
	"patient-dtr":             AnswerEnvelopeJSON,
	"cdex-attachment-submit":  AnswerEnvelopeFHIR,
	"cdex-attachment-request": AnswerEnvelopeFHIR,
	"federated-query":         AnswerEnvelopeFHIR,
	// A FHIR read and vread; stated here until its taxonomy row comes.
	"cdex-task-status": AnswerEnvelopeFHIR,
}

// TransactionTypeAnswerEnvelope is the envelope of a refusal the answering
// side writes itself on the leg named, in either form: AnswerEnvelopeFHIR or
// AnswerEnvelopeJSON. ok is false for a name no leg has. A participant's own
// answer is carried as it was sent on every leg. From shn-sdk v1.11.0.
func TransactionTypeAnswerEnvelope(name string) (string, bool) {
	flat, ok := FlatTransactionType(name)
	if !ok {
		return "", false
	}
	envelope, ok := transactionTypeAnswerEnvelope[flat]
	return envelope, ok
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
