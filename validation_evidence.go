package shnsdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

// ValidationState distinguishes content verdicts from execution or support gaps.
type ValidationState string

const (
	ValidationValid         ValidationState = "valid"
	ValidationInvalid       ValidationState = "invalid"
	ValidationUnavailable   ValidationState = "unavailable"
	ValidationNotApplicable ValidationState = "not_applicable"
)

// ValidationIssue contains only a recognized severity and closed adapter code.
// Diagnostics, paths, resource values and unrecognized server codes are excluded.
type ValidationIssue struct {
	Severity string
	Code     string
}

// ValidationCheckEvidence describes one independently supported check.
type ValidationCheckEvidence struct {
	State  ValidationState
	Code   string
	Issues []ValidationIssue
}

// ValidationEvidence separates profile execution from terminology coverage.
// A profile verdict never establishes coverage of the requested code systems.
type ValidationEvidence struct {
	// ExecutionAttempted distinguishes an actual checker invocation (including a
	// local checker or attempted transport) from adapter preprocessing. False
	// means unproven; it does not change the verdict or establish an outage.
	// Implementations and wrappers must preserve this provenance independently
	// of Profile and Terminology. An attempt alone proves neither coverage nor
	// success, and pre-dispatch cancellation must leave it false.
	ExecutionAttempted bool
	Profile            ValidationCheckEvidence
	Terminology        ValidationCheckEvidence
}

// EvidenceValidator is additive to Validator. Legacy-only implementations cannot
// certify execution or terminology support. NotApplicable requires explicit
// applicability evidence; neither built-in HTTP adapter supplies that evidence.
type EvidenceValidator interface {
	ValidateEvidence(context.Context, []byte, string) (ValidationEvidence, error)
}

// unavailableValidation is also the zero-work answer for an unconfigured fake.
func unavailableValidation(code string) ValidationEvidence {
	return ValidationEvidence{Profile: ValidationCheckEvidence{State: ValidationUnavailable, Code: code}, Terminology: ValidationCheckEvidence{State: ValidationUnavailable, Code: "terminology-support-unproven"}}
}

type decodedValidation struct {
	evidence ValidationEvidence
	result   Result
}

func executionFailure(err error) (decodedValidation, error) {
	return decodedValidation{evidence: unavailableValidation("execution-unavailable")}, err
}

func (v decodedValidation) legacy(err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	if v.evidence.Profile.State == ValidationUnavailable {
		return Result{}, fmt.Errorf("shnsdk: profile validation unavailable (%s)", v.evidence.Profile.Code)
	}
	return v.result, nil
}

// validationIssueCode implements adapter mapping v1. This is an allowlist, not
// diagnostic-text interpretation. Unmapped or unsupported categories cannot
// establish profile execution. Both adapters lack terminology coverage evidence.
func validationIssueCode(code string, wrapper bool) (safe string, unavailable, execution bool) {
	if wrapper {
		code = strings.ToLower(code)
		switch code {
		case "notsupported":
			code = "not-supported"
		case "notfound":
			code = "not-found"
		case "businessrule":
			code = "business-rule"
		case "codeinvalid":
			code = "code-invalid"
		}
	}
	switch code {
	case "invalid", "structure", "required", "value", "invariant", "too-long", "duplicate", "business-rule", "informational":
		return code, false, false
	case "code-invalid":
		// A specific failed binding is a content failure, but cannot prove complete
		// terminology coverage of the resource even when other issues are absent.
		return code, false, false
	case "not-supported", "not-found", "incomplete":
		return code, true, false
	case "processing", "exception", "timeout", "throttled", "security", "login", "forbidden", "expired", "too-costly", "transient", "lock-error", "no-store", "conflict":
		return code, true, true
	default:
		return "unmapped", true, false
	}
}

type wireValidationIssue struct {
	severity, code, text, messageID string
	expression                      []string
}

// These exact Java-core message IDs are witnessed by the pinned lane corpus.
// They refine the generic processing category only under the same wire shape.
func classifyValidationIssue(iss wireValidationIssue, wrapper bool) (string, bool, bool) {
	if iss.messageID == "" {
		return validationIssueCode(iss.code, wrapper)
	}
	code := iss.code
	if wrapper {
		code = strings.ToLower(code)
	}
	if code != "processing" {
		return "unmapped", true, false
	}
	switch iss.messageID {
	case "Terminology_TX_Code_ValueSet_Ext":
		// The pinned US Core Goal outcome is a completed extensible binding
		// check of its text-only description. Qualify only that exact element;
		// other no-code warnings may carry different clinical obligations.
		if wrapper || iss.severity != "warning" || len(iss.expression) != 1 || iss.expression[0] != "Goal.description" {
			return "unmapped", true, false
		}
		return "goal-description-text-advisory", false, false
	case "Terminology_TX_NoValid_2_CC":
		// The pinned OperationOutcome shape identifies a completed extensible
		// CodeableConcept binding check. It proves neither suitability nor
		// terminology coverage, and the CLI wrapper remains unqualified.
		if wrapper || iss.severity != "warning" {
			return "unmapped", true, false
		}
		return "extensible-binding-advisory", false, false
	case "http://hl7.org/fhir/StructureDefinition/DomainResource#dom-6":
		// Only the pinned OperationOutcome warning is qualified; the CLI wrapper
		// needs independent wire evidence. Narrative advice proves no terminology.
		if wrapper || iss.severity != "warning" {
			return "unmapped", true, false
		}
		return "narrative-advisory", false, false
	case "All_observations_should_have_a_performer":
		// The pinned Observation best-practice message is advisory only in
		// this exact OperationOutcome warning shape. Presence proves neither
		// a valid performer nor terminology coverage; CLI remains unqualified.
		if wrapper || iss.severity != "warning" {
			return "unmapped", true, false
		}
		return "observation-performer-advisory", false, false
	case "All_observations_should_have_an_effectiveDateTime_or_an_effectivePeriod":
		// Pinned Java-core best-practice presence advice has four alternatives:
		// DateTime, Period, Timing, or Instant. This exact warning proves no
		// source-time completeness, support obligation, or terminology coverage.
		if wrapper || iss.severity != "warning" {
			return "unmapped", true, false
		}
		return "observation-effective-time-advisory", false, false
	case "Extension_EXT_Type", "Reference_REF_BadTargetType":
		if iss.severity != "error" && iss.severity != "fatal" {
			return "unmapped", true, false
		}
		if iss.messageID == "Extension_EXT_Type" {
			return "extension-type", false, false
		}
		return "reference-target-type", false, false
	case "Validation_VAL_Profile_Unknown":
		return "profile-unsupported", true, false
	case "SLICING_CANNOT_BE_EVALUATED":
		return "slicing-unavailable", true, true
	default:
		return "unmapped", true, false
	}
}

func classifyValidation(issues []wireValidationIssue, status int, wrapper bool) (decodedValidation, error) {
	v := decodedValidation{evidence: ValidationEvidence{Profile: ValidationCheckEvidence{State: ValidationValid, Code: "profile-completed"}, Terminology: ValidationCheckEvidence{State: ValidationUnavailable, Code: "terminology-support-unproven"}}, result: Result{Valid: true}}
	var unsupported, execution bool
	for _, iss := range issues {
		safe, unavailable, failed := classifyValidationIssue(iss, wrapper)
		v.evidence.Profile.Issues = append(v.evidence.Profile.Issues, ValidationIssue{Severity: iss.severity, Code: safe})
		unsupported = unsupported || unavailable
		execution = execution || failed
		if iss.severity == "fatal" || iss.severity == "error" {
			v.result.Valid = false
			v.result.Issues = append(v.result.Issues, iss.text)
		}
	}
	if !v.result.Valid {
		v.evidence.Profile.State = ValidationInvalid
		v.evidence.Profile.Code = "profile-invalid"
	}
	if unsupported {
		v.evidence.Profile.State = ValidationUnavailable
		v.evidence.Profile.Code = "profile-support-unproven"
	}
	if execution {
		v.evidence.Profile.Code = "execution-unavailable"
		return v, fmt.Errorf("shnsdk: validator execution unavailable")
	}
	// Only 400/422 with exclusively mapped content categories and at least one
	// error/fatal is a pinned non-2xx content verdict. Service replies never are.
	if status/100 != 2 && (status != 400 && status != 422 || unsupported || v.result.Valid) {
		v.evidence.Profile.State = ValidationUnavailable
		v.evidence.Profile.Code = "execution-unavailable"
		return v, fmt.Errorf("shnsdk: validator HTTP %d is not a content validation outcome", status)
	}
	return v, nil
}

// ValidationResponseError retains bounded response bytes separately from its safe
// Error string. RawResponse is for private diagnostic storage only: never forward
// it to observer events, status or participant-facing errors. It does not certify
// content or support; callers use ValidationEvidence for those decisions.
type ValidationResponseError struct {
	status int
	raw    []byte
	cause  error
}

func (e *ValidationResponseError) Error() string {
	hash := sha256.Sum256(e.raw)
	return fmt.Sprintf("shnsdk: validator HTTP %d execution unavailable (response bytes=%d sha256=%x)", e.status, len(e.raw), hash)
}

// Unwrap preserves execution error identity without including it in Error.
func (e *ValidationResponseError) Unwrap() error { return e.cause }

// RawResponse returns an isolated copy, never more than MaxResponseBytes.
func (e *ValidationResponseError) RawResponse() []byte { return bytes.Clone(e.raw) }
