package shnsdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/samply/golang-fhir-models/fhir-models/fhir"
)

// TestBuildPADecisionEOB_DisplayFromParam (DEF-14, FR-28): the EOB's
// productOrService display is whatever CPTDisplay the caller passes (sourced from the
// request's ServiceRequest), NOT a hardcoded persona string. Guards against a
// regression to the old fixed "MRI lumbar spine w/o contrast" literal.
func TestBuildPADecisionEOB_DisplayFromParam(t *testing.T) {
	p := eobBaseParams()
	p.CPTCode, p.CPTDisplay = "29881", "Arthroscopy, knee, surgical, with meniscectomy"
	p.PreAuthRefs = []string{"A1"}
	b, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatalf("BuildPADecisionEOBExact: %v", err)
	}
	if bytes.Contains(b, []byte("MRI lumbar spine w/o contrast")) {
		t.Fatal("DEF-14 regression: builder still emits the hardcoded lumbar display")
	}
	if !bytes.Contains(b, []byte("Arthroscopy, knee, surgical, with meniscectomy")) {
		t.Fatal("builder must emit the passed CPTDisplay")
	}
}

// TestBuildEligibilityRequest_Shape checks the CoverageEligibilityRequest the SDK
// emits has the field shapes the substrate expects (resourceType, status, purpose,
// patient/provider/insurer references, created). This is the hermetic structural
// guard; wire-interop with the substrate parser is proven in test/sdkparity.
func TestBuildEligibilityRequest_Shape(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	b, err := BuildEligibilityRequestExact("MBR-COVERED", "9999999999", PayerIdentifier{System: CMSPayerIdentity.System, Value: "00402"}, now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequestExact: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal CER: %v", err)
	}
	if m["resourceType"] != "CoverageEligibilityRequest" {
		t.Errorf("resourceType = %v, want CoverageEligibilityRequest", m["resourceType"])
	}
	if m["status"] != "active" {
		t.Errorf("status = %v, want active", m["status"])
	}
	if m["created"] != "2026-06-03T00:00:00Z" {
		t.Errorf("created = %v, want 2026-06-03T00:00:00Z", m["created"])
	}
	pat, _ := m["patient"].(map[string]any)
	if pat["reference"] != "Patient/MBR-COVERED" {
		t.Errorf("patient.reference = %v, want Patient/MBR-COVERED", pat["reference"])
	}
	prov, _ := m["provider"].(map[string]any)
	if prov["reference"] != "Practitioner/9999999999" {
		t.Errorf("provider.reference = %v, want Practitioner/9999999999", prov["reference"])
	}
	// The insurer is the payer the member's coverage names, as given, and never
	// a placeholder reference.
	if got, _ := json.Marshal(m["insurer"]); string(got) != `{"identifier":{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00402"}}` {
		t.Errorf("insurer = %s, want the payer's identifier", got)
	}
}

// An eligibility request without the payer's identifier is refused, and nothing
// is built: it never names a placeholder payer.
func TestBuildEligibilityRequest_RefusesWithoutThePayer(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	for name, payer := range map[string]PayerIdentifier{
		"none":      {},
		"no value":  {System: CMSPayerIdentity.System},
		"no system": {Value: CMSPayerIdentity.Value},
	} {
		b, err := BuildEligibilityRequestExact("MBR-COVERED", "9999999999", payer, now)
		if !errors.Is(err, ErrPayerRecordAbsent) || b != nil {
			t.Errorf("%s: err=%v built=%s, want ErrPayerRecordAbsent and nothing built", name, err, b)
		}
	}
}

// ParseEligibilityRequestInsurer returns the insurer a request names, exactly
// as written and in whatever form, and nothing from a request that names none.
func TestParseEligibilityRequestInsurer(t *testing.T) {
	payer := PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00402"}
	req, err := BuildEligibilityRequestExact("MBR-COVERED", "9999999999", payer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ParseEligibilityRequestInsurer(req); !ok || string(got) != `{"identifier":{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00402"}}` {
		t.Fatalf("insurer = %s, %v; want the request's identifier", got, ok)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"an earlier release's literal reference": {`{"resourceType":"CoverageEligibilityRequest","insurer":{"reference":"Organization/payer"}}`, `{"reference":"Organization/payer"}`},
		"half an identifier, as sent":            {`{"resourceType":"CoverageEligibilityRequest","insurer":{"identifier":{"value":"00001"}}}`, `{"identifier":{"value":"00001"}}`},
	} {
		if got, ok := ParseEligibilityRequestInsurer([]byte(tc.body)); !ok || string(got) != tc.want {
			t.Errorf("%s: read %s, %v; want %s", name, got, ok, tc.want)
		}
	}
	for name, body := range map[string]string{
		"no insurer":       `{"resourceType":"CoverageEligibilityRequest"}`,
		"a null insurer":   `{"resourceType":"CoverageEligibilityRequest","insurer":null}`,
		"an empty insurer": `{"resourceType":"CoverageEligibilityRequest","insurer":{}}`,
		"another resource": `{"resourceType":"Claim","insurer":{"identifier":{"system":"s","value":"v"}}}`,
	} {
		if got, ok := ParseEligibilityRequestInsurer([]byte(body)); ok {
			t.Errorf("%s: read %s, want nothing", name, got)
		}
	}
}

// BuildEligibilityAnswer restates the request's insurer as written, a literal
// reference as much as an identifier, and refuses an answer whose request
// names none rather than name a payer of its own.
func TestBuildEligibilityAnswer_RestatesTheRequestsInsurer(t *testing.T) {
	t0 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	for _, insurer := range []string{
		`{"identifier":{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00402"}}`,
		`{"reference":"Organization/payer"}`,
		`{"reference":"https://payer.example/fhir/Organization/acme","display":"Acme Health Plan"}`,
		// Members the FHIR model has no field for are carried too: a
		// primitive's extension, and a member it does not know.
		`{"reference":"Organization/p","_reference":{"extension":[{"url":"http://example.org/x","valueString":"y"}]}}`,
		`{"display":"Acme","_display":{"id":"d1"},"identifier":{"system":"s","value":"v","_value":{"extension":[{"url":"http://example.org/z","valueCode":"c"}]}}}`,
		`{"identifier":{"use":"not-a-code","system":"s","value":"v"},"futureMember":1}`,
	} {
		b, err := BuildEligibilityAnswer("corr-1", "Patient/MBR-1", true, "", json.RawMessage(insurer), t0)
		if err != nil {
			t.Fatalf("%s: %v", insurer, err)
		}
		var m struct {
			Insurer json.RawMessage `json:"insurer"`
		}
		if err := json.Unmarshal(b, &m); err != nil || !jsonEqual(t, m.Insurer, []byte(insurer)) {
			t.Errorf("answer insurer %s, want the request's %s", m.Insurer, insurer)
		}
	}
	for _, insurer := range []string{``, `null`, `{}`, `"Organization/payer"`, `[]`} {
		if b, err := BuildEligibilityAnswer("corr-1", "Patient/MBR-1", true, "", json.RawMessage(insurer), t0); !errors.Is(err, ErrPayerRecordAbsent) || b != nil {
			t.Errorf("insurer %q: err=%v built=%s, want ErrPayerRecordAbsent and nothing built", insurer, err, b)
		}
	}
}

// TestParseEligibilityResponse_Branches checks the SDK parser reads both the
// covered (inforce=true) and not-covered (inforce=false + disposition) shapes.
// Wire-interop with substrate-built responses is proven in test/sdkparity.
func TestParseEligibilityResponse_Branches(t *testing.T) {
	covered := `{"resourceType":"CoverageEligibilityResponse","status":"active",` +
		`"purpose":["benefits"],"outcome":"complete",` +
		`"patient":{"reference":"Patient/MBR-COVERED"},` +
		`"insurance":[{"coverage":{"reference":"Coverage/MBR-COVERED"},"inforce":true}]}`
	gotCovered, reason, err := ParseEligibilityResponse([]byte(covered))
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(covered): %v", err)
	}
	if !gotCovered || reason != "" {
		t.Errorf("covered branch: got covered=%v reason=%q, want true/empty", gotCovered, reason)
	}

	notCovered := `{"resourceType":"CoverageEligibilityResponse","status":"active",` +
		`"purpose":["benefits"],"outcome":"complete","disposition":"member not enrolled",` +
		`"patient":{"reference":"Patient/MBR-NOTCOVERED"},` +
		`"insurance":[{"coverage":{"reference":"Coverage/MBR-NOTCOVERED"},"inforce":false}]}`
	gotNC, reasonNC, err := ParseEligibilityResponse([]byte(notCovered))
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(not-covered): %v", err)
	}
	if gotNC {
		t.Errorf("not-covered branch: got covered=true, want false")
	}
	if reasonNC != "member not enrolled" {
		t.Errorf("not-covered branch: reason = %q, want %q", reasonNC, "member not enrolled")
	}
}

// TestEligibilityRoundTrip is the hermetic build→parse seam: a covered/not-covered
// pair built by the SDK helper-shaped resources parses back consistently. (The
// substrate's BuildEligibilityResponseExact is the real producer; this asserts the SDK
// parser is self-consistent with the shape the parity test pins.)
func TestEligibilityRoundTrip(t *testing.T) {
	// CER round-trips through the SDK parser-of-record only on the response side;
	// here we confirm a not-covered disposition survives marshal/parse.
	src := `{"resourceType":"CoverageEligibilityResponse","status":"active",` +
		`"purpose":["benefits"],"outcome":"complete","disposition":"no active coverage",` +
		`"patient":{"reference":"Patient/X"},` +
		`"insurance":[{"coverage":{"reference":"Coverage/X"},"inforce":false}]}`
	covered, reason, err := ParseEligibilityResponse([]byte(src))
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if covered || reason != "no active coverage" {
		t.Errorf("round-trip: covered=%v reason=%q", covered, reason)
	}

	// Wrong resourceType is rejected.
	if _, _, err := ParseEligibilityResponse([]byte(`{"resourceType":"Patient"}`)); err == nil {
		t.Error("ParseEligibilityResponse should reject a non-CoverageEligibilityResponse resource")
	}
}

// TestParseEligibilityRequestMember checks the payer-side parser: round-trips the
// member out of the SDK's own BuildEligibilityRequestExact output, rejects a wrong
// resourceType, and rejects a CoverageEligibilityRequest missing patient.reference.
// Wire-interop with the substrate builder is proven in test/sdkparity.
func TestParseEligibilityRequestMember(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	// Round-trip: SDK-built CER → ParseEligibilityRequestMember → member.
	cerBytes, err := BuildEligibilityRequestExact("MBR-ROUNDTRIP", "1234567890", CMSPayerIdentity, now)
	if err != nil {
		t.Fatalf("BuildEligibilityRequestExact: %v", err)
	}
	member, err := ParseEligibilityRequestMember(cerBytes)
	if err != nil {
		t.Fatalf("ParseEligibilityRequestMember(SDK-built): %v", err)
	}
	if member != "MBR-ROUNDTRIP" {
		t.Errorf("member = %q, want MBR-ROUNDTRIP", member)
	}

	// Rejects wrong resourceType.
	if _, err := ParseEligibilityRequestMember([]byte(`{"resourceType":"Patient"}`)); err == nil {
		t.Error("ParseEligibilityRequestMember should reject a Patient resource")
	}

	// Rejects CoverageEligibilityRequest missing patient.reference.
	noPatRef := `{"resourceType":"CoverageEligibilityRequest","status":"active"}`
	if _, err := ParseEligibilityRequestMember([]byte(noPatRef)); err == nil {
		t.Error("ParseEligibilityRequestMember should reject a CER missing patient.reference")
	}
}

// TestBuildEligibilityResponse checks the payer-side builder: covered=true round-trips
// via the SDK's own ParseEligibilityResponse; covered=false with a reason round-trips
// both; and two calls with the same fixed clock produce byte-identical output.
// Wire-interop with the substrate parser is proven in test/sdkparity.
func TestBuildEligibilityResponse(t *testing.T) {
	t0 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	// covered=true round-trip.
	b, err := BuildEligibilityResponseExact("corr-1", "Patient/MBR-1", true, "", CMSPayerIdentity, t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponseExact(covered): %v", err)
	}
	gotCovered, gotReason, err := ParseEligibilityResponse(b)
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(covered): %v", err)
	}
	if !gotCovered || gotReason != "" {
		t.Errorf("covered round-trip: covered=%v reason=%q, want true/empty", gotCovered, gotReason)
	}

	// covered=false with reason round-trip.
	b2, err := BuildEligibilityResponseExact("corr-2", "Patient/MBR-2", false, "not a member", CMSPayerIdentity, t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponseExact(not-covered): %v", err)
	}
	gotCovered2, gotReason2, err := ParseEligibilityResponse(b2)
	if err != nil {
		t.Fatalf("ParseEligibilityResponse(not-covered): %v", err)
	}
	if gotCovered2 {
		t.Errorf("not-covered round-trip: covered=true, want false")
	}
	if gotReason2 != "not a member" {
		t.Errorf("not-covered round-trip: reason=%q, want %q", gotReason2, "not a member")
	}

	// Determinism: two calls with same args → byte-identical output.
	b3, err := BuildEligibilityResponseExact("corr-det", "Patient/MBR-DET", true, "", CMSPayerIdentity, t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponseExact(det-1): %v", err)
	}
	b4, err := BuildEligibilityResponseExact("corr-det", "Patient/MBR-DET", true, "", CMSPayerIdentity, t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponseExact(det-2): %v", err)
	}
	if string(b3) != string(b4) {
		t.Errorf("non-deterministic output:\n  call1=%s\n  call2=%s", b3, b4)
	}
}

// TestBuildEligibilityResponse_NoReasonOmitsDisposition guards the same bug the
// substrate's internal/fhirmap.BuildEligibilityResponse guards
// (internal/fhirmap/eligibility_test.go's TestBuildEligibilityResponse_NoReasonOmitsDisposition):
// a not-covered member with no reason text (SoR.CoverageInforce returns "" when no
// Coverage row is found at all, e.g. an unseeded fixture — this is exactly the bug
// that produced the MBR-UC04/MBR-UC08 502s in production) must never marshal
// disposition as an empty string. disposition is 0..1 in R4; an empty-string value
// is an invalid FHIR primitive ("Attribute value must not be empty") that a real
// $validate egress gate rejects. This is the copy production payer-gw actually
// calls (the promoted gateway/engine/inbound.go handleEligibilityInbound ->
// shnsdk.BuildEligibilityResponseExact directly), so this guard — not the substrate
// twin's — is the one that matters live.
func TestBuildEligibilityResponse_NoReasonOmitsDisposition(t *testing.T) {
	t0 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	b, err := BuildEligibilityResponseExact("c1", "Patient/MBR-UC08", false, "", CMSPayerIdentity, t0)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["disposition"]; ok {
		t.Fatalf("disposition must be omitted entirely when reason is empty, got present with value %v", v)
	}
	covered, reason, err := ParseEligibilityResponse(b)
	if err != nil {
		t.Fatal(err)
	}
	if covered {
		t.Fatal("not-covered response must parse as not covered")
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

// TestBuildEligibilityResponse_InsurerIsThePayers: the answer names the payer
// by its identifier (Reference.identifier), and an answer given no payer is
// refused and not built: it never names the placeholder Organization/payer.
func TestBuildEligibilityResponse_InsurerIsThePayers(t *testing.T) {
	t0 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	for name, payer := range map[string]PayerIdentifier{"none": {}, "no value": {System: CMSPayerIdentity.System}} {
		if b, err := BuildEligibilityResponseExact("corr-1", "Patient/MBR-1", true, "", payer, t0); !errors.Is(err, ErrPayerRecordAbsent) || b != nil {
			t.Fatalf("%s insurer: err=%v built=%s, want ErrPayerRecordAbsent and nothing built", name, err, b)
		}
	}

	derived, err := BuildEligibilityResponseExact("corr-1", "Patient/MBR-1", true, "", CMSPayerIdentity, t0)
	if err != nil {
		t.Fatalf("BuildEligibilityResponseExact(derived insurer): %v", err)
	}
	if strings.Contains(string(derived), "Organization/payer") {
		t.Fatalf("the insurer must not carry the placeholder: %s", derived)
	}
	var m struct {
		Insurer struct {
			Identifier struct {
				System string `json:"system"`
				Value  string `json:"value"`
			} `json:"identifier"`
		} `json:"insurer"`
	}
	if err := json.Unmarshal(derived, &m); err != nil {
		t.Fatal(err)
	}
	if m.Insurer.Identifier.System != CMSPayerIdentity.System || m.Insurer.Identifier.Value != CMSPayerIdentity.Value {
		t.Fatalf("insurer.identifier = %+v, want %+v", m.Insurer.Identifier, CMSPayerIdentity)
	}
}

// TestBuildPatientAccessCapabilityStatement_Shape verifies the SDK-promoted
// CMS-0057 CapabilityStatement has the required FHIR shape (FR-37): kind=instance,
// status=active, at least one rest.resource of type ExplanationOfBenefit with
// both read+search interactions and no supportedProfile. Wire-identity with the
// internal/fhirmap shim is proven in test/sdkparity/capabilitystatement_parity_test.go.
func TestBuildPatientAccessCapabilityStatement_Shape(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	b, err := BuildPatientAccessCapabilityStatementExact(at, SupportedContractVersions())
	if err != nil {
		t.Fatalf("BuildPatientAccessCapabilityStatementExact: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["resourceType"] != "CapabilityStatement" {
		t.Errorf("resourceType = %v, want CapabilityStatement", m["resourceType"])
	}
	if m["status"] != "active" {
		t.Errorf("status = %v, want active", m["status"])
	}
	if m["kind"] != "instance" {
		t.Errorf("kind = %v, want instance", m["kind"])
	}
	if m["date"] != "2026-06-15T00:00:00Z" {
		t.Errorf("date = %v, want 2026-06-15T00:00:00Z", m["date"])
	}
	rests, _ := m["rest"].([]any)
	if len(rests) == 0 {
		t.Fatal("rest array is empty")
	}
	rest0, _ := rests[0].(map[string]any)
	resources, _ := rest0["resource"].([]any)
	if len(resources) == 0 {
		t.Fatal("rest[0].resource array is empty")
	}
	res0, _ := resources[0].(map[string]any)
	if res0["type"] != "ExplanationOfBenefit" {
		t.Errorf("rest[0].resource[0].type = %v, want ExplanationOfBenefit", res0["type"])
	}
	// The decision EOB claims no profile: PDex pdex-priorauthorization's
	// closed adjudication slicing needs an amount, a reason or units on every
	// adjudication, so it cannot carry a payer's amountless decision without
	// an amount the payer never stated.
	if bytes.Contains(b, []byte("pdex-priorauthorization")) || bytes.Contains(b, []byte("davinci-pdex")) {
		t.Errorf("the Patient Access CapabilityStatement advertises pdex-priorauthorization: re-adding that claim requires every decision EOB variant to validate against PDex (test/conformance TestPADecisionEOBGoldensValidate): %s", b)
	}
	if _, ok := res0["supportedProfile"]; ok {
		t.Errorf("rest[0].resource[0] claims a supportedProfile the decision EOB does not meet: %v", res0["supportedProfile"])
	}
	interactions, _ := res0["interaction"].([]any)
	if len(interactions) != 2 {
		t.Errorf("want 2 interactions (read+search-type), got %d", len(interactions))
	}
	// It names no implementation guide either: the decision EOBs do not meet
	// PDex, so claiming the IG would claim what they cannot do.
	if _, ok := m["implementationGuide"]; ok {
		t.Fatalf("the Patient Access CapabilityStatement claims an implementationGuide the decision EOBs do not meet: re-adding it requires every decision EOB variant to validate against it: %v", m["implementationGuide"])
	}
	// Determinism: same input → byte-identical output.
	b2, _ := BuildPatientAccessCapabilityStatementExact(at, SupportedContractVersions())
	if string(b) != string(b2) {
		t.Error("BuildPatientAccessCapabilityStatementExact is non-deterministic")
	}
}

// TestBuildPatientAccessCapabilityStatement_DeclaredSet: no declared set makes
// the statement name an implementation guide — not a pa.pdex token at any line
// (the retired ContractPAPDex21 included: a caller still holding it gets no PDex
// claim), and not a set with no pa.pdex token at all (never a silent hardcoded
// fallback).
func TestBuildPatientAccessCapabilityStatement_DeclaredSet(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	var m map[string]any
	for _, declared := range [][]string{{"pa.pdex@9.9"}, {ContractPAPDex21}} {
		b, err := BuildPatientAccessCapabilityStatementExact(at, declared)
		if err != nil {
			t.Fatalf("BuildPatientAccessCapabilityStatementExact(%v): %v", declared, err)
		}
		m = map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, has := m["implementationGuide"]; has || bytes.Contains(b, []byte("pdex")) {
			t.Fatalf("%v: the statement claims PDex: %s", declared, b)
		}
	}

	// No pa.pdex token at all (e.g. a declared set naming only unrelated contracts) ->
	// no implementationGuide entries.
	b, err := BuildPatientAccessCapabilityStatementExact(at, []string{"pa.crd@2.0"})
	if err != nil {
		t.Fatalf("BuildPatientAccessCapabilityStatementExact(no pdex token): %v", err)
	}
	m = map[string]any{} // reset — json.Unmarshal into a non-nil map merges, never clears stale keys
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["implementationGuide"]; has {
		t.Fatalf("implementationGuide = %v, want absent (declared set carries no pa.pdex token)", m["implementationGuide"])
	}
}

// TestBuildProviderIngressCapabilityStatement_Shape pins the provider Da Vinci
// ingress statement (FR-37 gap closed: per-role CapabilityStatements): the
// versioned CRD/DTR/PAS 2.0.1 IG canonicals, the two FHIR operations the
// ingress serves (PAS Claim/$submit and Claim/$inquire, DTR
// $questionnaire-package), and
// determinism under a fixed clock. CRD is CDS Hooks (not FHIR REST) — it
// appears via its IG canonical and the rest documentation, never as a
// rest.resource.
func TestBuildProviderIngressCapabilityStatement_Shape(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	b, err := BuildProviderIngressCapabilityStatement(at, SupportedContractVersions())
	if err != nil {
		t.Fatalf("BuildProviderIngressCapabilityStatement: %v", err)
	}
	var cs map[string]any
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cs["resourceType"] != "CapabilityStatement" || cs["kind"] != "instance" {
		t.Fatalf("resourceType/kind wrong: %v/%v", cs["resourceType"], cs["kind"])
	}
	igs, _ := cs["implementationGuide"].([]any)
	want := map[string]bool{
		"http://hl7.org/fhir/us/davinci-crd/ImplementationGuide/hl7.fhir.us.davinci-crd|2.0.1": true,
		"http://hl7.org/fhir/us/davinci-dtr/ImplementationGuide/hl7.fhir.us.davinci-dtr|2.0.1": true,
		"http://hl7.org/fhir/us/davinci-pas/ImplementationGuide/hl7.fhir.us.davinci-pas|2.0.1": true,
	}
	if len(igs) != len(want) {
		t.Fatalf("implementationGuide = %v, want the three 2.0.1 canonicals", igs)
	}
	for _, ig := range igs {
		if s, _ := ig.(string); !want[s] {
			t.Fatalf("unexpected implementationGuide entry %v", ig)
		}
	}
	raw := string(b)
	for _, must := range []string{
		"OperationDefinition/Claim-submit",
		"OperationDefinition/Claim-inquiry",
		"OperationDefinition/questionnaire-package",
	} {
		if !strings.Contains(raw, must) {
			t.Fatalf("statement missing operation %q", must)
		}
	}
	b2, err := BuildProviderIngressCapabilityStatement(at, SupportedContractVersions())
	if err != nil || !bytes.Equal(b, b2) {
		t.Fatal("BuildProviderIngressCapabilityStatement is not deterministic")
	}
}

// TestBuildProviderIngressCapabilityStatement_Eligibility pins the coverage
// eligibility operation the ingress serves: CoverageEligibilityRequest/$submit
// by the base R4 OperationDefinition, whatever lines are declared, documented
// as answered one request at a time unless the payer's own endpoint answers a
// batch. The statement claims no batch interaction.
func TestBuildProviderIngressCapabilityStatement_Eligibility(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	for _, declared := range [][]string{SupportedContractVersions(), nil} {
		b, err := BuildProviderIngressCapabilityStatement(at, declared)
		if err != nil {
			t.Fatal(err)
		}
		var cs fhir.CapabilityStatement
		if err := json.Unmarshal(b, &cs); err != nil {
			t.Fatal(err)
		}
		var ops []fhir.CapabilityStatementRestResourceOperation
		for _, r := range cs.Rest[0].Resource {
			if r.Type == fhir.ResourceTypeCoverageEligibilityRequest {
				ops = append(ops, r.Operation...)
			}
		}
		if len(ops) != 1 || ops[0].Name != "submit" || ops[0].Definition != "http://hl7.org/fhir/OperationDefinition/CoverageEligibilityRequest-submit" {
			t.Fatalf("declared %v: CoverageEligibilityRequest operations = %+v, want $submit alone", declared, ops)
		}
		if ops[0].Documentation == nil || !strings.Contains(*ops[0].Documentation, "501") {
			t.Fatalf("declared %v: $submit documentation %v does not say how a batch is answered", declared, ops[0].Documentation)
		}
		if len(cs.Rest[0].Interaction) != 0 {
			t.Fatalf("declared %v: the statement claims interactions %+v", declared, cs.Rest[0].Interaction)
		}
	}
}

// TestBuildProviderIngressCapabilityStatement_DeclaredSet (declared-set driven): the
// implementationGuide list and the Claim resource's supportedProfile pins are
// declared-set-driven — declaring pa.crd@{2.0,2.2} yields EXACTLY two CRD IG
// canonicals and no dtr/pas canonicals at all (positive AND negative assertion).
func TestBuildProviderIngressCapabilityStatement_DeclaredSet(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	b, err := BuildProviderIngressCapabilityStatement(at, []string{"pa.crd@2.0", "pa.crd@2.2"})
	if err != nil {
		t.Fatalf("BuildProviderIngressCapabilityStatement: %v", err)
	}
	var cs map[string]any
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	igs, _ := cs["implementationGuide"].([]any)
	wantCRD := map[string]bool{
		"http://hl7.org/fhir/us/davinci-crd/ImplementationGuide/hl7.fhir.us.davinci-crd|2.0.1": true,
		"http://hl7.org/fhir/us/davinci-crd/ImplementationGuide/hl7.fhir.us.davinci-crd|2.2.1": true,
	}
	if len(igs) != 2 {
		t.Fatalf("implementationGuide = %v, want exactly 2 CRD canonicals", igs)
	}
	for _, ig := range igs {
		s, _ := ig.(string)
		if !wantCRD[s] {
			t.Fatalf("unexpected implementationGuide entry %v, want one of %v", ig, wantCRD)
		}
		if strings.Contains(s, "davinci-dtr") || strings.Contains(s, "davinci-pas") {
			t.Fatalf("declared set carried no pa.dtr/pa.pas — implementationGuide must not carry %v", ig)
		}
	}

	// PAS-declared-only: the Claim resource's supportedProfile carries one pin per
	// declared PAS line (multi-line, per the brief's "per-line supportedProfile
	// pins" requirement), and no CRD/DTR canonicals appear.
	b, err = BuildProviderIngressCapabilityStatement(at, []string{"pa.pas@2.0", "pa.pas@2.2"})
	if err != nil {
		t.Fatalf("BuildProviderIngressCapabilityStatement(pas-only): %v", err)
	}
	cs = map[string]any{} // reset — json.Unmarshal into a non-nil map merges, never clears stale keys
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rests, _ := cs["rest"].([]any)
	rest0, _ := rests[0].(map[string]any)
	resources, _ := rest0["resource"].([]any)
	claimRes, _ := resources[0].(map[string]any)
	if claimRes["type"] != "Claim" {
		t.Fatalf("resource[0].type = %v, want Claim", claimRes["type"])
	}
	profiles, _ := claimRes["supportedProfile"].([]any)
	wantProfiles := map[string]bool{
		"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim|2.0.1": true,
		"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-claim|2.2.1": true,
	}
	if len(profiles) != 2 {
		t.Fatalf("Claim supportedProfile = %v, want exactly 2 pins (one per declared PAS line)", profiles)
	}
	for _, p := range profiles {
		if s, _ := p.(string); !wantProfiles[s] {
			t.Fatalf("unexpected supportedProfile entry %v", p)
		}
	}

	// Unrecognized line + unrelated contract tokens are tolerated (skipped, no error,
	// no fabricated canonical) — an empty/irrelevant declared set yields an empty
	// implementationGuide.
	b, err = BuildProviderIngressCapabilityStatement(at, []string{"pa.crd@9.9", "pa.pdex@2.1"})
	if err != nil {
		t.Fatalf("BuildProviderIngressCapabilityStatement(unrecognized/unrelated): %v", err)
	}
	cs = map[string]any{} // reset — see comment above
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := cs["implementationGuide"]; has {
		t.Fatalf("implementationGuide = %v, want absent (no recognized pa.crd/pa.dtr/pa.pas line declared)", cs["implementationGuide"])
	}
}

// TestBuildHubCapabilityStatement_Shape pins the Hub statement (FR-37
// per-role): a deliberately MINIMAL kind=instance server statement with NO
// implementationGuide and NO rest resources — the Hub is payload-blind
// (OWD-2): it speaks no IG and must never advertise payload-version
// knowledge. cpb-1 requires a rest element; it carries only documentation.
func TestBuildHubCapabilityStatement_Shape(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	b, err := BuildHubCapabilityStatement(at)
	if err != nil {
		t.Fatalf("BuildHubCapabilityStatement: %v", err)
	}
	var cs map[string]any
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cs["resourceType"] != "CapabilityStatement" || cs["kind"] != "instance" {
		t.Fatalf("resourceType/kind wrong: %v/%v", cs["resourceType"], cs["kind"])
	}
	if _, has := cs["implementationGuide"]; has {
		t.Fatal("Hub statement must NOT declare implementationGuide (payload-blind, OWD-2)")
	}
	rest, _ := cs["rest"].([]any)
	if len(rest) != 1 {
		t.Fatalf("rest = %v, want exactly one server element (cpb-1)", rest)
	}
	if _, has := rest[0].(map[string]any)["resource"]; has {
		t.Fatal("Hub statement must NOT list rest resources (no FHIR REST surface)")
	}
	b2, err := BuildHubCapabilityStatement(at)
	if err != nil || !bytes.Equal(b, b2) {
		t.Fatal("BuildHubCapabilityStatement is not deterministic")
	}
}

// TestBuildPADecisionEOB_ProcedureSystem (DEF-14): the EOB's
// item[].productOrService.coding[].system must carry the order's ACTUAL procedure
// system (HCPCS, CPT, etc.) rather than being hardcoded to CPT. An empty
// ProcedureSystem is never defaulted: the coding carries no system.
func TestBuildPADecisionEOB_ProcedureSystem(t *testing.T) {
	base := eobBaseParams()
	base.ID, base.CPTCode, base.CPTDisplay = "eob-x", "L8000", "Breast prosthesis, mastectomy bra"
	base.PreAuthRefs = []string{"PA-1"}

	t.Run("explicit HCPCS system flows to productOrService.coding.system", func(t *testing.T) {
		p := base
		p.ProcedureSystem = "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
		got := eobProcedureSystem(t, p)
		if got != "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets" {
			t.Fatalf("EOB procedure system = %q, want HCPCS", got)
		}
	})
	t.Run("empty ProcedureSystem writes no system", func(t *testing.T) {
		got := eobProcedureSystem(t, base) // ProcedureSystem unset
		if got != "" {
			t.Fatalf("EOB procedure system = %q, want none (never a default)", got)
		}
	})
}

// eobProcedureSystem builds the EOB and returns item[0].productOrService.coding[0].system.
func eobProcedureSystem(t *testing.T, p PADecisionEOBExactParams) string {
	t.Helper()
	raw, err := BuildPADecisionEOBExact(p)
	if err != nil {
		t.Fatalf("BuildPADecisionEOBExact: %v", err)
	}
	var eob struct {
		Item []struct {
			ProductOrService struct {
				Coding []struct {
					System string `json:"system"`
				} `json:"coding"`
			} `json:"productOrService"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &eob); err != nil {
		t.Fatalf("unmarshal EOB: %v", err)
	}
	if len(eob.Item) == 0 || len(eob.Item[0].ProductOrService.Coding) == 0 {
		t.Fatalf("EOB has no productOrService coding: %s", raw)
	}
	return eob.Item[0].ProductOrService.Coding[0].System
}

// The standalone Responder's eligibility answer names the payer the request
// named as its insurer; a request that names none is refused, never answered
// under a placeholder payer.
func TestResponderEligibility_InsurerIsTheRequests(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	r := &Responder{cfg: ResponderConfig{Adjudicator: &paTestAdjudicator{now: now}}}
	req, err := BuildEligibilityRequestExact("MBR-COVERED", "9999999999", PayerIdentifier{System: "urn:oid:2.16.840.1.113883.6.300", Value: "00402"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res := r.handleEligibility(req, "corr-1", now)
	if res.appStatus != 0 || !bytes.Contains(res.payload, []byte(`"insurer":{"identifier":{"system":"urn:oid:2.16.840.1.113883.6.300","value":"00402"}}`)) {
		t.Fatalf("answer %d %q %s, want the request's insurer", res.appStatus, res.errMsg, res.payload)
	}
	// An earlier release's originator names its payer by a literal reference:
	// valid R4, and answered with that reference as written.
	res = r.handleEligibility([]byte(`{"resourceType":"CoverageEligibilityRequest","patient":{"reference":"Patient/MBR-COVERED"},"insurer":{"reference":"Organization/payer"}}`), "corr-2", now)
	if res.appStatus != 0 || !bytes.Contains(res.payload, []byte(`"insurer":{"reference":"Organization/payer"}`)) {
		t.Fatalf("a literal-reference insurer: %d %q %s, want it restated", res.appStatus, res.errMsg, res.payload)
	}
	for _, body := range []string{
		`{"resourceType":"CoverageEligibilityRequest","patient":{"reference":"Patient/MBR-COVERED"}}`,
		`{"resourceType":"CoverageEligibilityRequest","patient":{"reference":"Patient/MBR-COVERED"},"insurer":{}}`,
	} {
		res = r.handleEligibility([]byte(body), "corr-3", now)
		if res.appStatus != http.StatusBadRequest || res.payload != nil {
			t.Fatalf("no insurer (%s): %d %s, want 400 and no answer", body, res.appStatus, res.payload)
		}
	}
}
