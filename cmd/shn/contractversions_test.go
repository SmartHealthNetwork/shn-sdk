package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// fakeRegistrar keeps what the registrar keeps of a holder's contract
// versions: every POST /register and PUT /register/{id} replaces the set with
// the body's (absent is none), and GET /holders serves it in the published
// feed shape. feedStatus, when set, is the feed's answer instead.
type fakeRegistrar struct {
	mu         sync.Mutex
	held       map[string][]string
	feedStatus int
	gets, puts int
	lastBody   []byte
}

func newFakeRegistrar(t *testing.T, held map[string][]string) (*fakeRegistrar, *httptest.Server) {
	t.Helper()
	f := &fakeRegistrar{held: held}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/holders":
			f.gets++
			if f.feedStatus != 0 {
				w.WriteHeader(f.feedStatus)
				return
			}
			var feed []shnsdk.Holder
			for id, cv := range f.held {
				feed = append(feed, shnsdk.Holder{ID: id, ContractVersions: cv})
			}
			_ = json.NewEncoder(w).Encode(feed)
		case r.Method == http.MethodPost && r.URL.Path == "/register",
			r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/register/"):
			f.puts++
			f.lastBody, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var reg shnsdk.RegistrationRequest
			if err := json.Unmarshal(f.lastBody, &reg); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/register/")
			if r.Method == http.MethodPost {
				id = reg.ID
			}
			f.held[id] = reg.ContractVersions
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// seedHolder writes a current identity for id into a fresh key directory.
func seedHolder(t *testing.T, id string) string {
	t.Helper()
	keys := t.TempDir()
	cur, err := shnsdk.GenerateIdentity(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(keys, cur, "payer", "https://acme.example"); err != nil {
		t.Fatal(err)
	}
	return keys
}

var lines22 = []string{shnsdk.ContractPACRD22, shnsdk.ContractPADTR22, shnsdk.ContractPAPAS22, shnsdk.ContractPAPDex21}

// TestContractVersionsFlag: the flag takes what the gateway's own parser
// takes, and refuses an empty list, a token this build cannot build, a
// malformed token and a second use.
func TestContractVersionsFlag(t *testing.T) {
	var f contractVersionsFlag
	if err := f.Set("pa.crd@2.2, pa.dtr@2.2,pa.pas@2.2,pa.pdex@2.1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.forRegister(); !slices.Equal(got, lines22) {
		t.Fatalf("got %q", got)
	}
	if err := f.Set("pa.crd@2.0"); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("second use: %v", err)
	}
	for _, bad := range []string{"", " ", ",", " , ", "\n", "\r", ",\n,", " , \r ", "pa.crd@9.9", "pa.crd", "crd@2.2,pa.dtr@2.2",
		"pa.crd@2.2,pa.crd@2.2", "pa.crd@2.2, pa.dtr@2.2 ,pa.crd@2.2"} {
		var b contractVersionsFlag
		if err := b.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted", bad)
		}
	}
	var unset contractVersionsFlag
	if got, source := unset.forRegister(); !slices.Equal(got, shnsdk.SupportedContractVersions()) || !strings.Contains(source, "default") {
		t.Fatalf("unset: %q (%s)", got, source)
	}
}

// Register declares the flag's set, or this build's default, and says which.
func TestRegister_ContractVersions(t *testing.T) {
	for _, c := range []struct {
		args       []string
		want       []string
		wantSource string
	}{
		{nil, shnsdk.SupportedContractVersions(), "this build's default"},
		{[]string{"--contract-versions", strings.Join(lines22, ",")}, lines22, "from --contract-versions"},
	} {
		f, srv := newFakeRegistrar(t, map[string][]string{})
		stdout, stderr, code := runCLI(append([]string{"register", "--role", "payer", "--name", "ext-payer",
			"--base-url", "https://ext.example.com", "--registrar", srv.URL,
			"--admin-assertion", "X", "-out", t.TempDir()}, c.args...)...)
		if code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", c.args, code, stderr)
		}
		var reg shnsdk.RegistrationRequest
		_ = json.Unmarshal(f.lastBody, &reg)
		if !slices.Equal(reg.ContractVersions, c.want) {
			t.Errorf("%v: declared %q, want %q", c.args, reg.ContractVersions, c.want)
		}
		if !strings.Contains(stdout, "declaring contract versions "+strings.Join(c.want, ",")) || !strings.Contains(stdout, c.wantSource) {
			t.Errorf("%v: stdout does not state the set and its source:\n%s", c.args, stdout)
		}
	}
}

// The Accounts path declares the flag's set in its proof-of-possession body.
func TestRegisterAccounts_ContractVersions(t *testing.T) {
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

	stdout, stderr, code := runCLI2("register", "--accounts", acct.URL, "--cache", cache, "--role", "payer",
		"--name", "acme", "--base-url", "https://acme.example", "-out", t.TempDir(),
		"--contract-versions", strings.Join(lines22, ","))
	if code != 0 {
		t.Fatalf("register exit %d stderr=%s", code, stderr)
	}
	raw, _ := json.Marshal(gotPoP["contractVersions"])
	want, _ := json.Marshal(lines22)
	if !bytes.Equal(raw, want) {
		t.Fatalf("pop contractVersions = %s, want %s", raw, want)
	}
	if !strings.Contains(stdout, "from --contract-versions") {
		t.Errorf("stdout: %s", stdout)
	}
}

// Rotate without the flag re-declares what the registrar holds, so a held set
// that is not this build's default survives the rotation, and a holder that
// declares none stays silent.
func TestRotate_ReDeclaresTheHeldSet(t *testing.T) {
	for _, held := range [][]string{lines22, nil} {
		keys := seedHolder(t, "acme-7f3a")
		f, srv := newFakeRegistrar(t, map[string][]string{"acme-7f3a": held})
		stdout, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
		if code != 0 {
			t.Fatalf("held %q: exit=%d stderr=%s", held, code, stderr)
		}
		if f.puts != 1 || !slices.Equal(f.held["acme-7f3a"], held) {
			t.Errorf("held %q: after rotate the registrar holds %q (%d puts)", held, f.held["acme-7f3a"], f.puts)
		}
		if held == nil {
			if bytes.Contains(f.lastBody, []byte("contractVersions")) {
				t.Errorf("a silent holder's rotation declared a set: %s", f.lastBody)
			}
			if !strings.Contains(stdout, "declaring no contract versions (the registrar's entry for acme-7f3a declares none)") {
				t.Errorf("stdout: %s", stdout)
			}
		} else if !strings.Contains(stdout, "declaring contract versions "+strings.Join(held, ",")+" (the set the registrar holds for acme-7f3a)") {
			t.Errorf("stdout does not state the held set: %s", stdout)
		}
	}
}

// Rotate refuses, sending and changing nothing, when it cannot read the held
// set: the feed fails, or does not list the holder.
func TestRotate_RefusesWhenTheHeldSetCannotBeRead(t *testing.T) {
	for _, c := range []struct {
		name       string
		held       map[string][]string
		feedStatus int
	}{
		{"the feed fails", map[string][]string{"acme-7f3a": lines22}, http.StatusBadGateway},
		{"the holder is not on the feed", map[string][]string{"someone-else": lines22}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			keys := seedHolder(t, "acme-7f3a")
			before, _ := os.ReadFile(filepath.Join(keys, signKeyFile))
			f, srv := newFakeRegistrar(t, c.held)
			f.feedStatus = c.feedStatus
			_, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
			if code != 1 || !strings.Contains(stderr, "cannot read the contract versions the registrar holds for acme-7f3a") ||
				!strings.Contains(stderr, "--contract-versions") {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			if f.puts != 0 {
				t.Errorf("%d registrations sent", f.puts)
			}
			if after, _ := os.ReadFile(filepath.Join(keys, signKeyFile)); !bytes.Equal(before, after) {
				t.Error("the keys changed")
			}
		})
	}
}

// The flag overrides the held set, and rotate then does not read the feed.
func TestRotate_FlagOverridesTheHeldSet(t *testing.T) {
	keys := seedHolder(t, "acme-7f3a")
	f, srv := newFakeRegistrar(t, map[string][]string{"acme-7f3a": shnsdk.SupportedContractVersions()})
	stdout, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys,
		"--contract-versions", strings.Join(lines22, ","))
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if !slices.Equal(f.held["acme-7f3a"], lines22) || f.gets != 0 {
		t.Errorf("holds %q after %d feed reads", f.held["acme-7f3a"], f.gets)
	}
	if !strings.Contains(stdout, "from --contract-versions") {
		t.Errorf("stdout: %s", stdout)
	}
}

// A bad --contract-versions is a usage error on every command: nothing is
// generated or sent.
func TestContractVersionsOption_RefusedBeforeAnyRequest(t *testing.T) {
	for _, bad := range []string{"", "\r", ",\n,", "pa.crd@9.9", "pa.crd", "pa.crd@2.2,pa.crd@2.2"} {
		f, srv := newFakeRegistrar(t, map[string][]string{"acme-7f3a": lines22})
		keys := t.TempDir()
		if _, _, code := runCLI("register", "--role", "payer", "--name", "ext-payer",
			"--base-url", "https://ext.example.com", "--registrar", srv.URL, "-out", keys,
			"--contract-versions", bad); code != 2 {
			t.Errorf("register --contract-versions %q: exit %d, want 2", bad, code)
		}
		if _, _, code := runCLI2("register", "--accounts", srv.URL, "--role", "payer", "--name", "x",
			"--base-url", "https://ext.example.com", "-out", keys, "--contract-versions", bad); code != 2 {
			t.Errorf("register --accounts --contract-versions %q: exit %d, want 2", bad, code)
		}
		if _, _, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys,
			"--contract-versions", bad); code != 2 {
			t.Errorf("rotate --contract-versions %q: exit %d, want 2", bad, code)
		}
		if f.gets+f.puts != 0 {
			t.Errorf("--contract-versions %q: %d requests sent", bad, f.gets+f.puts)
		}
		if m, _ := filepath.Glob(filepath.Join(keys, "*")); len(m) != 0 {
			t.Errorf("--contract-versions %q: files written %v", bad, m)
		}
	}
}

// TestContractVersionsDocsMatchBuild: the README and the option's help name
// the build's actual default set.
func TestContractVersionsDocsMatchBuild(t *testing.T) {
	want := strings.Join(shnsdk.SupportedContractVersions(), ",")
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "(currently `"+want+"`)") {
		t.Errorf("sdk README does not state the default contract versions (currently `%s`)", want)
	}
	if !strings.Contains(contractVersionsUsage, "currently "+want+".") {
		t.Errorf("--contract-versions help does not state the default %s: %s", want, contractVersionsUsage)
	}
	var b strings.Builder
	usage(&b)
	if !strings.Contains(b.String(), "--contract-versions") {
		t.Errorf("usage text does not mention --contract-versions:\n%s", b.String())
	}
}

// A registrar that does not answer the feed read ends the rotation, refused
// with nothing sent, rather than hanging it.
func TestRotate_RefusesWhenTheFeedReadTimesOut(t *testing.T) {
	defer func(d time.Duration) { feedReadTimeout = d }(feedReadTimeout)
	feedReadTimeout = 100 * time.Millisecond
	release := make(chan struct{})
	var puts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/holders" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		mu.Lock()
		puts++
		mu.Unlock()
	}))
	defer srv.Close()
	defer close(release)
	keys := seedHolder(t, "acme-7f3a")
	_, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
	if code != 1 || !strings.Contains(stderr, "cannot read the contract versions the registrar holds for acme-7f3a") {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 0 {
		t.Errorf("%d other requests sent", puts)
	}
}
