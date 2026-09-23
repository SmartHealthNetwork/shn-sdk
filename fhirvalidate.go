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
	"strings"
)

// Result is a lossy compatibility view of profile validation. Issues contains
// only error/fatal diagnostics; it cannot certify terminology coverage.
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
	// Evidence is explicit synthetic support; nil proves neither check.
	Evidence *ValidationEvidence
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

// ValidateEvidence returns explicitly configured synthetic evidence. Legacy valid
// defaults never imply support. RejectIfContains applies to the profile only.
func (f *FakeValidator) ValidateEvidence(ctx context.Context, body []byte, _ string) (ValidationEvidence, error) {
	if err := ctx.Err(); err != nil {
		return unavailableValidation("execution-unavailable"), err
	}
	if f.Err != nil {
		return unavailableValidation("execution-unavailable"), f.Err
	}
	if f.Evidence == nil {
		return unavailableValidation("synthetic-support-unconfigured"), nil
	}
	ev := *f.Evidence
	ev.Profile.Issues = append([]ValidationIssue(nil), ev.Profile.Issues...)
	ev.Terminology.Issues = append([]ValidationIssue(nil), ev.Terminology.Issues...)
	if f.RejectIfContains != "" && bytes.Contains(body, []byte(f.RejectIfContains)) {
		ev.Profile = ValidationCheckEvidence{State: ValidationInvalid, Code: "synthetic-rejection", Issues: []ValidationIssue{{Severity: "error", Code: "synthetic-rejection"}}}
	}
	return ev, nil
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
	Outcomes []json.RawMessage `json:"outcomes"`
}

// issue decodes both wire shapes: the HL7 validator-wrapper's
// ValidationMessage (level/message) and the legacy severity/details shape. A
// level outside the recognised set is a decode failure, never a silent valid.
type issue struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Severity  string `json:"severity"`
	Details   string `json:"details"`
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	Line      int    `json:"line"`
}

var recognisedLevels = map[string]bool{"FATAL": true, "ERROR": true, "WARNING": true, "INFORMATION": true}

func (i issue) level() string {
	if i.Level != "" {
		return strings.ToUpper(i.Level)
	}
	return strings.ToUpper(i.Severity)
}

func (i issue) text() string {
	if i.Message != "" {
		return i.Message
	}
	return i.Details
}

// Validate returns the lossy compatibility view of the shared wire decoder.
func (h *HTTPValidator) Validate(ctx context.Context, resourceJSON []byte, profile string) (Result, error) {
	v, err := h.validate(ctx, resourceJSON, profile)
	return v.legacy(err)
}

// ValidateEvidence preserves safe issues and separate support evidence in one call.
func (h *HTTPValidator) ValidateEvidence(ctx context.Context, resourceJSON []byte, profile string) (ValidationEvidence, error) {
	v, err := h.validate(ctx, resourceJSON, profile)
	return v.evidence, err
}

func (h *HTTPValidator) validate(ctx context.Context, resourceJSON []byte, profile string) (result decodedValidation, err error) {
	attempted := false
	defer func() { result.evidence.ExecutionAttempted = attempted }()
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
		return executionFailure(fmt.Errorf("shnsdk: httpvalidator marshal request: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.BaseURL+"/validate", bytes.NewReader(encoded))
	if err != nil {
		return executionFailure(fmt.Errorf("shnsdk: httpvalidator build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}

	if err := ctx.Err(); err != nil {
		return executionFailure(err)
	}
	attempted = true
	resp, err := client.Do(req)
	if err != nil {
		return executionFailure(fmt.Errorf("shnsdk: httpvalidator do request: %w", err))
	}
	defer resp.Body.Close()

	return decodeValidationResponse(resp, true)
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

// Validate returns a profile compatibility view. Only pinned 400/422 content
// failures may be verdicts; all other non-2xx responses are execution failures.
func (o *OperationValidator) Validate(ctx context.Context, resourceJSON []byte, profile string) (Result, error) {
	v, err := o.validate(ctx, resourceJSON, profile)
	return v.legacy(err)
}

// ValidateEvidence decodes the response once, preserving warnings and separating
// profile results from the unproven terminology coverage of this adapter.
func (o *OperationValidator) ValidateEvidence(ctx context.Context, resourceJSON []byte, profile string) (ValidationEvidence, error) {
	v, err := o.validate(ctx, resourceJSON, profile)
	return v.evidence, err
}

func (o *OperationValidator) validate(ctx context.Context, resourceJSON []byte, profile string) (result decodedValidation, err error) {
	attempted := false
	defer func() { result.evidence.ExecutionAttempted = attempted }()
	var probe struct {
		ResourceType string `json:"resourceType"`
	}
	if err := json.Unmarshal(resourceJSON, &probe); err != nil {
		return executionFailure(fmt.Errorf("shnsdk: operationvalidator parse resourceType: %w", err))
	}
	if probe.ResourceType == "" {
		return executionFailure(fmt.Errorf("shnsdk: operationvalidator resource is missing resourceType"))
	}

	endpoint := o.BaseURL + "/" + probe.ResourceType + "/$validate"
	if profile != "" {
		endpoint += "?profile=" + url.QueryEscape(profile)
	}

	body := resourceJSON
	if probe.ResourceType == "Parameters" {
		// A bare Parameters body supplies $validate's arguments. Nest the exact
		// resource under test so its own parameters are not mistaken for those
		// arguments; preserve all original lexical values and whitespace.
		body = append(append([]byte(`{"resourceType":"Parameters","parameter":[{"name":"resource","resource":`), resourceJSON...), []byte(`}]}`)...)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return executionFailure(fmt.Errorf("shnsdk: operationvalidator build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/fhir+json")

	client := o.Client
	if client == nil {
		client = NewClient()
	}

	if err := ctx.Err(); err != nil {
		return executionFailure(err)
	}
	attempted = true
	resp, err := client.Do(req)
	if err != nil {
		return executionFailure(fmt.Errorf("shnsdk: operationvalidator do request: %w", err))
	}
	defer resp.Body.Close()

	return decodeValidationResponse(resp, false)
}

// decodeValidationResponse is the only response interpreter for both public
// views. It reads the complete bounded body before accepting any verdict.
func decodeValidationResponse(resp *http.Response, wrapper bool) (result decodedValidation, err error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	defer func() {
		if err != nil {
			captured := raw
			if len(captured) > MaxResponseBytes {
				captured = captured[:MaxResponseBytes]
			}
			err = &ValidationResponseError{status: resp.StatusCode, raw: bytes.Clone(captured), cause: err}
		}
	}()

	if err != nil {
		return executionFailure(fmt.Errorf("shnsdk: validator read response: %w", err))
	}
	if len(raw) > MaxResponseBytes {
		return executionFailure(fmt.Errorf("shnsdk: validator response exceeds %d bytes", MaxResponseBytes))
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != 400 && resp.StatusCode != 422 {
		return executionFailure(fmt.Errorf("shnsdk: validator server returned %d", resp.StatusCode))
	}

	var issues []wireValidationIssue
	if wrapper {
		var response validatorResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			return executionFailure(fmt.Errorf("shnsdk: validator malformed response"))
		}
		if len(response.Outcomes) == 0 {
			return executionFailure(fmt.Errorf("shnsdk: httpvalidator response carries no outcomes"))
		}
		for _, outcome := range response.Outcomes {
			var oc struct {
				Issues json.RawMessage `json:"issues"`
			}
			if err := json.Unmarshal(outcome, &oc); err != nil {
				return executionFailure(fmt.Errorf("shnsdk: validator malformed outcome"))
			}
			rawIssues := bytes.TrimSpace(oc.Issues)
			if len(rawIssues) == 0 || rawIssues[0] != '[' {
				return executionFailure(fmt.Errorf("shnsdk: validator missing or malformed issues"))
			}
			var decoded []issue
			if err := json.Unmarshal(rawIssues, &decoded); err != nil {
				return executionFailure(fmt.Errorf("shnsdk: validator malformed issues"))
			}
			for _, iss := range decoded {
				if !recognisedLevels[iss.level()] {
					return executionFailure(fmt.Errorf("shnsdk: httpvalidator unrecognised issue level"))
				}
				issues = append(issues, wireValidationIssue{severity: strings.ToLower(iss.level()), code: iss.Type, text: iss.text(), messageID: iss.MessageID})
			}
		}
	} else {
		var outcome struct {
			ResourceType string `json:"resourceType"`
			Issue        []struct {
				Severity    string   `json:"severity"`
				Code        string   `json:"code"`
				Diagnostics string   `json:"diagnostics"`
				Expression  []string `json:"expression"`
				Details     struct {
					Coding []struct {
						System string `json:"system"`
						Code   string `json:"code"`
					} `json:"coding"`
				} `json:"details"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(raw, &outcome); err != nil || outcome.ResourceType != "OperationOutcome" || len(outcome.Issue) == 0 {
			return executionFailure(fmt.Errorf("shnsdk: validator malformed OperationOutcome"))
		}
		for _, iss := range outcome.Issue {
			switch iss.Severity {
			case "fatal", "error", "warning", "information":
			default:
				return executionFailure(fmt.Errorf("shnsdk: validator unrecognised issue severity"))
			}
			if iss.Code == "" {
				return executionFailure(fmt.Errorf("shnsdk: validator missing issue code"))
			}
			messageID := ""
			if len(iss.Details.Coding) > 0 {
				messageID = "unmapped"
				if len(iss.Details.Coding) == 1 && iss.Details.Coding[0].System == "http://hl7.org/fhir/java-core-messageId" && iss.Details.Coding[0].Code != "" {
					messageID = iss.Details.Coding[0].Code
				}
			}
			issues = append(issues, wireValidationIssue{severity: iss.Severity, code: iss.Code, text: iss.Diagnostics, messageID: messageID, expression: iss.Expression})
		}
	}
	return classifyValidation(issues, resp.StatusCode, wrapper)
}

var (
	_ EvidenceValidator = (*HTTPValidator)(nil)
	_ EvidenceValidator = (*OperationValidator)(nil)
	_ EvidenceValidator = (*FakeValidator)(nil)
)
