package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestRequestFramesFlag: the option accepts a non-empty list of capabilities
// this build supports and refuses anything else; unset means the full set.
func TestRequestFramesFlag(t *testing.T) {
	var unset requestFramesFlag
	if got := unset.resolve(); !slices.Equal(got, shnsdk.SupportedRequestFrames()) {
		t.Fatalf("default = %q, want %q", got, shnsdk.SupportedRequestFrames())
	}
	for in, want := range map[string][]string{
		"v1":       {"v1"},
		"v1,v1op":  {"v1", "v1op"},
		"v1op, v1": {"v1op", "v1"},
		"v1op":     {"v1op"},
	} {
		var f requestFramesFlag
		if err := f.Set(in); err != nil {
			t.Errorf("Set(%q): %v", in, err)
			continue
		}
		if got := f.resolve(); !slices.Equal(got, want) {
			t.Errorf("Set(%q) resolves to %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", ",", "v1,", "V1", "v1-op", "v2", "x", "v1,v1", strings.Repeat("a", 17)} {
		var f requestFramesFlag
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted", bad)
		}
	}
	var twice requestFramesFlag
	if err := twice.Set("v1"); err != nil {
		t.Fatal(err)
	}
	if err := twice.Set("v1op"); err == nil {
		t.Error("a second --request-frames accepted")
	}
}

// registrarCapture is a stub registrar that records the last registration
// body for POST /register and PUT /register/{id}.
func registrarCapture(t *testing.T) (*httptest.Server, *shnsdk.RegistrationRequest, *atomic.Int32) {
	t.Helper()
	var got shnsdk.RegistrationRequest
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&got)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &hits
}

func TestRegister_RequestFramesOption(t *testing.T) {
	for _, c := range []struct {
		args []string
		want []string
	}{
		{nil, shnsdk.SupportedRequestFrames()},
		{[]string{"--request-frames", "v1"}, []string{"v1"}},
		{[]string{"--request-frames", "v1,v1op"}, []string{"v1", "v1op"}},
	} {
		srv, got, _ := registrarCapture(t)
		args := append([]string{"register", "--role", "payer", "--name", "ext-payer",
			"--base-url", "https://ext.example.com", "--registrar", srv.URL,
			"--admin-assertion", "X", "-out", t.TempDir()}, c.args...)
		if _, stderr, code := runCLI(args...); code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", c.args, code, stderr)
		}
		if !slices.Equal(got.RequestFrames, c.want) {
			t.Errorf("%v: declared %q, want %q", c.args, got.RequestFrames, c.want)
		}
	}
}

func TestRegisterAccounts_RequestFramesOption(t *testing.T) {
	var gotPoP map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "acme-7f3a"})
	})
	mux.HandleFunc("POST /clients/{id}/pop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPoP)
		w.WriteHeader(200)
	})
	acct := httptest.NewServer(mux)
	defer acct.Close()
	cache := filepath.Join(t.TempDir(), "creds")
	writeTestCache(t, cache, acct.URL, "id-token-xyz")

	_, stderr, code := runCLI2("register", "--accounts", acct.URL, "--cache", cache, "--role", "payer",
		"--name", "acme", "--base-url", "https://acme.example", "-out", t.TempDir(), "--request-frames", "v1")
	if code != 0 {
		t.Fatalf("register exit %d stderr=%s", code, stderr)
	}
	raw, _ := json.Marshal(gotPoP["requestFrames"])
	if string(raw) != `["v1"]` {
		t.Fatalf("pop requestFrames = %s, want [\"v1\"]", raw)
	}
}

func TestRotate_RequestFramesOption(t *testing.T) {
	keys := t.TempDir()
	cur, err := shnsdk.GenerateIdentity("acme-7f3a")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(keys, cur, "payer", "https://acme.example"); err != nil {
		t.Fatal(err)
	}
	srv, got, _ := registrarCapture(t)
	if _, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys, "--request-frames", "v1"); code != 0 {
		t.Fatalf("rotate exit=%d stderr=%s", code, stderr)
	}
	if !slices.Equal(got.RequestFrames, []string{"v1"}) {
		t.Fatalf("rotate declared %q, want [v1]", got.RequestFrames)
	}
}

// TestRequestFramesOption_RefusedBeforeAnyRequest: an invalid or unsupported
// value is a usage error; nothing is generated or sent.
func TestRequestFramesOption_RefusedBeforeAnyRequest(t *testing.T) {
	for _, bad := range []string{"v2", "V1", "v1,v1", ""} {
		srv, _, hits := registrarCapture(t)
		keys := t.TempDir()
		if _, _, code := runCLI("register", "--role", "payer", "--name", "ext-payer",
			"--base-url", "https://ext.example.com", "--registrar", srv.URL, "-out", keys,
			"--request-frames", bad); code != 2 {
			t.Errorf("register --request-frames %q: exit %d, want 2", bad, code)
		}
		if _, _, code := runCLI2("register", "--accounts", srv.URL, "--role", "payer", "--name", "x",
			"--base-url", "https://ext.example.com", "-out", keys, "--request-frames", bad); code != 2 {
			t.Errorf("register --accounts --request-frames %q: exit %d, want 2", bad, code)
		}
		if _, _, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys,
			"--request-frames", bad); code != 2 {
			t.Errorf("rotate --request-frames %q: exit %d, want 2", bad, code)
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("--request-frames %q: %d requests sent", bad, n)
		}
		if m, _ := filepath.Glob(filepath.Join(keys, "*")); len(m) != 0 {
			t.Errorf("--request-frames %q: files written %v", bad, m)
		}
	}
}

func TestUsage_MentionsRequestFrames(t *testing.T) {
	var b strings.Builder
	usage(&b)
	if !strings.Contains(b.String(), "--request-frames v1") || !strings.Contains(b.String(), "v0.44.0") {
		t.Fatalf("usage text does not explain --request-frames:\n%s", b.String())
	}
}

// TestRequestFramesDocsMatchBuild: the README and the option's help name the
// build's actual default set.
func TestRequestFramesDocsMatchBuild(t *testing.T) {
	want := strings.Join(shnsdk.SupportedRequestFrames(), ",")
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "(currently `"+want+"`)") {
		t.Errorf("sdk README does not state the default request frames (currently `%s`)", want)
	}
	if !strings.Contains(requestFramesUsage, "currently "+want+")") {
		t.Errorf("--request-frames help does not state the default %s: %s", want, requestFramesUsage)
	}
}

// TestRequestFramesOption_WarnsOnOperationsWithoutV1: declaring v1op without
// v1 is allowed but warned about, on every command; the full set and v1 alone
// are not.
func TestRequestFramesOption_WarnsOnOperationsWithoutV1(t *testing.T) {
	const warning = "warning: --request-frames lists v1op without v1"
	keys := t.TempDir()
	cur, err := shnsdk.GenerateIdentity("acme-7f3a")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(keys, cur, "payer", "https://acme.example"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "acme-7f3a"})
	})
	mux.HandleFunc("POST /clients/{id}/pop", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	acct := httptest.NewServer(mux)
	defer acct.Close()
	cache := filepath.Join(t.TempDir(), "creds")
	writeTestCache(t, cache, acct.URL, "id-token-xyz")

	for _, c := range []struct {
		frames string
		warns  bool
	}{{"v1op", true}, {"v1,v1op", false}, {"v1op,v1", false}, {"v1", false}} {
		srv, got, _ := registrarCapture(t)
		runs := map[string]func() (string, int){
			"register": func() (string, int) {
				_, stderr, code := runCLI("register", "--role", "payer", "--name", "ext-payer",
					"--base-url", "https://ext.example.com", "--registrar", srv.URL,
					"--admin-assertion", "X", "-out", t.TempDir(), "--request-frames", c.frames)
				return stderr, code
			},
			"register --accounts": func() (string, int) {
				_, stderr, code := runCLI2("register", "--accounts", acct.URL, "--cache", cache, "--role", "payer",
					"--name", "acme", "--base-url", "https://acme.example", "-out", t.TempDir(), "--request-frames", c.frames)
				return stderr, code
			},
			"rotate": func() (string, int) {
				_, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys, "--request-frames", c.frames)
				return stderr, code
			},
		}
		for name, run := range runs {
			stderr, code := run()
			if code != 0 {
				t.Fatalf("%s --request-frames %s: exit %d stderr=%s", name, c.frames, code, stderr)
			}
			if strings.Contains(stderr, warning) != c.warns {
				t.Errorf("%s --request-frames %s: warned=%v, want %v (stderr=%q)", name, c.frames, !c.warns, c.warns, stderr)
			}
		}
		if c.frames == "v1op" && !slices.Equal(got.RequestFrames, []string{"v1op"}) {
			t.Errorf("--request-frames v1op declared %q, want it sent as given", got.RequestFrames)
		}
	}
}
