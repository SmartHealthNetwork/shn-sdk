package shnsdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// conformantSubmitInputs returns the demo-persona inputs for the conformant $submit
// builder: the answered DTR QR + the CPT-72148/M51.16 ServiceRequest + the
// patient/coverage refs + a fixed clock. Deterministic (fixed Authored/
// Created). Mirrors the conformant golden's demo persona (Linda Johansson / MBR-COVERED).
func conformantSubmitInputs(t *testing.T) ConformantClaimInputs {
	t.Helper()
	const member = "MBR-COVERED"
	const patientRef = "Patient/" + member
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", patientRef)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}
	q := SandboxLumbarQuestionnaire()
	qrJSON, err := FillQuestionnaire(q, SandboxUC03Context(), QRContext{
		PatientRef:  patientRef,
		CoverageRef: "Coverage/" + conformantPASCoverageID,
		OrderRef:    "ServiceRequest/" + conformantPASServiceRequestID,
		Authored:    created,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	return ConformantClaimInputs{
		QR:          qrJSON,
		SR:          srJSON,
		PatientRef:  patientRef,
		CoverageRef: "Coverage/" + conformantPASCoverageID,
		Corr:        "convergence-pas-submit-0001",
		Created:     created,
	}
}

// TestBuildConformantClaimBundle_MatchesGolden: the SDK builder reproduces the
// (regenerated, LEAN) conformant $submit golden
// testdata/golden/conformant/pas-submit-request.json — demo persona, no br-payer
// foreign seed.
func TestBuildConformantClaimBundle_MatchesGolden(t *testing.T) {
	want := readConformantGolden(t, "pas-submit-request.json")
	got, err := BuildConformantClaimBundle(conformantSubmitInputs(t))
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}
	if !jsonEqual(t, got, want) {
		t.Fatalf("conformant $submit drift:\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildConformantClaimBundle_OwnsQRContextRefs proves Fix #1: the builder OWNS the
// QR's qr-context refs. Even when the caller passes a QR whose qr-context refs are WRONG
// (Coverage/WRONG, ServiceRequest/WRONG), the built bundle's QR qr-context refs are the
// bundle-local Coverage/convergence-coverage + ServiceRequest/convergence-sr — i.e. the
// builder corrected them (closing the dangling-ref hazard parseConformantPASSubjects does
// not catch). The caller's QRContext CoverageRef/OrderRef no longer has to match.
func TestBuildConformantClaimBundle_OwnsQRContextRefs(t *testing.T) {
	in := conformantSubmitInputs(t)
	// Deliberately mis-point the QR's qr-context refs.
	q := SandboxLumbarQuestionnaire()
	wrongQR, err := FillQuestionnaire(q, SandboxUC03Context(), QRContext{
		PatientRef:  in.PatientRef,
		CoverageRef: "Coverage/WRONG",
		OrderRef:    "ServiceRequest/WRONG",
		Authored:    in.Created,
	})
	if err != nil {
		t.Fatalf("FillQuestionnaire (wrong): %v", err)
	}
	in.QR = wrongQR

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle: %v", err)
	}

	// Extract the QR entry's qr-context refs from the built bundle.
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	const qrContextURL = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/qr-context"
	var coverageCtx, srCtx string
	foundQR := false
	for _, e := range bundle.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
			Extension    []struct {
				URL            string `json:"url"`
				ValueReference *struct {
					Reference string `json:"reference"`
				} `json:"valueReference"`
			} `json:"extension"`
		}
		if err := json.Unmarshal(e.Resource, &probe); err != nil {
			t.Fatalf("parse entry: %v", err)
		}
		if probe.ResourceType != "QuestionnaireResponse" {
			continue
		}
		foundQR = true
		for _, ext := range probe.Extension {
			if ext.URL != qrContextURL || ext.ValueReference == nil {
				continue
			}
			ref := ext.ValueReference.Reference
			switch {
			case len(ref) >= len("Coverage/") && ref[:len("Coverage/")] == "Coverage/":
				coverageCtx = ref
			case len(ref) >= len("ServiceRequest/") && ref[:len("ServiceRequest/")] == "ServiceRequest/":
				srCtx = ref
			}
		}
	}
	if !foundQR {
		t.Fatal("built bundle has no QuestionnaireResponse entry")
	}
	if coverageCtx != "Coverage/"+conformantPASCoverageID {
		t.Errorf("QR qr-context Coverage ref = %q, want %q (builder must own/correct it)", coverageCtx, "Coverage/"+conformantPASCoverageID)
	}
	if srCtx != "ServiceRequest/"+conformantPASServiceRequestID {
		t.Errorf("QR qr-context ServiceRequest ref = %q, want %q (builder must own/correct it)", srCtx, "ServiceRequest/"+conformantPASServiceRequestID)
	}
}

// TestConformantizePASClaim asserts conformantizePASClaim's intent directly (so a future
// golden regen can't silently change semantics): item[0].productOrService is the
// conformant CPT 72148, item[0].category STAYS X12 1365 "Medical Care" (unchanged), and
// the extension-requestedService is present → the SR ref.
func TestConformantizePASClaim(t *testing.T) {
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	claimJSON, err := buildPASClaim("Patient/MBR-COVERED", "Coverage/MBR-COVERED", "corr-1", created)
	if err != nil {
		t.Fatalf("buildPASClaim: %v", err)
	}
	const srRef = "ServiceRequest/convergence-sr"
	out, err := conformantizePASClaim(claimJSON, srRef)
	if err != nil {
		t.Fatalf("conformantizePASClaim: %v", err)
	}
	var claim struct {
		Item []struct {
			Category struct {
				Coding []struct {
					System  string `json:"system"`
					Code    string `json:"code"`
					Display string `json:"display"`
				} `json:"coding"`
			} `json:"category"`
			ProductOrService struct {
				Coding []struct {
					System string `json:"system"`
					Code   string `json:"code"`
				} `json:"coding"`
			} `json:"productOrService"`
			Extension []struct {
				URL            string `json:"url"`
				ValueReference struct {
					Reference string `json:"reference"`
				} `json:"valueReference"`
			} `json:"extension"`
		} `json:"item"`
	}
	if err := json.Unmarshal(out, &claim); err != nil {
		t.Fatalf("unmarshal conformantized claim: %v", err)
	}
	if len(claim.Item) != 1 {
		t.Fatalf("item count = %d, want 1", len(claim.Item))
	}
	it := claim.Item[0]
	// productOrService → CPT 72148.
	if len(it.ProductOrService.Coding) != 1 ||
		it.ProductOrService.Coding[0].System != "http://www.ama-assn.org/go/cpt" ||
		it.ProductOrService.Coding[0].Code != "72148" {
		t.Errorf("productOrService = %+v, want CPT system http://www.ama-assn.org/go/cpt code 72148", it.ProductOrService.Coding)
	}
	// category STAYS X12 1365 "Medical Care".
	if len(it.Category.Coding) != 1 ||
		it.Category.Coding[0].System != "https://codesystem.x12.org/005010/1365" ||
		it.Category.Coding[0].Code != "1" ||
		it.Category.Coding[0].Display != "Medical Care" {
		t.Errorf("category = %+v, want X12 1365 code 1 Medical Care (unchanged)", it.Category.Coding)
	}
	// extension-requestedService present → the SR ref.
	foundReqService := false
	for _, ext := range it.Extension {
		if ext.URL == extReqService {
			foundReqService = true
			if ext.ValueReference.Reference != srRef {
				t.Errorf("extension-requestedService ref = %q, want %q", ext.ValueReference.Reference, srRef)
			}
		}
	}
	if !foundReqService {
		t.Errorf("item[0] missing extension-requestedService (%s)", extReqService)
	}
}

// TestConformantizePASClaim_NoItem asserts the Fix #4 robustness guard: a Claim with no
// item yields the self-explanatory "claim has no item to conformantize" error (not the
// opaque EOF from unmarshalling a nil m["item"]).
func TestConformantizePASClaim_NoItem(t *testing.T) {
	noItem := []byte(`{"resourceType":"Claim","status":"active"}`)
	_, err := conformantizePASClaim(noItem, "ServiceRequest/convergence-sr")
	if err == nil {
		t.Fatal("conformantizePASClaim(no item) = nil error, want a clean missing-item error")
	}
	if got := err.Error(); got != "claim has no item to conformantize" {
		t.Errorf("error = %q, want %q", got, "claim has no item to conformantize")
	}
}

// TestStripMetaProfile asserts stripMetaProfile's intent directly: it removes meta.profile,
// PRESERVES other meta fields (meta.security survives), and deletes an emptied meta object
// (no "meta":{} litter).
func TestStripMetaProfile(t *testing.T) {
	// (1) Other meta fields survive; profile is gone.
	withSecurity := []byte(`{"resourceType":"Coverage","meta":{"profile":["http://example/p"],"security":[{"system":"s","code":"c"}]},"id":"x"}`)
	out, err := stripMetaProfile(withSecurity)
	if err != nil {
		t.Fatalf("stripMetaProfile (security): %v", err)
	}
	var m struct {
		Meta *struct {
			Profile  []string          `json:"profile"`
			Security []json.RawMessage `json:"security"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Meta == nil {
		t.Fatal("meta deleted entirely, want meta retained (security present)")
	}
	if len(m.Meta.Profile) != 0 {
		t.Errorf("meta.profile = %v, want removed", m.Meta.Profile)
	}
	if len(m.Meta.Security) != 1 {
		t.Errorf("meta.security count = %d, want 1 (preserved)", len(m.Meta.Security))
	}

	// (2) A meta with ONLY profile → the emptied meta object is deleted (no litter).
	onlyProfile := []byte(`{"resourceType":"ServiceRequest","meta":{"profile":["http://example/p"]},"id":"y"}`)
	out2, err := stripMetaProfile(onlyProfile)
	if err != nil {
		t.Fatalf("stripMetaProfile (only profile): %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out2, &raw); err != nil {
		t.Fatalf("unmarshal out2: %v", err)
	}
	if _, ok := raw["meta"]; ok {
		t.Errorf("emptied meta retained (%s), want deleted (no \"meta\":{} litter)", out2)
	}
}

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

// conformantUpdateInputsFromGolden returns ConformantClaimUpdateInputs reconstructed from the
// conformant golden pas-update-request.json. The QR, DR, and Provenance sub-resources are read
// directly from the golden (the amended QR is not reproducible via a standard FillQuestionnaire
// call — it omits prior-imaging, which has no clean SDK construction path). The Claim is
// fully builder-generated from Corr/OriginalCorr. The builder stamps IDs/strips meta.profile/
// rewrites QR context refs idempotently on already-conformant inputs, so the output
// byte-matches the golden.
func conformantUpdateInputsFromGolden(t *testing.T) ConformantClaimUpdateInputs {
	t.Helper()
	const member = "MBR-COVERED"
	created := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	// Read the golden and extract the QR, DR, and Provenance entries.
	goldenBytes := readConformantGolden(t, "pas-update-request.json")
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(goldenBytes, &bundle); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	var qrJSON, drJSON, provJSON []byte
	for _, e := range bundle.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		switch rt.ResourceType {
		case "QuestionnaireResponse":
			qrJSON = e.Resource
		case "DiagnosticReport":
			drJSON = e.Resource
		case "Provenance":
			provJSON = e.Resource
		}
	}
	if qrJSON == nil {
		t.Fatal("golden has no QuestionnaireResponse entry")
	}
	if drJSON == nil {
		t.Fatal("golden has no DiagnosticReport entry")
	}
	if provJSON == nil {
		t.Fatal("golden has no Provenance entry")
	}

	// Build the SR via the SDK — same as the submit builder uses.
	srJSON, err := BuildServiceRequest("72148", "MRI lumbar spine w/o contrast", "M51.16", "Patient/"+member)
	if err != nil {
		t.Fatalf("BuildServiceRequest: %v", err)
	}

	return ConformantClaimUpdateInputs{
		QR:               qrJSON,
		SR:               srJSON,
		PatientRef:       "Patient/" + member,
		CoverageRef:      "Coverage/" + conformantPASCoverageID,
		Provenance:       provJSON,
		DiagnosticReport: drJSON,
		Corr:             "convergence-pas-update-0001",
		OriginalCorr:     "convergence-pas-submit-0001",
		Created:          created,
	}
}

// TestBuildConformantClaimUpdateBundle_MatchesGolden: the SDK builder reproduces the
// (hand-derived, LEAN) conformant amended re-POST golden
// testdata/golden/conformant/pas-update-request.json — demo persona, no br-payer
// foreign seed.
func TestBuildConformantClaimUpdateBundle_MatchesGolden(t *testing.T) {
	want := readConformantGolden(t, "pas-update-request.json")
	got, err := BuildConformantClaimUpdateBundle(conformantUpdateInputsFromGolden(t))
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle: %v", err)
	}
	if !jsonEqual(t, got, want) {
		t.Fatalf("conformant update bundle drift:\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildConformantClaimUpdateBundle_NilDR_OmitsDREntry: when DiagnosticReport is nil,
// the builder emits no DiagnosticReport entry but still includes the Provenance entry.
func TestBuildConformantClaimUpdateBundle_NilDR_OmitsDREntry(t *testing.T) {
	in := conformantUpdateInputsFromGolden(t)
	in.DiagnosticReport = nil

	got, err := BuildConformantClaimUpdateBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle (nil DR): %v", err)
	}

	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}

	hasDR := false
	hasProvenance := false
	for _, e := range bundle.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		switch rt.ResourceType {
		case "DiagnosticReport":
			hasDR = true
		case "Provenance":
			hasProvenance = true
		}
	}
	if hasDR {
		t.Error("nil DiagnosticReport input: bundle must have no DiagnosticReport entry")
	}
	if !hasProvenance {
		t.Error("nil DiagnosticReport input: bundle must still have Provenance entry")
	}
}

// provenanceTargetOf extracts the (single) Provenance.target[0].reference from a built bundle.
func provenanceTargetOf(t *testing.T, bundleJSON []byte) string {
	t.Helper()
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	for _, e := range bundle.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
			Target       []struct {
				Reference string `json:"reference"`
			} `json:"target"`
		}
		if err := json.Unmarshal(e.Resource, &probe); err != nil {
			continue
		}
		if probe.ResourceType == "Provenance" {
			if len(probe.Target) == 0 {
				t.Fatal("Provenance entry has no target")
			}
			return probe.Target[0].Reference
		}
	}
	t.Fatal("built bundle has no Provenance entry")
	return ""
}

// TestBuildConformantClaimUpdateBundle_OwnsProvenanceTarget proves the builder OWNS the FR-32
// Provenance.target: even when the caller's Provenance targets the WRONG supplemental id (the
// pre-restamp SoR/per-UC id), the built bundle's Provenance targets the bundle-local supplemental
// resource — DiagnosticReport/<id> for the DR variant, QuestionnaireResponse/<id> for the nil-DR
// (QR) variant. This closes the dangling-ref hazard that 403s at the FR-32 inbound gate (the
// conformant analog of the cross-resource Provenance-target consistency check; surfaced live when
// the Originator's per-UC Provenance met the builder's id-restamping).
func TestBuildConformantClaimUpdateBundle_OwnsProvenanceTarget(t *testing.T) {
	base := conformantUpdateInputsFromGolden(t)

	// DR variant: a Provenance targeting the WRONG DR id → corrected to the bundle-local DR id.
	wrongProvDR, err := BuildProvenance("DiagnosticReport/WRONG-DR-ID", "Organization/provider", base.Created)
	if err != nil {
		t.Fatalf("BuildProvenance (DR): %v", err)
	}
	inDR := base
	inDR.Provenance = wrongProvDR
	gotDR, err := BuildConformantClaimUpdateBundle(inDR)
	if err != nil {
		t.Fatalf("build DR variant: %v", err)
	}
	if got, want := provenanceTargetOf(t, gotDR), "DiagnosticReport/"+conformantPASDRID; got != want {
		t.Errorf("DR variant Provenance target = %q, want %q (builder must own/correct it)", got, want)
	}

	// QR variant (nil DR): a Provenance targeting the WRONG QR id → corrected to the bundle-local QR id.
	wrongProvQR, err := BuildProvenance("QuestionnaireResponse/WRONG-QR-ID", "Practitioner/npi-1", base.Created)
	if err != nil {
		t.Fatalf("BuildProvenance (QR): %v", err)
	}
	inQR := base
	inQR.DiagnosticReport = nil
	inQR.Provenance = wrongProvQR
	gotQR, err := BuildConformantClaimUpdateBundle(inQR)
	if err != nil {
		t.Fatalf("build QR variant: %v", err)
	}
	if got, want := provenanceTargetOf(t, gotQR), "QuestionnaireResponse/"+conformantPASUpdateQRID; got != want {
		t.Errorf("QR variant Provenance target = %q, want %q (builder must own/correct it)", got, want)
	}
}
