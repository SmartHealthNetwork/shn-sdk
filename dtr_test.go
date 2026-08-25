package shnsdk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// demoQuestionnaireJSON is the FLAT variant of the demo lumbar-MRI
// prior-auth questionnaire: the supported canonical url + the seven leaves
// FillQuestionnaire knows, with no groups. The test-only fixture
// (demoLumbarQuestionnaire, lumbar_fixture_test.go) GROUPS those same leaves — see
// nestedDemoQuestionnaireJSON in dtr_nested_test.go for that shape. The flat
// variant stays here because the fill must keep working for a flat questionnaire
// too (a payer may serve the leaves ungrouped), and the flat-shape tests below
// pin exactly that.
const demoQuestionnaireJSON = `{
  "resourceType": "Questionnaire",
  "id": "pa-lumbar-mri",
  "url": "http://smarthealth.network/fhir/Questionnaire/pa-lumbar-mri",
  "version": "1.0.0",
  "status": "active",
  "item": [
    {"linkId": "conservative-therapy-weeks", "type": "integer", "text": "Weeks of conservative therapy completed"},
    {"linkId": "neuro-deficit", "type": "boolean", "text": "Progressive neurological deficit present?"},
    {"linkId": "prior-imaging", "type": "boolean", "text": "Prior imaging performed?"},
    {"linkId": "prior-surgery", "type": "boolean", "text": "Prior lumbar surgery?"},
    {"linkId": "high-disability", "type": "boolean", "text": "High disability index flag?"},
    {"linkId": "patient-reported-required", "type": "boolean", "text": "Patient-reported functional status required?"},
    {"linkId": "functional-status-oswestry", "type": "text", "text": "Oswestry disability index (clinician-attested)"}
  ]
}`

func mbrCoveredCC() ClinicalContext {
	return ClinicalContext{
		ConditionCode:            "M51.16",
		ConditionRef:             "Condition/cond-m5116",
		ConservativeTherapyWeeks: 6,
		ConservativeTherapyRef:   "Observation/obs-pt-weeks",
		ConservativeDate:         "2026-05-20",
		NeuroDeficit:             false,
		NeuroDeficitRef:          "Observation/obs-neuro",
		PriorImaging:             true,
		PriorImagingRef:          "DiagnosticReport/dr-xray",
	}
}

func mbrCoveredQC() QRContext {
	return QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC),
	}
}

// TestFillQuestionnaireFailsLoudOnUnknownCanonical: FillQuestionnaire must FAIL
// LOUDLY (naming the supported canonical) on a questionnaire whose url it does not
// recognize, and emit NO QR — never a half-filled one.
func TestFillQuestionnaireFailsLoudOnUnknownCanonical(t *testing.T) {
	bogus := `{
      "resourceType": "Questionnaire",
      "url": "http://example.org/fhir/Questionnaire/some-other-form",
      "status": "active",
      "item": [{"linkId": "conservative-therapy-weeks", "type": "integer"}]
    }`

	qr, err := FillQuestionnaire([]byte(bogus), mbrCoveredCC(), mbrCoveredQC())
	if err == nil {
		t.Fatalf("FillQuestionnaire accepted an unsupported questionnaire; want a fail-loud error")
	}
	if qr != nil {
		t.Errorf("FillQuestionnaire returned a non-nil QR (%q) on an unsupported questionnaire; must NEVER emit a half-filled QR", qr)
	}
	// The error must name BOTH the rejected canonical and the supported one.
	if !strings.Contains(err.Error(), SupportedQuestionnaireCanonical) {
		t.Errorf("error %q does not name the supported canonical %q", err, SupportedQuestionnaireCanonical)
	}
	if !strings.Contains(err.Error(), "some-other-form") {
		t.Errorf("error %q does not name the rejected canonical", err)
	}
}

// TestFillQuestionnaireHappyPath: the demo questionnaire fills into a non-empty
// QR that unmarshals as a completed QuestionnaireResponse.
func TestFillQuestionnaireHappyPath(t *testing.T) {
	qr, err := FillQuestionnaire([]byte(demoQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	if len(qr) == 0 {
		t.Fatal("FillQuestionnaire returned an empty QR")
	}
	var probe struct {
		ResourceType string `json:"resourceType"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(qr, &probe); err != nil {
		t.Fatalf("QR does not unmarshal: %v", err)
	}
	if probe.ResourceType != "QuestionnaireResponse" {
		t.Errorf("resourceType = %q, want QuestionnaireResponse", probe.ResourceType)
	}
	if probe.Status != "completed" {
		t.Errorf("status = %q, want completed", probe.Status)
	}
}

// TestFillQuestionnaireAtLine_RegressionFence: the legacy FillQuestionnaire is
// byte-identical to AtLine("2.0") for the same inputs (per-line parity).
func TestFillQuestionnaireAtLine_RegressionFence(t *testing.T) {
	legacy, err := FillQuestionnaire([]byte(demoQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
	if err != nil {
		t.Fatalf("FillQuestionnaire: %v", err)
	}
	atLine, err := FillQuestionnaireAtLine("2.0", []byte(demoQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
	if err != nil {
		t.Fatalf("FillQuestionnaireAtLine(2.0): %v", err)
	}
	if string(legacy) != string(atLine) {
		t.Fatalf("FillQuestionnaire != FillQuestionnaireAtLine(\"2.0\"):\n legacy: %s\n atLine: %s", legacy, atLine)
	}
}

// TestFillQuestionnaireAtLine_UnknownLineErrors: fail-closed rejection (per-line parity).
func TestFillQuestionnaireAtLine_UnknownLineErrors(t *testing.T) {
	if _, err := FillQuestionnaireAtLine("9.9", []byte(demoQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC()); err == nil {
		t.Fatal("FillQuestionnaireAtLine(\"9.9\") = nil error, want an error")
	}
}

// qrExtensionProbe is the shape used to inspect a built QR's top-level extensions
// (the per-line coverage/origin-code shape tests).
type qrExtensionProbe struct {
	Extension []struct {
		URL            string `json:"url"`
		ValueReference *struct {
			Reference string `json:"reference"`
		} `json:"valueReference"`
	} `json:"extension"`
	Item []struct {
		LinkId string `json:"linkId"`
		Answer []struct {
			Extension []struct {
				URL       string `json:"url"`
				Extension []struct {
					URL       string `json:"url"`
					ValueCode string `json:"valueCode"`
				} `json:"extension"`
			} `json:"extension"`
		} `json:"answer"`
	} `json:"item"`
}

// assertQRCoverageAndOriginByLine is shared by FillQuestionnaireAtLine and
// FillQuestionnaireFromAnswersAtLine's per-line shape tests: at "2.0"/"2.1" the
// coverage reference rides a qr-context extension (2 qr-context total: coverage +
// order) and 0 qr-coverage extensions; at "2.2" it rides a dedicated qr-coverage
// extension (1 qr-coverage + 1 qr-context, for the order only) — DTR package differential
// (StructureDefinition-dtr-questionnaireresponse.json + StructureDefinition-
// qr-coverage.json). wantOriginCode, when non-empty, is checked against every
// answer's information-origin source code found in the QR (FillQuestionnaireAtLine
// only — FillQuestionnaireFromAnswersAtLine stamps "manual", which never changes by
// line, so its test passes "").
func assertQRCoverageAndOriginByLine(t *testing.T, line string, raw []byte, coverageRef, orderRef, wantOriginCode string) {
	t.Helper()
	var qr qrExtensionProbe
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("line %s: QR does not unmarshal: %v", line, err)
	}
	var qrContextCount, qrCoverageCount int
	var qrContextRefs, qrCoverageRefs []string
	for _, ext := range qr.Extension {
		switch ext.URL {
		case qrContextExt:
			qrContextCount++
			if ext.ValueReference != nil {
				qrContextRefs = append(qrContextRefs, ext.ValueReference.Reference)
			}
		case qrCoverageExt:
			qrCoverageCount++
			if ext.ValueReference != nil {
				qrCoverageRefs = append(qrCoverageRefs, ext.ValueReference.Reference)
			}
		}
	}
	if line == "2.2" {
		if qrCoverageCount != 1 {
			t.Errorf("line 2.2: qr-coverage extension count = %d, want 1", qrCoverageCount)
		}
		if len(qrCoverageRefs) != 1 || qrCoverageRefs[0] != coverageRef {
			t.Errorf("line 2.2: qr-coverage refs = %v, want [%q]", qrCoverageRefs, coverageRef)
		}
		if qrContextCount != 1 {
			t.Errorf("line 2.2: qr-context extension count = %d, want 1 (order only)", qrContextCount)
		}
		if len(qrContextRefs) != 1 || qrContextRefs[0] != orderRef {
			t.Errorf("line 2.2: qr-context refs = %v, want [%q]", qrContextRefs, orderRef)
		}
	} else {
		if qrCoverageCount != 0 {
			t.Errorf("line %s: qr-coverage extension count = %d, want 0 (2.2-only)", line, qrCoverageCount)
		}
		if qrContextCount != 2 {
			t.Errorf("line %s: qr-context extension count = %d, want 2 (coverage+order)", line, qrContextCount)
		}
		wantRefs := map[string]bool{coverageRef: false, orderRef: false}
		for _, r := range qrContextRefs {
			if _, ok := wantRefs[r]; ok {
				wantRefs[r] = true
			}
		}
		for r, seen := range wantRefs {
			if !seen {
				t.Errorf("line %s: qr-context refs %v missing %q", line, qrContextRefs, r)
			}
		}
	}
	if wantOriginCode == "" {
		return
	}
	found := false
	for _, it := range qr.Item {
		for _, ans := range it.Answer {
			for _, ext := range ans.Extension {
				if ext.URL != informationOriginExt {
					continue
				}
				for _, sub := range ext.Extension {
					if sub.URL != "source" {
						continue
					}
					found = true
					if sub.ValueCode != wantOriginCode {
						t.Errorf("line %s: answer source code = %q, want %q", line, sub.ValueCode, wantOriginCode)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("line %s: no information-origin source code found in any answer", line)
	}
}

// TestFillQuestionnaireAtLine_CoverageAndOriginByLine: DTR 2.2's SingleCoverageConstraint
// (qr-coverage extension) and AutoOriginSourceCode ("auto-client") deltas — both
// verified in the DTR package differential.
func TestFillQuestionnaireAtLine_CoverageAndOriginByLine(t *testing.T) {
	cc := mbrCoveredCC()
	qc := mbrCoveredQC()
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		raw, err := FillQuestionnaireAtLine(line, []byte(demoQuestionnaireJSON), cc, qc)
		if err != nil {
			t.Fatalf("FillQuestionnaireAtLine(%s): %v", line, err)
		}
		def, ok := DTRLineDef(line)
		if !ok {
			t.Fatalf("DTRLineDef(%q) ok = false", line)
		}
		assertQRCoverageAndOriginByLine(t, line, raw, qc.CoverageRef, qc.OrderRef, def.AutoOriginSourceCode)
	}
}

// TestFillQuestionnaireAtLine_IntendedUseSystemByLine (Finding E): the DTR
// intendedUse extension required-binds to the CRD DocReason ValueSet
// (StructureDefinition-intendedUse.json Extension.value[x], strength=required at
// DTR 2.1.0 and 2.2.0). CRD moved the "withpa" concept: ValueSet-DocReason draws
// it from CodeSystem/temp at CRD 2.1.0 and from the renamed
// CodeSystem/coverage-information-codes at CRD 2.2.1 (where CodeSystem-temp no
// longer defines it). Same code, new system at 2.2.
func TestFillQuestionnaireAtLine_IntendedUseSystemByLine(t *testing.T) {
	cc := mbrCoveredCC()
	qc := mbrCoveredQC()
	wantSystem := map[string]string{
		"2.0": "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp",
		"2.1": "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp",
		"2.2": "http://hl7.org/fhir/us/davinci-crd/CodeSystem/coverage-information-codes",
	}
	for _, line := range []string{"2.0", "2.1", "2.2"} {
		raw, err := FillQuestionnaireAtLine(line, []byte(demoQuestionnaireJSON), cc, qc)
		if err != nil {
			t.Fatalf("FillQuestionnaireAtLine(%s): %v", line, err)
		}
		var qr struct {
			Extension []struct {
				URL                  string `json:"url"`
				ValueCodeableConcept *struct {
					Coding []struct {
						System string `json:"system"`
						Code   string `json:"code"`
					} `json:"coding"`
				} `json:"valueCodeableConcept"`
			} `json:"extension"`
		}
		if err := json.Unmarshal(raw, &qr); err != nil {
			t.Fatalf("line %s: QR does not unmarshal: %v", line, err)
		}
		var saw bool
		for _, ext := range qr.Extension {
			if ext.URL != intendedUseExt {
				continue
			}
			saw = true
			if ext.ValueCodeableConcept == nil || len(ext.ValueCodeableConcept.Coding) != 1 {
				t.Fatalf("line %s: intendedUse value = %+v, want exactly one coding", line, ext.ValueCodeableConcept)
			}
			c := ext.ValueCodeableConcept.Coding[0]
			if c.System != wantSystem[line] || c.Code != "withpa" {
				t.Errorf("line %s: intendedUse coding = %s#%s, want %s#withpa", line, c.System, c.Code, wantSystem[line])
			}
		}
		if !saw {
			t.Fatalf("line %s: QR carries no intendedUse extension", line)
		}
	}
}

// TestBuildQuestionnairePackageAtLine_RegressionFence: the legacy
// BuildQuestionnairePackage is byte-identical to AtLine("2.0", q, nil) (per-line parity).
func TestBuildQuestionnairePackageAtLine_RegressionFence(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"u"}`)
	legacy, err := BuildQuestionnairePackage(q)
	if err != nil {
		t.Fatalf("BuildQuestionnairePackage: %v", err)
	}
	atLine, err := BuildQuestionnairePackageAtLine("2.0", q, nil)
	if err != nil {
		t.Fatalf("BuildQuestionnairePackageAtLine(2.0): %v", err)
	}
	if string(legacy) != string(atLine) {
		t.Fatalf("BuildQuestionnairePackage != BuildQuestionnairePackageAtLine(\"2.0\", nil):\n legacy: %s\n atLine: %s", legacy, atLine)
	}
}

// TestBuildQuestionnairePackageAtLine_UnknownLineErrors: fail-closed rejection (per-line parity).
func TestBuildQuestionnairePackageAtLine_UnknownLineErrors(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"u"}`)
	if _, err := BuildQuestionnairePackageAtLine("9.9", q, nil); err == nil {
		t.Fatal("BuildQuestionnairePackageAtLine(\"9.9\") = nil error, want an error")
	}
}

// TestBuildQuestionnairePackageAtLine_QRRequiredAt22 is the rejection test for the
// verified DTR-QPackageBundle delta (per-line parity): Bundle.entry:questionnaireResponse is
// unconstrained at 2.0, optional (min=0) at 2.1, and REQUIRED (min=1) at 2.2. A nil
// questionnaireResponse is accepted at 2.0/2.1 (1-entry package, unchanged) and
// rejected at 2.2 (never a silently non-conformant package).
func TestBuildQuestionnairePackageAtLine_QRRequiredAt22(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"http://x/q"}`)

	for _, line := range []string{"2.0", "2.1"} {
		pkg, err := BuildQuestionnairePackageAtLine(line, q, nil)
		if err != nil {
			t.Fatalf("line %s: nil QR must be accepted: %v", line, err)
		}
		var probe struct {
			Entry []json.RawMessage `json:"entry"`
		}
		if err := json.Unmarshal(pkg, &probe); err != nil {
			t.Fatalf("line %s: unmarshal package: %v", line, err)
		}
		if len(probe.Entry) != 1 {
			t.Errorf("line %s: entry count = %d, want 1 (no QR supplied)", line, len(probe.Entry))
		}
	}

	// 2.2 with nil QR: rejected (profile requires the entry).
	if _, err := BuildQuestionnairePackageAtLine("2.2", q, nil); err == nil {
		t.Fatal("BuildQuestionnairePackageAtLine(\"2.2\", q, nil) = nil error, want an error (QR required)")
	}

	// 2.2 with a supplied QR: accepted, 2-entry package, second entry is the QR at its
	// derived fullUrl.
	qr := []byte(`{"resourceType":"QuestionnaireResponse","id":"qr-1","status":"completed"}`)
	pkg, err := BuildQuestionnairePackageAtLine("2.2", q, qr)
	if err != nil {
		t.Fatalf("BuildQuestionnairePackageAtLine(2.2, q, qr): %v", err)
	}
	var probe struct {
		Entry []struct {
			FullUrl  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(pkg, &probe); err != nil {
		t.Fatalf("unmarshal 2.2 package: %v", err)
	}
	if len(probe.Entry) != 2 {
		t.Fatalf("2.2 entry count = %d, want 2 (Questionnaire + QuestionnaireResponse)", len(probe.Entry))
	}
	if probe.Entry[1].FullUrl != "https://shn.example/fhir/QuestionnaireResponse/qr-1" {
		t.Errorf("QR entry fullUrl = %q, want the derived https://shn.example/fhir/QuestionnaireResponse/qr-1", probe.Entry[1].FullUrl)
	}
	var qrProbe struct {
		ResourceType string `json:"resourceType"`
		ID           string `json:"id"`
	}
	if err := json.Unmarshal(probe.Entry[1].Resource, &qrProbe); err != nil {
		t.Fatalf("unmarshal QR entry resource: %v", err)
	}
	if qrProbe.ResourceType != "QuestionnaireResponse" || qrProbe.ID != "qr-1" {
		t.Errorf("QR entry resource = %+v, want the qr-1 QuestionnaireResponse", qrProbe)
	}
}

// TestBuildQuestionnairePackageAtLine_QRMissingIDErrors: a supplied QR with no id
// cannot be given a resolvable fullUrl — never fabricated, so this errors.
func TestBuildQuestionnairePackageAtLine_QRMissingIDErrors(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"http://x/q"}`)
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed"}`)
	if _, err := BuildQuestionnairePackageAtLine("2.2", q, qr); err == nil {
		t.Fatal("BuildQuestionnairePackageAtLine(2.2, q, qr-without-id) = nil error, want an error")
	}
}

// TestBuildQuestionnaireFetchAndParseURL: the fetch request round-trips its canonical,
// and ParseQuestionnaireURL reads the url out of the demo questionnaire.
func TestBuildQuestionnaireFetchAndParseURL(t *testing.T) {
	fetch, err := BuildQuestionnaireFetch(SupportedQuestionnaireCanonical)
	if err != nil {
		t.Fatalf("BuildQuestionnaireFetch: %v", err)
	}
	var req QuestionnaireFetchRequest
	if err := json.Unmarshal(fetch, &req); err != nil {
		t.Fatalf("unmarshal fetch: %v", err)
	}
	if req.Canonical != SupportedQuestionnaireCanonical {
		t.Errorf("fetch canonical = %q, want %q", req.Canonical, SupportedQuestionnaireCanonical)
	}

	url, err := ParseQuestionnaireURL([]byte(demoQuestionnaireJSON))
	if err != nil {
		t.Fatalf("ParseQuestionnaireURL: %v", err)
	}
	if url != SupportedQuestionnaireCanonical {
		t.Errorf("parsed url = %q, want %q", url, SupportedQuestionnaireCanonical)
	}
}

// TestBuildQuestionnaireFetchWithCoverage_RoundTrip: the coverage-carrying builder
// round-trips both the canonical AND the exact coverage bytes.
func TestBuildQuestionnaireFetchWithCoverage_RoundTrip(t *testing.T) {
	covJSON := []byte(`{"resourceType":"Coverage","id":"coverage-MBR-COVERED","status":"active"}`)
	fetch, err := BuildQuestionnaireFetchWithCoverage(SupportedQuestionnaireCanonical, covJSON)
	if err != nil {
		t.Fatalf("BuildQuestionnaireFetchWithCoverage: %v", err)
	}
	var req QuestionnaireFetchRequest
	if err := json.Unmarshal(fetch, &req); err != nil {
		t.Fatalf("unmarshal fetch: %v", err)
	}
	if req.Canonical != SupportedQuestionnaireCanonical {
		t.Errorf("fetch canonical = %q, want %q", req.Canonical, SupportedQuestionnaireCanonical)
	}
	if !bytes.Equal([]byte(req.Coverage), covJSON) {
		t.Errorf("fetch coverage = %s, want exact bytes %s", req.Coverage, covJSON)
	}
}

// TestBuildQuestionnaireFetchWithCoverage_EmptyCanonicalErrors: an empty canonical
// is rejected (fail-closed, never a half-built fetch request).
func TestBuildQuestionnaireFetchWithCoverage_EmptyCanonicalErrors(t *testing.T) {
	covJSON := []byte(`{"resourceType":"Coverage","id":"coverage-MBR-COVERED","status":"active"}`)
	if _, err := BuildQuestionnaireFetchWithCoverage("", covJSON); err == nil {
		t.Fatal("BuildQuestionnaireFetchWithCoverage(\"\", covJSON) = nil error, want an error")
	}
}

// TestBuildQuestionnaireFetchWithCoverage_EmptyCoverageErrors: an empty coverageJSON
// is rejected — this builder exists specifically to carry a coverage; an empty one
// means the caller should use BuildQuestionnaireFetch instead.
func TestBuildQuestionnaireFetchWithCoverage_EmptyCoverageErrors(t *testing.T) {
	if _, err := BuildQuestionnaireFetchWithCoverage(SupportedQuestionnaireCanonical, nil); err == nil {
		t.Fatal("BuildQuestionnaireFetchWithCoverage(canonical, nil) = nil error, want an error")
	}
	if _, err := BuildQuestionnaireFetchWithCoverage(SupportedQuestionnaireCanonical, []byte{}); err == nil {
		t.Fatal("BuildQuestionnaireFetchWithCoverage(canonical, []byte{}) = nil error, want an error")
	}
}

// TestDemoLumbarQuestionnaireFixture_Unmarshals: the sdk package's own test-only
// demoLumbarQuestionnaire fixture returns bytes that unmarshal as a fhir.Questionnaire
// whose canonical == SupportedQuestionnaireCanonical.
func TestDemoLumbarQuestionnaireFixture_Unmarshals(t *testing.T) {
	data := demoLumbarQuestionnaire()
	if len(data) == 0 {
		t.Fatal("demoLumbarQuestionnaire returned empty bytes")
	}

	// Must unmarshal as fhir.Questionnaire (samply model).
	var q struct {
		ResourceType string  `json:"resourceType"`
		URL          *string `json:"url"`
		Version      *string `json:"version"`
	}
	if err := json.Unmarshal(data, &q); err != nil {
		t.Fatalf("demoLumbarQuestionnaire does not unmarshal: %v", err)
	}
	if q.ResourceType != "Questionnaire" {
		t.Errorf("resourceType = %q, want Questionnaire", q.ResourceType)
	}
	if q.URL == nil || *q.URL == "" {
		t.Fatal("demoLumbarQuestionnaire: url field is absent or empty")
	}
	// The canonical (url|version when version is set) must equal SupportedQuestionnaireCanonical.
	// questionnaireCanonical in the SDK appends "|version" when version is set; we test
	// the raw url here (the canonical constant has no version suffix) and verify via
	// ParseQuestionnaireURL which reads the url field directly.
	got, err := ParseQuestionnaireURL(data)
	if err != nil {
		t.Fatalf("ParseQuestionnaireURL on demoLumbarQuestionnaire: %v", err)
	}
	if got != SupportedQuestionnaireCanonical {
		t.Errorf("canonical = %q, want %q", got, SupportedQuestionnaireCanonical)
	}
}

// TestDemoLumbarQuestionnaireFixture_FillAccepts: FillQuestionnaire accepts
// demoLumbarQuestionnaire() and produces a non-empty completed QR, proving the SDK's own
// autofill accepts the fixture.
func TestDemoLumbarQuestionnaireFixture_FillAccepts(t *testing.T) {
	data := demoLumbarQuestionnaire()
	qc := QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    mbrCoveredQC().Authored,
	}
	qr, err := FillQuestionnaire(data, DemoLumbarContext(), qc)
	if err != nil {
		t.Fatalf("FillQuestionnaire(demoLumbarQuestionnaire, DemoLumbarContext): %v", err)
	}
	if len(qr) == 0 {
		t.Fatal("FillQuestionnaire returned empty QR")
	}
	var probe struct {
		ResourceType string `json:"resourceType"`
		Status       string `json:"status"`
		Item         []any  `json:"item"`
	}
	if err := json.Unmarshal(qr, &probe); err != nil {
		t.Fatalf("resulting QR does not unmarshal: %v", err)
	}
	if probe.ResourceType != "QuestionnaireResponse" {
		t.Errorf("resourceType = %q, want QuestionnaireResponse", probe.ResourceType)
	}
	if probe.Status != "completed" {
		t.Errorf("status = %q, want completed", probe.Status)
	}
	if len(probe.Item) == 0 {
		t.Error("resulting QR has no items; expected at least one filled item")
	}
}

// TestDemoLumbarQuestionnaireFixture_DeterministicBytes: demoLumbarQuestionnaire is
// deterministic — two calls return identical bytes.
func TestDemoLumbarQuestionnaireFixture_DeterministicBytes(t *testing.T) {
	a := demoLumbarQuestionnaire()
	b := demoLumbarQuestionnaire()
	if string(a) != string(b) {
		t.Errorf("demoLumbarQuestionnaire is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	// Verify callers get independent copies (mutation isolation).
	if len(a) > 0 {
		a[0] ^= 0xFF
		c := demoLumbarQuestionnaire()
		if c[0] == a[0] {
			t.Error("demoLumbarQuestionnaire shares underlying storage; mutation was visible to subsequent call")
		}
	}
}

// TestBuildQuestionnairePackage_ByteShape pins the canonical $questionnaire-package
// wire shape (§6.2): json.Marshal sorts map keys, so the bytes are
// {"entry":[{"fullUrl":<url>,"resource":<q>}],"resourceType":"Bundle","type":"collection"}.
// The fullUrl (the Questionnaire's canonical url) is required by a FHIR collection Bundle.
// This MUST stay byte-identical to the substrate gateway's buildQuestionnairePackage
// (test/sdkparity enforces cross-module parity).
func TestBuildQuestionnairePackage_ByteShape(t *testing.T) {
	q := []byte(`{"resourceType":"Questionnaire","url":"u"}`)
	pkg, err := BuildQuestionnairePackage(q)
	if err != nil {
		t.Fatalf("BuildQuestionnairePackage: %v", err)
	}
	want := `{"entry":[{"fullUrl":"u","resource":{"resourceType":"Questionnaire","url":"u"}}],"resourceType":"Bundle","type":"collection"}`
	if string(pkg) != want {
		t.Errorf("package wire drift:\n got=%s\nwant=%s", pkg, want)
	}
}

// TestBuildQuestionnairePackage_InvalidJSON: a non-JSON questionnaire is rejected with
// an error (never a malformed package).
func TestBuildQuestionnairePackage_InvalidJSON(t *testing.T) {
	pkg, err := BuildQuestionnairePackage([]byte("not json"))
	if err == nil {
		t.Fatalf("BuildQuestionnairePackage accepted invalid json; want an error")
	}
	if pkg != nil {
		t.Errorf("BuildQuestionnairePackage returned non-nil package (%q) on invalid json", pkg)
	}
}

// TestBuildAndExtractQuestionnairePackage_RoundTrip: wrap then extract returns the
// Questionnaire bytes verbatim.
func TestBuildAndExtractQuestionnairePackage_RoundTrip(t *testing.T) {
	q := demoLumbarQuestionnaire()
	pkg, err := BuildQuestionnairePackage(q)
	if err != nil {
		t.Fatalf("BuildQuestionnairePackage: %v", err)
	}
	got, err := ExtractQuestionnaireFromPackage(pkg)
	if err != nil {
		t.Fatalf("ExtractQuestionnaireFromPackage: %v", err)
	}
	if string(got) != string(q) {
		t.Errorf("round-trip drift:\n got=%s\nwant=%s", got, q)
	}
}

// TestExtractQuestionnaireFromPackage_NoQuestionnaire: a package whose entries contain
// no Questionnaire errors with the strict, package-only message.
func TestExtractQuestionnaireFromPackage_NoQuestionnaire(t *testing.T) {
	pkg := []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Library"}}]}`)
	got, err := ExtractQuestionnaireFromPackage(pkg)
	if err == nil {
		t.Fatalf("ExtractQuestionnaireFromPackage accepted a Questionnaire-free package; want an error")
	}
	if got != nil {
		t.Errorf("ExtractQuestionnaireFromPackage returned non-nil (%q) when no Questionnaire present", got)
	}
	if !strings.Contains(err.Error(), "$questionnaire-package response contains no Questionnaire") {
		t.Errorf("error %q does not name the strict no-Questionnaire failure", err)
	}
}

// TestExtractQuestionnaireFromPackage_BareQuestionnaireRejected: STRICT package-only —
// a bare Questionnaire (no entry array) is NOT tolerated; it errors (no dual-shape
// fallback). This is the full-uniform contract (§6.2).
func TestExtractQuestionnaireFromPackage_BareQuestionnaireRejected(t *testing.T) {
	got, err := ExtractQuestionnaireFromPackage(demoLumbarQuestionnaire())
	if err == nil {
		t.Fatalf("ExtractQuestionnaireFromPackage accepted a bare Questionnaire; full-uniform requires a package")
	}
	if got != nil {
		t.Errorf("ExtractQuestionnaireFromPackage returned non-nil (%q) on a bare Questionnaire", got)
	}
}

// TestExtractQuestionnaireFromPackage_ParametersWrapper: a conformant DTR
// $questionnaire-package response profiled on dtr-qpackage-output-parameters — a
// Parameters resource carrying the collection Bundle at
// parameter[name=="packagebundle"].resource — is unwrapped to its inner Bundle
// before the existing bundle-walk, so the Questionnaire entry is still found.
func TestExtractQuestionnaireFromPackage_ParametersWrapper(t *testing.T) {
	inner := `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Questionnaire","id":"q1","url":"http://example.org/fhir/Questionnaire/HomeOxygenDispatch"}}]}`
	wrapped := []byte(`{"resourceType":"Parameters","parameter":[{"name":"packagebundle","resource":` + inner + `}]}`)
	q, err := ExtractQuestionnaireFromPackage(wrapped)
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	var got struct{ ResourceType, ID string }
	_ = json.Unmarshal(q, &got)
	if got.ResourceType != "Questionnaire" || got.ID != "q1" {
		t.Fatalf("wrapper: got %+v", got)
	}
}

// TestExtractQuestionnaireFromPackage_WrapperWithoutQuestionnaire_Rejected: a
// Parameters{packagebundle} wrapper whose inner Bundle carries no Questionnaire
// entry is rejected exactly like the bare-Bundle case — the unwrap does not weaken
// the strict "must contain a Questionnaire" contract.
func TestExtractQuestionnaireFromPackage_WrapperWithoutQuestionnaire_Rejected(t *testing.T) {
	wrapped := []byte(`{"resourceType":"Parameters","parameter":[{"name":"packagebundle","resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Library","id":"l1"}}]}}]}`)
	if _, err := ExtractQuestionnaireFromPackage(wrapped); err == nil {
		t.Fatal("wrapper without a Questionnaire must be rejected")
	}
}

// TestExtractQuestionnaireFromPackage_Garbage: malformed JSON errors (never a panic).
func TestExtractQuestionnaireFromPackage_Garbage(t *testing.T) {
	if _, err := ExtractQuestionnaireFromPackage([]byte("not json")); err == nil {
		t.Fatalf("ExtractQuestionnaireFromPackage accepted garbage json; want an error")
	}
}

// TestDemoLumbarQuestionnaireFixture_IsCQLBacked: the demo questionnaire carries the
// cqf-library + per-item initialExpression extensions so a real operated $populate engine
// can populate it (the managed FillQuestionnaire ignores these and fills by linkId).
func TestDemoLumbarQuestionnaireFixture_IsCQLBacked(t *testing.T) {
	s := string(demoLumbarQuestionnaire())
	for _, want := range []string{
		"cqf-library",
		"Library/LumbarMRICQL",
		"sdc-questionnaire-initialExpression",
		"ConservativeTherapyWeeks",
		"PriorSurgery",
		"HighDisability",
		"PatientReportedRequired",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("questionnaire missing %q:\n%s", want, s)
		}
	}
	// NO launchContext — the SDC launchContext CodeSystem is unresolvable by the US-Core runtime
	// egress validator (it errors on the unknown code system); the engine binds the CQL Patient
	// context from the $populate subject parameter instead.
	if strings.Contains(s, "launchContext") {
		t.Fatalf("questionnaire must NOT carry launchContext (US-Core egress-validator unfriendly):\n%s", s)
	}
}

// TestValidatePatientAnswer covers both registered patient items: the Oswestry numeric item
// (the reference-payer UC-07 path) and the HomeHealthAssessment free-text functional-status item "3.2"
// (provider-data UC-07, the patient-authored narrative). The "3.2" rule is load-bearing: the
// phg patient-dtr responder calls ValidatePatientAnswer as the un-bypassable AI-10 signing guard,
// so without a "3.2" rule the provider-data UC-07 patient-dtr leg is rejected at phg.
func TestValidatePatientAnswer(t *testing.T) {
	// Oswestry: 0–100 integer.
	if err := ValidatePatientAnswer("functional-status-oswestry", "42"); err != nil {
		t.Fatalf("oswestry 42: unexpected error %v", err)
	}
	if err := ValidatePatientAnswer("functional-status-oswestry", "200"); err == nil {
		t.Fatalf("oswestry 200: want out-of-range error, got nil")
	}
	if err := ValidatePatientAnswer("functional-status-oswestry", "not-a-number"); err == nil {
		t.Fatalf("oswestry non-integer: want error, got nil")
	}

	// HHA "3.2": free-text functional-limitations narrative — any non-empty string is conformant.
	if err := ValidatePatientAnswer("3.2", "I have trouble walking without help."); err != nil {
		t.Fatalf(`HHA "3.2" non-empty narrative: unexpected error %v`, err)
	}
	if err := ValidatePatientAnswer("3.2", "   "); err == nil {
		t.Fatalf(`HHA "3.2" whitespace-only: want non-empty error, got nil (the signer must not attest an empty functional-status item)`)
	}

	// An unregistered item is still rejected (the signer must not attest a constraint it can't enforce).
	if err := ValidatePatientAnswer("unknown-item", "x"); err == nil {
		t.Fatalf("unknown item: want 'no attestation rule' error, got nil")
	}
}

// TestAmendQRWithItemSupersedes pins that amending a QuestionnaireResponse with
// an item whose linkId is ALREADY present replaces that item rather than
// appending a second one, leaving exactly one item for the linkId.
//
// This is what an amendment means: the clinician- or patient-attested answer
// supersedes whatever the populate step left there. Appending instead produced
// two items carrying the same linkId with different answers, which is a
// QuestionnaireResponse that asserts two conflicting values for one question —
// and adjudication cannot resolve it, so the reader refuses the whole
// submission rather than guessing. The UC-06 clinician resume hit exactly that:
// the populate step had already emitted a functional-status-oswestry item, the
// attestation appended a second, and the prior-auth came back 422.
//
// Note the pre-existing item here carries an UNSIGNED answer, which is the shape
// that makes the first submit pend in the first place — so this is the real
// sequence, not a contrived one.
func TestAmendQRWithItemSupersedes(t *testing.T) {
	const linkID = "functional-status-oswestry"
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"` + linkID + `","answer":[{"valueString":"unsigned-auto"}]}` +
		`]}`)

	item, err := BuildManualAttestedItem(linkID, "42",
		Attestation{NPI: "1999999999", Text: "I attest these are my clinical findings.", When: "2026-08-19"})
	if err != nil {
		t.Fatalf("BuildManualAttestedItem: %v", err)
	}

	got, err := AmendQRWithItem(qr, item)
	if err != nil {
		t.Fatalf("AmendQRWithItem: %v", err)
	}

	var doc struct {
		Item []struct {
			LinkId    string `json:"linkId"`
			Extension []struct {
				Url string `json:"url"`
			} `json:"extension"`
			Answer []struct {
				ValueString *string `json:"valueString"`
			} `json:"answer"`
		} `json:"item"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("decode amended QR: %v", err)
	}

	var matches int
	var attested bool
	var answer string
	for _, it := range doc.Item {
		if it.LinkId != linkID {
			continue
		}
		matches++
		for _, e := range it.Extension {
			if e.Url == ClinicianAttestationExt {
				attested = true
			}
		}
		if len(it.Answer) == 1 && it.Answer[0].ValueString != nil {
			answer = *it.Answer[0].ValueString
		}
	}
	if matches != 1 {
		t.Fatalf("amended QR carries %d items with linkId %q, want exactly 1 — an amendment supersedes, it does not duplicate: %s", matches, linkID, got)
	}
	if !attested {
		t.Errorf("the surviving %q item is not the attested one (no clinician-attestation extension): %s", linkID, got)
	}
	if answer != "42" {
		t.Errorf("surviving answer = %q, want the attested value %q (the auto value must not win)", answer, "42")
	}

	// The other item must be untouched — superseding is scoped to the amended linkId.
	var others int
	for _, it := range doc.Item {
		if it.LinkId == "conservative-therapy-weeks" {
			others++
		}
	}
	if others != 1 {
		t.Errorf("unrelated item count = %d, want 1 (superseding must not disturb other items)", others)
	}
}

// TestAmendQRWithItemAppendsWhenAbsent is the restraint half: when the linkId is
// NOT already present, the amendment still appends, so every existing caller and
// golden is byte-unchanged.
func TestAmendQRWithItemAppendsWhenAbsent(t *testing.T) {
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]}]}`)
	item, err := BuildManualAttestedItem("functional-status-oswestry", "42",
		Attestation{NPI: "1999999999", Text: "I attest these are my clinical findings.", When: "2026-08-19"})
	if err != nil {
		t.Fatalf("BuildManualAttestedItem: %v", err)
	}
	got, err := AmendQRWithItem(qr, item)
	if err != nil {
		t.Fatalf("AmendQRWithItem: %v", err)
	}
	var doc struct {
		Item []struct {
			LinkId string `json:"linkId"`
		} `json:"item"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Item) != 2 {
		t.Fatalf("item count = %d, want 2 (append when the linkId is absent): %s", len(doc.Item), got)
	}
	if doc.Item[0].LinkId != "conservative-therapy-weeks" || doc.Item[1].LinkId != "functional-status-oswestry" {
		t.Fatalf("append must preserve order and put the new item last, got %+v", doc.Item)
	}
}

// TestAmendedQRIsUnambiguousAfterSupersede is the end of the chain this exists to
// protect: the amended QR must be READABLE, not merely well-shaped. Before superseding
// it carried TWO functional-status-oswestry items — one clinical fact stated twice, which
// no reader can resolve without arbitrarily picking one. The retired stub adjudicator
// refused exactly that shape; the refusal was policy-shaped, the DEFECT is not, so the
// property is asserted directly: exactly one occurrence survives, carrying the attested
// value and its attestation.
func TestAmendedQRIsUnambiguousAfterSupersede(t *testing.T) {
	const linkID = "functional-status-oswestry"
	qr := []byte(`{"resourceType":"QuestionnaireResponse","status":"completed","item":[` +
		`{"linkId":"conservative-therapy-weeks","answer":[{"valueInteger":6}]},` +
		`{"linkId":"high-disability","answer":[{"valueBoolean":true}]},` +
		`{"linkId":"` + linkID + `","answer":[{"valueString":"unsigned-auto"}]}` +
		`]}`)
	item, err := BuildManualAttestedItem(linkID, "42",
		Attestation{NPI: "1234567893", Text: "I attest these are my clinical findings.", When: "2026-08-19"})
	if err != nil {
		t.Fatalf("BuildManualAttestedItem: %v", err)
	}
	amended, err := AmendQRWithItem(qr, item)
	if err != nil {
		t.Fatalf("AmendQRWithItem: %v", err)
	}
	if n := strings.Count(string(amended), `"`+linkID+`"`); n != 1 {
		t.Fatalf("%s occurs %d times in the amended QR, want exactly 1 (an ambiguous QR is unreadable):\n%s", linkID, n, amended)
	}
	if !strings.Contains(string(amended), `"valueString":"42"`) || strings.Contains(string(amended), "unsigned-auto") {
		t.Fatalf("the attested value did not supersede the placeholder:\n%s", amended)
	}
	if !strings.Contains(string(amended), ClinicianAttestationExt) {
		t.Fatalf("the surviving item lost its attestation:\n%s", amended)
	}
}
