package shnsdk

import (
	"encoding/json"
	"testing"
)

// An item's certificationType is compared only when the prior item and the
// amendment's item both state one. A different kind on the same item is
// a change the IG marks: PAS 2.2.1 Claim-HomecareAuthorizationUpdateExample marks
// its item 2, certificationType "3" (cancel) where it was "I", as "changed". A kind
// stated where the prior stated none (a PAS 2.0 submission writes none) has
// nothing to compare against and marks nothing from that alone.
func TestPASInfoChangedMarks_CertificationTypeComparedOnlyWhenBothState(t *testing.T) {
	item := func(certCode string) json.RawMessage {
		ext := ""
		if certCode != "" {
			ext = `"extension":[{"url":"` + pasExtCertificationType + `","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/1322","code":"` + certCode + `"}]}}],`
		}
		return json.RawMessage(`{` + ext + `"sequence":1,"productOrService":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0424"}]}}`)
	}
	bundleOf := func(it json.RawMessage) []byte {
		return []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim","item":[` + string(it) + `]}}]}`)
	}
	for _, tc := range []struct {
		name       string
		prior, now string
		wantMark   string
	}{
		{"I then 3 (cancel) is changed", "I", "3", pasInfoChangedChanged},
		{"the same kind on both sides marks nothing", "I", "I", ""},
		{"a 2.0 prior stating none marks nothing", "", "I", ""},
		{"a kind dropped by the amendment marks nothing from the kind alone", "I", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marks, err := pasInfoChangedMarks(bundleOf(item(tc.now)), []json.RawMessage{item(tc.prior)})
			if err != nil {
				t.Fatal(err)
			}
			if got := marks[0]; got != tc.wantMark {
				t.Fatalf("item 1 marked %q, want %q (marks %v)", got, tc.wantMark, marks)
			}
		})
	}
}
