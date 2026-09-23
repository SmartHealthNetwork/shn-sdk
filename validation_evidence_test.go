package shnsdk_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// Adapter mapping v1 is pinned in validation_evidence_mapping.md beside these
// tests. Diagnostic prose deliberately never supplies support evidence.
func TestValidatorEvidenceWireContracts(t *testing.T) {
	for _, adapter := range []string{"operation", "wrapper"} {
		t.Run(adapter, func(t *testing.T) {
			outcome := func(severity, code string) string {
				if adapter == "operation" {
					return fmt.Sprintf(`{"resourceType":"OperationOutcome","issue":[{"severity":%q,"code":%q,"diagnostics":"PRIVATE-SENTINEL"}]}`, severity, code)
				}
				return fmt.Sprintf(`{"outcomes":[{"issues":[{"level":%q,"type":%q,"message":"PRIVATE-SENTINEL"}]}]}`, strings.ToUpper(severity), strings.ToUpper(strings.ReplaceAll(code, "-", "")))
			}
			malformed := []string{`null`, `{`, `{}`, `[]`}
			if adapter == "operation" {
				malformed = append(malformed, `{"resourceType":"OperationOutcome","issue":null}`, `{"resourceType":"OperationOutcome","issue":[]}`, `{"resourceType":"OperationOutcome","issue":{}}`, `{"resourceType":"OperationOutcome","issue":[null]}`, `{"resourceType":"OperationOutcome","issue":[{"severity":"error"}]}`)
			} else {
				malformed = append(malformed, `{"outcomes":[{}]}`, `{"outcomes":[{"issues":null}]}`, `{"outcomes":[{"issues":{}}]}`, `{"outcomes":[{"issues":[null]}]}`)
			}
			type row struct {
				name      string
				status    int
				body      string
				state     shnsdk.ValidationState
				execution bool
				code      string
			}
			rows := []row{
				{"information", 200, outcome("information", "informational"), shnsdk.ValidationValid, false, "informational"},
				{"warning", 200, outcome("warning", "invariant"), shnsdk.ValidationValid, false, "invariant"},
				{"error", 200, outcome("error", "structure"), shnsdk.ValidationInvalid, false, "structure"},
				{"fatal", 200, outcome("fatal", "invalid"), shnsdk.ValidationInvalid, false, "invalid"},
				{"unsupported profile", 200, outcome("error", "not-supported"), shnsdk.ValidationUnavailable, false, "not-supported"},
				{"unsupported terminology", 200, outcome("warning", "not-supported"), shnsdk.ValidationUnavailable, false, "not-supported"},
				{"unknown code", 200, outcome("warning", "PRIVATE-SENTINEL"), shnsdk.ValidationUnavailable, false, "unmapped"},
				{"unknown severity", 200, outcome("unknown", "informational"), shnsdk.ValidationUnavailable, true, ""},
				{"processing", 200, outcome("error", "processing"), shnsdk.ValidationUnavailable, true, "processing"},
			}
			for _, status := range []int{400, 422} {
				rows = append(rows, row{fmt.Sprint(status, " content"), status, outcome("error", "structure"), shnsdk.ValidationInvalid, false, "structure"}, row{fmt.Sprint(status, " warning"), status, outcome("warning", "structure"), shnsdk.ValidationUnavailable, true, ""}, row{fmt.Sprint(status, " unsupported"), status, outcome("error", "not-supported"), shnsdk.ValidationUnavailable, true, ""})
			}
			for _, status := range []int{401, 429, 500} {
				rows = append(rows, row{fmt.Sprint(status), status, outcome("error", "invalid"), shnsdk.ValidationUnavailable, true, ""})
			}
			for i, b := range malformed {
				rows = append(rows, row{fmt.Sprint("malformed", i), 200, b, shnsdk.ValidationUnavailable, true, ""})
			}
			if adapter == "wrapper" {
				rows = append(rows, row{"explicit empty", 200, `{"outcomes":[{"issues":[]}]}`, shnsdk.ValidationValid, false, ""})
			}
			for _, tc := range rows {
				t.Run(tc.name, func(t *testing.T) {
					var hits atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						w.WriteHeader(tc.status)
						fmt.Fprint(w, tc.body)
					}))
					defer srv.Close()
					var v shnsdk.Validator
					if adapter == "operation" {
						v = shnsdk.NewOperationValidator(srv.URL)
					} else {
						v = shnsdk.NewHTTPValidator(srv.URL)
					}
					ev, err := v.(shnsdk.EvidenceValidator).ValidateEvidence(context.Background(), []byte(`{"resourceType":"Patient"}`), "http://example.org/profile")
					if !ev.ExecutionAttempted || (err != nil) != tc.execution || ev.Profile.State != tc.state || ev.Terminology.State != shnsdk.ValidationUnavailable {
						t.Fatalf("evidence=%+v err=%v", ev, err)
					}
					if tc.code != "" && (len(ev.Profile.Issues) != 1 || ev.Profile.Issues[0].Code != tc.code) {
						t.Fatalf("lost safe issue: %+v", ev)
					}
					if strings.Contains(fmt.Sprintf("%+v %v", ev, err), "PRIVATE-SENTINEL") {
						t.Fatal("raw diagnostics escaped structured evidence")
					}
					if hits.Load() != 1 {
						t.Fatalf("evidence calls=%d", hits.Load())
					}
					res, legacyErr := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "http://example.org/profile")
					if (legacyErr != nil) != (tc.state == shnsdk.ValidationUnavailable) || res.Valid != (tc.state == shnsdk.ValidationValid) {
						t.Fatalf("legacy=%+v err=%v", res, legacyErr)
					}
					if tc.state == shnsdk.ValidationInvalid && (len(res.Issues) != 1 || res.Issues[0] != "PRIVATE-SENTINEL") {
						t.Fatalf("lossy compatibility content diagnostics changed: %+v", res)
					}
					if hits.Load() != 2 {
						t.Fatalf("legacy did not execute exactly once: %d", hits.Load())
					}
				})
			}
		})
	}
}

func TestValidatorEvidenceBoundedCompleteAndCanceled(t *testing.T) {
	for _, adapter := range []string{"operation", "wrapper"} {
		for _, shape := range []string{"oversize", "truncated", "canceled"} {
			t.Run(adapter+"/"+shape, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if shape == "oversize" {
						fmt.Fprint(w, strings.Repeat(" ", shnsdk.MaxResponseBytes+1))
						return
					}
					w.Header().Set("Content-Length", "1000")
					fmt.Fprint(w, `{"resourceType":`)
				}))
				defer srv.Close()
				var v shnsdk.Validator = shnsdk.NewOperationValidator(srv.URL)
				if adapter == "wrapper" {
					v = shnsdk.NewHTTPValidator(srv.URL)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if shape == "canceled" {
					cancel()
				}
				ev, err := v.(shnsdk.EvidenceValidator).ValidateEvidence(ctx, []byte(`{"resourceType":"Patient"}`), "")
				if err == nil || ev.Profile.State != shnsdk.ValidationUnavailable || ev.ExecutionAttempted != (shape != "canceled") {
					t.Fatalf("%+v %v", ev, err)
				}
				if shape == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if _, err := v.Validate(ctx, []byte(`{"resourceType":"Patient"}`), ""); err == nil {
					t.Fatal("legacy accepted execution failure")
				}
			})
		}
	}
}

func TestFakeValidatorExplicitEvidence(t *testing.T) {
	f := shnsdk.NewFakeValidator()
	ev, err := f.ValidateEvidence(context.Background(), nil, "")
	if err != nil || ev.Profile.State != shnsdk.ValidationUnavailable || ev.Terminology.State != shnsdk.ValidationUnavailable {
		t.Fatalf("implicit fake support: %+v %v", ev, err)
	}
	f.Evidence = &shnsdk.ValidationEvidence{Profile: shnsdk.ValidationCheckEvidence{State: shnsdk.ValidationValid, Code: "synthetic"}, Terminology: shnsdk.ValidationCheckEvidence{State: shnsdk.ValidationInvalid, Code: "synthetic"}}
	ev, err = f.ValidateEvidence(context.Background(), nil, "")
	if err != nil || ev.Profile.State != shnsdk.ValidationValid || ev.Terminology.State != shnsdk.ValidationInvalid {
		t.Fatalf("configured fake: %+v %v", ev, err)
	}
}

func TestValidatorEvidenceResponseErrorPrivateCopy(t *testing.T) {
	for _, adapter := range []string{"operation", "wrapper"} {
		for _, status := range []int{200, 500} {
			t.Run(fmt.Sprintf("%s/%d", adapter, status), func(t *testing.T) {
				const raw = `{"PRIVATE-SENTINEL":"bad response"}`
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, raw) }))
				defer srv.Close()
				var v shnsdk.Validator = shnsdk.NewOperationValidator(srv.URL)
				if adapter == "wrapper" {
					v = shnsdk.NewHTTPValidator(srv.URL)
				}
				_, err := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "")
				var response *shnsdk.ValidationResponseError
				if !errors.As(fmt.Errorf("wrapped: %w", err), &response) || string(response.RawResponse()) != raw {
					t.Fatalf("bounded raw evidence lost: %v", err)
				}
				if strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
					t.Fatal("raw error exposed")
				}
				copy := response.RawResponse()
				copy[0] = 'X'
				if string(response.RawResponse()) != raw {
					t.Fatal("raw evidence aliases caller")
				}
			})
		}
	}
}

func TestValidatorEvidencePinnedStructuredMessageIDs(t *testing.T) {
	for _, adapter := range []string{"operation", "wrapper"} {
		for _, tc := range []struct {
			id, code  string
			state     shnsdk.ValidationState
			execution bool
		}{
			{"Extension_EXT_Type", "extension-type", shnsdk.ValidationInvalid, false},
			{"Reference_REF_BadTargetType", "reference-target-type", shnsdk.ValidationInvalid, false},
			{"Validation_VAL_Profile_Unknown", "profile-unsupported", shnsdk.ValidationUnavailable, false},
			{"SLICING_CANNOT_BE_EVALUATED", "slicing-unavailable", shnsdk.ValidationUnavailable, true},
			{"PRIVATE-SENTINEL", "unmapped", shnsdk.ValidationUnavailable, false},
		} {
			t.Run(adapter+"/"+tc.id, func(t *testing.T) {
				raw := fmt.Sprintf(`{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"processing","details":{"coding":[{"system":"http://hl7.org/fhir/java-core-messageId","code":%q}]},"diagnostics":"PRIVATE-SENTINEL"}]}`, tc.id)
				if adapter == "wrapper" {
					raw = fmt.Sprintf(`{"outcomes":[{"issues":[{"level":"ERROR","type":"PROCESSING","messageId":%q,"message":"PRIVATE-SENTINEL"}]}]}`, tc.id)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) }))
				defer srv.Close()
				var v shnsdk.Validator = shnsdk.NewOperationValidator(srv.URL)
				if adapter == "wrapper" {
					v = shnsdk.NewHTTPValidator(srv.URL)
				}
				ev, err := v.(shnsdk.EvidenceValidator).ValidateEvidence(context.Background(), []byte(`{"resourceType":"Patient"}`), "profile")
				if ev.Profile.State != tc.state || (err != nil) != tc.execution || len(ev.Profile.Issues) != 1 || ev.Profile.Issues[0].Code != tc.code || ev.Terminology.State != shnsdk.ValidationUnavailable {
					t.Fatalf("%+v %v", ev, err)
				}
				if strings.Contains(fmt.Sprint(ev, err), "PRIVATE-SENTINEL") {
					t.Fatal("raw message ID leaked")
				}
				result, legacyErr := v.Validate(context.Background(), []byte(`{"resourceType":"Patient"}`), "profile")
				if (legacyErr != nil) != (tc.state == shnsdk.ValidationUnavailable) || result.Valid {
					t.Fatalf("legacy %+v %v", result, legacyErr)
				}
			})
		}
	}
}

func TestOperationValidatorEvidenceStructuredIDCannotOverrideAmbiguity(t *testing.T) {
	for _, tc := range []struct{ name, code, severity, coding string }{
		{"wrong system", "processing", "error", `[{"system":"http://example.org/untrusted","code":"Extension_EXT_Type"}]`},
		{"conflicting ids", "processing", "error", `[{"system":"http://hl7.org/fhir/java-core-messageId","code":"Extension_EXT_Type"},{"system":"http://hl7.org/fhir/java-core-messageId","code":"Validation_VAL_Profile_Unknown"}]`},
		{"conflicting category", "exception", "error", `[{"system":"http://hl7.org/fhir/java-core-messageId","code":"Extension_EXT_Type"}]`},
		{"conflicting severity", "processing", "warning", `[{"system":"http://hl7.org/fhir/java-core-messageId","code":"Extension_EXT_Type"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"resourceType":"OperationOutcome","issue":[{"severity":%q,"code":%q,"details":{"coding":%s}}]}`, tc.severity, tc.code, tc.coding)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) }))
			defer srv.Close()
			ev, _ := shnsdk.NewOperationValidator(srv.URL).ValidateEvidence(context.Background(), []byte(`{"resourceType":"Patient"}`), "profile")
			if ev.Profile.State != shnsdk.ValidationUnavailable || ev.Terminology.State != shnsdk.ValidationUnavailable || len(ev.Profile.Issues) != 1 || ev.Profile.Issues[0].Code != "unmapped" {
				t.Fatalf("ambiguous execution became content: %+v", ev)
			}
		})
	}
}

type evidenceTransport func(*http.Request) (*http.Response, error)

func (f evidenceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestValidatorEvidenceAttemptProvenance(t *testing.T) {
	for _, adapter := range []string{"operation", "wrapper"} {
		t.Run(adapter, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: evidenceTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("controlled transport outage")
			})}
			var v shnsdk.EvidenceValidator
			if adapter == "operation" {
				op := shnsdk.NewOperationValidator("http://checker.invalid")
				op.Client = client
				v = op
			} else {
				h := shnsdk.NewHTTPValidator("http://checker.invalid")
				h.Client = client
				v = h
			}
			body := []byte(`{"resourceType":"Bundle"}`)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			ev, err := v.ValidateEvidence(ctx, body, "")
			if err == nil || ev.ExecutionAttempted || calls != 0 {
				t.Fatalf("pre-dispatch cancellation: %+v %v calls=%d", ev, err, calls)
			}
			if adapter == "operation" {
				for _, bad := range []string{`{}`, `{"resourceType":123}`, `not-json`} {
					ev, err = v.ValidateEvidence(context.Background(), []byte(bad), "")
					if err == nil || ev.ExecutionAttempted || calls != 0 {
						t.Fatalf("preprocessing: %+v %v calls=%d", ev, err, calls)
					}
				}
			}
			ev, err = v.ValidateEvidence(context.Background(), body, "")
			if err == nil || !ev.ExecutionAttempted || calls != 1 || ev.Profile.State != shnsdk.ValidationUnavailable {
				t.Fatalf("transport: %+v %v calls=%d", ev, err, calls)
			}
		})
	}
}

func TestValidatorEvidenceBuildFailureIsUnproven(t *testing.T) {
	for _, v := range []shnsdk.EvidenceValidator{shnsdk.NewOperationValidator(":invalid"), shnsdk.NewHTTPValidator(":invalid")} {
		ev, err := v.ValidateEvidence(context.Background(), []byte(`{"resourceType":"Bundle"}`), "")
		if err == nil || ev.ExecutionAttempted {
			t.Fatalf("build failure: %+v %v", ev, err)
		}
	}
}
