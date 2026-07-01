package shnsdk

import "testing"

func TestParsePayerIdentifier(t *testing.T) {
	containedOrg := []byte(`{"resourceType":"Coverage","payor":[{"reference":"#p"}],
	  "contained":[{"resourceType":"Organization","id":"p",
	  "identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00078"}]}]}`)
	externalOrg := []byte(`{"resourceType":"Coverage","payor":[{"reference":"Organization/ext"}]}`)
	inline := []byte(`{"resourceType":"Coverage","payor":[{"identifier":
	  {"system":"urn:oid:2.16.840.1.113883.6.300","value":"00099"}}]}`)
	noPayor := []byte(`{"resourceType":"Coverage","payor":[{"reference":"Organization/none"}]}`)

	resolve := func(ref string) ([]byte, bool) {
		if ref == "Organization/ext" {
			return []byte(`{"resourceType":"Organization","id":"ext",
			  "identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00078"}]}`), true
		}
		return nil, false
	}

	cases := []struct {
		name string
		cov  []byte
		want PayerIdentifier
		ok   bool
	}{
		{"contained", containedOrg, PayerIdentifier{"urn:oid:2.16.840.1.113883.6.300", "00078"}, true},
		{"external", externalOrg, PayerIdentifier{"urn:oid:2.16.840.1.113883.6.300", "00078"}, true},
		{"inline", inline, PayerIdentifier{"urn:oid:2.16.840.1.113883.6.300", "00099"}, true},
		{"missing", noPayor, PayerIdentifier{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParsePayerIdentifier(c.cov, resolve)
			if ok != c.ok || got != c.want {
				t.Fatalf("got (%+v,%v) want (%+v,%v)", got, ok, c.want, c.ok)
			}
		})
	}
}
