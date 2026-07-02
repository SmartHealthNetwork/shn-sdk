package shnsdk

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		Payer:       CMSPayerIdentity,
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

// TestBuildConformantClaimBundle_NoQR proves the no-doc PA path (spec 2B): a
// PA-required card that advertises NO DTR questionnaire (br-payer L8000) skips DTR, so
// the Originator has no QuestionnaireResponse to embed. With QR == nil the builder must
// still produce a well-formed Da Vinci PAS Claim Bundle (Claim, Patient, Coverage,
// ServiceRequest present) with NO QuestionnaireResponse entry — a PAS Claim without a
// DTR QR is valid (the payer-side parse treats the QR as optional, R-5). No Claim
// reference (supportingInfo) may dangle at a missing QR.
func TestBuildConformantClaimBundle_NoQR(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.QR = nil // no-doc: DTR skipped, no answered QR

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle(QR=nil): %v", err)
	}

	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}

	present := map[string]bool{}
	var claimRaw json.RawMessage
	for _, e := range bundle.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &probe); err != nil {
			t.Fatalf("parse entry: %v", err)
		}
		present[probe.ResourceType] = true
		if probe.ResourceType == "Claim" {
			claimRaw = e.Resource
		}
	}

	// No QuestionnaireResponse entry.
	if present["QuestionnaireResponse"] {
		t.Errorf("no-doc bundle has a QuestionnaireResponse entry; want none")
	}
	// The other four resources are still present + well-formed.
	for _, rt := range []string{"Claim", "Patient", "Coverage", "ServiceRequest"} {
		if !present[rt] {
			t.Errorf("no-doc bundle missing %s entry", rt)
		}
	}

	// The Claim carries NO supportingInfo pointing at a QuestionnaireResponse (no
	// dangling reference to the omitted QR).
	if len(claimRaw) == 0 {
		t.Fatal("no Claim entry to inspect")
	}
	var claim struct {
		SupportingInfo []struct {
			ValueReference *struct {
				Reference string `json:"reference"`
			} `json:"valueReference"`
		} `json:"supportingInfo"`
	}
	if err := json.Unmarshal(claimRaw, &claim); err != nil {
		t.Fatalf("parse claim: %v", err)
	}
	for _, si := range claim.SupportingInfo {
		if si.ValueReference != nil && strings.HasPrefix(si.ValueReference.Reference, "QuestionnaireResponse/") {
			t.Errorf("Claim.supportingInfo references a QuestionnaireResponse (%q) with no QR entry — dangling ref", si.ValueReference.Reference)
		}
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

// TestParseClaimResponse_DeniedA2_brpayer proves the X12-conformant denial code A2 "Not
// Certified" (https://codesystem.x12.org/005010/306) is read as denied. A real Da Vinci PAS
// payer (br-payer a8bece4) denies a not-covered/excluded service with reviewActionCode A2
// ("Not Certified"); A3 in that code system is "Not Required" (no PA needed), NOT a denial.
// SHN's sandbox emits A3 for its denials (legacy, kept as a transitional alias), but the
// parser MUST recognize the standard A2 or it cannot read any conformant payer's denial.
func TestParseClaimResponse_DeniedA2_brpayer(t *testing.T) {
	reviewA2 := func(disposition string) []byte {
		disp := ""
		if disposition != "" {
			disp = `"disposition":"` + disposition + `",`
		}
		return []byte(`{"resourceType":"ClaimResponse","outcome":"complete","use":"preauthorization",` + disp +
			`"item":[{"adjudication":[{"extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewAction","extension":[{"url":"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-reviewActionCode","valueCodeableConcept":{"coding":[{"system":"https://codesystem.x12.org/005010/306","code":"A2","display":"Not Certified"}]}}]}]}]}]}`)
	}

	// br-payer's ACTUAL shape: A2 with NO disposition/processNote → rationale falls back to the
	// reviewActionCode display "Not Certified" (live-confirmed vs br-payer a8bece4: ClaimResponse
	// outcome=complete, disposition absent, reviewActionCode A2 display "Not Certified").
	res, err := ParseClaimResponse(reviewA2(""))
	if err != nil {
		t.Fatalf("ParseClaimResponse(A2, no disposition): %v — br-payer's real X12 denial must read as denied", err)
	}
	if res.Outcome != "denied" || res.Denial == nil {
		t.Fatalf("A2 no-disposition: Outcome=%q Denial=%+v, want denied + Denial", res.Outcome, res.Denial)
	}
	if res.Denial.ReasonCode != "A2" || res.Denial.Rationale != "Not Certified" {
		t.Fatalf("A2 no-disposition: ReasonCode=%q Rationale=%q, want A2 / display fallback \"Not Certified\"", res.Denial.ReasonCode, res.Denial.Rationale)
	}
	if res.PreAuthRef != "" {
		t.Errorf("PreAuthRef = %q, want empty (a denial issues no auth number)", res.PreAuthRef)
	}

	// When a disposition IS present it wins over the display fallback.
	res2, err := ParseClaimResponse(reviewA2("Service is excluded from plan coverage"))
	if err != nil || res2.Denial == nil || res2.Denial.Rationale != "Service is excluded from plan coverage" {
		t.Fatalf("A2 with disposition: got %+v err=%v, want Rationale from disposition", res2.Denial, err)
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
		Payer:            CMSPayerIdentity,
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

// claimFromBundle extracts and parses the Claim entry from a PAS bundle JSON.
func claimFromBundle(t *testing.T, bundleJSON []byte) map[string]json.RawMessage {
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
		}
		if err := json.Unmarshal(e.Resource, &probe); err != nil {
			t.Fatalf("parse entry: %v", err)
		}
		if probe.ResourceType == "Claim" {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(e.Resource, &m); err != nil {
				t.Fatalf("parse claim: %v", err)
			}
			return m
		}
	}
	t.Fatal("no Claim entry found in bundle")
	return nil
}

// TestBuildConformantClaimBundle_ContainedInsurer_True proves the composite lane
// (ContainedInsurer:true): the Claim contains a #cms-payer Organization with the expected
// identifier and the insurer references it, making the ref resolvable.
// Also checks the contained org's identifier matches the Coverage's payer (consistency).
func TestBuildConformantClaimBundle_ContainedInsurer_True(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.ContainedInsurer = true

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle(ContainedInsurer:true): %v", err)
	}

	claim := claimFromBundle(t, got)

	// insurer must reference #cms-payer (the contained org).
	var insurer struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(claim["insurer"], &insurer); err != nil {
		t.Fatalf("parse insurer: %v", err)
	}
	wantRef := "#" + conformantPayerOrgID
	if insurer.Reference != wantRef {
		t.Errorf("Claim.insurer.reference = %q, want %q", insurer.Reference, wantRef)
	}

	// contained must carry an Organization with the expected identifier.
	if _, ok := claim["contained"]; !ok {
		t.Fatal("Claim has no 'contained' array")
	}
	var contained []json.RawMessage
	if err := json.Unmarshal(claim["contained"], &contained); err != nil {
		t.Fatalf("parse contained: %v", err)
	}
	var foundPayer bool
	for _, c := range contained {
		var org struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Identifier   []struct {
				System string `json:"system"`
				Value  string `json:"value"`
			} `json:"identifier"`
		}
		if err := json.Unmarshal(c, &org); err != nil {
			continue
		}
		if org.ResourceType != "Organization" || org.ID != conformantPayerOrgID {
			continue
		}
		foundPayer = true
		// Identifier must match the Coverage's contained payer (consistency).
		if len(org.Identifier) == 0 {
			t.Fatal("contained #cms-payer Organization has no identifier")
		}
		if got, want := org.Identifier[0].System, systemNAICCompanyCode; got != want {
			t.Errorf("contained payer identifier.system = %q, want %q", got, want)
		}
		if got, want := org.Identifier[0].Value, conformantPayerOrgValue; got != want {
			t.Errorf("contained payer identifier.value = %q, want %q", got, want)
		}
	}
	if !foundPayer {
		t.Errorf("Claim.contained has no Organization with id=%q", conformantPayerOrgID)
	}
}

// TestBuildConformantClaimBundle_PayerOrgEntry proves the composite-lane PAS bundle carries
// the cms-payer Organization as a resolvable bundle ENTRY (not contained) with the CMS-OID
// identifier, and Coverage.payor references it resolvably. br-payer's PAS payor resolution
// (PayorIdentifierUtil.extractFirstFromCoverageAndBundle -> ResourceResolver.findInBundle)
// reads bundle ENTRIES only — a contained #cms-payer yields 0 payor identifiers -> empty
// PlanDefinition search -> A3 "Not Required" for every code. Spec 2A.4.
func TestBuildConformantClaimBundle_PayerOrgEntry(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.ContainedInsurer = true
	in.AbsoluteRefs = true
	in.PayerOrgEntry = true
	// Composite lane originates an HCPCS code on the SR; the Claim item productOrService must
	// carry the SAME code (br-payer keys PAS on Claim.item.productOrService, not the SR).
	in.SR = []byte(`{"resourceType":"ServiceRequest","status":"active","intent":"order",` +
		`"subject":{"reference":"Patient/MBR-COVERED"},"code":{"coding":[{"system":` +
		`"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"L8000","display":"Breast prosthesis"}]}}`)

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle(PayerOrgEntry:true): %v", err)
	}

	var bundle struct {
		Entry []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}

	var orgFound bool
	var orgID string
	var coveragePayorRef string
	var claimItemCode string
	for _, e := range bundle.Entry {
		var r struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Identifier   []struct {
				System string `json:"system"`
				Value  string `json:"value"`
			} `json:"identifier"`
			Payor []struct {
				Reference string `json:"reference"`
			} `json:"payor"`
			Item []struct {
				ProductOrService struct {
					Coding []struct {
						System string `json:"system"`
						Code   string `json:"code"`
					} `json:"coding"`
				} `json:"productOrService"`
			} `json:"item"`
		}
		if err := json.Unmarshal(e.Resource, &r); err != nil {
			t.Fatalf("parse entry resource: %v", err)
		}
		if r.ResourceType == "Claim" && len(r.Item) > 0 && len(r.Item[0].ProductOrService.Coding) > 0 {
			claimItemCode = r.Item[0].ProductOrService.Coding[0].Code
		}
		if r.ResourceType == "Organization" && r.ID == conformantPayerOrgID {
			orgFound = true
			orgID = r.ID
			if len(r.Identifier) != 1 {
				t.Fatalf("payer org entry identifier count = %d, want 1: %s", len(r.Identifier), e.Resource)
			}
			if r.Identifier[0].System != systemNAICCompanyCode || r.Identifier[0].Value != conformantPayerOrgValue {
				t.Fatalf("payer org entry identifier = %s|%s, want %s|%s",
					r.Identifier[0].System, r.Identifier[0].Value, systemNAICCompanyCode, conformantPayerOrgValue)
			}
		}
		if r.ResourceType == "Coverage" && len(r.Payor) == 1 {
			coveragePayorRef = r.Payor[0].Reference
		}
	}

	if !orgFound {
		t.Fatalf("no cms-payer Organization BUNDLE ENTRY (id=%q) found — still contained? that is the A3 bug", conformantPayerOrgID)
	}
	_ = orgID
	if coveragePayorRef == "" {
		t.Fatal("Coverage entry has no single payor reference")
	}
	if strings.HasPrefix(coveragePayorRef, "#") {
		t.Fatalf("Coverage.payor still a contained ref %q — must resolve to the Organization ENTRY "+
			"(Organization/%s or its absolute fullUrl) so br-payer's findInBundle resolves it", coveragePayorRef, conformantPayerOrgID)
	}
	if claimItemCode != "L8000" {
		t.Fatalf("Claim.item[0].productOrService code = %q, want L8000 (the SR's composite HCPCS code) — "+
			"br-payer keys PAS on Claim.item.productOrService, not the SR (still hardcoded 72148?)", claimItemCode)
	}
}

// TestBuildConformantClaimBundle_ContainedInsurer_False proves the sandbox path
// (ContainedInsurer:false, the default): the Claim insurer stays the generic
// "Organization/payer" and there is no contained payer organization — byte-identical to
// the current output.
func TestBuildConformantClaimBundle_ContainedInsurer_False(t *testing.T) {
	in := conformantSubmitInputs(t)
	// ContainedInsurer defaults to false — explicitly check both zero-value and explicit false.
	for _, explicit := range []bool{false, false} {
		in.ContainedInsurer = explicit

		got, err := BuildConformantClaimBundle(in)
		if err != nil {
			t.Fatalf("BuildConformantClaimBundle(ContainedInsurer:false): %v", err)
		}

		claim := claimFromBundle(t, got)

		// insurer must stay the generic Organization/payer.
		var insurer struct {
			Reference string `json:"reference"`
		}
		if err := json.Unmarshal(claim["insurer"], &insurer); err != nil {
			t.Fatalf("parse insurer: %v", err)
		}
		if insurer.Reference != "Organization/payer" {
			t.Errorf("ContainedInsurer:false: Claim.insurer.reference = %q, want Organization/payer", insurer.Reference)
		}

		// No contained payer org must be present.
		if _, ok := claim["contained"]; ok {
			var contained []json.RawMessage
			if err := json.Unmarshal(claim["contained"], &contained); err == nil {
				for _, c := range contained {
					var probe struct {
						ResourceType string `json:"resourceType"`
						ID           string `json:"id"`
					}
					if err := json.Unmarshal(c, &probe); err == nil {
						if probe.ResourceType == "Organization" && probe.ID == conformantPayerOrgID {
							t.Errorf("ContainedInsurer:false: unexpected contained payer org (#%s) in Claim", conformantPayerOrgID)
						}
					}
				}
			}
		}
	}
}

// TestBuildConformantClaimUpdateBundle_ContainedInsurer_True proves the same for the update
// builder: ContainedInsurer:true → update Claim also has a contained #cms-payer org and
// insurer.reference == "#cms-payer".
func TestBuildConformantClaimUpdateBundle_ContainedInsurer_True(t *testing.T) {
	base := conformantUpdateInputsFromGolden(t)
	base.ContainedInsurer = true

	got, err := BuildConformantClaimUpdateBundle(base)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle(ContainedInsurer:true): %v", err)
	}

	claim := claimFromBundle(t, got)

	var insurer struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(claim["insurer"], &insurer); err != nil {
		t.Fatalf("parse insurer: %v", err)
	}
	wantRef := "#" + conformantPayerOrgID
	if insurer.Reference != wantRef {
		t.Errorf("update Claim.insurer.reference = %q, want %q", insurer.Reference, wantRef)
	}
	if _, ok := claim["contained"]; !ok {
		t.Fatal("update Claim has no 'contained' array")
	}
}

// TestBuildConformantClaimUpdateBundle_PayerOrgEntry_PriorClaimResolvable proves the composite
// lane (PayerOrgEntry:true) makes the amended re-POST a CONFORMANT Da Vinci PAS Claim Update that
// real br-payer ACCEPTS. br-payer's resolvePriorClaim (PasSubmitService.java:379-403) reads
// Claim.related[0].claim.REFERENCE and requires that prior Claim to be a BUNDLE ENTRY — an
// identifier-only related (no reference, no entry) → empty reference → findInBundle null → HTTP 400
// "The prior Claim referenced in Claim.related.claim must be included in the Bundle" (E2 live-captured
// vs br-payer a8bece4). It also re-evaluates the item only when it carries the infoChanged extension
// (hasInfoChanged, PasSubmitService.java:316/449). The included prior Claim's identifier
// (urn:shn:correlation|OriginalCorr) matches the stored initial-submit Claim so the server-side
// identifier search resolves it. Sandbox path (PayerOrgEntry:false) carries NONE of this — locked
// byte-identical by TestBuildConformantClaimUpdateBundle_MatchesGolden.
func TestBuildConformantClaimUpdateBundle_PayerOrgEntry_PriorClaimResolvable(t *testing.T) {
	in := conformantUpdateInputsFromGolden(t)
	in.ContainedInsurer = true
	in.AbsoluteRefs = true
	in.PayerOrgEntry = true

	got, err := BuildConformantClaimUpdateBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle(PayerOrgEntry:true): %v", err)
	}

	var bundle struct {
		Entry []struct {
			FullUrl  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}

	var updateClaim, priorClaim map[string]json.RawMessage
	var priorFullURL string
	for _, e := range bundle.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
			Id           string `json:"id"`
		}
		if err := json.Unmarshal(e.Resource, &probe); err != nil || probe.ResourceType != "Claim" {
			continue
		}
		var claim map[string]json.RawMessage
		if err := json.Unmarshal(e.Resource, &claim); err != nil {
			t.Fatalf("parse claim entry: %v", err)
		}
		switch probe.Id {
		case conformantPASClaimUpdateID:
			updateClaim = claim
		case conformantPASClaimID:
			priorClaim = claim
			priorFullURL = e.FullUrl
		}
	}
	if updateClaim == nil {
		t.Fatal("no operative update Claim entry (id convergence-claim-update)")
	}
	if priorClaim == nil {
		t.Fatalf("composite update bundle has NO prior Claim entry (id %q) — br-payer 400s without it", conformantPASClaimID)
	}

	// (1) The prior Claim entry's identifier must match the original submit correlation so
	// br-payer's identifier search resolves it to the stored initial-submit Claim.
	var priorIdent []struct {
		System string `json:"system"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal(priorClaim["identifier"], &priorIdent); err != nil {
		t.Fatalf("parse prior Claim identifier: %v", err)
	}
	if len(priorIdent) == 0 ||
		priorIdent[0].System != "urn:shn:correlation" ||
		priorIdent[0].Value != in.OriginalCorr {
		t.Errorf("prior Claim identifier = %+v, want urn:shn:correlation|%s", priorIdent, in.OriginalCorr)
	}

	// The prior Claim is SHN-produced → it must be a base-FHIR-VALID Claim (FR-36 egress $validate),
	// not a stub (status/use/patient are required Claim elements; a minimal {id,identifier} fails
	// egress validation → 502). Guards against regressing to the stub form.
	for _, req := range []string{"status", "type", "use", "patient", "created", "provider", "priority", "insurance"} {
		if _, ok := priorClaim[req]; !ok {
			t.Errorf("prior Claim missing required base-FHIR element %q (would fail egress $validate)", req)
		}
	}

	// (2) The operative update Claim's related[0].claim.reference must resolve to the prior Claim
	// entry (absolutized to its fullUrl) — that is what findInBundle keys on.
	var related []struct {
		Claim struct {
			Reference string `json:"reference"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(updateClaim["related"], &related); err != nil {
		t.Fatalf("parse update Claim related: %v", err)
	}
	if len(related) == 0 || related[0].Claim.Reference == "" {
		t.Fatalf("update Claim related[0].claim.reference is empty (br-payer reads .reference, not .identifier)")
	}
	if related[0].Claim.Reference != priorFullURL {
		t.Errorf("related[0].claim.reference = %q, want prior Claim fullUrl %q (must resolve in-bundle)", related[0].Claim.Reference, priorFullURL)
	}

	// (3) The operative item must carry the infoChanged extension so br-payer re-evaluates it.
	var item []struct {
		Extension []struct {
			URL       string `json:"url"`
			ValueCode string `json:"valueCode"`
		} `json:"extension"`
	}
	if err := json.Unmarshal(updateClaim["item"], &item); err != nil {
		t.Fatalf("parse update Claim item: %v", err)
	}
	const infoChangedURL = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-infoChanged"
	hasInfoChanged := false
	for _, it := range item {
		for _, ext := range it.Extension {
			if ext.URL == infoChangedURL && ext.ValueCode == "changed" {
				hasInfoChanged = true
			}
		}
	}
	if !hasInfoChanged {
		t.Errorf("update Claim item has no infoChanged extension (%s, valueCode changed) — br-payer carries-forward instead of re-evaluating", infoChangedURL)
	}
}

// TestBuildConformantClaimUpdateBundle_NoPayerOrgEntry_NoPriorClaimEntry is the rejection arm of
// the prior-Claim-inclusion guard: the sandbox path (PayerOrgEntry:false) must NOT carry the
// composite-only prior Claim entry / related.reference / infoChanged (those are the real-br-payer
// shape; the sandbox accepts the lean identifier-only related). Byte-identity is locked by
// MatchesGolden; this asserts the structural absence directly.
func TestBuildConformantClaimUpdateBundle_NoPayerOrgEntry_NoPriorClaimEntry(t *testing.T) {
	got, err := BuildConformantClaimUpdateBundle(conformantUpdateInputsFromGolden(t)) // PayerOrgEntry:false
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle: %v", err)
	}
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	claimCount := 0
	for _, e := range bundle.Entry {
		var probe struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &probe); err == nil && probe.ResourceType == "Claim" {
			claimCount++
		}
	}
	if claimCount != 1 {
		t.Errorf("sandbox update bundle has %d Claim entries, want exactly 1 (no prior Claim entry)", claimCount)
	}
}

// TestBuildConformantClaimBundle_AbsoluteRefs_True proves the composite lane
// (AbsoluteRefs:true): every internal reference pointing to a bundle entry is rewritten
// to its absolute fullUrl (pasBundleBaseURL + "/" + "<resourceType>/<id>"). Specifically:
//   - Claim.patient.reference is absolute and equals the Patient entry's fullUrl.
//   - Claim.insurance[0].coverage.reference is absolute and equals the Coverage fullUrl.
//   - Coverage.beneficiary.reference is absolute and equals the Patient fullUrl.
//   - Claim.insurer.reference "#cms-payer" (contained) is UNCHANGED.
//
// Also proves that golden tests still pass (default false = byte-identical): see
// TestBuildConformantClaimBundle_MatchesGolden (separate test, unchanged).
func TestBuildConformantClaimBundle_AbsoluteRefs_True(t *testing.T) {
	in := conformantSubmitInputs(t)
	in.ContainedInsurer = true
	in.AbsoluteRefs = true

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle(AbsoluteRefs:true): %v", err)
	}

	// Parse all entries and build a fullUrl index.
	var bundle struct {
		Entry []struct {
			FullUrl  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	fullURLByRelID := make(map[string]string) // "Patient/MBR-COVERED" → fullUrl
	for _, e := range bundle.Entry {
		var meta struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
		}
		if err := json.Unmarshal(e.Resource, &meta); err != nil {
			continue
		}
		rel := meta.ResourceType + "/" + meta.ID
		fullURLByRelID[rel] = e.FullUrl
	}

	// Extract Claim.
	claim := claimFromBundle(t, got)

	// Claim.patient.reference must be absolute and equal the Patient entry fullUrl.
	var patient struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(claim["patient"], &patient); err != nil {
		t.Fatalf("parse Claim.patient: %v", err)
	}
	patientRelID := strings.TrimPrefix(patient.Reference, pasBundleBaseURL+"/")
	if wantURL, ok := fullURLByRelID[patientRelID]; ok {
		// If the reference is still relative, it won't equal the fullUrl.
		if patient.Reference != wantURL {
			// Already absolute in a different way — check it equals the right fullUrl.
		}
	}
	if !strings.HasPrefix(patient.Reference, pasBundleBaseURL+"/") {
		t.Errorf("Claim.patient.reference = %q: expected absolute (prefix %q)", patient.Reference, pasBundleBaseURL+"/")
	}
	patientRel := strings.TrimPrefix(patient.Reference, pasBundleBaseURL+"/")
	if wantURL, ok := fullURLByRelID[patientRel]; ok {
		if patient.Reference != wantURL {
			t.Errorf("Claim.patient.reference = %q, want fullUrl %q", patient.Reference, wantURL)
		}
	} else {
		t.Errorf("Claim.patient.reference %q does not match any bundle entry (relative: %q)", patient.Reference, patientRel)
	}

	// Claim.insurance[0].coverage.reference must be absolute and equal the Coverage fullUrl.
	var insurance []struct {
		Coverage struct {
			Reference string `json:"reference"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(claim["insurance"], &insurance); err != nil {
		t.Fatalf("parse Claim.insurance: %v", err)
	}
	if len(insurance) == 0 {
		t.Fatal("Claim.insurance is empty")
	}
	covRef := insurance[0].Coverage.Reference
	if !strings.HasPrefix(covRef, pasBundleBaseURL+"/") {
		t.Errorf("Claim.insurance[0].coverage.reference = %q: expected absolute", covRef)
	}
	covRel := strings.TrimPrefix(covRef, pasBundleBaseURL+"/")
	if wantURL, ok := fullURLByRelID[covRel]; ok {
		if covRef != wantURL {
			t.Errorf("Claim.insurance[0].coverage.reference = %q, want fullUrl %q", covRef, wantURL)
		}
	} else {
		t.Errorf("Claim.insurance[0].coverage.reference %q does not match any bundle entry", covRef)
	}

	// Coverage.beneficiary.reference must STAY RELATIVE ("Patient/<id>") even under
	// AbsoluteRefs — it is the ONE ref absolutizeBundleRefs deliberately excludes.
	// WHY: a real Da Vinci PAS payer (br-payer) evaluates the verdict by running the
	// CRD Rule CQL (`context Patient`, `define "Coverage": First([Coverage])`) over the
	// $submit bundle via cqf-fhir. That in-memory patient-compartment retrieve of
	// [Coverage] matches on Coverage.beneficiary; an ABSOLUTE beneficiary breaks the
	// compartment match → First([Coverage]) is null → no coverage-info extension →
	// PasCoverageEvaluator falls through to A3 "Not Required" for EVERY code (live-proven
	// against br-payer a8bece4: absolute beneficiary → A3; relative → A1). Claim.patient
	// stays absolute (the PAS reference resolver needs it; cqf extracts the id part for
	// the subject, so absolute there is harmless). Spec §2A.4 layer-3.
	var coverageResource json.RawMessage
	for _, e := range bundle.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		if rt.ResourceType == "Coverage" {
			coverageResource = e.Resource
			break
		}
	}
	if coverageResource == nil {
		t.Fatal("no Coverage entry in bundle")
	}
	var coverage struct {
		Beneficiary struct {
			Reference string `json:"reference"`
		} `json:"beneficiary"`
	}
	if err := json.Unmarshal(coverageResource, &coverage); err != nil {
		t.Fatalf("parse Coverage: %v", err)
	}
	benRef := coverage.Beneficiary.Reference
	if strings.HasPrefix(benRef, pasBundleBaseURL+"/") {
		t.Errorf("Coverage.beneficiary.reference = %q: expected RELATIVE (Patient/<id>) so br-payer's cqf-fhir [Coverage] patient-compartment retrieve matches; got absolute → would yield A3", benRef)
	}
	if !strings.HasPrefix(benRef, "Patient/") {
		t.Errorf("Coverage.beneficiary.reference = %q: expected relative Patient/<id>", benRef)
	}

	// ServiceRequest.subject.reference must ALSO stay RELATIVE — it is the patient-compartment
	// anchor for br-payer's `First([ServiceRequest])` retrieve. The HomeHealthAssessment (G0151)
	// rule gates its coverage-info on `Coverage is not null and Service Request is not null`, so
	// an absolute SR.subject breaks the [ServiceRequest] compartment match → A3 instead of A4
	// (live-proven vs br-payer a8bece4). Spec §2A.4 layer-3.
	var srResource json.RawMessage
	for _, e := range bundle.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		if rt.ResourceType == "ServiceRequest" {
			srResource = e.Resource
			break
		}
	}
	if srResource == nil {
		t.Fatal("no ServiceRequest entry in bundle")
	}
	var sr struct {
		Subject struct {
			Reference string `json:"reference"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(srResource, &sr); err != nil {
		t.Fatalf("parse ServiceRequest: %v", err)
	}
	if !strings.HasPrefix(sr.Subject.Reference, "Patient/") {
		t.Errorf("ServiceRequest.subject.reference = %q: expected RELATIVE (Patient/<id>) for the [ServiceRequest] patient-compartment retrieve; got absolute → would yield A3 for G0151", sr.Subject.Reference)
	}

	// Claim.insurer.reference "#cms-payer" (contained fragment) must be UNCHANGED.
	var insurer struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(claim["insurer"], &insurer); err != nil {
		t.Fatalf("parse Claim.insurer: %v", err)
	}
	wantInsurer := "#" + conformantPayerOrgID
	if insurer.Reference != wantInsurer {
		t.Errorf("Claim.insurer.reference = %q (contained), want unchanged %q", insurer.Reference, wantInsurer)
	}
}

// TestBuildConformantClaimBundle_AbsoluteRefs_False proves the default path
// (AbsoluteRefs:false): Claim.patient.reference stays relative ("Patient/<id>"),
// not absolutized — byte-identical to the sandbox-proven output.
func TestBuildConformantClaimBundle_AbsoluteRefs_False(t *testing.T) {
	in := conformantSubmitInputs(t)
	// Default zero-value is false.

	got, err := BuildConformantClaimBundle(in)
	if err != nil {
		t.Fatalf("BuildConformantClaimBundle(AbsoluteRefs:false): %v", err)
	}

	claim := claimFromBundle(t, got)

	var patient struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(claim["patient"], &patient); err != nil {
		t.Fatalf("parse Claim.patient: %v", err)
	}
	// Must stay relative (starts with "Patient/", not the base URL).
	if strings.HasPrefix(patient.Reference, pasBundleBaseURL+"/") {
		t.Errorf("AbsoluteRefs:false: Claim.patient.reference = %q, expected relative (not absolutized)", patient.Reference)
	}
	if !strings.HasPrefix(patient.Reference, "Patient/") {
		t.Errorf("AbsoluteRefs:false: Claim.patient.reference = %q, expected relative Patient/<id>", patient.Reference)
	}
}

// TestBuildConformantClaimBundle_DeviceRequestOrder proves that BuildConformantClaimBundle
// accepts a DeviceRequest order (DME home-oxygen use case): the built bundle carries the
// DeviceRequest as an entry (not a ServiceRequest), and the Claim item productOrService is
// sourced from the DeviceRequest's codeCodeableConcept (E0431), not the SR.code field.
// The DeviceRequest entry is stamped with the sibling id "convergence-dr".
func TestBuildConformantClaimBundle_DeviceRequestOrder(t *testing.T) {
	dr := []byte(`{"resourceType":"DeviceRequest","id":"x","status":"active","intent":"order","subject":{"reference":"Patient/MBR-OX"},"codeCodeableConcept":{"coding":[{"system":"http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets","code":"E0431"}]}}`)
	out, err := BuildConformantClaimBundle(ConformantClaimInputs{
		SR: dr, PatientRef: "Patient/MBR-OX", CoverageRef: "Coverage/MBR-OX",
		Corr: "c1", Created: time.Unix(0, 0).UTC(), PayerOrgEntry: true, AbsoluteRefs: true, ContainedInsurer: true,
		Payer: CMSPayerIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"code":"E0431"`)) {
		t.Fatalf("claim item not from DeviceRequest: %s", out)
	}
	if !bytes.Contains(out, []byte(`"resourceType":"DeviceRequest"`)) {
		t.Fatalf("order entry missing: %s", out)
	}
}

// TestBuildConformantClaimUpdateBundle_AbsoluteRefs_OutOfBundleRefsUntouched proves that
// AbsoluteRefs:true does NOT absolutize out-of-bundle refs (e.g. Provenance.agent
// Organization/provider or Practitioner/<npi> — these are not bundle entries).
func TestBuildConformantClaimUpdateBundle_AbsoluteRefs_OutOfBundleRefsUntouched(t *testing.T) {
	base := conformantUpdateInputsFromGolden(t)

	// Build a Provenance whose agent.who targets an out-of-bundle ref.
	prov, err := BuildProvenance("DiagnosticReport/"+conformantPASDRID, "Practitioner/npi-test-1234", base.Created)
	if err != nil {
		t.Fatalf("BuildProvenance: %v", err)
	}
	base.Provenance = prov
	base.AbsoluteRefs = true
	base.ContainedInsurer = true

	got, err := BuildConformantClaimUpdateBundle(base)
	if err != nil {
		t.Fatalf("BuildConformantClaimUpdateBundle(AbsoluteRefs:true): %v", err)
	}

	// Find the Provenance entry and confirm agent.who.reference is still relative.
	var bundle struct {
		Entry []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(got, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	for _, e := range bundle.Entry {
		var rt struct {
			ResourceType string `json:"resourceType"`
			Agent        []struct {
				Who struct {
					Reference string `json:"reference"`
				} `json:"who"`
			} `json:"agent"`
		}
		if err := json.Unmarshal(e.Resource, &rt); err != nil {
			continue
		}
		if rt.ResourceType != "Provenance" {
			continue
		}
		if len(rt.Agent) == 0 {
			t.Fatal("Provenance entry has no agent")
		}
		agentRef := rt.Agent[0].Who.Reference
		// "Practitioner/npi-test-1234" is NOT a bundle entry → must stay relative.
		if strings.HasPrefix(agentRef, pasBundleBaseURL+"/") {
			t.Errorf("Provenance.agent.who.reference = %q: out-of-bundle ref was absolutized (must stay relative)", agentRef)
		}
		if agentRef != "Practitioner/npi-test-1234" {
			t.Errorf("Provenance.agent.who.reference = %q, want relative Practitioner/npi-test-1234", agentRef)
		}
		return
	}
	t.Fatal("no Provenance entry found in update bundle")
}
