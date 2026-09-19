package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
)

// testMemberCoverageWithContainedPayer is the OTHER shape a participant's own
// Coverage comes in, and the one the seeded reference systems actually hold: the
// payer organization travels INSIDE the coverage, and payor names it locally.
//
// A reader that is handed only the Coverage's own bytes has nowhere else to look,
// so this is the shape a system of record uses when it holds no separately
// addressable payer Organization — every persona the reference provider tenant
// seeds, including the bridging and Cambia members whose payer is their own.
// The fixtures beside it name the payer by an external reference; both shapes
// reach these builders, and only one of them used to come out valid.
func testMemberCoverageWithContainedPayer(member, orgID string, payer PayerIdentifier) []byte {
	return []byte(`{"resourceType":"Coverage","id":"cov-` + strings.ToLower(member) + `","status":"active",` +
		`"identifier":[{"type":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v2-0203","code":"MB"}]},` +
		`"system":"urn:shn:coverage","value":"` + member + `"}],` +
		`"beneficiary":{"reference":"Patient/` + member + `"},` +
		`"relationship":{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/subscriber-relationship","code":"self"}]},` +
		`"payor":[{"reference":"#` + orgID + `"}],` +
		`"contained":[{"resourceType":"Organization","id":"` + orgID + `",` +
		`"identifier":[{"system":"` + payer.System + `","value":"` + payer.Value + `"}],` +
		`"name":"The Member's Own Payer"}]}`)
}

// bridgingPayer is a demo-namespaced payer identity — one whose organization id
// is NOT the minted conformantPayerOrgID. The distinction is the whole bug: the
// drop that ran when a re-point displaced a contained payer organization was
// keyed on the minted id, so a participant whose own record used any other id
// kept a contained organization nothing pointed at.
var bridgingPayer = PayerIdentifier{System: "urn:shn:demo-payer", Value: "SHN-BRIDGE-DEMO"}

// strandedContained reads dom-3 off assembled bytes INDEPENDENTLY of the guard —
// the regression rows below assert the property the validator checks, not the
// guard's opinion of it, so they stay meaningful if the guard is ever wrong.
func strandedContained(t *testing.T, bundleJSON []byte) []string {
	t.Helper()
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		t.Fatalf("read the authored request: %v", err)
	}
	var stranded []string
	for _, e := range bundle.Entry {
		var r struct {
			ResourceType string            `json:"resourceType"`
			ID           string            `json:"id"`
			Contained    []json.RawMessage `json:"contained"`
		}
		if json.Unmarshal(e.Resource, &r) != nil {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(e.Resource, &m) != nil {
			continue
		}
		delete(m, "contained")
		outer, err := json.Marshal(m)
		if err != nil {
			continue
		}
		for _, c := range r.Contained {
			var head struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(c, &head) != nil || head.ID == "" {
				continue
			}
			if !strings.Contains(string(outer), `"#`+head.ID+`"`) {
				stranded = append(stranded, r.ResourceType+"/"+r.ID+" contains "+head.ID)
			}
		}
	}
	return stranded
}

// TestBuildConformantClaimBundle_ContainedPayerOrgNotStranded is the regression
// row for the deployed egress refusal "The contained resource
// 'bridge-demo-payer-org' is not referenced to from elsewhere": a member whose own
// Coverage CONTAINS its payer organization, authored on the lane that carries the
// payer as a resolvable entry. The contained copy must not survive the re-point
// that stopped naming it, and the record itself must still ride the request.
func TestBuildConformantClaimBundle_ContainedPayerOrgNotStranded(t *testing.T) {
	const orgID = "bridge-demo-payer-org"
	in := conformantSubmitInputs(t)
	in.Coverage = testMemberCoverageWithContainedPayer("MBR-BRIDGE-DEMO", orgID, bridgingPayer)
	in.Insurer = []byte(`{"resourceType":"Organization","id":"` + orgID + `",` +
		`"identifier":[{"system":"` + bridgingPayer.System + `","value":"` + bridgingPayer.Value + `"}],` +
		`"name":"The Member's Own Payer"}`)
	in.Payer = bridgingPayer
	in.PayerOrgEntry, in.ContainedInsurer, in.AbsoluteRefs = true, true, true

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	if stranded := strandedContained(t, got); len(stranded) != 0 {
		t.Errorf("the authored request strands %v — a contained resource referenced from nowhere is FHIR dom-3, which the egress validator refuses: %s", stranded, got)
	}
	// Dropping the contained copy is only correct because the same record rides
	// the request as the entry payor and insurer now both name.
	if err := checkPASInsurerResolves(got); err != nil {
		t.Errorf("the payer organization must still ride the request: %v", err)
	}
	if !strings.Contains(string(got), bridgingPayer.Value) {
		t.Errorf("the request must still name the payer identity the member's coverage asserted: %s", got)
	}
}

// TestBuildConformantClaimUpdateBundle_ContainedPayerOrgNotStranded is the same
// row on the amendment builder: an amendment is made under the same policy and
// carries the same coverage record, so it strands the same organization.
func TestBuildConformantClaimUpdateBundle_ContainedPayerOrgNotStranded(t *testing.T) {
	const orgID = "bridge-demo-payer-org"
	in := conformantUpdateInputsFromGolden(t)
	in.Coverage = testMemberCoverageWithContainedPayer("MBR-BRIDGE-DEMO", orgID, bridgingPayer)
	in.Insurer = []byte(`{"resourceType":"Organization","id":"` + orgID + `",` +
		`"identifier":[{"system":"` + bridgingPayer.System + `","value":"` + bridgingPayer.Value + `"}],` +
		`"name":"The Member's Own Payer"}`)
	in.Payer = bridgingPayer
	in.PayerOrgEntry, in.ContainedInsurer, in.AbsoluteRefs = true, true, true

	got, err := BuildConformantClaimUpdateBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle: %v", err)
	}
	if stranded := strandedContained(t, got); len(stranded) != 0 {
		t.Errorf("the authored amendment strands %v: %s", stranded, got)
	}
}

// TestCheckPASContainedReferenced is the guard's own control-and-rejection
// matrix, asserted AT the guard: a passing row proves the control is genuinely
// valid, and each rejection row is that same control with exactly one mutation.
func TestCheckPASContainedReferenced(t *testing.T) {
	bundleWith := func(resources ...string) []byte {
		entries := make([]string, 0, len(resources))
		for _, r := range resources {
			entries = append(entries, `{"fullUrl":"https://shn.example/fhir/x","resource":`+r+`}`)
		}
		return []byte(`{"resourceType":"Bundle","type":"collection","entry":[` + strings.Join(entries, ",") + `]}`)
	}
	const containedOrg = `{"resourceType":"Organization","id":"payer-org","name":"P"}`

	for _, tc := range []struct {
		name   string
		bundle []byte
		reject string // the record the refusal must name; "" = the control, which must pass
	}{
		{
			name: "control: the payor names the organization the coverage contains",
			bundle: bundleWith(`{"resourceType":"Coverage","id":"c1","payor":[{"reference":"#payer-org"}],` +
				`"contained":[` + containedOrg + `]}`),
		},
		{
			name: "control: a contained resource named from a SIBLING contained resource",
			bundle: bundleWith(`{"resourceType":"Claim","id":"cl1","insurer":{"reference":"#insurer"},` +
				`"contained":[{"resourceType":"Organization","id":"insurer","partOf":{"reference":"#parent"}},` +
				`{"resourceType":"Organization","id":"parent"}]}`),
		},
		{
			name:   "control: no contained resources at all",
			bundle: bundleWith(`{"resourceType":"Claim","id":"cl1","insurer":{"reference":"Organization/payer-org"}}`),
		},
		{
			name: "the payor is re-pointed at the entry and the contained copy is left behind",
			bundle: bundleWith(`{"resourceType":"Coverage","id":"c1","payor":[{"reference":"Organization/payer-org"}],` +
				`"contained":[` + containedOrg + `]}`),
			reject: "payer-org",
		},
		{
			name: "the claim's insurer is re-pointed and the contained copy is left behind",
			bundle: bundleWith(`{"resourceType":"Claim","id":"cl1","insurer":{"reference":"Organization/payer-org"},` +
				`"contained":[` + containedOrg + `]}`),
			reject: "payer-org",
		},
		{
			name: "a contained resource that refers only to ITSELF is not its own referrer",
			bundle: bundleWith(`{"resourceType":"Coverage","id":"c1","payor":[{"reference":"Organization/payer-org"}],` +
				`"contained":[{"resourceType":"Organization","id":"payer-org","partOf":{"reference":"#payer-org"}}]}`),
			reject: "payer-org",
		},
		{
			name:   "the reference that named it is gone from the resource entirely",
			bundle: bundleWith(`{"resourceType":"Coverage","id":"c1","contained":[` + containedOrg + `]}`),
			reject: "payer-org",
		},
		{
			// The scope boundary, asserted rather than assumed: a caller's own
			// QuestionnaireResponse carries the qr-context records a DTR fill put
			// there, and whether those are referenced is a fact about the record
			// handed in, not about anything this builder re-pointed. Egress
			// $validate answers that one; this row exists so the boundary is
			// visible instead of looking like coverage.
			name: "out of scope: a questionnaire response's own contained records are not this guard's business",
			bundle: bundleWith(`{"resourceType":"QuestionnaireResponse","id":"qr1","status":"completed",` +
				`"contained":[{"resourceType":"Coverage","id":"qr-context-coverage"}]}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPASContainedReferenced(tc.bundle)
			if tc.reject == "" {
				if err != nil {
					t.Fatalf("control must pass, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want a refusal naming %q, got nil", tc.reject)
			}
			if !strings.Contains(err.Error(), tc.reject) {
				t.Errorf("the refusal must name the stranded record %q, got: %v", tc.reject, err)
			}
		})
	}
}

// TestDropContainedPayerOrg_KeepsEveryOtherContainedRecord proves the drop is
// exactly one record wide: the organization the re-point displaced goes, and
// anything else the participant's record contained travels on untouched. A drop
// that swept the array would silently delete what the record asserted.
func TestDropContainedPayerOrg_KeepsEveryOtherContainedRecord(t *testing.T) {
	coverage := []byte(`{"resourceType":"Coverage","id":"c1","payor":[{"reference":"#payer-org"}],` +
		`"extension":[{"url":"urn:example:sponsor","valueReference":{"reference":"#sponsor"}}],` +
		`"contained":[{"resourceType":"Organization","id":"payer-org","name":"P"},` +
		`{"resourceType":"Organization","id":"sponsor","name":"S"}]}`)
	out, err := repointPayorToEntry(coverage, "payer-org")
	if err != nil {
		t.Fatalf("repointPayorToEntry: %v", err)
	}
	var m struct {
		Contained []struct {
			ID string `json:"id"`
		} `json:"contained"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("parse the re-pointed coverage: %v", err)
	}
	if len(m.Contained) != 1 || m.Contained[0].ID != "sponsor" {
		t.Fatalf("contained = %+v, want only the record the re-point did not displace (sponsor)", m.Contained)
	}
}

// TestDropContainedPayerOrg_KeepsARecordSomethingStillNames proves the drop is
// conditioned on the displacement having actually happened: a contained record
// the resource STILL names by its local id stays, even when its id is the one the
// entry carries. Dropping it would leave the reference that names it pointing at
// nothing — the mirror image of the stranding this change exists to stop.
func TestDropContainedPayerOrg_KeepsARecordSomethingStillNames(t *testing.T) {
	m := map[string]json.RawMessage{
		"resourceType": json.RawMessage(`"Coverage"`),
		"extension":    json.RawMessage(`[{"url":"urn:example:administered-by","valueReference":{"reference":"#payer-org"}}]`),
		"contained":    json.RawMessage(`[{"resourceType":"Organization","id":"payer-org","name":"P"}]`),
	}
	if err := dropContainedPayerOrg(m, "payer-org"); err != nil {
		t.Fatalf("dropContainedPayerOrg: %v", err)
	}
	if !strings.Contains(string(m["contained"]), "payer-org") {
		t.Fatalf("contained = %s, want the record the resource still names to survive", m["contained"])
	}
}
