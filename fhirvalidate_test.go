package shnsdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// ---------------------------------------------------------------------------
// FakeValidator tests (ported from internal/fhirvalidate/validator_test.go)
// ---------------------------------------------------------------------------

// FakeValidator must satisfy the Validator interface.
var _ shnsdk.Validator = (*shnsdk.FakeValidator)(nil)

func TestFakeValidator_ValidByDefault(t *testing.T) {
	v := shnsdk.NewFakeValidator()
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"CoverageEligibilityRequest"}`), "http://example/profile")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("fake should pass by default, got issues %v", res.Issues)
	}
}

func TestFakeValidator_RejectsConfiguredResource(t *testing.T) {
	v := shnsdk.NewFakeValidator()
	v.RejectIfContains = "BAD" // mark certain payloads invalid
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"BAD"}`), "http://example/profile")
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Issues) == 0 {
		t.Fatal("fake should reject payloads containing the configured marker, with issues")
	}
}

// ---------------------------------------------------------------------------
// HTTPValidator tests (ported from internal/fhirvalidate/httpvalidator_test.go)
// ---------------------------------------------------------------------------

func TestHTTPValidator_ValidWhenNoErrors(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"outcomes": []any{
				map[string]any{
					"issues": []any{
						map[string]any{"severity": "information", "details": "All OK"},
					},
				},
			},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewHTTPValidator(stub.URL)
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "http://example/profile")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected Valid=true, got Valid=false; issues: %v", res.Issues)
	}
	if len(res.Issues) != 0 {
		t.Fatalf("expected no issues, got %v", res.Issues)
	}
}

func TestHTTPValidator_InvalidOnError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"outcomes": []any{
				map[string]any{
					"issues": []any{
						map[string]any{"severity": "error", "details": "Foo"},
					},
				},
			},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewHTTPValidator(stub.URL)
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatalf("expected no error (error is a valid outcome), got %v", err)
	}
	if res.Valid {
		t.Fatal("expected Valid=false for error-severity issue")
	}
	found := false
	for _, iss := range res.Issues {
		if iss == "Foo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Issues to contain 'Foo', got %v", res.Issues)
	}
}

func TestHTTPValidator_RequestShape(t *testing.T) {
	const profile = "http://hl7.org/fhir/us/davinci-hrex/StructureDefinition/hrex-coverage"
	resource := []byte(`{"resourceType":"Coverage","id":"test"}`)

	var capturedBody struct {
		CLIContext struct {
			SV       string   `json:"sv"`
			IGs      []string `json:"igs"`
			Profiles []string `json:"profiles"`
		} `json:"cliContext"`
		FilesToValidate []struct {
			FileName    string `json:"fileName"`
			FileContent string `json:"fileContent"`
			FileType    string `json:"fileType"`
		} `json:"filesToValidate"`
	}

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"outcomes": []any{
				map[string]any{"issues": []any{}},
			},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewHTTPValidator(stub.URL)
	_, err := v.Validate(context.Background(), resource, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedBody.FilesToValidate) != 1 {
		t.Fatalf("expected 1 file to validate, got %d", len(capturedBody.FilesToValidate))
	}
	if capturedBody.FilesToValidate[0].FileContent != string(resource) {
		t.Fatalf("fileContent mismatch: got %q, want %q",
			capturedBody.FilesToValidate[0].FileContent, string(resource))
	}
	found := false
	for _, p := range capturedBody.CLIContext.Profiles {
		if p == profile {
			found = true
		}
	}
	if !found {
		t.Fatalf("profile %q not found in cliContext.profiles: %v", profile, capturedBody.CLIContext.Profiles)
	}
}

func TestHTTPValidator_ServerErrorPropagates(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer stub.Close()

	v := shnsdk.NewHTTPValidator(stub.URL)
	_, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err == nil {
		t.Fatal("expected non-nil error for HTTP 500, got nil")
	}
}

// ---------------------------------------------------------------------------
// OperationValidator tests (ported from internal/fhirvalidate/operationvalidator_test.go)
// ---------------------------------------------------------------------------

func TestOperationValidator_ValidWithWarnings(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "OperationOutcome",
			"issue": []any{
				map[string]any{"severity": "warning", "diagnostics": "dom-6 narrative"},
				map[string]any{"severity": "information", "diagnostics": "All OK"},
			},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewOperationValidator(stub.URL)
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !res.Valid {
		t.Fatalf("warnings/info only must be Valid, got issues %v", res.Issues)
	}
}

func TestOperationValidator_InvalidOnError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "OperationOutcome",
			"issue": []any{
				map[string]any{"severity": "error", "diagnostics": "Object must have some content"},
			},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewOperationValidator(stub.URL)
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatalf("error issue is a valid outcome, expected no transport error, got %v", err)
	}
	if res.Valid {
		t.Fatal("expected Valid=false for an error-severity issue")
	}
	found := false
	for _, iss := range res.Issues {
		if iss == "Object must have some content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Issues to contain the error diagnostics, got %v", res.Issues)
	}
}

func TestOperationValidator_PostsToTypeValidatePathWithProfile(t *testing.T) {
	const profile = "http://example.org/StructureDefinition/foo"
	var gotPath, gotProfile, gotContentType string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotProfile = r.URL.Query().Get("profile")
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/fhir+json")
		json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "OperationOutcome",
			"issue":        []any{},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewOperationValidator(stub.URL)
	_, err := v.Validate(context.Background(), []byte(`{"resourceType":"CoverageEligibilityRequest"}`), profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/CoverageEligibilityRequest/$validate" {
		t.Fatalf("path = %q, want /CoverageEligibilityRequest/$validate", gotPath)
	}
	if gotProfile != profile {
		t.Fatalf("profile query = %q, want %q", gotProfile, profile)
	}
	if gotContentType != "application/fhir+json" {
		t.Fatalf("Content-Type = %q, want application/fhir+json", gotContentType)
	}
}

func TestOperationValidator_ErrorOnNon2xxUnparseableBody(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("not json at all <<<"))
	}))
	defer stub.Close()

	v := shnsdk.NewOperationValidator(stub.URL)
	_, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err == nil {
		t.Fatal("expected an error for 500 + unparseable body (validator outage)")
	}
}

func TestOperationValidator_UsesOperationOutcomeDespiteNon2xx(t *testing.T) {
	// A request-body parse failure returns an OperationOutcome with an error
	// issue, possibly with a non-2xx status. The OO issues must win.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/fhir+json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "OperationOutcome",
			"issue": []any{
				map[string]any{"severity": "error", "diagnostics": "parse failure"},
			},
		})
	}))
	defer stub.Close()

	v := shnsdk.NewOperationValidator(stub.URL)
	res, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
	if err != nil {
		t.Fatalf("a parseable OperationOutcome must not surface as a transport error, got %v", err)
	}
	if res.Valid {
		t.Fatal("expected Valid=false from the OperationOutcome error issue")
	}
}

func TestOperationValidator_MissingResourceTypeErrors(t *testing.T) {
	v := shnsdk.NewOperationValidator("http://unused.example")
	_, err := v.Validate(context.Background(), []byte(`{"status":"active"}`), "")
	if err == nil {
		t.Fatal("expected an error when resourceType is missing")
	}
}
