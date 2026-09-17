package shnsdk

import (
	"errors"
	"strings"
	"testing"
)

const (
	cbPayerA = `{"resourceType":"Organization","id":"org-a","identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00001"}]}`
	cbPayerB = `{"resourceType":"Organization","id":"org-b","identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00002"}]}`
)

func cbCoverage(id, patient, payor string) string {
	return `{"resourceType":"Coverage","id":"` + id + `","status":"active","beneficiary":{"reference":"` + patient + `"},"payor":[` + payor + `]}`
}

func cbBundle(typ string, entries ...string) []byte {
	return []byte(`{"resourceType":"Bundle","type":"` + typ + `","entry":[` + strings.Join(entries, ",") + `]}`)
}

func cbEntry(fullURL, resource string) string {
	if fullURL == "" {
		return `{"resource":` + resource + `}`
	}
	return `{"fullUrl":"` + fullURL + `","resource":` + resource + `}`
}

// TestParsePayerIdentifier_CoverageBundle: a Bundle of Coverages routes when
// every Coverage resolves to the same payer (inline, contained, or an
// Organization in the same Bundle or behind resolveRef); no payer, an
// unresolved Coverage or two payers do not route.
func TestParsePayerIdentifier_CoverageBundle(t *testing.T) {
	a := PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00001"}
	contained := strings.Replace(cbCoverage("c2", "Patient/p1", `{"reference":"#org-a"}`), `"status"`, `"contained":[`+cbPayerA+`],"status"`, 1)
	resolveB := func(ref string) ([]byte, bool) {
		if ref == "Organization/org-b" {
			return []byte(cbPayerB), true
		}
		return nil, false
	}
	ok := []struct {
		name   string
		bundle []byte
		want   PayerIdentifier
	}{
		{"one Coverage, payer Organization entry by relative reference", cbBundle("searchset",
			cbEntry("https://ehr.example/fhir/Coverage/c1", cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-a"}`)),
			cbEntry("https://ehr.example/fhir/Organization/org-a", cbPayerA)), a},
		{"payer Organization entry by fullUrl", cbBundle("collection",
			cbEntry("urn:uuid:c1", cbCoverage("c1", "Patient/p1", `{"reference":"urn:uuid:9c7b2f5e-0000-4000-8000-000000000001"}`)),
			cbEntry("urn:uuid:9c7b2f5e-0000-4000-8000-000000000001", cbPayerA)), a},
		{"two Coverages, same payer (inline and contained)", cbBundle("searchset",
			cbEntry("", cbCoverage("c1", "Patient/p1", `{"identifier":{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00001"}}`)),
			cbEntry("", contained)), a},
		{"payer Organization entry by absolute reference equal to its fullUrl", cbBundle("searchset",
			cbEntry("https://ehr.example/fhir/Coverage/c1", cbCoverage("c1", "Patient/p1", `{"reference":"https://ehr.example/fhir/Organization/org-a"}`)),
			cbEntry("https://ehr.example/fhir/Organization/org-a", cbPayerA)), a},
		{"payer behind resolveRef", cbBundle("searchset",
			cbEntry("", cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-b"}`))),
			PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00002"}},
	}
	for _, r := range ok {
		t.Run(r.name, func(t *testing.T) {
			got, found := ParsePayerIdentifier(r.bundle, resolveB)
			if !found || got != r.want {
				t.Fatalf("ParsePayerIdentifier = %+v, %v", got, found)
			}
			got, err := ParseCoveragePayer(r.bundle, resolveB)
			if err != nil || got != r.want {
				t.Fatalf("ParseCoveragePayer = %+v, %v", got, err)
			}
		})
	}
	refused := []struct {
		name   string
		bundle []byte
		want   error
	}{
		{"two payers", cbBundle("searchset",
			cbEntry("", cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-a"}`)),
			cbEntry("", cbCoverage("c2", "Patient/p1", `{"reference":"Organization/org-b"}`)),
			cbEntry("", cbPayerA)), ErrAmbiguousCoveragePayer},
		{"absolute reference to another server's Organization", cbBundle("searchset",
			cbEntry("https://ehr.example/fhir/Coverage/c1", cbCoverage("c1", "Patient/p1", `{"reference":"https://other.example/fhir/Organization/org-a"}`)),
			cbEntry("https://ehr.example/fhir/Organization/org-a", cbPayerA)), ErrNoCoveragePayer},
		{"no Coverage", cbBundle("searchset", cbEntry("", cbPayerA)), ErrNoCoveragePayer},
		{"empty searchset", cbBundle("searchset"), ErrNoCoveragePayer},
		{"one Coverage without a resolvable payer", cbBundle("searchset",
			cbEntry("", cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-a"}`)),
			cbEntry("", cbCoverage("c2", "Patient/p1", `{"reference":"Organization/org-x"}`)),
			cbEntry("", cbPayerA)), ErrNoCoveragePayer},
	}
	for _, r := range refused {
		t.Run(r.name, func(t *testing.T) {
			if got, found := ParsePayerIdentifier(r.bundle, resolveB); found {
				t.Fatalf("routed to %+v", got)
			}
			if _, err := ParseCoveragePayer(r.bundle, resolveB); !errors.Is(err, r.want) {
				t.Fatalf("err = %v, want %v", err, r.want)
			}
		})
	}
	// A bare Coverage behaves as before.
	bare := []byte(cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-b"}`))
	if got, found := ParsePayerIdentifier(bare, resolveB); !found || got.Value != "00002" {
		t.Fatalf("bare Coverage: %+v %v", got, found)
	}
	if _, found := ParsePayerIdentifier([]byte(`null`), nil); found {
		t.Fatal("null routed")
	}
	if _, err := ParseCoveragePayer([]byte(`null`), nil); !errors.Is(err, ErrNoCoveragePayer) {
		t.Fatalf("null: %v", err)
	}
}

// TestParseCoverageBeneficiary_CoverageBundle: a Bundle of Coverages has one
// beneficiary only when every Coverage names the same one.
func TestParseCoverageBeneficiary_CoverageBundle(t *testing.T) {
	one := cbBundle("searchset",
		cbEntry("", cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-a"}`)),
		cbEntry("", cbCoverage("c2", "Patient/p1", `{"reference":"Organization/org-a"}`)),
		cbEntry("", cbPayerA))
	if got, err := ParseCoverageBeneficiary(one); err != nil || got != "Patient/p1" {
		t.Fatalf("one beneficiary: %q %v", got, err)
	}
	for name, b := range map[string][]byte{
		"two beneficiaries": cbBundle("searchset",
			cbEntry("", cbCoverage("c1", "Patient/p1", `{"reference":"Organization/org-a"}`)),
			cbEntry("", cbCoverage("c2", "Patient/p2", `{"reference":"Organization/org-a"}`))),
		"no Coverage": cbBundle("searchset", cbEntry("", cbPayerA)),
		"Coverage without a beneficiary": cbBundle("searchset",
			cbEntry("", `{"resourceType":"Coverage","id":"c1"}`)),
		"not JSON": []byte(`{"resourceType":"Bundle"`),
	} {
		if got, err := ParseCoverageBeneficiary(b); err == nil {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got, err := ParseCoverageBeneficiary([]byte(cbCoverage("c1", "Patient/p9", ""))); err != nil || got != "Patient/p9" {
		t.Fatalf("bare Coverage: %q %v", got, err)
	}
	if _, err := ParseCoverageBeneficiary([]byte(cbPayerA)); err == nil {
		t.Fatal("an Organization has no beneficiary")
	}
}
