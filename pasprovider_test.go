package shnsdk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The party a prior-authorization request names.
//
// A Reference carrying only display text satisfies Claim.provider 1..1 at every
// PAS line, so structural validation certifies a request that identifies nobody
// — which is exactly how a submission whose provider was the string "provider"
// shipped. Every row here is about MEANING: which party was selected, that the
// request resolves it, and that a request which does not is refused rather than
// sent.

const (
	// testProviderNPI is pinned as a LITERAL, not read from the record under
	// test, so a row about the NPI cannot pass by comparing the code to itself.
	testProviderNPI  = "1417947384"
	testOrgProvider  = `{"resourceType":"Organization","id":"prov-org","identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"` + testProviderNPI + `"}],"name":"Requesting Provider"}`
	testRoleProvider = `{"resourceType":"PractitionerRole","id":"prov-role",` +
		`"identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1730958431"}]}`
	testPractitioner = `{"resourceType":"Practitioner","id":"prac-1","identifier":[{"system":"http://hl7.org/fhir/sid/us-npi","value":"1644556676"}]}`
)

func testResolver(byRef map[string]string) PASProviderResolver {
	return func(ref string) ([]byte, bool, error) {
		raw, ok := byRef[ref]
		if !ok {
			return nil, false, nil
		}
		return []byte(raw), true, nil
	}
}

// TestSelectPASProvider_WalksRequesterThenPerformer: the ONE selection rule both
// builders use. The requester comes first because that is who asked for the
// service; an order whose requester is a bare Practitioner — a real ordering
// clinician, and not a party this element accepts — falls through to the
// performer rather than failing.
func TestSelectPASProvider_WalksRequesterThenPerformer(t *testing.T) {
	held := testResolver(map[string]string{
		"Organization/prov-org":      testOrgProvider,
		"PractitionerRole/prov-role": testRoleProvider,
		"Practitioner/prac-1":        testPractitioner,
	})
	for _, tc := range []struct {
		name, order, wantRef string
	}{
		{"the requester when an inquiry can carry it",
			`{"resourceType":"DeviceRequest","requester":{"reference":"PractitionerRole/prov-role"},"performer":{"reference":"Organization/prov-org"}}`,
			"PractitionerRole/prov-role"},
		{"the performer when the requester is a bare Practitioner",
			`{"resourceType":"DeviceRequest","requester":{"reference":"Practitioner/prac-1"},"performer":{"reference":"Organization/prov-org"}}`,
			"Organization/prov-org"},
		{"a ServiceRequest states its performers as a LIST",
			`{"resourceType":"ServiceRequest","performer":[{"reference":"Organization/prov-org"}]}`,
			"Organization/prov-org"},
		{"a DeviceRequest states ONE performer",
			`{"resourceType":"DeviceRequest","performer":{"reference":"Organization/prov-org"}}`,
			"Organization/prov-org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, resource, err := SelectPASProvider([]byte(tc.order), held)
			if err != nil {
				t.Fatalf("select: %v", err)
			}
			if ref != tc.wantRef {
				t.Fatalf("ref = %q, want %q", ref, tc.wantRef)
			}
			if got := PASResourceTypeOf(resource); !strings.HasPrefix(tc.wantRef, got+"/") {
				t.Fatalf("resolved a %s for %q", got, tc.wantRef)
			}
		})
	}
}

// TestSelectPASProvider_RefusesByName is the rejection row for the rule above.
// Each refusal NAMES what it found, because "the prior authorization failed" at
// a caller leaves an operator with nowhere to look.
func TestSelectPASProvider_RefusesByName(t *testing.T) {
	held := testResolver(map[string]string{"Practitioner/prac-1": testPractitioner})
	for _, tc := range []struct {
		name, order string
		resolve     PASProviderResolver
		wants       []string
	}{
		{"the order names nobody",
			`{"resourceType":"ServiceRequest"}`, held,
			[]string{"names no requesting provider"}},
		{"the participant's system does not hold the party",
			`{"resourceType":"ServiceRequest","performer":[{"reference":"Organization/missing"}]}`, held,
			[]string{"has no requesting provider", "Organization/missing"}},
		{"nobody a request can carry",
			`{"resourceType":"DeviceRequest","requester":{"reference":"Practitioner/prac-1"}}`, held,
			[]string{"Practitioner/prac-1", "is a Practitioner", "Organization or a PractitionerRole"}},
		{"the read itself failed",
			`{"resourceType":"ServiceRequest","performer":[{"reference":"Organization/prov-org"}]}`,
			func(string) ([]byte, bool, error) { return nil, false, errors.New("connector down") },
			[]string{"read the requesting provider", "connector down"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := SelectPASProvider([]byte(tc.order), tc.resolve)
			if err == nil {
				t.Fatal("an order naming no carryable provider must be refused")
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not say %q", err, want)
				}
			}
		})
	}
}

// TestPASProviderNPI pins the identifier a payer matches on against a LITERAL,
// and the absence case: a party carrying no NPI records none rather than
// something invented.
func TestPASProviderNPI(t *testing.T) {
	if got := PASProviderNPI([]byte(testOrgProvider)); got != testProviderNPI {
		t.Fatalf("NPI = %q, want %q", got, testProviderNPI)
	}
	if got := PASProviderNPI([]byte(`{"resourceType":"Organization","id":"x"}`)); got != "" {
		t.Fatalf("a party with no NPI recorded %q", got)
	}
	if got := PASProviderNPI([]byte(`{"resourceType":"Organization","id":"x","identifier":[{"system":"urn:other","value":"12345"}]}`)); got != "" {
		t.Fatalf("an identifier in another namespace was read as an NPI: %q", got)
	}
}

// --- the submitted request names a party, and resolves it ---

func testSubmitInputs(provider string) ConformantClaimInputs {
	return ConformantClaimInputs{Insurer: testPayerOrganization(CMSPayerIdentity), Coverage: testMemberCoverage("MBR-1"),
		SR:             []byte(`{"resourceType":"ServiceRequest","id":"sr-1","status":"active","intent":"order","subject":{"reference":"Patient/MBR-1"},"code":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"72148","display":"MRI lumbar"}]}}`),
		Provider:       []byte(provider),
		PatientRef:     "Patient/MBR-1",
		CoverageRef:    "Coverage/MBR-1",
		MemberID:       "MBR-1",
		Corr:           "corr-1",
		Created:        time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Payer:          CMSPayerIdentity,
		PayerOrgEntry:  true,
		MemberIDSystem: MemberSystem,
	}
}

// claimProviderRef reads the Claim's provider reference out of a built request.
func claimProviderRef(t *testing.T, bundle []byte) string {
	t.Helper()
	claim, err := firstBundleResource(bundle, "Claim")
	if err != nil {
		t.Fatalf("read the Claim: %v", err)
	}
	var c struct {
		Provider struct {
			Reference string `json:"reference"`
			Display   string `json:"display"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(claim, &c); err != nil {
		t.Fatal(err)
	}
	if c.Provider.Display != "" && c.Provider.Reference == "" {
		t.Fatalf("the Claim names its provider by display text alone (%q) — which identifies nobody", c.Provider.Display)
	}
	return c.Provider.Reference
}

// TestBuildConformantClaimBundle_NamesAndResolvesItsProvider: the submitted
// request names the party it comes from BY REFERENCE, and that reference
// resolves to an entry the request carries — the reference payer's response
// graph refuses one it cannot resolve, and a payer that stored an unresolvable
// party has nothing to match an inquiry against.
func TestBuildConformantClaimBundle_NamesAndResolvesItsProvider(t *testing.T) {
	for _, tc := range []struct{ name, provider, wantRef string }{
		{"an Organization", testOrgProvider, "Organization/prov-org"},
		{"a PractitionerRole", testRoleProvider, "PractitionerRole/prov-role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := BuildConformantClaimBundle(testSubmitInputs(tc.provider))
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := claimProviderRef(t, body); got != tc.wantRef {
				t.Fatalf("Claim.provider = %q, want %q", got, tc.wantRef)
			}
			// The named party rides the request.
			var b struct {
				Entry []struct {
					FullURL  string          `json:"fullUrl"`
					Resource json.RawMessage `json:"resource"`
				} `json:"entry"`
			}
			if err := json.Unmarshal(body, &b); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range b.Entry {
				if e.FullURL == "https://shn.example/fhir/"+tc.wantRef {
					found = true
					if got := PASProviderNPI(e.Resource); got == "" {
						t.Fatalf("the carried party has no NPI, so a payer has nothing to match on: %s", e.Resource)
					}
				}
			}
			if !found {
				t.Fatalf("the request names %s and does not carry it: %s", tc.wantRef, body)
			}
			// Under the reference-payer lane the reference is absolutized to the
			// entry's own fullUrl, which is the form the payer resolves.
			abs := testSubmitInputs(tc.provider)
			abs.AbsoluteRefs = true
			absBody, err := BuildConformantClaimBundle(abs)
			if err != nil {
				t.Fatalf("build absolute: %v", err)
			}
			if got := claimProviderRef(t, absBody); got != "https://shn.example/fhir/"+tc.wantRef {
				t.Fatalf("absolute Claim.provider = %q", got)
			}
		})
	}
}

// TestBuildConformantClaimBundle_RefusesAProviderItCannotName is the rejection
// row: every shape that could not ride the request as a resolvable entry is
// refused at the builder, naming what was wrong.
func TestBuildConformantClaimBundle_RefusesAProviderItCannotName(t *testing.T) {
	for _, tc := range []struct{ name, provider, want string }{
		{"none at all", "", "requesting provider record is required"},
		{"a Practitioner", testPractitioner, "Organization or a PractitionerRole"},
		{"a Patient", `{"resourceType":"Patient","id":"p"}`, "Organization or a PractitionerRole"},
		{"no id to resolve", `{"resourceType":"Organization","name":"No id"}`, "no valid id"},
		{"not one object", `[{"resourceType":"Organization","id":"x"}]`, "not one JSON object"},
		{"the payer's own entry", `{"resourceType":"Organization","id":"org-cms-payer"}`, "same bundle entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildConformantClaimBundle(testSubmitInputs(tc.provider)); err == nil {
				t.Fatal("a request that would identify nobody must be refused")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestCheckPASProviderResolves is the guard's OWN rejection row, asserted on the
// guard's own signal rather than on a builder that happens to construct both
// halves. A request whose Claim names a provider the Bundle does not resolve is
// refused — $validate would certify it, because a Reference is structurally
// valid whether or not it reaches anybody.
func TestCheckPASProviderResolves(t *testing.T) {
	const entry = `{"fullUrl":"https://shn.example/fhir/Organization/prov-org","resource":` + testOrgProvider + `}`
	claim := func(provider string) string {
		return `{"fullUrl":"https://shn.example/fhir/Claim/c","resource":{"resourceType":"Claim","id":"c","provider":` + provider + `}}`
	}
	for _, tc := range []struct {
		name, bundle, want string
	}{
		{"names an entry the Bundle does not carry",
			`{"resourceType":"Bundle","entry":[` + claim(`{"reference":"Organization/elsewhere"}`) + `,` + entry + `]}`,
			`names provider "Organization/elsewhere", which the Bundle does not resolve`},
		{"names nobody at all, by display text",
			`{"resourceType":"Bundle","entry":[` + claim(`{"display":"provider"}`) + `,` + entry + `]}`,
			"names no provider by reference"},
		{"carries the party but names none",
			`{"resourceType":"Bundle","entry":[` + claim(`{}`) + `,` + entry + `]}`,
			"names no provider by reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPASProviderResolves([]byte(tc.bundle))
			if err == nil {
				t.Fatal("an unresolvable provider must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
	// The accepting side, both spellings a payer resolves.
	for _, ok := range []string{
		`{"resourceType":"Bundle","entry":[` + claim(`{"reference":"Organization/prov-org"}`) + `,` + entry + `]}`,
		`{"resourceType":"Bundle","entry":[` + claim(`{"reference":"https://shn.example/fhir/Organization/prov-org"}`) + `,` + entry + `]}`,
	} {
		if err := checkPASProviderResolves([]byte(ok)); err != nil {
			t.Fatalf("a resolvable provider was refused: %v", err)
		}
	}
}

// --- the submitted request names the MEMBER, the other half of the same rule ---

// TestBuildConformantClaimBundle_NamesTheMember: the Patient a request carries
// identifies the member, typed MB and under the namespace the participant's own
// records name them by.
//
// Which gate would fail if this were wrong? None of the structural ones: an
// id-only Patient satisfies every reference the Claim makes and $validate
// certifies the request. Only the payer knows — it stores the authorization
// under a member it cannot key on, and answers a later inquiry with no match.
// So the row reads the built bytes.
func TestBuildConformantClaimBundle_NamesTheMember(t *testing.T) {
	body, err := BuildConformantClaimBundle(testSubmitInputs(testOrgProvider))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, e := range b.Entry {
		var p struct {
			ResourceType string `json:"resourceType"`
			Identifier   []struct {
				System string `json:"system"`
				Value  string `json:"value"`
				Type   struct {
					Coding []PASCoding `json:"coding"`
				} `json:"type"`
			} `json:"identifier"`
		}
		if json.Unmarshal(e.Resource, &p) != nil || p.ResourceType != "Patient" {
			continue
		}
		seen = true
		if len(p.Identifier) != 1 {
			t.Fatalf("the Patient carries %d identifiers: %s", len(p.Identifier), e.Resource)
		}
		id := p.Identifier[0]
		// Pinned as LITERALS: the member id the request is about, the namespace
		// the caller stated, and the v2-0203 type a payer keys on.
		if id.Value != "MBR-1" || id.System != "urn:shn:member" {
			t.Fatalf("the Patient is identified as %s|%s", id.System, id.Value)
		}
		if len(id.Type.Coding) != 1 || id.Type.Coding[0].System != "http://terminology.hl7.org/CodeSystem/v2-0203" || id.Type.Coding[0].Code != "MB" {
			t.Fatalf("the member identifier is not typed MB: %s", e.Resource)
		}
		// And no demographics travel with it.
		for _, leaked := range []string{`"name"`, `"birthDate"`, `"gender"`, `"address"`, `"telecom"`} {
			if strings.Contains(string(e.Resource), leaked) {
				t.Fatalf("the request carries %s on its Patient: %s", leaked, e.Resource)
			}
		}
	}
	if !seen {
		t.Fatalf("the request carries no Patient: %s", body)
	}
}

// TestBuildConformantClaimBundle_RefusesAMemberItCannotName is the rejection row.
// A request that cannot say who the member is is refused rather than sent: the
// payer would accept and adjudicate it, and only a later inquiry — days on —
// would report that no authorization matches.
func TestBuildConformantClaimBundle_RefusesAMemberItCannotName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ConformantClaimInputs)
		want   string
	}{
		{"no namespace for the member", func(in *ConformantClaimInputs) { in.MemberIDSystem = "" }, "member identifier system is required"},
		{"a namespace of blanks", func(in *ConformantClaimInputs) { in.MemberIDSystem = "   " }, "member identifier system is required"},
		{"a patient reference naming no id", func(in *ConformantClaimInputs) { in.PatientRef = "Patient/" }, "names no FHIR id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := testSubmitInputs(testOrgProvider)
			tc.mutate(&in)
			if _, err := BuildConformantClaimBundle(in); err == nil {
				t.Fatal("a request that identifies no member must be refused")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestCheckPASMemberIdentified is the guard's OWN rejection row, asserted on the
// guard's signal rather than through a builder that constructs both halves.
func TestCheckPASMemberIdentified(t *testing.T) {
	bundle := func(patient string) []byte {
		return []byte(`{"resourceType":"Bundle","entry":[{"resource":` + patient + `}]}`)
	}
	typed := `{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},"system":"urn:shn:member","value":"MBR-1"}`
	for _, tc := range []struct{ name, patient, want string }{
		{"an id and nothing else", `{"resourceType":"Patient","id":"MBR-1"}`, "carries no member identifier typed MB"},
		{"an identifier with no system", `{"resourceType":"Patient","id":"MBR-1","identifier":[{"value":"MBR-1"}]}`, "carries no member identifier typed MB"},
		{"an identifier that is not typed MB", `{"resourceType":"Patient","id":"MBR-1","identifier":[{"system":"urn:shn:member","value":"MBR-1"}]}`, "carries no member identifier typed MB"},
		{"an identifier for a different member", `{"resourceType":"Patient","id":"MBR-1","identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},"system":"urn:shn:member","value":"MBR-2"}]}`, "carries no member identifier typed MB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPASMemberIdentified(bundle(tc.patient), "MBR-1")
			if err == nil {
				t.Fatal("a request whose Patient identifies nobody must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
	if err := checkPASMemberIdentified(bundle(`{"resourceType":"Patient","id":"MBR-1","identifier":[`+typed+`]}`), "MBR-1"); err != nil {
		t.Fatalf("an identified member was refused: %v", err)
	}
	if err := checkPASMemberIdentified([]byte(`{"resourceType":"Bundle","entry":[]}`), "MBR-1"); err == nil {
		t.Fatal("a request with no Patient at all must be refused")
	}
}

// TestMemberIdentifierSystemOf: the namespace comes from the caller's OWN
// record, and a record that names the member under none is a refusal rather than
// a guess.
func TestMemberIdentifierSystemOf(t *testing.T) {
	got, err := memberIdentifierSystemOf(testMemberPatient("MBR-1"), "MBR-1")
	if err != nil || got != "urn:shn:member" {
		t.Fatalf("system = %q, %v", got, err)
	}
	// A participant whose records name members under their own namespace sends
	// THAT one — nothing here defaults.
	own := []byte(`{"resourceType":"Patient","id":"p1","identifier":[{"system":"https://partner.example/members","value":"MBR-1"}]}`)
	if got, err := memberIdentifierSystemOf(own, "MBR-1"); err != nil || got != "https://partner.example/members" {
		t.Fatalf("system = %q, %v", got, err)
	}
	for _, tc := range []struct{ name, patient, want string }{
		{"no record at all", "", "namespace your own records name this member under is required"},
		{"not one object", `[{"resourceType":"Patient"}]`, "not one JSON object"},
		{"names another member", `{"resourceType":"Patient","id":"p1","identifier":[{"system":"urn:shn:member","value":"MBR-2"}]}`, "carries no identifier naming member"},
		{"an identifier with no system", `{"resourceType":"Patient","id":"p1","identifier":[{"value":"MBR-1"}]}`, "carries no identifier naming member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := memberIdentifierSystemOf([]byte(tc.patient), "MBR-1"); err == nil {
				t.Fatal("a record that names no member must be refused")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// --- the continuation records the party the request NAMED ---

// TestNewPriorAuthContinuation_ReadsTheSubmittedProvider: the continuation's
// provider identifier comes off the SUBMITTED BUNDLE, not from a second reading
// of the order the request was built from.
//
// That matters beyond tidiness. The derivation this replaced took an NPI stated
// INLINE on the order's requester reference first, so an order whose requester
// carried an inline NPI and whose performer was the carryable Organization
// recorded one party and named the other — and an inquiry built from that
// continuation would have asked a payer about an authorization stored under a
// different provider, matching nothing, with no signal anywhere.
func TestNewPriorAuthContinuation_ReadsTheSubmittedProvider(t *testing.T) {
	in := testSubmitInputs(testOrgProvider)
	// The order names the ordering clinician inline AND the carryable
	// Organization: the shape that used to record the wrong party.
	in.SR = []byte(`{"resourceType":"ServiceRequest","id":"sr-1","status":"active","intent":"order","subject":{"reference":"Patient/MBR-1"},` +
		`"requester":{"identifier":{"system":"http://hl7.org/fhir/sid/us-npi","value":"1644556676"}},` +
		`"performer":[{"reference":"Organization/prov-org"}],` +
		`"code":{"coding":[{"system":"http://www.ama-assn.org/go/cpt","code":"72148","display":"MRI lumbar"}]}}`)
	body, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-1", body, nil)
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if cont.ProviderNPI != testProviderNPI {
		t.Fatalf("ProviderNPI = %q, want %q — the NPI of the party the request NAMED, not the one stated inline on its requester",
			cont.ProviderNPI, testProviderNPI)
	}
}

// TestNewPriorAuthContinuation_RefusesAProviderTheRequestDoesNotResolve is that
// reader's rejection row. Recording nothing would leave a requester holding a
// continuation that names no party, and the inquiry built from it would match
// nothing with no signal that anything was missing.
func TestNewPriorAuthContinuation_RefusesAProviderTheRequestDoesNotResolve(t *testing.T) {
	for _, tc := range []struct{ name, request, want string }{
		{"the Claim names nobody",
			`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim","id":"c","provider":{"display":"provider"},"item":[{"sequence":1}]}}]}`,
			"names no requesting provider by reference"},
		{"the Claim names a party the request does not carry",
			`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim","id":"c","provider":{"reference":"Organization/elsewhere"},"item":[{"sequence":1}]}}]}`,
			`names provider "Organization/elsewhere", which the request does not resolve`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPriorAuthContinuation("2.0", "payer", "MBR-1", []byte(tc.request), nil)
			if err == nil {
				t.Fatal("a continuation that could name no party must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestBuildConformantClaimUpdateBundle_NamesTheSameParty: an amendment names the
// party the submission named. A payer that stored an authorization under one
// provider and is amended under another is holding two requests, not one.
func TestBuildConformantClaimUpdateBundle_NamesTheSameParty(t *testing.T) {
	in := testSubmitInputs(testOrgProvider)
	submit, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	qr := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-1","status":"completed","subject":{"reference":"Patient/MBR-1"}}`)
	prov := []byte(`{"resourceType":"Provenance","id":"prov-1","recorded":"2026-06-01T12:00:00Z","target":[{"reference":"QuestionnaireResponse/qr-1"}],"agent":[{"who":{"identifier":{"system":"http://hl7.org/fhir/sid/us-npi","value":"1234567890"}}}]}`)
	update, err := BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{Insurer: testPayerOrganization(in.Payer), Coverage: testMemberCoverage(in.MemberID),
		QR: qr, SR: in.SR, Provider: in.Provider, PatientRef: in.PatientRef, CoverageRef: in.CoverageRef,
		MemberID: in.MemberID, MemberIDSystem: in.MemberIDSystem, Provenance: prov, Corr: "corr-2", OriginalCorr: in.Corr,
		Created: in.Created, Payer: in.Payer, PayerOrgEntry: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, want := claimProviderRef(t, update), claimProviderRef(t, submit); got != want {
		t.Fatalf("the amendment names %q and the submission named %q", got, want)
	}
	if _, err := BuildConformantClaimUpdateBundle(ConformantClaimUpdateInputs{Insurer: testPayerOrganization(in.Payer), Coverage: testMemberCoverage(in.MemberID),
		QR: qr, SR: in.SR, PatientRef: in.PatientRef, CoverageRef: in.CoverageRef,
		MemberID: in.MemberID, MemberIDSystem: in.MemberIDSystem, Provenance: prov, Corr: "corr-2", OriginalCorr: in.Corr,
		Created: in.Created, Payer: in.Payer, PayerOrgEntry: true,
	}); err == nil {
		t.Fatal("an amendment naming no provider must be refused")
	}
}
