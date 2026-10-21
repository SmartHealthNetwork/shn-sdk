package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// scenarioBundle is a gateway bundle directory whose manifest names holder.
func scenarioBundle(t *testing.T, holder string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte(`{"id":"`+holder+`","role":"provider"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// readCallerEntries reads a callers file as scenario-key writes it.
func readCallerEntries(t *testing.T, path string) []scenarioCallerEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var es []scenarioCallerEntry
	if err := json.Unmarshal(raw, &es); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return es
}

// verifyScenario checks a header the way a gateway does: the signature by
// pub, the audience for gateway, the lifetime, and the binding of this
// request. It is this package's own check; the gateway's Verify checks the
// same output in the platform's test/scenariowire.
func verifyScenario(t *testing.T, header, method, target string, body []byte, gateway string, pub ed25519.PublicKey) shnsdk.Assertion {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("header is not base64: %v", err)
	}
	var a shnsdk.Assertion
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("header is not an assertion: %v", err)
	}
	if err := shnsdk.VerifyAssertion(a, "scenario:"+gateway, pub, time.Now()); err != nil {
		t.Fatalf("assertion does not verify for %s: %v", gateway, err)
	}
	sum := sha256.Sum256(scenarioBinding(method, target, body))
	if a.BodyHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("bh %q is not bound to %s %s", a.BodyHash, method, target)
	}
	if a.Expiry.Sub(a.IssuedAt) != scenarioTTL {
		t.Fatalf("lifetime %v, want %v", a.Expiry.Sub(a.IssuedAt), scenarioTTL)
	}
	return a
}

// scenario-key writes the caller's key, readable by its owner only, and adds
// the caller to the bundle's callers file beside the entries already there;
// it says what it did and never prints the key.
func TestScenarioKey_WritesTheKeyAndAddsTheCaller(t *testing.T) {
	bundle := scenarioBundle(t, "my-provider")
	callers := filepath.Join(bundle, scenarioCallersFileName)
	other := `[{"label":"monitor","public_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"}]`
	if err := os.WriteFile(callers, []byte(other), 0o640); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"scenario-key", "--bundle", bundle, "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	keyPath := filepath.Join(out, "scenario-caller-send-test.sign")
	st, err := os.Stat(keyPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v, want mode 0600", st, err)
	}
	priv, err := loadScenarioKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	es := readCallerEntries(t, callers)
	if len(es) != 2 || es[0].Label != "monitor" || es[1].Label != "send-test" {
		t.Fatalf("callers %+v, want monitor kept and send-test added", es)
	}
	if es[1].PublicKey != base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)) {
		t.Fatal("the callers file names another public key")
	}
	if st, _ := os.Stat(callers); st.Mode().Perm() != 0o640 {
		t.Fatalf("callers file mode %v, want its own 0640 kept", st.Mode().Perm())
	}
	assertNoKeyMaterial(t, stdout.String()+stderr.String(), priv)
	if !strings.Contains(stdout.String(), keyPath) || !strings.Contains(stdout.String(), "restart the gateway") {
		t.Fatalf("output %q does not say where the key is and that the gateway must restart", stdout.String())
	}
}

// Every refusal changes nothing: an existing key file, a caller the file
// already names, a key written into the gateway's bundle, a bad label, a
// callers file that is not one, and a bundle that is not a directory.
func TestScenarioKey_RefusesWithoutChangingAnything(t *testing.T) {
	bundle := scenarioBundle(t, "my-provider")
	callers := filepath.Join(bundle, scenarioCallersFileName)
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"scenario-key", "--bundle", bundle, "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("first key: exit %d: %s", code, stderr.String())
	}
	keyPath := filepath.Join(out, "scenario-caller-send-test.sign")
	keyBefore, _ := os.ReadFile(keyPath)
	callersBefore, _ := os.ReadFile(callers)

	otherOut := t.TempDir()
	badCallers := filepath.Join(t.TempDir(), "callers.json")
	if err := os.WriteFile(badCallers, []byte(`{"not":"an array"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	k := base64.StdEncoding.EncodeToString(make([]byte, 32))
	dupCallers := filepath.Join(t.TempDir(), "callers.json")
	if err := os.WriteFile(dupCallers, []byte(`[{"label":"a","public_key":"`+k+`"},{"label":"a","public_key":"`+k+`"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(bundle, "keys")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "bundle-link")
	if err := os.Symlink(bundle, link); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"an existing key file":            {"--callers-file", filepath.Join(t.TempDir(), "c.json"), "--out", out},
		"a caller the file names":         {"--bundle", bundle, "--out", otherOut},
		"the key in the bundle":           {"--bundle", bundle, "--out", bundle, "--label", "other"},
		"the key inside the bundle":       {"--bundle", bundle, "--out", inside, "--label", "other"},
		"the key in the bundle by link":   {"--bundle", bundle, "--out", link, "--label", "other"},
		"the key beside a callers file":   {"--callers-file", callers, "--out", bundle, "--label", "other"},
		"a bad label":                     {"--bundle", bundle, "--out", otherOut, "--label", "../x"},
		"a callers file that is not":      {"--callers-file", badCallers, "--out", otherOut, "--label", "other"},
		"a callers file naming one twice": {"--callers-file", dupCallers, "--out", otherOut, "--label", "other"},
		"a bundle that is no dir":         {"--bundle", filepath.Join(bundle, "absent"), "--out", otherOut, "--label", "other"},
		"an out that is no dir":           {"--bundle", bundle, "--out", filepath.Join(otherOut, "absent"), "--label", "other"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(append([]string{"scenario-key"}, args...), &stdout, &stderr); code == 0 {
			t.Errorf("%s: exit 0, want a refusal", name)
		}
		if k, _ := os.ReadFile(keyPath); !bytes.Equal(k, keyBefore) {
			t.Fatalf("%s: the key file changed", name)
		}
		if c, _ := os.ReadFile(callers); !bytes.Equal(c, callersBefore) {
			t.Fatalf("%s: the callers file changed", name)
		}
		if entries, _ := os.ReadDir(otherOut); len(entries) != 0 {
			t.Fatalf("%s: a refusal wrote %v", name, entries)
		}
		if entries, _ := os.ReadDir(inside); len(entries) != 0 {
			t.Fatalf("%s: a refusal wrote %v into the bundle", name, entries)
		}
		if _, err := os.Stat(filepath.Join(bundle, "scenario-caller-other.sign")); err == nil {
			t.Fatalf("%s: a refusal wrote a key into the bundle", name)
		}
	}
	if b, _ := os.ReadFile(badCallers); string(b) != `{"not":"an array"}` {
		t.Fatal("a refused callers file was rewritten")
	}
}

// --force replaces the caller's key and its own entry, and no other.
func TestScenarioKey_ForceReplacesOnlyThatCaller(t *testing.T) {
	bundle := scenarioBundle(t, "my-provider")
	callers := filepath.Join(bundle, scenarioCallersFileName)
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	for _, label := range []string{"send-test", "other"} {
		if code := run([]string{"scenario-key", "--bundle", bundle, "--out", out, "--label", label}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s: exit %d: %s", label, code, stderr.String())
		}
	}
	before := readCallerEntries(t, callers)
	if code := run([]string{"scenario-key", "--bundle", bundle, "--out", out, "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--force: exit %d: %s", code, stderr.String())
	}
	after := readCallerEntries(t, callers)
	byLabel := map[string]string{}
	for _, e := range after {
		byLabel[e.Label] = e.PublicKey
	}
	priv, err := loadScenarioKey(filepath.Join(out, "scenario-caller-send-test.sign"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || byLabel["send-test"] != base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)) || byLabel["send-test"] == before[0].PublicKey {
		t.Fatalf("after --force %+v: want send-test's entry replaced by the new key", after)
	}
	if byLabel["other"] != before[1].PublicKey {
		t.Fatal("--force changed another caller's entry")
	}
}

// --force replaces a link at the key's path with the new key file; it never
// writes through the link to what it names.
func TestScenarioKey_ForceNeverWritesThroughALink(t *testing.T) {
	out := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(out, "scenario-caller-send-test.sign")
	if err := os.Symlink(target, keyPath); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"scenario-key", "--out", out}, &stdout, &stderr); code == 0 {
		t.Fatal("a link at the key's path was replaced without --force")
	}
	if code := run([]string{"scenario-key", "--out", out, "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--force: exit %d: %s", code, stderr.String())
	}
	if b, _ := os.ReadFile(target); string(b) != "not yours" {
		t.Fatalf("--force wrote the key through the link: %q", b)
	}
	st, err := os.Lstat(keyPath)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key path after --force: %v %v, want a 0600 file", st, err)
	}
	if _, err := loadScenarioKey(keyPath); err != nil {
		t.Fatal(err)
	}
}

// With no callers file to edit, scenario-key prints the entry to add.
func TestScenarioKey_PrintsTheEntryWithNoCallersFile(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"scenario-key", "--out", out, "--label", "ci"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	priv, err := loadScenarioKey(filepath.Join(out, "scenario-caller-ci.sign"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(scenarioCallerEntry{Label: "ci", PublicKey: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))})
	if !strings.Contains(stdout.String(), string(want)) {
		t.Fatalf("output %q does not carry the entry %s", stdout.String(), want)
	}
	assertNoKeyMaterial(t, stdout.String()+stderr.String(), priv)
}

// assertNoKeyMaterial fails if out carries the private key in any encoding a
// slip could print: raw, base64 or hex, whole or its seed.
func assertNoKeyMaterial(t *testing.T, out string, priv ed25519.PrivateKey) {
	t.Helper()
	seed := priv.Seed()
	for name, enc := range map[string]string{
		"the key, raw":             string(priv),
		"the seed, raw":            string(seed),
		"the key, base64":          base64.StdEncoding.EncodeToString(priv),
		"the seed, base64":         base64.StdEncoding.EncodeToString(seed),
		"the key, raw url base64":  base64.RawURLEncoding.EncodeToString(priv),
		"the seed, raw url base64": base64.RawURLEncoding.EncodeToString(seed),
		"the key, hex":             hex.EncodeToString(priv),
		"the seed, hex":            hex.EncodeToString(seed),
	} {
		if strings.Contains(out, enc) {
			t.Fatalf("the output carries %s", name)
		}
	}
}

// scenario-sign prints one line, the header value, which signs exactly the
// request named, for the gateway --gateway-id (or --bundle's manifest, or
// SCENARIO_GATEWAY) names; it carries no key material.
func TestScenarioSign_PrintsOnlyTheHeader(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "k.sign")
	if err := writeKeyFile(keyPath, priv, false); err != nil {
		t.Fatal(err)
	}
	body := `{"branch":"covered","member":"M-1"}`
	bodyFile := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := scenarioBundle(t, "from-bundle")
	t.Setenv(scenarioGatewayEnv, "")
	for name, tc := range map[string]struct {
		args                 []string
		env                  string
		method, target, body string
		gateway              string
	}{
		"--gateway-id":     {[]string{"--key", keyPath, "--gateway-id", "my-provider", "POST", "/scenario/uc01?wait=30", body}, "", "POST", "/scenario/uc01?wait=30", body, "my-provider"},
		"--bundle":         {[]string{"--key", keyPath, "--bundle", bundle, "POST", "/scenario/uc01?wait=30", body}, "", "POST", "/scenario/uc01?wait=30", body, "from-bundle"},
		"SCENARIO_GATEWAY": {[]string{"--key", keyPath, "--bundle", bundle, "POST", "/scenario/uc01?wait=30", body}, "from-env", "POST", "/scenario/uc01?wait=30", body, "from-env"},
		"--body-file":      {[]string{"--key", keyPath, "--gateway-id", "my-provider", "--body-file", bodyFile, "POST", "/scenario/uc01?wait=30"}, "", "POST", "/scenario/uc01?wait=30", body, "my-provider"},
		"no body":          {[]string{"--key", keyPath, "--gateway-id", "my-provider", "GET", "/scenario/uc07/pending?patient=P-1"}, "", "GET", "/scenario/uc07/pending?patient=P-1", "", "my-provider"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(scenarioGatewayEnv, tc.env)
			var stdout, stderr bytes.Buffer
			if code := run(append([]string{"scenario-sign"}, tc.args...), &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			if len(lines) != 1 || stderr.Len() != 0 {
				t.Fatalf("stdout %q, stderr %q: want the header alone", stdout.String(), stderr.String())
			}
			a := verifyScenario(t, lines[0], tc.method, tc.target, []byte(tc.body), tc.gateway, pub)
			if a.HolderID != defaultScenarioLabel {
				t.Fatalf("label %q, want %q", a.HolderID, defaultScenarioLabel)
			}
			assertNoKeyMaterial(t, stdout.String()+stderr.String(), priv)
		})
	}
}

// scenario-sign refuses, and prints no header, without a key, without a
// gateway, or for a target outside /scenario/.
func TestScenarioSign_Refuses(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "k.sign")
	if err := writeKeyFile(keyPath, priv, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(scenarioKeyEnv, "")
	t.Setenv(scenarioKeyFileEnv, "")
	t.Setenv(scenarioGatewayEnv, "")
	for name, args := range map[string][]string{
		"no key":              {"--gateway-id", "p", "POST", "/scenario/uc01", "{}"},
		"no gateway":          {"--key", keyPath, "POST", "/scenario/uc01", "{}"},
		"another route":       {"--key", keyPath, "--gateway-id", "p", "POST", "/metadata", "{}"},
		"a bundle with no id": {"--key", keyPath, "--bundle", t.TempDir(), "POST", "/scenario/uc01", "{}"},
		"no target":           {"--key", keyPath, "--gateway-id", "p", "POST"},
		"a lower-case method": {"--key", keyPath, "--gateway-id", "p", "post", "/scenario/uc01", "{}"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"scenario-sign"}, args...), &stdout, &stderr); code == 0 || stdout.Len() != 0 {
			t.Errorf("%s: exit %d, stdout %q; want a refusal and no header", name, code, stdout.String())
		}
		assertNoKeyMaterial(t, stdout.String()+stderr.String(), priv)
	}
}

// send-test signs each scenario as its caller for the gateway's holder id, a
// key with no gateway stops it, and a 401 "unauthorized caller" carries a
// hint: how to create a caller when unsigned, what to check when signed.
func TestSendTest_SignsItsScenarios(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "k.sign")
	if err := writeKeyFile(keyPath, priv, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(scenarioKeyEnv, "")
	t.Setenv(scenarioKeyFileEnv, "")
	t.Setenv(scenarioGatewayEnv, "")
	refuse := false
	var verified, requests int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		if refuse || r.Header.Get(scenarioHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized caller"}`))
			return
		}
		verifyScenario(t, r.Header.Get(scenarioHeader), r.Method, r.URL.RequestURI(), body, "my-provider", pub)
		verified++
		_, _ = w.Write([]byte(`{"covered":true,"paRequired":true,"authNumber":"A-1","attested":true,"consentDenied":true,"denied":true}`))
	}))
	defer gw.Close()
	bundle := scenarioBundle(t, "my-provider")

	var stdout, stderr bytes.Buffer
	cmdSendTest([]string{"--gateway", gw.URL, "--scenario-key", keyPath, "--bundle", bundle}, &stdout, &stderr)
	if verified != len(sendTestCases()) {
		t.Fatalf("%d of %d scenarios arrived signed for my-provider:\n%s%s", verified, len(sendTestCases()), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "signing /scenario requests as send-test for my-provider") {
		t.Fatalf("stderr %q does not say it signs, as whom and for which gateway", stderr.String())
	}
	assertNoKeyMaterial(t, stdout.String()+stderr.String(), priv)

	requests = 0
	stdout.Reset()
	stderr.Reset()
	if code := cmdSendTest([]string{"--gateway", gw.URL, "--scenario-key", keyPath}, &stdout, &stderr); code != 2 || requests != 0 || !strings.Contains(stderr.String(), "--gateway-id") {
		t.Fatalf("a key with no gateway: exit %d after %d requests (%s), want 2 before any, naming --gateway-id", code, requests, stderr.String())
	}

	stdout.Reset()
	cmdSendTest([]string{"--gateway", gw.URL}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "shn scenario-key") {
		t.Fatalf("an unsigned 401 carries no hint to create a caller:\n%s", stdout.String())
	}
	refuse = true
	stdout.Reset()
	cmdSendTest([]string{"--gateway", gw.URL, "--scenario-key", keyPath, "--gateway-id", "my-provider"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), `refused the caller "send-test" signing for "my-provider"`) {
		t.Fatalf("a signed 401 carries no hint naming the caller and gateway:\n%s", stdout.String())
	}
}

// signScenarioRequest signs the request as it will be sent: its target with
// the query, and its body, which it puts back for sending.
func TestSignScenarioRequest_BindsTheQueryAndKeepsTheBody(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"branch":"covered"}`
	req := httptest.NewRequest(http.MethodPost, "http://gw.example/scenario/uc01?wait=30", strings.NewReader(body))
	if err := signScenarioRequest(req, "send-test", "my-provider", priv, time.Now()); err != nil {
		t.Fatal(err)
	}
	verifyScenario(t, req.Header.Get(scenarioHeader), http.MethodPost, "/scenario/uc01?wait=30", []byte(body), "my-provider", pub)
	if got, _ := io.ReadAll(req.Body); string(got) != body || req.ContentLength != int64(len(body)) {
		t.Fatalf("the body to send is %q (length %d), want %q", got, req.ContentLength, body)
	}
}

// TestMain runs the package's tests without the /scenario caller settings of
// the shell they run in, so a key in a developer's environment never makes a
// send-test row sign.
func TestMain(m *testing.M) {
	for _, k := range []string{scenarioKeyEnv, scenarioKeyFileEnv, scenarioGatewayEnv} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}
