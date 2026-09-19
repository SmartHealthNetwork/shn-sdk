package shnsdk

import "strings"

// testRequestingProvider is the participant record a prior-authorization request
// in this package names as its requesting provider.
//
// It is not decoration on the request. Da Vinci PAS says a payer SHALL match an
// inquiry on the member id PLUS the ordering or rendering provider identifier,
// so a request that identifies no provider is one no conformant inquiry can find
// again — and a Claim.provider carrying only display text satisfies the element
// at every line, so nothing structural would have said so.
// testMemberPatient is the caller's OWN Patient record for a member: the member
// identifier, typed MB, and nothing else. A payer matches a prior authorization
// on that identifier, so a request built without one identifies nobody — the
// same defect as a provider named by display text, one element over.
func testMemberPatient(member string) []byte {
	return []byte(`{"resourceType":"Patient","id":"` + member + `",` +
		`"identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},` +
		`"system":"` + MemberSystem + `","value":"` + member + `"}]}`)
}

func testRequestingProvider() []byte {
	return []byte(`{"resourceType":"Organization","id":"convergence-provider",` +
		`"identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1417947384"}],` +
		`"name":"Convergence Health Partners"}`)
}

// testMemberCoverage is the caller's OWN Coverage record for a member — its own
// id and its own member identifier. A payer locates the policy from the Coverage
// a request names and matches a later inquiry against the coverage it stored, so
// a request carrying a Coverage this package minted names a record no inquiry
// built from the participant's own system could name again; and one minted id,
// the same for every member, is one member's coverage overwriting another's on a
// payer that stores by client-assigned id.
func testMemberCoverage(member string) []byte {
	return []byte(`{"resourceType":"Coverage","id":"cov-` + strings.ToLower(member) + `","status":"active",` +
		`"identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},` +
		`"system":"urn:shn:coverage","value":"` + member + `"}],` +
		`"beneficiary":{"reference":"Patient/` + member + `"},` +
		`"relationship":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/subscriber-relationship","code":"self"}]},` +
		`"payor":[{"reference":"Organization/org-cms-payer"}]}`)
}

// testMemberCoverageSearch is that Coverage as the participant's own system
// returns it — a searchset holding the member's Coverage and the payor
// Organization it names. It is the shape a coverage check is built from and the
// shape a prior authorization reads its coverage record out of.
func testMemberCoverageSearch(member string) []byte {
	return []byte(`{"resourceType":"Bundle","type":"searchset","entry":[` +
		`{"fullUrl":"http://sor.example/fhir/Coverage/cov-` + strings.ToLower(member) + `","resource":` +
		string(testMemberCoverage(member)) + `},` +
		`{"fullUrl":"http://sor.example/fhir/Organization/org-cms-payer","resource":` +
		`{"resourceType":"Organization","id":"org-cms-payer","name":"Centers for Medicare and Medicaid Services",` +
		`"identifier":[{"system":"` + CMSPayerIdentity.System + `","value":"` + CMSPayerIdentity.Value + `"}]}}]}`)
}

// testMemberCoverageRef is the reference the participant's own records name that
// Coverage by — what a caller passes as CoverageRef for its QR-context and
// native-lane roles, and the entry the built request's Claim resolves to.
func testMemberCoverageRef(member string) string {
	return "Coverage/cov-" + strings.ToLower(member)
}

// testPayerOrganization is the participant record a prior-authorization request
// in this package names as the payer it is made under.
//
// It is the third element of the same rule the provider and the coverage follow.
// The payer scopes an inquiry's search by the insurer and resolves an
// Organization to one it holds only through an NPI, which a payer organization
// does not carry -- so whatever a submission names is what a later inquiry has
// to name, and a request naming a payer organization the builder minted is one
// the participant's own inquiry can never match.
// testPayerOrgID is the id the participant's own payer record carries — what the
// request's insurer entry is named by, and what an inquiry from the same records
// names too.
const testPayerOrgID = "org-cms-payer"

func testPayerOrganization(payer PayerIdentifier) []byte {
	return []byte(`{"resourceType":"Organization","id":"org-cms-payer",` +
		`"identifier":[{"system":"` + payer.System + `","value":"` + payer.Value + `"}],` +
		`"name":"Centers for Medicare and Medicaid Services"}`)
}
