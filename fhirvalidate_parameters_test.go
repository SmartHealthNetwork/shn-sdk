package shnsdk

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOperationValidatorParametersResourceTransport(t *testing.T) {
	const profile = "http://example.test/StructureDefinition/p|2.0.1"
	for _, api := range []string{"legacy", "evidence"} {
		for _, resource := range []string{`{ "resourceType":"Parameters", "parameter":[{"name":"x","valueDecimal":1.000}] }`, `{ "resourceType":"Patient", "id":"synthetic" }`} {
			t.Run(api+resource, func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					want := []byte(resource)
					typ := "Patient"
					if bytes.Contains(want, []byte(`"Parameters"`)) {
						typ = "Parameters"
						want = append(append([]byte(`{"resourceType":"Parameters","parameter":[{"name":"resource","resource":`), want...), []byte(`}]}`)...)
					}
					got, _ := io.ReadAll(r.Body)
					if !bytes.Equal(got, want) {
						t.Errorf("operation request changed or unwrapped: %s want %s", got, want)
					}
					if r.Method != "POST" || r.URL.Path != "/"+typ+"/$validate" || r.URL.Query().Get("profile") != profile {
						t.Errorf("request: %s %s", r.Method, r.URL)
					}
					w.Header().Set("Content-Type", "application/fhir+json")
					io.WriteString(w, `{"resourceType":"OperationOutcome","issue":[{"severity":"information","code":"informational"}]}`)
				}))
				defer srv.Close()
				v := NewOperationValidator(srv.URL)
				if api == "legacy" {
					result, err := v.Validate(context.Background(), []byte(resource), profile)
					if err != nil || !result.Valid {
						t.Fatalf("%+v %v", result, err)
					}
				} else {
					result, err := v.ValidateEvidence(context.Background(), []byte(resource), profile)
					if err != nil || result.Profile.State != ValidationValid || result.Terminology.State != ValidationUnavailable {
						t.Fatalf("%+v %v", result, err)
					}
				}
				if calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}
