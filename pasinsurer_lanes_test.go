package shnsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ownPayerRecord is a participant's own Organization record for its member's
// payer: its own id (the one its Coverage names as payor) and name, and the
// payer identity the leg is routed on. The name is deliberately not the one
// a builder could make up, so a made-up payer is told apart from this one.
const ownPayerRecord = `{"resourceType":"Organization","id":"org-cms-payer",` +
	`"identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00001"}],"name":"Member Payer Of Record"}`

// pasLane is one combination of the lane flags a caller may still set. From
// shn-sdk v0.59.0 none of them changes whose payer the request names.
type pasLane struct{ payerOrgEntry, containedInsurer, absoluteRefs bool }

func (l pasLane) String() string {
	return fmt.Sprintf("PayerOrgEntry=%t,ContainedInsurer=%t,AbsoluteRefs=%t", l.payerOrgEntry, l.containedInsurer, l.absoluteRefs)
}

func everyPASLane() []pasLane {
	var out []pasLane
	for _, e := range []bool{false, true} {
		for _, c := range []bool{false, true} {
			for _, a := range []bool{false, true} {
				out = append(out, pasLane{e, c, a})
			}
		}
	}
	return out
}

// buildOnLane builds the submit (update=false) or the amendment on lane with
// insurer as the caller's payer record.
func buildOnLane(t *testing.T, update bool, lane pasLane, insurer []byte, payer PayerIdentifier) ([]byte, error) {
	t.Helper()
	if update {
		in := conformantUpdateInputsFromGolden(t)
		in.PayerOrgEntry, in.ContainedInsurer, in.AbsoluteRefs = lane.payerOrgEntry, lane.containedInsurer, lane.absoluteRefs
		in.Insurer, in.Payer = insurer, payer
		return BuildConformantClaimUpdateBundle(in)
	}
	in := conformantSubmitInputs(t)
	in.PayerOrgEntry, in.ContainedInsurer, in.AbsoluteRefs = lane.payerOrgEntry, lane.containedInsurer, lane.absoluteRefs
	in.Insurer, in.Payer = insurer, payer
	return BuildConformantClaimBundle(in)
}

// TestConformantClaimBuilders_RefuseWithoutThePayersOwnRecordOnEveryLane: a
// prior-authorization request names the payer, and from shn-sdk v0.59.0 the
// only payer it can name is the participant's own record. On every lane, a
// caller with no such record, or with one for another payer than the leg is
// routed to, is refused and gets no request.
func TestConformantClaimBuilders_RefuseWithoutThePayersOwnRecordOnEveryLane(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, lane := range everyPASLane() {
			for name, row := range map[string]struct {
				insurer []byte
				want    string
			}{
				"no record":    {nil, "Organization record is required"},
				"empty record": {[]byte(" "), "Organization record is required"},
				"another payer's record": {[]byte(`{"resourceType":"Organization","id":"org-other",` +
					`"identifier":[{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00078"}]}`), "names two"},
			} {
				t.Run(fmt.Sprintf("update=%t/%s/%s", update, lane, name), func(t *testing.T) {
					out, err := buildOnLane(t, update, lane, row.insurer, CMSPayerIdentity)
					if out != nil || err == nil || !strings.Contains(err.Error(), row.want) {
						t.Fatalf("out=%s err=%v; want no request and a refusal saying %q", out, err, row.want)
					}
				})
			}
		}
	}
}

// TestConformantClaimBuilders_CarryThePayersOwnRecordOnEveryLane: on every
// lane the caller's own payer record rides the request byte for byte as one
// entry, and the Claim's insurer and the Coverage's payor both name that
// entry. Nothing names a payer the caller did not supply: no contained payer
// Organization, no Organization the builder made up, and no reference to
// an Organization the request does not carry.
func TestConformantClaimBuilders_CarryThePayersOwnRecordOnEveryLane(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, lane := range everyPASLane() {
			t.Run(fmt.Sprintf("update=%t/%s", update, lane), func(t *testing.T) {
				out, err := buildOnLane(t, update, lane, []byte(ownPayerRecord), CMSPayerIdentity)
				if err != nil {
					t.Fatalf("build: %v", err)
				}
				assertNamesOnlyTheOwnPayer(t, out, []byte(ownPayerRecord), "org-cms-payer")
			})
		}
	}
}

// assertNamesOnlyTheOwnPayer checks a built request against the caller's own
// payer record (compact, without meta.profile, so it travels unchanged).
func assertNamesOnlyTheOwnPayer(t *testing.T, bundle, record []byte, id string) {
	t.Helper()
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	entryURL, carried := "", 0
	var insurers, payors []string
	for _, e := range b.Entry {
		var r struct {
			ResourceType string            `json:"resourceType"`
			ID           string            `json:"id"`
			Contained    []json.RawMessage `json:"contained"`
			Insurer      struct {
				Reference string `json:"reference"`
			} `json:"insurer"`
			Payor []struct {
				Reference string `json:"reference"`
			} `json:"payor"`
		}
		if err := json.Unmarshal(e.Resource, &r); err != nil {
			t.Fatal(err)
		}
		for _, c := range r.Contained {
			var head struct{ ResourceType string }
			if json.Unmarshal(c, &head) == nil && head.ResourceType == "Organization" {
				t.Errorf("%s/%s contains an Organization: %s", r.ResourceType, r.ID, c)
			}
		}
		switch r.ResourceType {
		case "Organization":
			if r.ID == id {
				carried++
				entryURL = e.FullURL
				if !bytes.Equal(e.Resource, record) {
					t.Errorf("the payer entry is not the caller's record as supplied:\n got %s\nwant %s", e.Resource, record)
				}
			}
		case "Claim":
			if r.Insurer.Reference != "" {
				insurers = append(insurers, r.Insurer.Reference)
			}
		case "Coverage":
			for _, p := range r.Payor {
				payors = append(payors, p.Reference)
			}
		}
	}
	if carried != 1 {
		t.Fatalf("the request carries the caller's payer record %d times, want once: %s", carried, bundle)
	}
	names := func(ref string) bool { return ref == entryURL || ref == "Organization/"+id }
	if len(insurers) == 0 || len(payors) == 0 {
		t.Fatalf("insurers %v, payors %v: want every Claim and the Coverage to name the payer: %s", insurers, payors, bundle)
	}
	for _, ref := range append(insurers, payors...) {
		if !names(ref) {
			t.Errorf("a Claim or the Coverage names %q, not the caller's payer entry %q", ref, entryURL)
		}
	}
	for _, minted := range []string{`"id":"cms-payer"`, `Centers for Medicare and Medicaid Services`, `"Organization/payer"`, `#cms-payer`} {
		if bytes.Contains(bundle, []byte(minted)) {
			t.Errorf("the request carries %s, which the caller did not supply: %s", minted, bundle)
		}
	}
	if err := checkPASInsurerResolves(bundle); err != nil {
		t.Errorf("checkPASInsurerResolves: %v", err)
	}
}

// TestConformantClaimBuilders_DeprecatedLaneFlagsChangeNothing: from shn-sdk
// v0.59.0 PayerOrgEntry and ContainedInsurer are no-ops. Whatever they are
// set to, a submission and an amendment are byte for byte the request built
// with both unset — the payer as the caller's own entry and, on an amendment,
// the prior Claim as the entry its related claim names.
func TestConformantClaimBuilders_DeprecatedLaneFlagsChangeNothing(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, absolute := range []bool{false, true} {
			base, err := buildOnLane(t, update, pasLane{absoluteRefs: absolute}, []byte(ownPayerRecord), CMSPayerIdentity)
			if err != nil {
				t.Fatal(err)
			}
			for _, lane := range everyPASLane() {
				if lane.absoluteRefs != absolute {
					continue
				}
				got, err := buildOnLane(t, update, lane, []byte(ownPayerRecord), CMSPayerIdentity)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, base) {
					t.Errorf("update=%t %s: the request differs from the one built with neither flag set:\n got %s\nwant %s", update, lane, got, base)
				}
			}
			if update {
				assertAmendsItsPriorClaim(t, base)
			}
		}
	}
}

// assertAmendsItsPriorClaim checks an amendment carries two Claims: the
// operative one first, whose related claim names the second, the prior Claim.
func assertAmendsItsPriorClaim(t *testing.T, bundle []byte) {
	t.Helper()
	var b struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		t.Fatal(err)
	}
	type claim struct {
		url, id, related string
	}
	var claims []claim
	for _, e := range b.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Related      []struct {
				Claim struct {
					Reference string `json:"reference"`
				} `json:"claim"`
			} `json:"related"`
		}
		if json.Unmarshal(e.Resource, &r) != nil || r.ResourceType != "Claim" {
			continue
		}
		c := claim{url: e.FullURL, id: r.ID}
		if len(r.Related) > 0 {
			c.related = r.Related[0].Claim.Reference
		}
		claims = append(claims, c)
	}
	if len(claims) != 2 {
		t.Fatalf("the amendment carries %d Claims, want the operative Claim and the prior Claim it amends: %s", len(claims), bundle)
	}
	if prior := claims[1]; claims[0].related != prior.url && claims[0].related != "Claim/"+prior.id {
		t.Errorf("the operative Claim's related claim is %q, not the prior Claim entry %q", claims[0].related, prior.url)
	}
}

// TestConformantClaimBundle_KeepsAContainedOrganizationThatIsNotThePayerEntry:
// only the contained copy of the payer entry itself is dropped when payor
// names the entry. A contained Organization under another id — the "cms-payer"
// id earlier releases dropped on sight — is part of the caller's record, so it
// stays, and a record whose contained Organization nothing names is refused
// (FHIR dom-3) instead of being quietly edited.
func TestConformantClaimBundle_KeepsAContainedOrganizationThatIsNotThePayerEntry(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.Coverage = testMemberCoverageWithContainedPayer("MBR-COVERED", "cms-payer", CMSPayerIdentity)
	in.Insurer = []byte(ownPayerRecord)
	out, err := BuildConformantClaimBundle(in)
	if out != nil || err == nil || !strings.Contains(err.Error(), "cms-payer") {
		t.Fatalf("out=%s err=%v; want a refusal naming the contained record nothing references", out, err)
	}
}

// TestConformantClaimBuilders_AbsoluteRefsCarryNumbersAsWritten: absolute
// references re-encode a resource whose references they rewrite (the
// Coverage, whose payor names the payer entry), and the numbers in it are
// carried as the caller wrote them — a decimal 10.50 stays 10.50.
func TestConformantClaimBuilders_AbsoluteRefsCarryNumbersAsWritten(t *testing.T) {
	const ext = `"extension":[{"url":"http://example.org/fhir/StructureDefinition/copay","valueDecimal":10.50}]`
	for _, update := range []bool{false, true} {
		for _, absolute := range []bool{false, true} {
			var out []byte
			var err error
			coverage := bytes.Replace(testMemberCoverage("MBR-COVERED"), []byte(`{"resourceType":"Coverage",`), []byte(`{"resourceType":"Coverage",`+ext+`,`), 1)
			if update {
				in := conformantUpdateInputsFromGolden(t)
				in.Coverage, in.AbsoluteRefs = coverage, absolute
				out, err = BuildConformantClaimUpdateBundle(in)
			} else {
				in := conformantSubmitInputs(t)
				in.Coverage, in.AbsoluteRefs = coverage, absolute
				out, err = BuildConformantClaimBundle(in)
			}
			if err != nil {
				t.Fatalf("update=%t absolute=%t: %v", update, absolute, err)
			}
			if absolute && !bytes.Contains(out, []byte(`"payor":[{"reference":"`+pasBundleBaseURL+`/Organization/`)) {
				t.Fatalf("update=%t: the Coverage's payor was not rewritten, so this row proves nothing: %s", update, out)
			}
			if !bytes.Contains(out, []byte(`"valueDecimal":10.50`)) {
				t.Errorf("update=%t absolute=%t: the Coverage's 10.50 was not carried as written: %s", update, absolute, out)
			}
		}
	}
}
