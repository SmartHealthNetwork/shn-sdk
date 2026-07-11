package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SmartHealthNetwork/shn-sdk/health"
)

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v (body %q)", err, rec.Body.String())
		}
	}
	return rec, body
}

func TestHandler_ZeroChecksIsLiveness(t *testing.T) {
	reg := health.New("hub", "abc123")
	rec, body := get(t, reg.Handler(), "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["service"] != "hub" || body["version"] != "abc123" || body["status"] != "ok" {
		t.Fatalf("body = %v", body)
	}
	if checks, ok := body["checks"].([]any); !ok || checks == nil {
		t.Fatalf("checks must be a JSON array (never null): %v", body["checks"])
	}
	if _, ok := body["uptimeSeconds"].(float64); !ok {
		t.Fatalf("uptimeSeconds missing: %v", body)
	}
}

func TestHandler_VersionOmittedWhenEmpty(t *testing.T) {
	reg := health.New("hub", "")
	_, body := get(t, reg.Handler(), "/health")
	if _, present := body["version"]; present {
		t.Fatalf("empty version must be omitted: %v", body)
	}
}

func TestHandler_WorstCheckWins(t *testing.T) {
	reg := health.New("audit", "v")
	reg.Register(func(context.Context) health.Check {
		return health.Check{Name: "db", Status: health.StatusOK}
	})
	reg.Register(func(context.Context) health.Check {
		return health.Check{Name: "registrar-poller", Status: health.StatusDegraded, LastError: "feed fetch failed"}
	})
	_, body := get(t, reg.Handler(), "/health")
	if body["status"] != "degraded" {
		t.Fatalf("service status = %v, want degraded", body["status"])
	}
	checks := body["checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("len(checks) = %d", len(checks))
	}
	second := checks[1].(map[string]any)
	if second["name"] != "registrar-poller" || second["lastError"] != "feed fetch failed" {
		t.Fatalf("check[1] = %v", second)
	}
	if _, present := second["lastSuccess"]; present {
		t.Fatalf("zero lastSuccess must be omitted: %v", second)
	}
}

func TestWrap_MountsHealthAndDelegates(t *testing.T) {
	reg := health.New("registrar", "v")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := health.Wrap(reg, next)
	rec, _ := get(t, h, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health = %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/holders", nil))
	if rec2.Code != http.StatusTeapot {
		t.Fatalf("delegation broken: /holders = %d", rec2.Code)
	}
	// POST /health is NOT the health route (GET-only pattern) — it falls to next.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec3.Code != http.StatusTeapot {
		t.Fatalf("POST /health should fall through to next: %d", rec3.Code)
	}
}

func TestDBPing(t *testing.T) {
	ok := health.DBPing("db", func(context.Context) error { return nil })(context.Background())
	if ok.Status != health.StatusOK || ok.Name != "db" {
		t.Fatalf("ok ping: %+v", ok)
	}
	bad := health.DBPing("db", func(context.Context) error { return errors.New("conn refused") })(context.Background())
	if bad.Status != health.StatusDegraded || bad.LastError != "ping failed" {
		t.Fatalf("bad ping: %+v", bad)
	}

	timedOut := health.DBPing("db", func(context.Context) error {
		return fmt.Errorf("dial tcp 10.0.0.5:5432: %w", context.DeadlineExceeded)
	})(context.Background())
	if timedOut.Status != health.StatusDegraded || timedOut.LastError != "ping timeout" {
		t.Fatalf("timed-out ping: %+v", timedOut)
	}
}
