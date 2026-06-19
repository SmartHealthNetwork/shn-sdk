package shnsdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildClaimBundle_Shape verifies the PAS claim Bundle is a collection Bundle
// with exactly three entries (Claim+QR+SR), the bundle identifier/timestamp stamped
// from the inputs + injected clock, and each entry carrying a resolvable absolute
// fullUrl consistent with its resource id (FHIR bdl-7).
func TestBuildClaimBundle_Shape(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed"}`)
	sr := []byte(`{"resourceType":"ServiceRequest","status":"active"}`)
	created := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

	b, err := BuildClaimBundle(qr, sr, "Patient/MBR-COVERED", "Coverage/MBR-COVERED", "corr-1", created)
	if err != nil {
		t.Fatalf("BuildClaimBundle: %v", err)
	}

	var got struct {
		ResourceType string `json:"resourceType"`
		Type         string `json:"type"`
		Identifier   struct {
			System string `json:"system"`
			Value  string `json:"value"`
		} `json:"identifier"`
		Timestamp string `json:"timestamp"`
		Entry     []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ResourceType != "Bundle" || got.Type != "collection" {
		t.Errorf("resourceType/type = %q/%q, want Bundle/collection", got.ResourceType, got.Type)
	}
	if got.Identifier.System != "urn:shn:pas:bundle" || got.Identifier.Value != "corr-1" {
		t.Errorf("identifier = %q/%q, want urn:shn:pas:bundle/corr-1", got.Identifier.System, got.Identifier.Value)
	}
	if got.Timestamp != "2026-06-04T00:00:00Z" {
		t.Errorf("timestamp = %q, want 2026-06-04T00:00:00Z", got.Timestamp)
	}
	if len(got.Entry) != 3 {
		t.Fatalf("entry count = %d, want 3 (Claim+QR+SR)", len(got.Entry))
	}
	// Each entry's fullUrl resolves to its resource's resourceType/id.
	for i, e := range got.Entry {
		var meta struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if err := json.Unmarshal(e.Resource, &meta); err != nil {
			t.Fatalf("entry[%d] resource parse: %v", i, err)
		}
		want := "https://shn.example/fhir/" + meta.ResourceType + "/" + meta.ID
		if e.FullURL != want {
			t.Errorf("entry[%d] fullUrl = %q, want %q", i, e.FullURL, want)
		}
	}
	// The first entry is the Claim (use preauthorization, patient INSIDE the Claim).
	var claim struct {
		ResourceType string `json:"resourceType"`
		Use          string `json:"use"`
		Patient      struct {
			Reference string `json:"reference"`
		} `json:"patient"`
	}
	if err := json.Unmarshal(got.Entry[0].Resource, &claim); err != nil {
		t.Fatalf("claim parse: %v", err)
	}
	if claim.ResourceType != "Claim" || claim.Use != "preauthorization" {
		t.Errorf("entry[0] = %q/%q, want Claim/preauthorization", claim.ResourceType, claim.Use)
	}
	if claim.Patient.Reference != "Patient/MBR-COVERED" {
		t.Errorf("claim.patient = %q, want Patient/MBR-COVERED", claim.Patient.Reference)
	}
}

// TestBuildClaimBundle_Deterministic proves the bundle is byte-stable under a fixed
// clock + inputs (the property the golden + byte-parity rely on).
func TestBuildClaimBundle_Deterministic(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed"}`)
	sr := []byte(`{"resourceType":"ServiceRequest","status":"active"}`)
	created := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)

	a, err := BuildClaimBundle(qr, sr, "Patient/MBR-COVERED", "Coverage/MBR-COVERED", "corr-1", created)
	if err != nil {
		t.Fatalf("BuildClaimBundle a: %v", err)
	}
	b, err := BuildClaimBundle(qr, sr, "Patient/MBR-COVERED", "Coverage/MBR-COVERED", "corr-1", created)
	if err != nil {
		t.Fatalf("BuildClaimBundle b: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("BuildClaimBundle not deterministic:\n a=%s\n b=%s", a, b)
	}
}

// TestParseClaimResponse_Approved: an approved ClaimResponse (outcome complete +
// preAuthRef) parses to Outcome "approved" with the preAuthRef + validUntil.
func TestParseClaimResponse_Approved(t *testing.T) {
	cr := []byte(`{"resourceType":"ClaimResponse","outcome":"complete","use":"preauthorization","preAuthRef":"PA-0123456789ab","preAuthPeriod":{"end":"2026-09-02"}}`)
	res, err := ParseClaimResponse(cr)
	if err != nil {
		t.Fatalf("ParseClaimResponse: %v", err)
	}
	if res.Outcome != "approved" {
		t.Errorf("Outcome = %q, want approved", res.Outcome)
	}
	if res.PreAuthRef != "PA-0123456789ab" {
		t.Errorf("PreAuthRef = %q, want PA-0123456789ab", res.PreAuthRef)
	}
	if res.ValidUntil != "2026-09-02" {
		t.Errorf("ValidUntil = %q, want 2026-09-02", res.ValidUntil)
	}
}

// TestParseClaimResponse_DeniedWithNumberStaysDenied is the rejection row for the
// reviewAction `number` preAuthRef fallback (added for real Da Vinci RIs like br-payer
// that carry the auth number there rather than in top-level preAuthRef): an A3 denial
// that ALSO carries a `number` sub-extension must STILL read as denied — the number
// must never flip a denial to approved (the A3 branch returns before the approved gate).
func TestParseClaimResponse_DeniedWithNumberStaysDenied(t *testing.T) {
	cr := []byte(`{"resourceType":"ClaimResponse","outcome":"complete","use":"preauthorization","item":[{"adjudication":[{"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A3"}]}},{"url":"number","valueString":"SHOULD-NOT-BECOME-PREAUTHREF"}]}]}]}]}`)
	res, err := ParseClaimResponse(cr)
	if err != nil {
		t.Fatalf("ParseClaimResponse: %v", err)
	}
	if res.Outcome != "denied" {
		t.Fatalf("Outcome = %q, want denied (A3 must win over the number fallback)", res.Outcome)
	}
	if res.PreAuthRef != "" {
		t.Errorf("PreAuthRef = %q, want empty (a denial issues no auth number)", res.PreAuthRef)
	}
}

// TestParseClaimResponse_NonApproved: an AMBIGUOUS bare ClaimResponse — one that is
// neither approved (no preAuthRef) nor explicitly denied (no reviewActionCode A3) —
// fails loud with an error, NOT a wrong Outcome. Absence of a preAuthRef alone is not
// enough to conclude a denial (denial is keyed on the explicit A3 signal); a real
// denial carries A3 and is asserted by the parity + vector tests.
func TestParseClaimResponse_NonApproved(t *testing.T) {
	cases := map[string][]byte{
		"ambiguous (outcome queued)":             []byte(`{"resourceType":"ClaimResponse","outcome":"queued","use":"preauthorization"}`),
		"ambiguous (complete, no preAuthRef/A3)": []byte(`{"resourceType":"ClaimResponse","outcome":"complete","use":"preauthorization","disposition":"not medically necessary"}`),
	}
	for name, cr := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := ParseClaimResponse(cr)
			if err == nil {
				t.Fatalf("ParseClaimResponse(%s) = %+v, want explicit-signal-boundary error", name, res)
			}
			if res.Outcome != "" {
				t.Errorf("Outcome = %q on error, want empty (no wrong outcome)", res.Outcome)
			}
		})
	}
}

func TestParsePASOutcomeDispatch(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "vectors", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return b
	}
	// Pended Bundle → Outcome "pended" + NeededItems (Resume filled by the caller, not here).
	res, err := parsePASOutcome(read("claimresponse-pended.json"))
	if err != nil {
		t.Fatalf("parsePASOutcome(pended): %v", err)
	}
	if res.Outcome != "pended" || len(res.NeededItems) == 0 {
		t.Fatalf("pended dispatch = %+v, want Outcome pended + NeededItems", res)
	}
	// Denied bare ClaimResponse → Outcome "denied".
	res, err = parsePASOutcome(read("claimresponse-denied-uc08.json"))
	if err != nil {
		t.Fatalf("parsePASOutcome(denied): %v", err)
	}
	if res.Outcome != "denied" || res.Denial == nil {
		t.Fatalf("denied dispatch = %+v, want Outcome denied + Denial", res)
	}
	// Approved bare ClaimResponse → Outcome "approved".
	res, err = parsePASOutcome(read("claimresponse-approved.json"))
	if err != nil {
		t.Fatalf("parsePASOutcome(approved): %v", err)
	}
	if res.Outcome != "approved" || res.PreAuthRef == "" {
		t.Fatalf("approved dispatch = %+v, want Outcome approved + PreAuthRef", res)
	}
}
