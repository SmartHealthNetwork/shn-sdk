package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// rotateRegistrar is a registrar stub that answers a rotation of id with
// status, and the holder feed with this build's contract versions.
func rotateRegistrar(t *testing.T, id string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/holders" {
			_ = json.NewEncoder(w).Encode([]shnsdk.Holder{{ID: id, ContractVersions: shnsdk.SupportedContractVersions()}})
			return
		}
		if r.Method != http.MethodPut || r.URL.Path != "/register/"+id {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedKeyDir writes a fresh identity for id into a new key directory, giving
// each key file the permissions perms names.
func seedKeyDir(t *testing.T, id string, perms map[string]os.FileMode) (string, shnsdk.Identity) {
	t.Helper()
	dir := t.TempDir()
	cur, err := shnsdk.GenerateIdentity(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(dir, cur, "provider", "https://acme.example"); err != nil {
		t.Fatal(err)
	}
	for name, mode := range perms {
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir, cur
}

// noStaging fails unless dir holds no predecessor staging directory.
func noStaging(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+predecessorDir+"-") {
			t.Fatalf("a staging directory was left behind: %s", e.Name())
		}
	}
}

// A rotation keeps the signing key it replaces, and the manifest naming its
// holder, in <out>/predecessor/: each byte for byte, readable and writable by
// the owner alone, and never the encryption key. Its output names the
// directory and says to set IDENTITY_PREDECESSOR_DIR to it for the overlap
// window, then remove it.
func TestRotate_KeepsTheReplacedSigningKeyAsThePredecessor(t *testing.T) {
	perms := map[string]os.FileMode{signKeyFile: 0o600, encKeyFile: 0o640, manifestFile: 0o644}
	keys, cur := seedKeyDir(t, "acme-7f3a", perms)
	before := map[string][]byte{}
	for _, name := range []string{signKeyFile, manifestFile} {
		b, err := os.ReadFile(filepath.Join(keys, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = b
	}
	srv := rotateRegistrar(t, "acme-7f3a", http.StatusOK)
	out, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
	if code != 0 {
		t.Fatalf("rotate exit=%d stderr=%s", code, stderr)
	}
	pred := filepath.Join(keys, predecessorDir)
	entries, err := os.ReadDir(pred)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{manifestFile, signKeyFile}) {
		t.Fatalf("the predecessor holds %q, want only the manifest and the signing key", names)
	}
	for name, want := range map[string]os.FileMode{signKeyFile: 0o600, manifestFile: 0o600} {
		info, err := os.Stat(filepath.Join(pred, name))
		if err != nil {
			t.Fatalf("predecessor %s: %v", name, err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("predecessor %s mode %v, want %v (the original's, owner only)", name, info.Mode().Perm(), want)
		}
		b, _ := os.ReadFile(filepath.Join(pred, name))
		if !bytes.Equal(b, before[name]) {
			t.Errorf("predecessor %s is not the replaced file byte for byte", name)
		}
	}
	signed, err := loadIdentitySignKey(pred)
	if err != nil || !bytes.Equal(signed, cur.SignPriv) {
		t.Fatalf("the kept signing key is not the replaced one: %v", err)
	}
	if man, err := readManifest(pred); err != nil || man.ID != "acme-7f3a" {
		t.Fatalf("the kept manifest does not name the holder: %+v, %v", man, err)
	}
	now, err := shnsdk.LoadBundle(keys)
	if err != nil || bytes.Equal(now.Identity.SignPub, cur.SignPub) {
		t.Fatalf("the key directory does not hold the new keys: %v", err)
	}
	for _, want := range []string{"The replaced signing key is in " + pred + ", with the manifest naming its holder.", "IDENTITY_PREDECESSOR_DIR=" + pred, "unset it and remove " + pred} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "replacing") {
		t.Errorf("output says it replaced a predecessor where there was none:\n%s", out)
	}
	noStaging(t, keys)

	// A second rotation replaces the predecessor with the key it replaces,
	// and says so.
	out, stderr, code = runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
	if code != 0 {
		t.Fatalf("second rotate exit=%d stderr=%s", code, stderr)
	}
	if signed, err := loadIdentitySignKey(pred); err != nil || !bytes.Equal(signed, now.Identity.SignPriv) {
		t.Fatalf("the second rotation's predecessor is not the key it replaced: %v", err)
	}
	if !strings.Contains(out, "replacing what an earlier rotation kept there") {
		t.Errorf("output does not say the predecessor was replaced:\n%s", out)
	}
	noStaging(t, keys)
}

// loadIdentitySignKey reads the signing key a key directory holds.
func loadIdentitySignKey(dir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, signKeyFile))
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(string(b))
}

// A staging directory an interrupted rotation left is swept at the start of
// the next run, whatever it holds.
func TestRotate_SweepsAnInterruptedRunsStaging(t *testing.T) {
	keys, _ := seedKeyDir(t, "acme-7f3a", nil)
	stale := filepath.Join(keys, "."+predecessorDir+"-12345")
	if err := os.MkdirAll(filepath.Join(stale, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, signKeyFile), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := rotateRegistrar(t, "acme-7f3a", http.StatusConflict)
	if _, _, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys); code != 1 {
		t.Fatalf("rotate exit=%d, want 1", code)
	}
	noStaging(t, keys)
}

// A rotation the registrar refuses keeps nothing: no predecessor directory,
// no staging directory, and the current keys in place.
func TestRotate_ARefusedRotationKeepsNoPredecessor(t *testing.T) {
	keys, cur := seedKeyDir(t, "acme-7f3a", nil)
	srv := rotateRegistrar(t, "acme-7f3a", http.StatusConflict)
	if _, _, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys); code != 1 {
		t.Fatalf("rotate exit=%d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(keys, predecessorDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused rotation kept a predecessor: %v", err)
	}
	noStaging(t, keys)
	b, err := shnsdk.LoadBundle(keys)
	if err != nil || !bytes.Equal(b.Identity.SignPub, cur.SignPub) {
		t.Fatalf("a refused rotation changed the keys: %v", err)
	}
}

// A predecessor that cannot be kept after the registrar accepted the new keys
// does not stop them being written, and the rotation exits 3, its own code,
// naming it.
func TestRotate_APredecessorThatCannotBeKept(t *testing.T) {
	keys, cur := seedKeyDir(t, "acme-7f3a", nil)
	locked := filepath.Join(keys, predecessorDir, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	srv := rotateRegistrar(t, "acme-7f3a", http.StatusOK)
	_, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
	if exitRotatedPredecessorNotKept != 3 || code != 3 || !strings.Contains(stderr, "the new keys are in place, but the replaced signing key was not kept") {
		t.Fatalf("rotate exit=%d stderr=%s; want 3 naming the predecessor", code, stderr)
	}
	b, err := shnsdk.LoadBundle(keys)
	if err != nil || bytes.Equal(b.Identity.SignPub, cur.SignPub) {
		t.Fatalf("the new keys were not written: %v", err)
	}
	noStaging(t, keys)
}

// A key directory whose keys cannot be copied aside is refused before
// anything is sent.
func TestRotate_RefusesWhenTheKeysCannotBeCopied(t *testing.T) {
	keys, _ := seedKeyDir(t, "acme-7f3a", nil)
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/holders" {
			_ = json.NewEncoder(w).Encode([]shnsdk.Holder{{ID: "acme-7f3a", ContractVersions: shnsdk.SupportedContractVersions()}})
			return
		}
		sent = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// The key directory is read-only: its keys can be read, but no copy of
	// them can be made inside it.
	if err := os.Chmod(keys, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(keys, 0o700) })
	_, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
	if code != 1 || sent || !strings.Contains(stderr, "shn rotate: keep the current keys:") {
		t.Fatalf("rotate exit=%d sent=%v stderr=%s; want 1 with nothing sent", code, sent, stderr)
	}
	noStaging(t, keys)
}

// installPredecessor reports a staging directory it cannot move.
func TestInstallPredecessor_ReportsAFailedMove(t *testing.T) {
	dir := t.TempDir()
	if replaced, err := installPredecessor(filepath.Join(dir, "missing"), filepath.Join(dir, predecessorDir)); err == nil || replaced {
		t.Fatalf("installPredecessor = %v, %v; want an error", replaced, err)
	}
}
