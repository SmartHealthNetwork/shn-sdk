package main

import (
	"bytes"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renewRegistrar is a registrar stub that answers a renewal of id with status
// and body, and counts the renewals it was sent.
func renewRegistrar(t *testing.T, id string, status int, body string, sent *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/register/"+id+"/lease" || r.Header.Get("X-Holder-Assertion") == "" {
			t.Errorf("renew sent %s %s, want PUT /register/%s/lease with an assertion", r.Method, r.URL.Path, id)
		}
		*sent++
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// shn renew exits 0 when the registrar renewed the registration, 1 when it
// refused it, and 3 when it did not confirm it, and writes nothing to the key
// directory either way.
func TestRenew_ExitsByOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, body, out string
		status, exit    int
	}{
		{"renewed with no lease", `{"leaseExpiresAt":null}`, "renewed acme-7f3a. No lease runs", http.StatusOK, 0},
		{"renewed with a lease", `{"leaseExpiresAt":"2026-12-27T00:00:00Z"}`, "lapses at 2026-12-27T00:00:00Z", http.StatusOK, 0},
		{"refused, not the current key", `{"error":"a holder may only renew itself","type":"tag:smarthealthnetwork.org,2026:registrar/refusal/not-self","title":"Not the holder itself","status":403}`, "refused", http.StatusForbidden, 1},
		{"refused, a founding holder", `{"error":"founding holder, manifest-authoritative","type":"tag:smarthealthnetwork.org,2026:registrar/refusal/founding-holder","title":"Founding holder","status":409}`, "refused", http.StatusConflict, 1},
		{"unknown, a 403 from something in front of the registrar", "<html>Request blocked</html>", "renew again later", http.StatusForbidden, exitRenewalUnknown},
		{"unknown, a registrar that predates the renewal (404)", "404 page not found", "renew again later", http.StatusNotFound, exitRenewalUnknown},
		{"unknown, a registrar that predates the renewal (405)", "", "renew again later", http.StatusMethodNotAllowed, exitRenewalUnknown},
		{"unknown, a 5xx", "store error", "renew again later", http.StatusServiceUnavailable, exitRenewalUnknown},
		{"unknown, a 200 it cannot read", `{}`, "renew again later", http.StatusOK, exitRenewalUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys, _ := seedKeyDir(t, "acme-7f3a", nil)
			before := dirBytes(t, keys)
			sent := 0
			srv := renewRegistrar(t, "acme-7f3a", tc.status, tc.body, &sent)
			var stdout, stderr bytes.Buffer
			code := run([]string{"renew", "acme-7f3a", "--registrar", srv.URL, "-out", keys}, &stdout, &stderr)
			if code != tc.exit || sent != 1 {
				t.Fatalf("exit %d after %d renewals, want %d after 1; stdout %q stderr %q", code, sent, tc.exit, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), tc.out) {
				t.Fatalf("output %q %q, want it to say %q", stdout.String(), stderr.String(), tc.out)
			}
			if after := dirBytes(t, keys); !maps.Equal(before, after) {
				t.Fatal("shn renew changed the key directory")
			}
		})
	}
}

// shn renew without an id or a registrar is misuse (2), and without keys
// fails (1); neither sends anything.
func TestRenew_MisuseSendsNothing(t *testing.T) {
	sent := 0
	srv := renewRegistrar(t, "acme-7f3a", http.StatusOK, `{"leaseExpiresAt":null}`, &sent)
	keys, _ := seedKeyDir(t, "acme-7f3a", nil)
	for _, tc := range []struct {
		name string
		args []string
		exit int
	}{
		{"no id", []string{"renew", "--registrar", srv.URL, "-out", keys}, 2},
		{"no registrar", []string{"renew", "acme-7f3a", "-out", keys}, 2},
		{"no keys", []string{"renew", "acme-7f3a", "--registrar", srv.URL, "-out", t.TempDir()}, 1},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(tc.args, &stdout, &stderr); code != tc.exit {
			t.Fatalf("%s: exit %d, want %d; stderr %q", tc.name, code, tc.exit, stderr.String())
		}
	}
	if sent != 0 {
		t.Fatalf("%d renewals were sent", sent)
	}
}

// dirBytes reads every file in dir.
func dirBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = string(b)
	}
	return m
}
