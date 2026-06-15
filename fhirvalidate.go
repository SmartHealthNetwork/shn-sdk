// Package shnsdk fhirvalidate: FHIR $validate client surface, promoted from
// internal/fhirvalidate. Gateways validate resources on egress and
// ingress (production-grade, AI-2 means validation lives at the gateways, not
// the payload-blind Hub). Substrate consumers continue to resolve these symbols
// via the internal/fhirvalidate delegating shim.
package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Result reports whether a resource conforms to a profile and lists any issues.
type Result struct {
	Valid  bool
	Issues []string
}

// Validator validates a FHIR resource (JSON) against an IG profile URL.
type Validator interface {
	Validate(ctx context.Context, resourceJSON []byte, profile string) (Result, error)
}

// ---------------------------------------------------------------------------
// FakeValidator
// ---------------------------------------------------------------------------

// FakeValidator is a hermetic test double. By default everything is valid; set
// RejectIfContains to mark payloads containing that substring invalid. Set Err
// to simulate a validator outage (Validate returns Result{}, Err).
type FakeValidator struct {
	RejectIfContains string
	Err              error
}

func NewFakeValidator() *FakeValidator { return &FakeValidator{} }

func (f *FakeValidator) Validate(_ context.Context, resourceJSON []byte, _ string) (Result, error) {
	if f.Err != nil {
		return Result{}, f.Err
	}
	if f.RejectIfContains != "" && bytes.Contains(resourceJSON, []byte(f.RejectIfContains)) {
		return Result{Valid: false, Issues: []string{"fake: contains " + f.RejectIfContains}}, nil
	}
	return Result{Valid: true}, nil
}

// ---------------------------------------------------------------------------
// HTTPValidator — calls the HL7 validator_cli.jar running in server mode
// ---------------------------------------------------------------------------

// HTTPValidator calls the HL7 validator_cli.jar running in server mode
// (validator_cli.jar -server). It posts to POST /validate and maps the
// JSON response to a Result.
type HTTPValidator struct {
	BaseURL string
	SV      string
	IGs     []string
	Client  *http.Client
}

// NewHTTPValidator returns an HTTPValidator targeting baseURL with FHIR R4
// defaults (sv=4.0.1, no IGs).
func NewHTTPValidator(baseURL string) *HTTPValidator {
	return &HTTPValidator{
		BaseURL: baseURL,
		SV:      "4.0.1",
		IGs:     []string{},
		Client:  http.DefaultClient,
	}
}

// Compile-time interface check.
var _ Validator = (*HTTPValidator)(nil)

// validatorRequest is the JSON body sent to POST /validate.
type validatorRequest struct {
	CLIContext      cliContext       `json:"cliContext"`
	FilesToValidate []fileToValidate `json:"filesToValidate"`
}

type cliContext struct {
	SV       string   `json:"sv"`
	IGs      []string `json:"igs"`
	Profiles []string `json:"profiles"`
}

type fileToValidate struct {
	FileName    string `json:"fileName"`
	FileContent string `json:"fileContent"`
	FileType    string `json:"fileType"`
}

// validatorResponse is the JSON body returned by the validator server.
type validatorResponse struct {
	Outcomes []outcome `json:"outcomes"`
}

type outcome struct {
	Issues []issue `json:"issues"`
}

type issue struct {
	Severity string `json:"severity"`
	Details  string `json:"details"`
	Type     string `json:"type"`
	Line     int    `json:"line"`
}

// Validate posts resourceJSON to the HL7 validator server and returns a Result.
// Network errors, non-2xx responses, and JSON decode errors all return a
// non-nil error (the caller decides how to treat a validator outage).
func (h *HTTPValidator) Validate(ctx context.Context, resourceJSON []byte, profile string) (Result, error) {
	profiles := []string{}
	if profile != "" {
		profiles = []string{profile}
	}

	body := validatorRequest{
		CLIContext: cliContext{
			SV:       h.SV,
			IGs:      h.IGs,
			Profiles: profiles,
		},
		FilesToValidate: []fileToValidate{
			{
				FileName:    "resource.json",
				FileContent: string(resourceJSON),
				FileType:    "json",
			},
		},
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.BaseURL+"/validate", bytes.NewReader(encoded))
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator server returned %d", resp.StatusCode)
	}

	var vresp validatorResponse
	if err := json.NewDecoder(resp.Body).Decode(&vresp); err != nil {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator decode response: %w", err)
	}

	var issues []string
	for _, oc := range vresp.Outcomes {
		for _, iss := range oc.Issues {
			if iss.Severity == "error" || iss.Severity == "fatal" {
				issues = append(issues, iss.Details)
			}
		}
	}

	return Result{
		Valid:  len(issues) == 0,
		Issues: issues,
	}, nil
}

// ---------------------------------------------------------------------------
// OperationValidator — validates via the standard FHIR $validate operation
// ---------------------------------------------------------------------------

// OperationValidator validates via the standard FHIR $validate operation against
// a FHIR server base URL (e.g. a HAPI FHIR server). This is the substrate's REAL
// per-message validator (FR-36).
type OperationValidator struct {
	BaseURL string
	Client  *http.Client
}

// NewOperationValidator returns an OperationValidator targeting a FHIR base URL
// (e.g. http://hapi:8080/fhir), using the shared wire HTTP client.
func NewOperationValidator(baseURL string) *OperationValidator {
	return &OperationValidator{
		BaseURL: baseURL,
		Client:  NewClient(),
	}
}

// Compile-time interface check.
var _ Validator = (*OperationValidator)(nil)

// operationOutcome is the subset of the FHIR OperationOutcome returned by
// $validate that we care about.
type operationOutcome struct {
	ResourceType string `json:"resourceType"`
	Issue        []struct {
		Severity    string `json:"severity"`
		Diagnostics string `json:"diagnostics"`
	} `json:"issue"`
}

// Validate POSTs resourceJSON to {BaseURL}/{resourceType}/$validate (with an
// optional ?profile=) and maps the returned OperationOutcome to a Result. VALID
// means no issue with severity error or fatal. If the response body parses as an
// OperationOutcome its issues are used regardless of HTTP status; a non-2xx
// response whose body is NOT a parseable OperationOutcome is treated as a
// validator outage and returns an error (gateways fail closed on errors).
func (o *OperationValidator) Validate(ctx context.Context, resourceJSON []byte, profile string) (Result, error) {
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(resourceJSON, &probe); err != nil {
		return Result{}, fmt.Errorf("shnsdk: operationvalidator parse resourceType: %w", err)
	}
	if probe.ResourceType == "" {
		return Result{}, fmt.Errorf("shnsdk: operationvalidator resource is missing resourceType")
	}

	endpoint := o.BaseURL + "/" + probe.ResourceType + "/$validate"
	if profile != "" {
		endpoint += "?profile=" + url.QueryEscape(profile)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(resourceJSON))
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: operationvalidator build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")

	client := o.Client
	if client == nil {
		client = NewClient()
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: operationvalidator do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: operationvalidator read response: %w", err)
	}

	var oo operationOutcome
	parsed := json.Unmarshal(body, &oo) == nil && oo.ResourceType == "OperationOutcome"

	// A parseable OperationOutcome wins regardless of HTTP status (a request-body
	// parse failure returns an OO with an error issue, sometimes with non-2xx).
	if !parsed {
		if resp.StatusCode/100 != 2 {
			return Result{}, fmt.Errorf("shnsdk: operationvalidator server returned %d with non-OperationOutcome body: %s",
				resp.StatusCode, string(body))
		}
		return Result{}, fmt.Errorf("shnsdk: operationvalidator 2xx response was not a parseable OperationOutcome: %s",
			string(body))
	}

	var issues []string
	for _, iss := range oo.Issue {
		if iss.Severity == "error" || iss.Severity == "fatal" {
			issues = append(issues, iss.Diagnostics)
		}
	}

	return Result{
		Valid:  len(issues) == 0,
		Issues: issues,
	}, nil
}
