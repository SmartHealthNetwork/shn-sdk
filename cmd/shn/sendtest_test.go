package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGateway serves canned /scenario/ucNN responses so send-test's tabulation can be tested
// hermetically (no real gateway). Each handler returns the shape the real gateway would.
func fakeGateway(t *testing.T, overrides map[string]string) *httptest.Server {
	t.Helper()
	def := map[string]string{
		"/scenario/uc01": `{"covered":true}`, // default branch handler keys off the request body
		"/scenario/uc02": `{"paRequired":false}`,
		"/scenario/uc03": `{"paRequired":true,"authNumber":"AUTH-3"}`,
		"/scenario/uc04": `{"authNumber":"AUTH-4"}`,
		"/scenario/uc05": `{"authNumber":"AUTH-5"}`,
		"/scenario/uc06": `{"authNumber":"AUTH-6","attested":true}`,
		"/scenario/uc07": `{"authNumber":"AUTH-7","attested":true}`,
		"/scenario/uc08": `{"denied":true}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "notcovered") {
			w.Write([]byte(`{"covered":false}`))
			return
		}
		w.Write([]byte(def["/scenario/uc01"]))
	})
	mux.HandleFunc("/scenario/uc05", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "noconsent") {
			w.Write([]byte(`{"consentDenied":true}`))
			return
		}
		w.Write([]byte(def["/scenario/uc05"]))
	})
	for path, body := range def {
		if path == "/scenario/uc01" || path == "/scenario/uc05" {
			continue
		}
		b := body
		if o, ok := overrides[path]; ok {
			b = o
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(b)) })
	}
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestSendTestAllGreen(t *testing.T) {
	s := fakeGateway(t, nil)
	var out, errb strings.Builder
	code := cmdSendTest([]string{"--gateway", s.URL}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%q err=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "ALL SCENARIOS GREEN") {
		t.Errorf("missing summary; out=%q", out.String())
	}
	// All 10 checks (uc01 x2, uc05 x2) must show ✓.
	if strings.Contains(out.String(), "✗") {
		t.Errorf("unexpected failure mark; out=%q", out.String())
	}
}

func TestSendTestReportsFailure(t *testing.T) {
	// uc03 returns paRequired:false → its predicate (paRequired && authNumber) fails.
	s := fakeGateway(t, map[string]string{"/scenario/uc03": `{"paRequired":false}`})
	var out, errb strings.Builder
	code := cmdSendTest([]string{"--gateway", s.URL}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "✗ uc03") || !strings.Contains(out.String(), "SEND-TEST FAILED") {
		t.Errorf("expected uc03 failure + summary; out=%q", out.String())
	}
}

func TestSendTestNon200IsFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	var out, errb strings.Builder
	if code := cmdSendTest([]string{"--gateway", s.URL}, &out, &errb); code != 1 {
		t.Fatalf("exit = %d, want 1 (every UC should fail on 500)", code)
	}
}

func TestSendTestRequiresGateway(t *testing.T) {
	var out, errb strings.Builder
	if code := cmdSendTest([]string{}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (missing --gateway)", code)
	}
}
