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
	"slices"
	"strings"
)

// Issue is one OperationOutcome issue as the validator reported it.
type Issue struct {
	// Severity is fatal, error, warning or information.
	Severity string
	// Code is the OperationOutcome issue.code (a FHIR IssueType), or "" when
	// absent or not a string. A HAPI FHIR validator reports "processing" for
	// nearly every issue, so Code alone does not say what kind of issue it is.
	Code string
	// MessageID is the validator's own identifier for the kind of issue: the
	// details.coding code in the java-core-messageId system, or else the
	// operationoutcome-message-id extension. "" when the validator sends
	// neither.
	MessageID string
	// Expression is the issue's FHIRPath location in the checked resource
	// (issue.expression); nil when the validator sends none.
	Expression []string
	// Diagnostics is the validator's own text.
	Diagnostics string
}

// Result reports whether a resource conforms to a profile and lists any issues.
//
// Issues, and every Issue field except Severity and Code, are the validator's
// own text and can contain values from the checked payload: never log them,
// and return them only to the sender of the message that was checked. Severity
// and Code are safe to log only when they are values from their FHIR value
// sets.
type Result struct {
	Valid  bool
	Issues []string
	// Details is every issue the validator reported, at every severity, in the
	// order reported. It is additive to Issues, which keeps only error and
	// fatal diagnostics. OperationValidator fills it; HTTPValidator does not,
	// and FakeValidator does only when RejectIssue is set. An invalid Result
	// with nil Details, and an error or fatal Issue with an empty MessageID,
	// are unclassified: a caller that sorts issues by kind must treat them as
	// the most serious kind, never as having none.
	Details []Issue
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
	// RejectIssue, when set, is the rejection's single Details entry, with the
	// rejection's diagnostics and (when empty) severity "error". A warning or
	// information severity yields an invalid result whose Details hold no error,
	// which is unclassified (see Result). Nil leaves Details nil.
	RejectIssue *Issue
	Err         error
}

func NewFakeValidator() *FakeValidator { return &FakeValidator{} }

func (f *FakeValidator) Validate(_ context.Context, resourceJSON []byte, _ string) (Result, error) {
	if f.Err != nil {
		return Result{}, f.Err
	}
	if f.RejectIfContains != "" && bytes.Contains(resourceJSON, []byte(f.RejectIfContains)) {
		msg := "fake: contains " + f.RejectIfContains
		res := Result{Valid: false, Issues: []string{msg}}
		if f.RejectIssue != nil {
			iss := *f.RejectIssue
			iss.Expression = slices.Clone(iss.Expression)
			if iss.Severity == "" {
				iss.Severity = "error"
			}
			iss.Diagnostics = msg
			res.Details = []Issue{iss}
		}
		return res, nil
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
	Outcomes []json.RawMessage `json:"outcomes"`
}

// issue decodes both wire shapes: the HL7 validator-wrapper's
// ValidationMessage (level/message) and the legacy severity/details shape. A
// level outside the recognised set is a decode failure, never a silent valid.
type issue struct {
	Level    string `json:"level"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Details  string `json:"details"`
	Type     string `json:"type"`
	Line     int    `json:"line"`
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

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
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

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator read response: %w", err)
	}
	if len(raw) > MaxResponseBytes {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator response exceeds %d bytes", MaxResponseBytes)
	}
	var vresp validatorResponse
	if err := json.Unmarshal(raw, &vresp); err != nil {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator decode response: %w", err)
	}
	// Fail closed: a body without outcomes is not a verdict (a wrong URL, a
	// proxy page or a changed schema must never read as valid).
	if len(vresp.Outcomes) == 0 {
		return Result{}, fmt.Errorf("shnsdk: httpvalidator response carries no outcomes (body %q)", truncateBytes(raw, 200))
	}
	var issues []string
	for _, rawOutcome := range vresp.Outcomes {
		trimmed := bytes.TrimSpace(rawOutcome)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return Result{}, fmt.Errorf("shnsdk: httpvalidator invalid outcome object")
		}
		var oc struct {
			Issues json.RawMessage `json:"issues"`
		}
		if err := json.Unmarshal(rawOutcome, &oc); err != nil {
			return Result{}, fmt.Errorf("shnsdk: httpvalidator decode outcome: %w", err)
		}
		rawIssues := bytes.TrimSpace(oc.Issues)
		if len(rawIssues) == 0 || rawIssues[0] != '[' {
			return Result{}, fmt.Errorf("shnsdk: httpvalidator missing or malformed issues")
		}
		var decoded []issue
		if err := json.Unmarshal(rawIssues, &decoded); err != nil {
			return Result{}, fmt.Errorf("shnsdk: httpvalidator decode issues: %w", err)
		}
		for _, iss := range decoded {
			lvl := iss.level()
			if !recognisedLevels[lvl] {
				return Result{}, fmt.Errorf("shnsdk: httpvalidator unrecognised issue level %q (message %q)", lvl, iss.text())
			}
			if lvl == "ERROR" || lvl == "FATAL" {
				issues = append(issues, iss.text())
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
// a FHIR server base URL (e.g. a HAPI FHIR server).
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

// javaCoreMessageIDSystem is the coding system HAPI uses for its message ids.
const javaCoreMessageIDSystem = "http://hl7.org/fhir/java-core-messageId"

// messageIDExtension is the OperationOutcome extension carrying the same id.
const messageIDExtension = "http://hl7.org/fhir/StructureDefinition/operationoutcome-message-id"

// outcomeDetails decodes each issue of an OperationOutcome body into an Issue,
// leniently and separately from the verdict: a member of an unexpected shape
// becomes "" or nil, and never makes the outcome unreadable. The verdict
// (operationOutcome above) reads only severity and diagnostics, as it always
// has, and Validate overwrites each Issue's Severity and Diagnostics with the
// verdict's own, so the two always agree.
func outcomeDetails(body []byte) []Issue {
	var oo struct {
		Issue []map[string]json.RawMessage `json:"issue"`
	}
	if json.Unmarshal(body, &oo) != nil {
		return nil
	}
	var out []Issue
	for _, m := range oo.Issue {
		out = append(out, Issue{
			Severity:    rawString(m["severity"]),
			Code:        rawString(m["code"]),
			MessageID:   messageID(m),
			Expression:  rawStrings(m["expression"]),
			Diagnostics: rawString(m["diagnostics"]),
		})
	}
	return out
}

// messageID is the issue's java-core message id: details.coding first, then
// the first message-id extension that carries a value.
func messageID(m map[string]json.RawMessage) string {
	var details struct {
		Coding []map[string]json.RawMessage `json:"coding"`
	}
	if json.Unmarshal(m["details"], &details) == nil {
		for _, c := range details.Coding {
			if rawString(c["system"]) == javaCoreMessageIDSystem {
				if id := rawString(c["code"]); id != "" {
					return id
				}
			}
		}
	}
	var exts []map[string]json.RawMessage
	if json.Unmarshal(m["extension"], &exts) == nil {
		for _, e := range exts {
			if rawString(e["url"]) != messageIDExtension {
				continue
			}
			if id := rawString(e["valueString"]); id != "" {
				return id
			}
			if id := rawString(e["valueCode"]); id != "" {
				return id
			}
		}
	}
	return ""
}

// rawString is v as a JSON string, or "" when absent or not a string.
func rawString(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

// rawStrings is v as a JSON array of strings, or nil when absent or not one.
func rawStrings(v json.RawMessage) []string {
	var s []string
	if json.Unmarshal(v, &s) != nil {
		return nil
	}
	return s
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
	details := outcomeDetails(body)
	// Severity and Diagnostics in Details come from the same decode as the
	// verdict, so every error or fatal diagnostic in Issues has its matching
	// Details entry. Both decodes accept the same elements, so the lengths
	// agree whenever the verdict decodes; the rebuild below is a guard that no
	// input is known to reach.
	if len(details) != len(oo.Issue) {
		details = make([]Issue, len(oo.Issue))
	}
	for i, iss := range oo.Issue {
		details[i].Severity, details[i].Diagnostics = iss.Severity, iss.Diagnostics
	}
	if len(details) == 0 {
		details = nil
	}
	for _, iss := range oo.Issue {
		if iss.Severity == "error" || iss.Severity == "fatal" {
			issues = append(issues, iss.Diagnostics)
		}
	}

	return Result{
		Valid:   len(issues) == 0,
		Issues:  issues,
		Details: details,
	}, nil
}
