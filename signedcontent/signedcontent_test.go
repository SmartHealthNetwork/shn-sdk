package signedcontent_test

import (
	"strings"
	"testing"

	"github.com/SmartHealthNetwork/shn-sdk/signedcontent"
)

// span returns the byte range of the first occurrence of sub in doc.
func span(t *testing.T, doc, sub string) (int, int) {
	t.Helper()
	i := strings.Index(doc, sub)
	if i < 0 {
		t.Fatalf("fixture: %q not in %q", sub, doc)
	}
	return i, i + len(sub)
}

// Each kind of signature covers what the package doc says, named by its
// carrier, and a document with none has no region.
func TestCoverage_RegionsAndCarriers(t *testing.T) {
	const sig = `{"data":"c2ln"}`
	basic := `{"resourceType":"Basic","id":"b1","code":{"text":"x"},"extension":[{"url":"urn:x","valueSignature":` + sig + `}]}`
	for _, row := range []struct {
		name, doc, covered, carrier string
	}{
		{"a Signature at a place R4 types Signature covers its resource",
			`{"resourceType":"Task","id":"t1","status":"requested","intent":"order","extension":[{"url":"urn:x","valueSignature":` + sig + `}]}`,
			"", "Signature in Task"},
		{"one in a resource the Task contains covers that resource alone",
			`{"resourceType":"Task","id":"t1","status":"requested","intent":"order","contained":[` + basic + `]}`,
			basic, "Signature in Basic"},
		{"a Bundle's own signature covers the Bundle",
			`{"resourceType":"Bundle","type":"collection","signature":` + sig + `}`,
			"", "Bundle.signature"},
		{"a Bundle's own signature named in another case",
			`{"resourceType":"Bundle","type":"collection","SIGNATURE":` + sig + `}`,
			"", "Bundle.signature"},
		{"a contained Provenance's # target covers its container",
			`{"resourceType":"Task","id":"t1","status":"requested","intent":"order","contained":[{"resourceType":"Provenance","id":"p1","target":[{"reference":"#"}],"signature":[` + sig + `]}]}`,
			"", "Provenance.signature"},
	} {
		t.Run(row.name, func(t *testing.T) {
			regions, err := signedcontent.Coverage([]byte(row.doc))
			if err != nil {
				t.Fatal(err)
			}
			start, end := 0, len(row.doc)
			if row.covered != "" {
				start, end = span(t, row.doc, row.covered)
			}
			for _, r := range regions {
				if r.Start == start && r.End == end && r.Carrier == row.carrier {
					return
				}
			}
			t.Fatalf("no region [%d,%d) %q among %+v", start, end, row.carrier, regions)
		})
	}
	t.Run("nothing signed, no region", func(t *testing.T) {
		regions, err := signedcontent.Coverage([]byte(`{"resourceType":"Task","id":"t1","status":"requested","contained":[` + strings.Replace(basic, "valueSignature", "valueString", 1) + `]}`))
		if err != nil || len(regions) != 0 {
			t.Fatalf("Coverage = %+v, %v, want no region", regions, err)
		}
	})
}

// RootCarrier names a signature covering the whole document, and none when a
// signature covers only something inside it.
func TestRootCarrier(t *testing.T) {
	const sig = `{"data":"c2ln"}`
	for _, row := range []struct{ name, doc, want string }{
		{"the Task's own Signature", `{"resourceType":"Task","id":"t1","extension":[{"url":"urn:x","valueSignature":` + sig + `}]}`, "Signature in Task"},
		{"a contained resource's only", `{"resourceType":"Task","id":"t1","contained":[{"resourceType":"Basic","id":"b1","extension":[{"url":"urn:x","valueSignature":` + sig + `}]}]}`, ""},
		{"a Provenance targeting the Task", `{"resourceType":"Task","id":"t1","contained":[{"resourceType":"Provenance","id":"p1","target":[{"reference":"Task/t1"}],"signature":[` + sig + `]}]}`, "Provenance.signature"},
		{"a Provenance targeting another resource", `{"resourceType":"Task","id":"t1","contained":[{"resourceType":"Provenance","id":"p1","target":[{"reference":"Patient/x"}],"signature":[` + sig + `]}]}`, ""},
		{"nothing signed", `{"resourceType":"Task","id":"t1"}`, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, err := signedcontent.RootCarrier([]byte(row.doc))
			if err != nil || got != row.want {
				t.Fatalf("RootCarrier = %q, %v, want %q", got, err, row.want)
			}
		})
	}
}

// A document the reader refuses is an error, never an empty reading.
func TestCoverage_RefusedDocuments(t *testing.T) {
	for _, row := range []struct{ name, doc string }{
		{"not JSON", `{"resourceType":`},
		{"two values", `{} {}`},
		{"a member named twice", `{"id":"a","id":"b"}`},
		{"a member named again in another case", `{"id":"a","ID":"b"}`},
		{"not UTF-8", "{\"id\":\"\xff\"}"},
		{"a lone surrogate escape", `{"id":"\ud800"}`},
		{"nested past 64 levels", strings.Repeat("[", 65) + strings.Repeat("]", 65)},
	} {
		t.Run(row.name, func(t *testing.T) {
			if regions, err := signedcontent.Coverage([]byte(row.doc)); err == nil {
				t.Fatalf("Coverage = %+v, want an error", regions)
			}
			if c, err := signedcontent.RootCarrier([]byte(row.doc)); err == nil {
				t.Fatalf("RootCarrier = %q, want an error", c)
			}
		})
	}
}

// OwnSignatureMember names a Bundle's or a Provenance's own signature member,
// in any case, and nothing else; and a member it names is the carrier
// Coverage reads for that resource.
func TestOwnSignatureMember(t *testing.T) {
	for _, c := range []struct {
		typ, name string
		want      bool
	}{
		{"Bundle", "signature", true},
		{"Provenance", "signature", true},
		{"Bundle", "Signature", true},
		{"Provenance", "SIGNATURE", true},
		{"Bundle", "signatures", false},
		{"Bundle", "entry", false},
		{"Provenance", "target", false},
		{"Patient", "signature", false},
		{"Contract", "signer", false},
		{"bundle", "signature", false},
		{"", "signature", false},
	} {
		if got := signedcontent.OwnSignatureMember(c.typ, c.name); got != c.want {
			t.Errorf("OwnSignatureMember(%q, %q) = %v, want %v", c.typ, c.name, got, c.want)
		}
	}
	for _, c := range []struct{ doc, carrier string }{
		{`{"resourceType":"Bundle","Signature":{"type":[],"when":"x","who":{}},"type":"collection"}`, "Bundle.signature"},
		{`{"resourceType":"Provenance","SIGNATURE":[{}],"target":[]}`, "Provenance.signature"},
	} {
		got, err := signedcontent.RootCarrier([]byte(c.doc))
		if err != nil || got != c.carrier {
			t.Errorf("RootCarrier(%s) = %q, %v; want %q", c.doc, got, err, c.carrier)
		}
	}
}
