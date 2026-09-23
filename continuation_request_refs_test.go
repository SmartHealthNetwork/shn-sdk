package shnsdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// A later payer decision can refer to the exact Claim this participant sent.
// The continuation must retain those request identities without accepting a
// same-id Claim at another base or a new identity from the payer's answer.
func TestPriorAuthContinuation_SubmittedClaimReferences(t *testing.T) {
	in := conformantSubmitInputs(t)
	request, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	cont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-COVERED", request, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Claim/convergence-claim", "https://shn.example/fhir/Claim/convergence-claim"}
	if !reflect.DeepEqual(cont.ClaimReferences, want) {
		t.Fatalf("sent Claim references = %q, want %q", cont.ClaimReferences, want)
	}
	encoded, err := json.Marshal(cont)
	if err != nil {
		t.Fatal(err)
	}
	var resumed PriorAuthContinuation
	if err := json.Unmarshal(encoded, &resumed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed.ClaimReferences, want) {
		t.Fatalf("JSON round-trip references = %q", resumed.ClaimReferences)
	}
	if len(resumed.ClaimIdentifiers) == 0 {
		t.Fatal("submitted Claim identifier was lost")
	}
	idJSON, err := json.Marshal(resumed.ClaimIdentifiers[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, fields string
		valid        bool
	}{
		{"no linkage", ``, true},
		{"submitted full URL", `,"request":{"reference":"https://shn.example/fhir/Claim/convergence-claim"}`, true},
		{"submitted relative URL", `,"request":{"reference":"Claim/convergence-claim"}`, true},
		{"both constraints match", `,"request":{"reference":"Claim/convergence-claim","identifier":` + string(idJSON) + `}`, true},
		{"foreign same id", `,"request":{"reference":"https://foreign.example/fhir/Claim/convergence-claim"}`, false},
		{"other Claim", `,"request":{"reference":"Claim/other"}`, false},
		{"both constraints conflict", `,"request":{"reference":"Claim/convergence-claim","identifier":{"system":"urn:other","value":"wrong"}}`, false},
		{"trace conflicts", `,"item":[{"extension":[{"url":"` + pasExtItemTraceNumber + `","valueIdentifier":{"system":"urn:other","value":"wrong"}}]}]`, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := resumed.ValidateResponseLinkage([]byte(`{"resourceType":"ClaimResponse"` + row.fields + `}`))
			if (err == nil) != row.valid {
				t.Fatalf("valid=%v err=%v", row.valid, err)
			}
		})
	}
	before := append([]string(nil), resumed.ClaimReferences...)
	if err := resumed.Record([]byte(`{"resourceType":"ClaimResponse","request":{"reference":"https://payer.example/Claim/alien"},"preAuthRef":"AUTH-1861"}`)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed.ClaimReferences, before) {
		t.Fatalf("payer answer added request reference authority: %q", resumed.ClaimReferences)
	}
	legacy := PriorAuthContinuation{ClaimIdentifiers: resumed.ClaimIdentifiers}
	if err := legacy.ValidateResponseLinkage([]byte(`{"resourceType":"ClaimResponse","request":{"reference":"Claim/convergence-claim"}}`)); err == nil {
		t.Fatal("legacy continuation without sent refs accepted an unknown request URL")
	}
}

func TestPriorAuthContinuation_ExplicitRelatedClaimReference(t *testing.T) {
	in := conformantUpdateInputsFromGolden(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, err := BuildConformantClaimUpdateBundleAtLine("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	cont, err := NewPriorAuthContinuation("2.2", "payer", "MBR-COVERED", request, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Claim/convergence-claim-update",
		"https://shn.example/fhir/Claim/convergence-claim-update",
		"Claim/convergence-claim",
	}
	if !reflect.DeepEqual(cont.ClaimReferences, want) {
		t.Fatalf("amendment request references = %q, want %q", cont.ClaimReferences, want)
	}
	if err := cont.ValidateResponseLinkage([]byte(`{"resourceType":"ClaimResponse","request":{"reference":"Claim/convergence-claim"}}`)); err != nil {
		t.Fatalf("an explicitly sent related Claim reference was refused: %v", err)
	}
}

func TestClaimLinkage_IdentifierLinkedPriorWithoutEntryURL(t *testing.T) {
	in := conformantUpdateInputsFromGolden(t)
	in.PayerOrgEntry = true
	in.Insurer = testPayerOrganization(CMSPayerIdentity)
	request, err := BuildConformantClaimUpdateBundleAtLine("2.2", in)
	if err != nil {
		t.Fatal(err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(request, &bundle); err != nil {
		t.Fatal(err)
	}
	claims := 0
	for _, raw := range bundle["entry"].([]any) {
		entry := raw.(map[string]any)
		resource := entry["resource"].(map[string]any)
		if resource["resourceType"] != "Claim" {
			continue
		}
		claims++
		if claims == 1 {
			related := resource["related"].([]any)[0].(map[string]any)["claim"].(map[string]any)
			delete(related, "reference") // the already-supported identifier remains
		} else {
			delete(entry, "fullUrl") // later Claim has no address to select
		}
	}
	if claims != 2 {
		t.Fatalf("update fixture has %d Claims, want operative and prior", claims)
	}
	request, err = json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	cont, err := NewPriorAuthContinuation("2.2", "payer", "MBR-COVERED", request, nil)
	if err != nil {
		t.Fatalf("identifier-linked update continuation: %v", err)
	}
	if got := cont.ClaimReferences; !reflect.DeepEqual(got, []string{"Claim/convergence-claim-update", "https://shn.example/fhir/Claim/convergence-claim-update"}) {
		t.Fatalf("selected Claim references = %q", got)
	}
	prior := PASIdentifier{System: "urn:shn:correlation", Value: in.OriginalCorr}
	priorJSON, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	answer := []byte(`{"resourceType":"ClaimResponse","request":{"identifier":` + string(priorJSON) + `}}`)
	if err := ValidatePASResponseLinkage(request, answer); err != nil {
		t.Fatalf("immediate identifier linkage: %v", err)
	}
	if err := cont.ValidateResponseLinkage(answer); err != nil {
		t.Fatalf("continuation identifier linkage: %v", err)
	}
}

func TestClaimLinkage_IdentifierOnlySubmittedClaim(t *testing.T) {
	request, err := BuildConformantClaimBundle(conformantSubmitInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(request, &bundle); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range bundle["entry"].([]any) {
		entry := raw.(map[string]any)
		claim := entry["resource"].(map[string]any)
		if claim["resourceType"] != "Claim" {
			continue
		}
		delete(claim, "id")
		delete(entry, "fullUrl")
		found = true
		break
	}
	if !found {
		t.Fatal("submitted Bundle has no Claim")
	}
	request, err = json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	cont, err := NewPriorAuthContinuation("2.0", "payer", "MBR-COVERED", request, nil)
	if err != nil {
		t.Fatalf("identifier-only submitted Claim refused: %v", err)
	}
	if len(cont.ClaimReferences) != 0 || len(cont.ClaimIdentifiers) == 0 {
		t.Fatalf("identifier-only facts = %+v", cont)
	}
	encoded, err := json.Marshal(cont)
	if err != nil {
		t.Fatal(err)
	}
	var restored PriorAuthContinuation
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.ClaimReferences) != 0 || !reflect.DeepEqual(restored.ClaimIdentifiers, cont.ClaimIdentifiers) {
		t.Fatalf("identifier-only JSON roundtrip = %+v", restored)
	}
	idJSON, err := json.Marshal(cont.ClaimIdentifiers[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, requestField string
		valid              bool
	}{
		{"absent linkage", ``, true},
		{"matching identifier", `,"request":{"identifier":` + string(idJSON) + `}`, true},
		{"foreign identifier", `,"request":{"identifier":{"system":"urn:foreign","value":"wrong"}}`, false},
		{"unknown URL", `,"request":{"reference":"Claim/unasserted"}`, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			answer := []byte(`{"resourceType":"ClaimResponse"` + row.requestField + `}`)
			for name, check := range map[string]func() error{
				"immediate":    func() error { return ValidatePASResponseLinkage(request, answer) },
				"continuation": func() error { return restored.ValidateResponseLinkage(answer) },
			} {
				if err := check(); (err == nil) != row.valid {
					t.Fatalf("%s valid=%v err=%v", name, row.valid, err)
				}
			}
		})
	}
}

func TestSentClaimLinkage_OperativeFirstAndUnambiguous(t *testing.T) {
	for _, row := range []struct {
		name, request string
		valid         bool
	}{
		{"linked prior", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update","related":[{"claim":{"reference":"https://owner/fhir/Claim/prior"}}]}},{"fullUrl":"https://owner/fhir/Claim/prior","resource":{"resourceType":"Claim","id":"prior"}}]}`, true},
		{"unlinked extra does not change first", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update"}},{"fullUrl":"https://owner/fhir/Claim/prior","resource":{"resourceType":"Claim","id":"prior"}}]}`, true},
		{"later related is not operative", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/prior","resource":{"resourceType":"Claim","id":"prior"}},{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update","related":[{"claim":{"reference":"Claim/prior"}}]}}]}`, true},
		{"identifier linked prior without fullUrl", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update","related":[{"claim":{"identifier":{"system":"urn:shn:correlation","value":"prior"}}}]}},{"resource":{"resourceType":"Claim","id":"prior"}}]}`, true},
		{"duplicate id", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update","related":[{"claim":{"reference":"https://owner/fhir/Claim/prior"}}]}},{"fullUrl":"https://owner/fhir/Claim/prior","resource":{"resourceType":"Claim","id":"update"}}]}`, false},
		{"duplicate later identity does not change first", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update","related":[{"claim":{"reference":"Claim/prior"}}]}},{"fullUrl":"https://owner/fhir/Claim/prior","resource":{"resourceType":"Claim","id":"prior"}},{"fullUrl":"https://owner/fhir/Claim/prior","resource":{"resourceType":"Claim","id":"prior"}}]}`, true},
		{"duplicate selected fullUrl", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"update"}},{"fullUrl":"https://owner/fhir/Claim/update","resource":{"resourceType":"Claim","id":"prior"}}]}`, false},
		{"no reference identity", `{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim"}}]}`, true},
		{"oversized reference", `{"resourceType":"Bundle","entry":[{"fullUrl":"https://owner/fhir/Claim/` + strings.Repeat("a", 2049) + `","resource":{"resourceType":"Claim","id":"update"}}]}`, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			claim, refs, err := sentClaimLinkage([]byte(row.request))
			if (err == nil) != row.valid {
				t.Fatalf("valid=%v err=%v", row.valid, err)
			}
			if row.valid {
				var first struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(claim, &first); err != nil {
					t.Fatal(err)
				}
				if first.ID != "" && (len(refs) == 0 || refs[0] != "Claim/"+first.ID) || first.ID == "" && len(refs) != 0 {
					t.Fatalf("operative first Claim identity lost: %s refs=%q err=%v", claim, refs, err)
				}
			}
		})
	}
}

func TestSentClaimLinkage_ReferenceBounds(t *testing.T) {
	request := func(refs []string) []byte {
		related := make([]any, 0, len(refs))
		for _, ref := range refs {
			related = append(related, map[string]any{"claim": map[string]any{"reference": ref}})
		}
		bundle := map[string]any{"resourceType": "Bundle", "entry": []any{map[string]any{"resource": map[string]any{"resourceType": "Claim", "id": "c", "related": related}}}}
		b, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	check := func(name string, refs []string, wantError string) {
		t.Helper()
		_, got, err := sentClaimLinkage(request(refs))
		if wantError == "" && err != nil || wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError)) {
			t.Fatalf("%s: refs=%d returned %d, err=%v; want %q", name, len(refs), len(got), err, wantError)
		}
	}
	maxString := strings.Repeat("x", 2048)
	check("individual boundary", []string{maxString}, "")
	check("individual boundary plus one byte", []string{maxString + "x"}, "too large")
	countRefs := make([]string, 255) // plus Claim/c = 256 distinct retained refs
	for i := range countRefs {
		countRefs[i] = fmt.Sprintf("Claim/related-%03d", i)
	}
	check("count boundary", countRefs, "")
	check("count boundary plus one reference", append(append([]string(nil), countRefs...), "Claim/related-256"), "too many")
	totalRefs := make([]string, 32) // Claim/c is 7 bytes; 31*2048 + 2041 = 65536
	for i := 0; i < 31; i++ {
		totalRefs[i] = fmt.Sprintf("%03d", i) + strings.Repeat("x", 2045)
	}
	totalRefs[31] = "031" + strings.Repeat("x", 2038)
	check("total bytes boundary", totalRefs, "")
	totalRefs[31] += "x"
	check("total bytes boundary plus one", totalRefs, "too large")
}
