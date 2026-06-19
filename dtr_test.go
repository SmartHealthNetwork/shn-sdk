package shnsdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sandboxQuestionnaireJSON is a minimal-but-faithful copy of the sandbox
// lumbar-MRI prior-auth questionnaire (the substrate's dtr.QuestionnaireFor output): the
// supported canonical url + the known items FillQuestionnaire fills. The SDK can't
// import the substrate fixture, so the recognized shape lives here; the parity test
// (test/sdkparity/dtr_parity_test.go) proves the fill matches the substrate on the
// REAL substrate-built questionnaire.
const sandboxQuestionnaireJSON = `{
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

// TestFillQuestionnaireHappyPath: the sandbox questionnaire fills into a non-empty
// QR that unmarshals as a completed QuestionnaireResponse.
func TestFillQuestionnaireHappyPath(t *testing.T) {
	qr, err := FillQuestionnaire([]byte(sandboxQuestionnaireJSON), mbrCoveredCC(), mbrCoveredQC())
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

// TestBuildQuestionnaireFetchAndParseURL: the fetch request round-trips its canonical,
// and ParseQuestionnaireURL reads the url out of the sandbox questionnaire.
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

	url, err := ParseQuestionnaireURL([]byte(sandboxQuestionnaireJSON))
	if err != nil {
		t.Fatalf("ParseQuestionnaireURL: %v", err)
	}
	if url != SupportedQuestionnaireCanonical {
		t.Errorf("parsed url = %q, want %q", url, SupportedQuestionnaireCanonical)
	}
}

// TestSandboxLumbarQuestionnaire_Unmarshals: SandboxLumbarQuestionnaire returns bytes
// that unmarshal as a fhir.Questionnaire whose canonical == QuestionnaireCanonicalLumbarMRI.
func TestSandboxLumbarQuestionnaire_Unmarshals(t *testing.T) {
	data := SandboxLumbarQuestionnaire()
	if len(data) == 0 {
		t.Fatal("SandboxLumbarQuestionnaire returned empty bytes")
	}

	// Must unmarshal as fhir.Questionnaire (samply model).
	var q struct {
		ResourceType string  `json:"resourceType"`
		URL          *string `json:"url"`
		Version      *string `json:"version"`
	}
	if err := json.Unmarshal(data, &q); err != nil {
		t.Fatalf("SandboxLumbarQuestionnaire does not unmarshal: %v", err)
	}
	if q.ResourceType != "Questionnaire" {
		t.Errorf("resourceType = %q, want Questionnaire", q.ResourceType)
	}
	if q.URL == nil || *q.URL == "" {
		t.Fatal("SandboxLumbarQuestionnaire: url field is absent or empty")
	}
	// The canonical (url|version when version is set) must equal QuestionnaireCanonicalLumbarMRI.
	// questionnaireCanonical in the SDK appends "|version" when version is set; we test
	// the raw url here (the canonical constant has no version suffix) and verify via
	// ParseQuestionnaireURL which reads the url field directly.
	got, err := ParseQuestionnaireURL(data)
	if err != nil {
		t.Fatalf("ParseQuestionnaireURL on SandboxLumbarQuestionnaire: %v", err)
	}
	if got != QuestionnaireCanonicalLumbarMRI {
		t.Errorf("canonical = %q, want %q", got, QuestionnaireCanonicalLumbarMRI)
	}
}

// TestSandboxLumbarQuestionnaire_FillAccepts: FillQuestionnaire accepts
// SandboxLumbarQuestionnaire() and produces a non-empty completed QR, proving the
// SDK's own autofill accepts the fixture.
func TestSandboxLumbarQuestionnaire_FillAccepts(t *testing.T) {
	data := SandboxLumbarQuestionnaire()
	qc := QRContext{
		PatientRef:  "Patient/MBR-COVERED",
		CoverageRef: "Coverage/MBR-COVERED",
		OrderRef:    "ServiceRequest/sr-MBR-COVERED",
		Authored:    mbrCoveredQC().Authored,
	}
	qr, err := FillQuestionnaire(data, SandboxUC03Context(), qc)
	if err != nil {
		t.Fatalf("FillQuestionnaire(SandboxLumbarQuestionnaire, SandboxUC03Context): %v", err)
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

// TestSandboxLumbarQuestionnaire_DeterministicBytes: SandboxLumbarQuestionnaire is
// deterministic — two calls return identical bytes.
func TestSandboxLumbarQuestionnaire_DeterministicBytes(t *testing.T) {
	a := SandboxLumbarQuestionnaire()
	b := SandboxLumbarQuestionnaire()
	if string(a) != string(b) {
		t.Errorf("SandboxLumbarQuestionnaire is non-deterministic:\n a=%s\n b=%s", a, b)
	}
	// Verify callers get independent copies (mutation isolation).
	if len(a) > 0 {
		a[0] ^= 0xFF
		c := SandboxLumbarQuestionnaire()
		if c[0] == a[0] {
			t.Error("SandboxLumbarQuestionnaire shares underlying storage; mutation was visible to subsequent call")
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
	q := SandboxLumbarQuestionnaire()
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
	got, err := ExtractQuestionnaireFromPackage(SandboxLumbarQuestionnaire())
	if err == nil {
		t.Fatalf("ExtractQuestionnaireFromPackage accepted a bare Questionnaire; full-uniform requires a package")
	}
	if got != nil {
		t.Errorf("ExtractQuestionnaireFromPackage returned non-nil (%q) on a bare Questionnaire", got)
	}
}

// TestExtractQuestionnaireFromPackage_Garbage: malformed JSON errors (never a panic).
func TestExtractQuestionnaireFromPackage_Garbage(t *testing.T) {
	if _, err := ExtractQuestionnaireFromPackage([]byte("not json")); err == nil {
		t.Fatalf("ExtractQuestionnaireFromPackage accepted garbage json; want an error")
	}
}

// TestSandboxLumbarQuestionnaire_IsCQLBacked: the sandbox questionnaire carries the
// cqf-library + per-item initialExpression extensions so a real operated $populate engine
// can populate it (the managed FillQuestionnaire ignores these and fills by linkId).
func TestSandboxLumbarQuestionnaire_IsCQLBacked(t *testing.T) {
	s := string(SandboxLumbarQuestionnaire())
	for _, want := range []string{
		"cqf-library",
		"Library/LumbarMRICQL",
		"sdc-questionnaire-launchContext",
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
}
