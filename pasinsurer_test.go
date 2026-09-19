package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
)

// payerOrgEntryInputs is a reference-payer-lane submit: the lane that carries the
// payer as a resolvable entry, which is the lane these rows are about.
func payerOrgEntryInputs(t *testing.T) ConformantClaimInputs {
	t.Helper()
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry = true
	in.AbsoluteRefs = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	return in
}

// TestConformantClaimBundleNamesThePayersOwnRecord: the request carries the
// participant's own Organization for the payer, and its Claim and its Coverage
// both name THAT entry.
//
// This is the element that kept an originated authorization unfindable after the
// coverage was closed. The payer scopes an inquiry's search by the insurer and
// re-homes an Organization only through an NPI, which a payer organization does
// not carry — so a submission naming a minted `Organization/cms-payer` while the
// inquiry named the participant's own record matched nothing, and swapping only
// that one id made the same inquiry return the authorization.
func TestConformantClaimBundleNamesThePayersOwnRecord(t *testing.T) {
	b, err := BuildConformantClaimBundle(payerOrgEntryInputs(t))
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	var bundle struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(b, &bundle); err != nil {
		t.Fatal(err)
	}
	insurer, payor, carried := "", "", false
	for _, e := range bundle.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Insurer      struct {
				Reference string `json:"reference"`
			} `json:"insurer"`
			Payor []struct {
				Reference string `json:"reference"`
			} `json:"payor"`
		}
		if json.Unmarshal(e.Resource, &r) != nil {
			continue
		}
		switch {
		case r.ResourceType == "Organization" && r.ID == testPayerOrgID:
			carried = true
		case r.ResourceType == "Claim" && insurer == "":
			insurer = r.Insurer.Reference
		case r.ResourceType == "Coverage" && len(r.Payor) == 1:
			payor = r.Payor[0].Reference
		}
	}
	if !carried {
		t.Fatalf("the request does not carry the participant's own payer record (Organization/%s): %s", testPayerOrgID, b)
	}
	want := pasBundleBaseURL + "/Organization/" + testPayerOrgID
	if insurer != want {
		t.Errorf("Claim.insurer = %q, want the participant's own payer record %q", insurer, want)
	}
	if payor != want {
		t.Errorf("Coverage.payor = %q, want the same entry the Claim names %q", payor, want)
	}
	if err := checkPASInsurerResolves(b); err != nil {
		t.Errorf("checkPASInsurerResolves: %v", err)
	}
}

// TestConformantClaimBundleRefusesWithoutThePayersOwnRecord: a caller that cannot
// supply the payer's own record is refused, never given a minted one.
func TestConformantClaimBundleRefusesWithoutThePayersOwnRecord(t *testing.T) {
	cases := []struct {
		name, want string
		insurer    []byte
		payer      PayerIdentifier
	}{
		{name: "no record", want: "Organization record is required", payer: CMSPayerIdentity},
		{name: "empty record", want: "Organization record is required", insurer: []byte("  "), payer: CMSPayerIdentity},
		{name: "not an Organization", want: "names the payer as an Organization", payer: CMSPayerIdentity,
			insurer: []byte(`{"resourceType":"Patient","id":"p"}`)},
		{name: "no id", want: "no valid id", payer: CMSPayerIdentity,
			insurer: []byte(`{"resourceType":"Organization","identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00001"}]}`)},
		{name: "not one object", want: "not one JSON object", payer: CMSPayerIdentity,
			insurer: []byte(`[{"resourceType":"Organization","id":"o"}]`)},
		{name: "another payer than the leg routed to", want: "names two", payer: CMSPayerIdentity,
			insurer: []byte(`{"resourceType":"Organization","id":"org-other","identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00078"}]}`)},
		{name: "carries no identifier at all", want: "names two", payer: CMSPayerIdentity,
			insurer: []byte(`{"resourceType":"Organization","id":"org-cms-payer","name":"Nameless"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := payerOrgEntryInputs(t)
			in.Insurer, in.Payer = tc.insurer, tc.payer
			_, err := BuildConformantClaimBundle(in)
			if err == nil {
				t.Fatal("a request naming a payer organization the participant does not hold must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestConformantClaimUpdateBundleRefusesWithoutThePayersOwnRecord: the same
// refusal on the amendment builder — an amendment names the payer the submission
// named.
func TestConformantClaimUpdateBundleRefusesWithoutThePayersOwnRecord(t *testing.T) {
	in := conformantUpdateInputsFromGolden(t)
	in.PayerOrgEntry, in.AbsoluteRefs = true, true
	in.Insurer = nil
	if _, err := BuildConformantClaimUpdateBundle(in); err == nil {
		t.Fatal("an amendment with no payer record of the participant's own must be refused")
	}
}

// TestCheckPASInsurerResolvesRefusesAnUnresolvableInsurer is the guard's own
// rejection row, asserted on the guard's signal.
func TestCheckPASInsurerResolvesRefusesAnUnresolvableInsurer(t *testing.T) {
	const org = `{"resourceType":"Organization","id":"org-cms-payer"}`
	cases := []struct{ name, bundle string }{
		{"names nothing at all", `{"resourceType":"Bundle","entry":[` +
			`{"fullUrl":"http://x/Claim/c","resource":{"resourceType":"Claim","id":"c"}},` +
			`{"fullUrl":"http://x/Organization/org-cms-payer","resource":` + org + `}]}`},
		{"dangling", `{"resourceType":"Bundle","entry":[` +
			`{"fullUrl":"http://x/Claim/c","resource":{"resourceType":"Claim","id":"c","insurer":{"reference":"Organization/payer"}}},` +
			`{"fullUrl":"http://x/Organization/org-cms-payer","resource":` + org + `}]}`},
		{"names something that is not an Organization", `{"resourceType":"Bundle","entry":[` +
			`{"fullUrl":"http://x/Claim/c","resource":{"resourceType":"Claim","id":"c","insurer":{"reference":"Patient/p"}}},` +
			`{"fullUrl":"http://x/Patient/p","resource":{"resourceType":"Patient","id":"p"}}]}`},
		{"no Claim", `{"resourceType":"Bundle","entry":[` +
			`{"fullUrl":"http://x/Organization/org-cms-payer","resource":` + org + `}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkPASInsurerResolves([]byte(tc.bundle)); err == nil {
				t.Fatal("checkPASInsurerResolves accepted a Claim naming an insurer the Bundle does not resolve")
			}
		})
	}
	for _, ok := range []string{
		`{"resourceType":"Bundle","entry":[` +
			`{"fullUrl":"http://x/Claim/c","resource":{"resourceType":"Claim","id":"c","insurer":{"reference":"Organization/org-cms-payer"}}},` +
			`{"fullUrl":"http://x/Organization/org-cms-payer","resource":` + org + `}]}`,
		// The contained shape the non-entry lanes put on the wire resolves too.
		`{"resourceType":"Bundle","entry":[{"fullUrl":"http://x/Claim/c","resource":{"resourceType":"Claim","id":"c",` +
			`"insurer":{"reference":"#cms-payer"},"contained":[{"resourceType":"Organization","id":"cms-payer"}]}}]}`,
	} {
		if err := checkPASInsurerResolves([]byte(ok)); err != nil {
			t.Fatalf("checkPASInsurerResolves refused a resolvable insurer: %v", err)
		}
	}
}

// TestConformantClaimBundleRefusesACoverageThatReferencesWhatItDoesNotCarry is
// the closed-reference row.
//
// A participant's Coverage makes references into that participant's own server.
// This request carries the member and the payer, so the beneficiary and the payor
// are re-homed onto its own entries, and a subscriber or policy holder naming the
// same member is re-homed with them. Anything else names a record that does not
// ride the request — a real payer refuses the whole graph for it — and dropping
// it would change what the participant's record asserts, so the request is
// refused instead, naming the element.
func TestConformantClaimBundleRefusesACoverageThatReferencesWhatItDoesNotCarry(t *testing.T) {
	const member = "MBR-COVERED"
	base := `{"resourceType":"Coverage","id":"cov-mbr-covered","status":"active",` +
		`"identifier":[{"system":"urn:shn:coverage","value":"` + member + `"}],` +
		`"beneficiary":{"reference":"Patient/` + member + `"},` +
		`"payor":[{"reference":"Organization/org-cms-payer"}]`

	t.Run("a subscriber who is the member travels, re-homed", func(t *testing.T) {
		in := payerOrgEntryInputs(t)
		in.Coverage = []byte(base + `,"subscriber":{"reference":"Patient/` + member + `"}}`)
		b, err := BuildConformantClaimBundle(in)
		if err != nil {
			t.Fatalf("a subscriber naming the member must travel, re-homed: %v", err)
		}
		if !strings.Contains(string(b), `"subscriber"`) {
			t.Fatalf("the subscriber the record asserted was dropped: %s", b)
		}
	})

	for _, tc := range []struct{ name, coverage, want string }{
		{"a subscriber who is somebody else", base + `,"subscriber":{"reference":"RelatedPerson/spouse"}}`, "names someone other than the member"},
		{"a policy holder who is somebody else", base + `,"policyHolder":{"reference":"Organization/employer"}}`, "names someone other than the member"},
		{"a contract", base + `,"contract":[{"reference":"Contract/c-1"}]}`, "Coverage.contract"},
		{"a reference inside an extension", base + `,"extension":[{"url":"urn:x","valueReference":{"reference":"Organization/other"}}]}`, "Coverage.extension"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := payerOrgEntryInputs(t)
			in.Coverage = []byte(tc.coverage)
			_, err := BuildConformantClaimBundle(in)
			if err == nil {
				t.Fatal("a coverage referencing a record this request does not carry must be refused, not quietly stripped")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not name %q", err, tc.want)
			}
		})
	}
}

// TestRunPriorAuthRefusesBeforeTheFirstLeg: the records a prior authorization
// cannot invent are checked before anything is sent, and the refusal names which
// one is missing.
func TestRunPriorAuthRefusesBeforeTheFirstLeg(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*PriorAuthRequest)
	}{
		{"no patient", "Patient record", func(r *PriorAuthRequest) { r.Patient = nil }},
		{"no coverage", "Coverage search result", func(r *PriorAuthRequest) { r.Coverage = nil }},
		{"no provider", "requesting provider", func(r *PriorAuthRequest) { r.Provider = nil }},
		{"a coverage search naming no payer organization", "names no payer organization", func(r *PriorAuthRequest) {
			r.Coverage = []byte(`{"resourceType":"Bundle","type":"searchset","entry":[{"resource":{"resourceType":"Coverage","id":"c",` +
				`"beneficiary":{"reference":"Patient/MBR-COVERED"},"payor":[{"identifier":{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00001"}}]}}]}`)
		}},
		{"a coverage search that leaves its payer organization out", "does not include it", func(r *PriorAuthRequest) {
			r.Coverage = []byte(`{"resourceType":"Bundle","type":"searchset","entry":[{"resource":{"resourceType":"Coverage","id":"c",` +
				`"beneficiary":{"reference":"Patient/MBR-COVERED"},"payor":[{"reference":"Organization/missing"}]}}]}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := demoPARequest()
			tc.mutate(&req)
			err := requirePriorAuthRecords(req)
			if err == nil {
				t.Fatal("a prior authorization with no record of its own must be refused before its first leg")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not name %q", err, tc.want)
			}
		})
	}
	if err := requirePriorAuthRecords(demoPARequest()); err != nil {
		t.Fatalf("a complete request must not be refused: %v", err)
	}
}
