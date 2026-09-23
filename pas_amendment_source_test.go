package shnsdk

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FR-21: an amendment carries the participant's original Claim only on PAS
// lines whose request Bundle permits a second Claim entry.
func TestPASAmendmentUsesSubmittedPriorClaimAtSupportedLine(t *testing.T) {
	sub := conformantSubmitInputs(t)
	sub.Insurer = testPayerOrganization(CMSPayerIdentity)
	sub.PayerOrgEntry, sub.AbsoluteRefs = true, true
	original, err := BuildConformantClaimBundleAtLine("2.2", sub)
	if err != nil {
		t.Fatal(err)
	}
	prior := claimEntryForAmendmentTest(t, original, "convergence-claim")
	prior = bytes.Replace(prior, []byte(`"resourceType":"Claim"`),
		[]byte(`"resourceType" : "Claim", "extension" : [{"url":"https://example.org/source-proof","valueDecimal":9007199254740993.2300}]`), 1)
	if !bytes.Contains(prior, []byte(`9007199254740993.2300`)) {
		t.Fatal("failed to prepare exact-decimal source Claim")
	}
	update := conformantUpdateInputsFromGolden(t)
	update.OriginalCorr = sub.Corr
	update.Insurer = sub.Insurer
	update.PayerOrgEntry, update.AbsoluteRefs = true, true
	update.PriorClaim = prior
	got, err := BuildConformantClaimUpdateBundleAtLine("2.2", update)
	if err != nil {
		t.Fatal(err)
	}
	carried := claimEntryForAmendmentTest(t, got, "convergence-claim")
	if !bytes.Equal(prior, carried) {
		t.Fatalf("prior Claim bytes changed: original=%s carried=%s", prior, carried)
	}
}

func TestPAS20AmendmentHasOnlyOperativeClaim(t *testing.T) {
	update := conformantUpdateInputsFromGolden(t)
	update.PayerOrgEntry = true
	update.AbsoluteRefs = true
	update.Insurer = testPayerOrganization(CMSPayerIdentity)
	update.PriorClaim = submittedClaimForUpdateTest(t, "2.0", update.OriginalCorr)
	got, err := BuildConformantClaimUpdateBundleAtLine("2.0", update)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Entry []struct {
			Resource struct {
				ResourceType string `json:"resourceType"`
			} `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &b); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range b.Entry {
		if e.Resource.ResourceType == "Claim" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("PAS 2.0.1 Bundle.entry:Claim permits 1..1; got %d", count)
	}
}

func submittedClaimForUpdateTest(t *testing.T, line, corr string) []byte {
	t.Helper()
	sub := conformantSubmitInputs(t)
	sub.Corr = corr
	sub.Insurer = testPayerOrganization(CMSPayerIdentity)
	sub.PayerOrgEntry, sub.AbsoluteRefs = true, true
	bundle, err := BuildConformantClaimBundleAtLine(line, sub)
	if err != nil {
		t.Fatal(err)
	}
	return claimEntryForAmendmentTest(t, bundle, "convergence-claim")
}

func TestPASAmendmentRefusesMissingOrMismatchedPriorSource(t *testing.T) {
	update := conformantUpdateInputsFromGolden(t)
	update.PayerOrgEntry = true
	update.AbsoluteRefs = true
	update.Insurer = testPayerOrganization(CMSPayerIdentity)
	update.PriorClaim = nil
	if _, err := BuildConformantClaimUpdateBundleAtLine("2.2", update); err == nil {
		t.Fatal("missing original Claim source was fabricated")
	}
	update.PriorClaim = []byte(`{"resourceType":"Claim","id":"convergence-claim","identifier":[{"system":"urn:shn:correlation","value":"another-claim"}]}`)
	if _, err := BuildConformantClaimUpdateBundleAtLine("2.2", update); err == nil {
		t.Fatal("unrelated original Claim source was accepted")
	}
}

func TestPASAmendmentRequiresSourceOnSingleClaimLane(t *testing.T) {
	update := conformantUpdateInputsFromGolden(t)
	update.PayerOrgEntry = false
	update.PriorClaim = nil
	if _, err := BuildConformantClaimUpdateBundleAtLine("2.0", update); err == nil {
		t.Fatal("one-Claim amendment accepted a correlation without the original submitted Claim")
	}
	update.PriorClaim = submittedClaimForUpdateTest(t, "2.0", update.OriginalCorr)
	if _, err := BuildConformantClaimUpdateBundleAtLine("2.0", update); err != nil {
		t.Fatalf("one-Claim amendment refused source-bound prior identifier: %v", err)
	}
}

func TestPAS22AmendmentIncludesPriorWithoutPayerOrgFlag(t *testing.T) {
	update := conformantUpdateInputsFromGolden(t)
	update.PayerOrgEntry = false
	update.PriorClaim = submittedClaimForUpdateTest(t, "2.2", update.OriginalCorr)
	got, err := BuildConformantClaimUpdateBundleAtLine("2.2", update)
	if err != nil {
		t.Fatal(err)
	}
	prior := claimEntryForAmendmentTest(t, got, "convergence-claim")
	if !bytes.Equal(prior, update.PriorClaim) {
		t.Fatal("ordinary 2.2 amendment changed original Claim bytes")
	}
	var operative struct {
		Related []struct {
			Claim struct {
				Reference string `json:"reference"`
			} `json:"claim"`
		} `json:"related"`
	}
	if err := json.Unmarshal(claimEntryForAmendmentTest(t, got, "convergence-claim-update"), &operative); err != nil {
		t.Fatal(err)
	}
	if len(operative.Related) == 0 || operative.Related[0].Claim.Reference != "Claim/convergence-claim" {
		t.Fatalf("ordinary 2.2 amendment lacks source Claim reference: %+v", operative.Related)
	}
}

func TestSubmittedPASClaimReturnsOnlyActualSubmittedClaim(t *testing.T) {
	sub := conformantSubmitInputs(t)
	bundle, err := BuildConformantClaimBundleAtLine("2.0", sub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SubmittedPASClaim(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := claimEntryForAmendmentTest(t, bundle, "convergence-claim")
	if string(got) != string(want) {
		t.Fatalf("submitted Claim bytes changed: got %s want %s", got, want)
	}
	for _, bad := range [][]byte{
		[]byte(`{"resourceType":"Bundle","entry":[]}`),
		[]byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Claim","id":"a"}},{"resource":{"resourceType":"Claim","id":"b"}}]}`),
		[]byte(`{"resourceType":"Parameters","entry":[{"resource":{"resourceType":"Claim","id":"a"}}]}`),
	} {
		if _, err := SubmittedPASClaim(bad); err == nil {
			t.Fatalf("accepted ambiguous or non-PAS source %s", bad)
		}
	}
}

func claimEntryForAmendmentTest(t *testing.T, bundle []byte, id string) []byte {
	t.Helper()
	var b struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	for _, e := range b.Entry {
		var h struct{ ResourceType, ID string }
		if err := json.Unmarshal(e.Resource, &h); err != nil {
			t.Fatal(err)
		}
		if h.ResourceType == "Claim" && h.ID == id {
			return e.Resource
		}
	}
	t.Fatalf("missing Claim/%s", id)
	return nil
}

func jsonEqualForAmendmentTest(a, b any) bool {
	aRaw, _ := json.Marshal(a)
	bRaw, _ := json.Marshal(b)
	return string(aRaw) == string(bRaw)
}
