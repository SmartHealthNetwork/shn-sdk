package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// runCLI invokes the dispatcher with args, capturing stdout+stderr and the exit code.
func runCLI(args ...string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return out.String(), errb.String(), code
}

// TestKeygen_WritesRoundTrippableKeys checks keygen writes 0600 key files plus a
// manifest snippet, and that the keys round-trip back into a usable Identity.
func TestKeygen_WritesRoundTrippableKeys(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runCLI("keygen", "--id", "ext-provider", "--role", "provider", "--base-url", "https://ext.example.com", "-out", dir)
	if code != 0 {
		t.Fatalf("keygen exit=%d stderr=%s", code, stderr)
	}

	for _, name := range []string{"sign.key", "enc.key", "manifest.json"} {
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 600", name, perm)
		}
	}

	// Manifest snippet carries the public fields.
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var man manifest
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if man.ID != "ext-provider" || man.Role != "provider" || man.BaseURL != "https://ext.example.com" {
		t.Errorf("manifest fields: %+v", man)
	}
	if man.SignPub == "" || man.EncPub == "" {
		t.Error("manifest missing public keys")
	}

	// Round-trip: the written keys load back into an Identity whose public keys
	// match the manifest.
	id, err := loadIdentity(dir, "ext-provider")
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	if got := base64.StdEncoding.EncodeToString(id.SignPub); got != man.SignPub {
		t.Errorf("loaded signPub %q != manifest %q", got, man.SignPub)
	}
	if got := base64.StdEncoding.EncodeToString(id.EncPub[:]); got != man.EncPub {
		t.Errorf("loaded encPub %q != manifest %q", got, man.EncPub)
	}
	// The loaded private signing key must sign verifiably under the loaded public key.
	sig := ed25519.Sign(id.SignPriv, []byte("probe"))
	if !ed25519.Verify(id.SignPub, []byte("probe"), sig) {
		t.Error("loaded signing key pair does not round-trip")
	}
}

// TestKeygen_RequiresID: missing --id is an error.
func TestKeygen_RequiresID(t *testing.T) {
	_, stderr, code := runCLI("keygen", "-out", t.TempDir())
	if code == 0 {
		t.Fatal("keygen without --id should fail")
	}
	if !strings.Contains(stderr, "id") {
		t.Errorf("stderr should mention id: %s", stderr)
	}
}

// TestNoSubcommand prints usage and fails.
func TestNoSubcommand(t *testing.T) {
	_, stderr, code := runCLI()
	if code == 0 {
		t.Fatal("no subcommand should fail")
	}
	if !strings.Contains(stderr, "keygen") || !strings.Contains(stderr, "register") || !strings.Contains(stderr, "eligibility") {
		t.Errorf("usage should list subcommands: %s", stderr)
	}
}

// TestUnknownSubcommand fails with usage.
func TestUnknownSubcommand(t *testing.T) {
	_, _, code := runCLI("bogus")
	if code == 0 {
		t.Fatal("unknown subcommand should fail")
	}
}

// TestRegister_RequiresFlags: missing required flags error out.
func TestRegister_RequiresFlags(t *testing.T) {
	cases := [][]string{
		{"register"}, // no role/name/target
		{"register", "--role", "provider", "--name", "x"},      // no base-url/registrar
		{"register", "--name", "x", "--registrar", "http://r"}, // no role
		// base-url is required: it is signed into the PoP and stored as the holder's
		// reachable endpoint, so a fabricated default would silently register a bogus URL.
		{"register", "--role", "provider", "--name", "x", "--registrar", "http://r"}, // no base-url
	}
	for _, args := range cases {
		_, stderr, code := runCLI(append(args, "-out", t.TempDir())...)
		if code == 0 {
			t.Errorf("register %v should fail; stderr=%s", args, stderr)
		}
	}
}

// TestRegister_PortalDeprecated: --portal is a deprecated alias; it must exit
// non-zero pointing the developer at the --accounts path.
func TestRegister_PortalDeprecated(t *testing.T) {
	_, stderr, code := runCLI("register", "--role", "provider", "--name", "x", "--portal", "http://portal", "-out", t.TempDir())
	if code == 0 {
		t.Fatal("--portal should not succeed")
	}
	if !strings.Contains(strings.ToLower(stderr), "--accounts") {
		t.Errorf("stderr should point at --accounts: %s", stderr)
	}
}

// TestRegister_PostsAndReportsStatus: register POSTs the PoP body to --registrar
// and forwards an --admin-assertion as X-Holder-Assertion. Against a stub that
// asserts the body shape and admin header, it reports the registrar's status.
func TestRegister_PostsAndReportsStatus(t *testing.T) {
	var gotBody shnsdk.RegistrationRequest
	var gotAdmin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/register" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAdmin = r.Header.Get("X-Holder-Assertion")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stdout, stderr, code := runCLI("register", "--role", "provider", "--name", "ext-prov",
		"--base-url", "https://ext.example.com", "--registrar", srv.URL,
		"--admin-assertion", "FAKEADMINHEADER", "-out", dir)
	if code != 0 {
		t.Fatalf("register exit=%d stderr=%s", code, stderr)
	}
	if gotAdmin != "FAKEADMINHEADER" {
		t.Errorf("admin header forwarded = %q", gotAdmin)
	}
	if gotBody.ID != "ext-prov" || gotBody.Role != "provider" || gotBody.Pop == "" {
		t.Errorf("posted body: %+v", gotBody)
	}
	if !strings.Contains(stdout, "201") {
		t.Errorf("stdout should report status 201: %s", stdout)
	}
}

// TestEligibility_RequiresFlags: missing required eligibility flags error out.
func TestEligibility_RequiresFlags(t *testing.T) {
	_, _, code := runCLI("eligibility", "--member", "M1")
	if code == 0 {
		t.Fatal("eligibility without hub/authz/payer flags should fail")
	}
}

// TestEligibility_RejectsBadPayerKey: a non-base64 payer enc key is a clear error.
func TestEligibility_RejectsBadPayerKey(t *testing.T) {
	_, stderr, code := runCLI("eligibility",
		"--member", "M1", "--dob", "1980-01-01", "--family", "Johansson",
		"--hub", "http://hub", "--authz", "http://authz",
		"--payer-id", "payer-1", "--payer-enc", "!!notbase64!!", "--authz-pub", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if code == 0 {
		t.Fatal("bad payer-enc should fail")
	}
	if !strings.Contains(strings.ToLower(stderr), "payer-enc") {
		t.Errorf("stderr should mention payer-enc: %s", stderr)
	}
}
