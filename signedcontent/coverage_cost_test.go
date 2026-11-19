// The gateway relay's test of the signed-content reading (its coverage_cost_test.go),
// carried with the reading.

package signedcontent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

// signedProvenanceBundle is a Bundle of n resources and one signed Provenance with
// n targets, each naming one of them.
func signedProvenanceBundle(n int) string {
	var b strings.Builder
	b.WriteString(`{"resourceType":"Bundle","type":"collection","id":"b","entry":[{"resource":{"resourceType":"Provenance","signature":[{"type":[],"when":"x","who":{}}],"target":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"reference":"Basic/%d"}`, i)
	}
	b.WriteString(`]}}`)
	for i := range n {
		fmt.Fprintf(&b, `,{"fullUrl":"https://x.example/fhir/Basic/%d","resource":{"resourceType":"Basic","id":"%d"}}`, i, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

// Finding what a signed Provenance covers costs time near linear in its
// targets and the document's resources, not their product.
func TestCoverageCostIsNearLinearInProvenanceTargets(t *testing.T) {
	wantLinearCost(t, "coverage", 32768, func(n int) func() {
		src := signedProvenanceBundle(n)
		d, err := splice.Scan([]byte(src), splice.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if c := coverage(d); len(hitsEach(c.regions, effect{start: 0, end: len(src)})) == 0 {
			t.Fatal("the Provenance covers nothing")
		}
		return func() { _ = coverage(d) }
	})
}

// typeName is the i-th of distinct names: a capital, then letters.
func typeName(i int) string {
	b := []byte{'A'}
	for {
		b = append(b, byte('a'+i%26))
		i /= 26
		if i == 0 {
			return string(b)
		}
	}
}
