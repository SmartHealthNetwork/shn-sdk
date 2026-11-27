package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// redirectingRegistrar answers every request with status, naming the same
// path on a probe on another host (localhost, where each request goes to
// 127.0.0.1). The probe fails the test if anything reaches it.
func redirectingRegistrar(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Errorf("a redirect was followed to another host: %s %s, assertion sent %v", r.Method, r.URL.Path, r.Header.Get("X-Holder-Assertion") != "")
	}))
	t.Cleanup(probe.Close)
	other := strings.Replace(probe.URL, "127.0.0.1", "localhost", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other+r.URL.Path, status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// readDir reads every file directly in dir.
func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = string(b)
	}
	return m
}

// The CLI's registrar sends follow no cross-host redirect: a rotation under
// the holder's own assertion, and a registration under an admin's. Each fails
// (exit 1) and writes no keys, and the probe receives nothing.
func TestRegistrarSends_FollowNoRedirect(t *testing.T) {
	for _, status := range []int{http.StatusSeeOther, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, hits := redirectingRegistrar(t, status)

			keys, _ := seedKeyDir(t, "acme-7f3a", nil)
			before := readDir(t, keys)
			_, stderr, code := runCLI("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys,
				"--contract-versions", strings.Join(shnsdk.SupportedContractVersions(), ","))
			if code != 1 || !strings.Contains(stderr, fmt.Sprintf("registrar responded %d", status)) {
				t.Errorf("rotate through a redirect: exit %d, want 1; stderr %q", code, stderr)
			}
			if after := readDir(t, keys); !maps.Equal(before, after) {
				t.Error("rotate through a redirect changed the key directory")
			}

			dir := t.TempDir()
			stdout, stderr, code := runCLI("register", "--role", "provider", "--name", "ext-prov",
				"--base-url", "https://ext.example.com", "--registrar", srv.URL,
				"--admin-assertion", "ADMINASSERTION", "-out", dir)
			if code != 1 || !strings.Contains(stdout, fmt.Sprintf("registrar responded %d", status)) || !strings.Contains(stderr, "redirect") {
				t.Errorf("register through a redirect: exit %d, want 1; stderr %q", code, stderr)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("the probe was reached %d times", n)
			}
		})
	}
}
