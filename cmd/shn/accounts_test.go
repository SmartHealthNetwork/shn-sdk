package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// runCLI2 invokes the dispatcher with args, capturing stdout, stderr and the exit code.
// (Same as runCLI in main_test.go; named distinctly for the accounts suite.)
func runCLI2(args ...string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return out.String(), errb.String(), code
}

// assertBearer fails the test unless the request carries the cached Cognito ID token
// as a Bearer credential.
func assertBearer(t *testing.T, r *http.Request) {
	t.Helper()
	got := r.Header.Get("Authorization")
	if got != "Bearer id-token-xyz" {
		t.Errorf("Authorization = %q, want Bearer id-token-xyz", got)
	}
}

// writeTestCache writes a credential cache JSON scoped to accountsURL with a
// far-future expiry, so loadToken returns the token. It uses writeCreds so the test
// exercises the same on-disk shape login.go produces.
func writeTestCache(t *testing.T, path, accountsURL, token string) {
	t.Helper()
	if err := writeCreds(path, cachedCreds{
		Accounts: strings.TrimRight(accountsURL, "/"),
		Token:    token,
		Expiry:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("writeTestCache: %v", err)
	}
}

// TestRegisterTwoStep_AgainstStubAccounts drives register --accounts (two-step),
// clients, and revoke against a stub Accounts service implementing the four endpoints.
func TestRegisterTwoStep_AgainstStubAccounts(t *testing.T) {
	var gotCreate, gotPoP map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		_ = json.NewDecoder(r.Body).Decode(&gotCreate)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "acme-7f3a"})
	})
	mux.HandleFunc("POST /clients/{id}/pop", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		_ = json.NewDecoder(r.Body).Decode(&gotPoP)
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "acme-7f3a", "name": "acme", "role": "provider", "status": "active", "createdAt": "2026-06-09T00:00:00Z", "signPubFp": "abcd1234", "encPubFp": "ef567890"}})
	})
	mux.HandleFunc("POST /clients/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		w.WriteHeader(200)
	})
	acct := httptest.NewServer(mux)
	defer acct.Close()

	cache := filepath.Join(t.TempDir(), "creds")
	writeTestCache(t, cache, acct.URL, "id-token-xyz")
	keys := t.TempDir()

	// register: two-step (POST /clients -> id, set HolderID, Registration PoP, POST /pop)
	out, stderr, code := runCLI2("register", "--accounts", acct.URL, "--cache", cache, "--role", "provider", "--name", "acme", "--base-url", "https://acme.example", "-out", keys)
	if code != 0 {
		t.Fatalf("register exit %d stderr=%s", code, stderr)
	}
	if gotCreate["name"] != "acme" || gotPoP["pop"] == nil {
		t.Errorf("create=%+v pop=%+v", gotCreate, gotPoP)
	}
	if gotCreate["role"] != "provider" || gotCreate["baseURL"] != "https://acme.example" {
		t.Errorf("create body missing role/baseURL: %+v", gotCreate)
	}
	if gotCreate["encPub"] == nil || gotCreate["signPub"] == nil {
		t.Errorf("create body missing keys: %+v", gotCreate)
	}
	// The PoP step must carry the server-assigned id's keys + role/baseURL.
	if gotPoP["role"] != "provider" || gotPoP["baseURL"] != "https://acme.example" {
		t.Errorf("pop body missing role/baseURL: %+v", gotPoP)
	}
	if !strings.Contains(out, "acme-7f3a") {
		t.Errorf("register output: %s", out)
	}

	// The PoP must verify against the registered signPub for the server-assigned id —
	// proving HolderID was set to the assigned id BEFORE signing.
	id, err := loadIdentity(keys, "acme-7f3a")
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	reg := id.Registration("provider", "https://acme.example")
	if gotPoP["pop"] != reg.Pop {
		t.Errorf("pop %v != recomputed %v (HolderID not set to assigned id before signing?)", gotPoP["pop"], reg.Pop)
	}

	// clients: prints the table
	out, stderr, code = runCLI2("clients", "--accounts", acct.URL, "--cache", cache)
	if code != 0 || !strings.Contains(out, "acme-7f3a") {
		t.Errorf("clients exit=%d out=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, "provider") || !strings.Contains(out, "active") {
		t.Errorf("clients table missing fields: %s", out)
	}

	// revoke
	_, stderr, code = runCLI2("revoke", "acme-7f3a", "--accounts", acct.URL, "--cache", cache)
	if code != 0 {
		t.Errorf("revoke exit %d stderr=%s", code, stderr)
	}
}

// TestRegister_NoToken_PromptsLogin: register --accounts with no cached token must
// fail with a "login" prompt and never hit the API.
func TestRegister_NoToken_PromptsLogin(t *testing.T) {
	acct := httptest.NewServer(http.NewServeMux()) // never hit
	defer acct.Close()
	_, stderr, code := runCLI2("register", "--accounts", acct.URL, "--cache", filepath.Join(t.TempDir(), "absent"), "--role", "provider", "--name", "x", "--base-url", "https://x", "-out", t.TempDir())
	if code == 0 {
		t.Error("register without a cached token should fail")
	}
	if !strings.Contains(strings.ToLower(stderr), "login") {
		t.Errorf("should prompt login: %s", stderr)
	}
}

// TestClients_NoToken_PromptsLogin: clients with no cached token must prompt login.
func TestClients_NoToken_PromptsLogin(t *testing.T) {
	acct := httptest.NewServer(http.NewServeMux())
	defer acct.Close()
	_, stderr, code := runCLI2("clients", "--accounts", acct.URL, "--cache", filepath.Join(t.TempDir(), "absent"))
	if code == 0 {
		t.Error("clients without a cached token should fail")
	}
	if !strings.Contains(strings.ToLower(stderr), "login") {
		t.Errorf("should prompt login: %s", stderr)
	}
}

// TestRevoke_NoToken_PromptsLogin: revoke with no cached token must prompt login.
func TestRevoke_NoToken_PromptsLogin(t *testing.T) {
	acct := httptest.NewServer(http.NewServeMux())
	defer acct.Close()
	_, stderr, code := runCLI2("revoke", "acme-7f3a", "--accounts", acct.URL, "--cache", filepath.Join(t.TempDir(), "absent"))
	if code == 0 {
		t.Error("revoke without a cached token should fail")
	}
	if !strings.Contains(strings.ToLower(stderr), "login") {
		t.Errorf("should prompt login: %s", stderr)
	}
}

// TestRevoke_RequiresID: revoke without a positional id is a usage error.
func TestRevoke_RequiresID(t *testing.T) {
	_, _, code := runCLI2("revoke", "--accounts", "http://x", "--cache", filepath.Join(t.TempDir(), "absent"))
	if code == 0 {
		t.Error("revoke without an id should fail")
	}
}

// TestRegisterAccounts_ServerError surfaces a non-2xx from the create step.
func TestRegisterAccounts_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "client quota reached"})
	})
	acct := httptest.NewServer(mux)
	defer acct.Close()

	cache := filepath.Join(t.TempDir(), "creds")
	writeTestCache(t, cache, acct.URL, "id-token-xyz")
	_, stderr, code := runCLI2("register", "--accounts", acct.URL, "--cache", cache, "--role", "provider", "--name", "acme", "--base-url", "https://acme.example", "-out", t.TempDir())
	if code == 0 {
		t.Error("register should fail when the create step errors")
	}
	if !strings.Contains(stderr, "quota") {
		t.Errorf("stderr should surface the server body: %s", stderr)
	}
}

// TestRotate_AgainstStubRegistrar drives rotate (CLI-direct holder-self against the
// registrar PUT /register/{id}): a holder-self assertion signed by the CURRENT key +
// a body with NEW keys and a PoP by the NEW key. It does NOT touch the Accounts service.
func TestRotate_AgainstStubRegistrar(t *testing.T) {
	keys := t.TempDir()
	// Seed a current identity for "acme-7f3a".
	cur, err := shnsdk.GenerateIdentity("acme-7f3a")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := writeIdentity(keys, cur, "provider", "https://acme.example"); err != nil {
		t.Fatalf("writeIdentity: %v", err)
	}
	curSignPub := append(ed25519.PublicKey(nil), cur.SignPub...)

	var gotAssertion string
	var gotBody shnsdk.RegistrationRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/register/acme-7f3a" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAssertion = r.Header.Get("X-Holder-Assertion")
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	out, stderr, code := runCLI2("rotate", "acme-7f3a", "--registrar", srv.URL, "-out", keys)
	if code != 0 {
		t.Fatalf("rotate exit=%d stderr=%s", code, stderr)
	}

	// The holder-self assertion must be a registrar-audience assertion for the id,
	// signed by the CURRENT key.
	raw, err := base64.StdEncoding.DecodeString(gotAssertion)
	if err != nil {
		t.Fatalf("assertion not base64: %v", err)
	}
	var a struct {
		HolderID string    `json:"holderId"`
		Audience string    `json:"audience"`
		IssuedAt time.Time `json:"issuedAt"`
		Expiry   time.Time `json:"expiry"`
		JTI      string    `json:"jti"`
		Sig      []byte    `json:"sig"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("assertion json: %v", err)
	}
	if a.HolderID != "acme-7f3a" || a.Audience != "registrar" {
		t.Errorf("assertion holderID/audience: %+v", a)
	}
	// Verify the assertion signature is by the CURRENT key (zero Sig before verify).
	signed := a
	signed.Sig = nil
	payload, _ := json.Marshal(signed)
	if !ed25519.Verify(curSignPub, payload, a.Sig) {
		t.Error("assertion not signed by the current key")
	}

	// The body must carry NEW keys (different from current) and a PoP verifying under
	// the NEW signPub.
	if gotBody.ID != "acme-7f3a" || gotBody.Role != "provider" || gotBody.BaseURL != "https://acme.example" {
		t.Errorf("rotate body fields: %+v", gotBody)
	}
	curSignPubB64 := base64.StdEncoding.EncodeToString(curSignPub)
	if gotBody.SignPub == curSignPubB64 {
		t.Error("rotate did not produce a NEW signing key")
	}
	newSignPub, err := base64.StdEncoding.DecodeString(gotBody.SignPub)
	if err != nil {
		t.Fatalf("new signPub not base64: %v", err)
	}
	popSig, err := base64.StdEncoding.DecodeString(gotBody.Pop)
	if err != nil {
		t.Fatalf("pop not base64: %v", err)
	}
	popPayload := []byte(gotBody.ID + "\n" + gotBody.Role + "\n" + gotBody.EncPub + "\n" + gotBody.SignPub + "\n" + gotBody.BaseURL)
	if !ed25519.Verify(ed25519.PublicKey(newSignPub), popPayload, popSig) {
		t.Error("rotate PoP does not verify under the new signPub")
	}

	// The new keys must be persisted to -out so a subsequent op uses them.
	reloaded, err := loadIdentity(keys, "acme-7f3a")
	if err != nil {
		t.Fatalf("reload after rotate: %v", err)
	}
	if base64.StdEncoding.EncodeToString(reloaded.SignPub) != gotBody.SignPub {
		t.Error("rotated keys not persisted to -out")
	}

	if !strings.Contains(out, "acme-7f3a") {
		t.Errorf("rotate output: %s", out)
	}
}

// TestRotate_RequiresFlags: rotate needs an id and a registrar.
func TestRotate_RequiresFlags(t *testing.T) {
	cases := [][]string{
		{"rotate", "--registrar", "http://r"}, // no id
		{"rotate", "acme-7f3a"},               // no registrar
	}
	for _, args := range cases {
		_, _, code := runCLI2(append(args, "-out", t.TempDir())...)
		if code == 0 {
			t.Errorf("rotate %v should fail", args)
		}
	}
}
