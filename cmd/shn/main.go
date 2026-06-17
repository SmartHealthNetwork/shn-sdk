// Command shn is the public SHN participant CLI. It drives the standalone
// participant protocol via the shnsdk package only (stdlib + shnsdk; never the
// substrate's internal/ packages). Subcommands:
//
//	shn keygen       — generate signing+encryption keys and a public manifest snippet
//	shn register     — build a proof-of-possession registration and POST it to a registrar
//	shn eligibility  — run a coverage-eligibility round-trip through the Hub
//	shn priorauth    — run a prior-authorization (CRD→DTR→PAS) through the Hub
//	shn login        — authenticate the CLI to the Accounts service (OAuth loopback-PKCE)
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	"golang.org/x/crypto/curve25519"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches a subcommand and returns a process exit code. It is the testable
// core of main: all I/O goes through stdout/stderr and the returned code, so tests
// drive it without spawning a process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "keygen":
		return cmdKeygen(rest, stdout, stderr)
	case "register":
		return cmdRegister(rest, stdout, stderr)
	case "eligibility":
		return cmdEligibility(rest, stdout, stderr)
	case "priorauth":
		if len(rest) > 0 && rest[0] == "resume" {
			return cmdPriorAuthResume(rest[1:], stdout, stderr)
		}
		return cmdPriorAuth(rest, stdout, stderr)
	case "login":
		return runLogin(rest, stdout, stderr)
	case "clients":
		return cmdClients(rest, stdout, stderr)
	case "revoke":
		return cmdRevoke(rest, stdout, stderr)
	case "rotate":
		return cmdRotate(rest, stdout, stderr)
	case "doctor":
		return cmdDoctor(rest, stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "shn: unknown subcommand %q\n", sub)
		usage(stderr)
		return 2
	}
}

// hasFlag reports whether args contains the flag named name in any accepted form
// (--name, -name, --name=v, -name=v). It is used to route `register` to the
// Accounts path when --accounts is present without disturbing the operator flag set.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || a == "-"+name ||
			strings.HasPrefix(a, "--"+name+"=") || strings.HasPrefix(a, "-"+name+"=") {
			return true
		}
	}
	return false
}

// usage prints the top-level command summary.
func usage(w io.Writer) {
	fmt.Fprint(w, `shn — SHN participant CLI

usage: shn <command> [flags]

commands:
  keygen        generate signing+encryption keys and a public manifest snippet
  register      register a holder: --accounts (Accounts service) or --registrar (operator)
  eligibility   run a coverage-eligibility round-trip through the Hub
  priorauth     run a prior-authorization (CRD→DTR→PAS) through the Hub
  login         authenticate the CLI to the Accounts service (OAuth loopback-PKCE)
  clients       list your registered clients (Accounts service)
  revoke        revoke a client by id (Accounts service)
  rotate        rotate a holder's keys against the registrar (holder-self)
  doctor        self-validate against a sandbox: discovery + eligibility (wire-correctness)
`)
}

// File name constants for the registration bundle written by keygen/register.
const (
	signKeyFile  = "sign.key" // base64 ed25519 private key (64B seed||pub)
	encKeyFile   = "enc.key"  // base64 X25519 private key (32B)
	manifestFile = "manifest.json"
)

// cmdKeygen implements `shn keygen`: generate fresh keys for --id and write the
// private keys (0600) plus a public manifest snippet to the output dir.
func cmdKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "holder id (required)")
	role := fs.String("role", "", "holder role: provider|payer|facility|phg")
	baseURL := fs.String("base-url", "", "holder externally reachable base URL")
	out := fs.String("out", ".", "output directory for keys + manifest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		fmt.Fprintln(stderr, "shn keygen: --id is required")
		return 2
	}

	ident, err := shnsdk.GenerateIdentity(*id)
	if err != nil {
		fmt.Fprintf(stderr, "shn keygen: generate identity: %v\n", err)
		return 1
	}
	if err := writeIdentity(*out, ident, *role, *baseURL); err != nil {
		fmt.Fprintf(stderr, "shn keygen: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote keys + manifest for %q to %s\n", *id, *out)
	return 0
}

// writeIdentity writes the private signing/encryption keys (0600) and the public
// manifest snippet to dir. Delegates to the canonical shnsdk.WriteBundle so the
// on-disk format is owned by the SDK (producer and consumers share one type).
func writeIdentity(dir string, id shnsdk.Identity, role, baseURL string) error {
	return shnsdk.WriteBundle(dir, id, role, baseURL)
}

// loadIdentity reconstructs an Identity from the private key files written by
// keygen, deriving the public keys from the private material.
func loadIdentity(dir, holderID string) (shnsdk.Identity, error) {
	signB64, err := os.ReadFile(filepath.Join(dir, signKeyFile))
	if err != nil {
		return shnsdk.Identity{}, fmt.Errorf("read %s: %w", signKeyFile, err)
	}
	signPriv, err := base64.StdEncoding.DecodeString(string(signB64))
	if err != nil {
		return shnsdk.Identity{}, fmt.Errorf("decode %s: %w", signKeyFile, err)
	}
	if len(signPriv) != ed25519.PrivateKeySize {
		return shnsdk.Identity{}, fmt.Errorf("%s: bad ed25519 private key length %d", signKeyFile, len(signPriv))
	}
	encB64, err := os.ReadFile(filepath.Join(dir, encKeyFile))
	if err != nil {
		return shnsdk.Identity{}, fmt.Errorf("read %s: %w", encKeyFile, err)
	}
	encPrivRaw, err := base64.StdEncoding.DecodeString(string(encB64))
	if err != nil {
		return shnsdk.Identity{}, fmt.Errorf("decode %s: %w", encKeyFile, err)
	}
	if len(encPrivRaw) != 32 {
		return shnsdk.Identity{}, fmt.Errorf("%s: bad X25519 private key length %d", encKeyFile, len(encPrivRaw))
	}

	priv := ed25519.PrivateKey(signPriv)
	signPub := priv.Public().(ed25519.PublicKey)

	var encPriv, encPub [32]byte
	copy(encPriv[:], encPrivRaw)
	curve25519.ScalarBaseMult(&encPub, &encPriv)

	return shnsdk.Identity{
		HolderID: holderID,
		SignPub:  signPub,
		SignPriv: priv,
		EncPub:   &encPub,
		EncPriv:  &encPriv,
	}, nil
}

// loadOrGenIdentity loads keys from dir if present, else generates and writes a
// fresh identity for holderID (so register/eligibility work without a prior keygen).
// When it generates fresh keys it announces so on notify, so an operator who
// fat-fingers --out does not silently register throwaway keys.
func loadOrGenIdentity(dir, holderID, role, baseURL string, notify io.Writer) (shnsdk.Identity, error) {
	if _, err := os.Stat(filepath.Join(dir, signKeyFile)); err == nil {
		return loadIdentity(dir, holderID)
	}
	id, err := shnsdk.GenerateIdentity(holderID)
	if err != nil {
		return shnsdk.Identity{}, err
	}
	if err := writeIdentity(dir, id, role, baseURL); err != nil {
		return shnsdk.Identity{}, err
	}
	fmt.Fprintf(notify, "shn register: no keys in %s; generated a new identity there\n", dir)
	return id, nil
}

// cmdRegister implements `shn register`: load/generate keys, build the
// proof-of-possession registration body, and POST it to --registrar. The
// registrar's POST /register is Trust-admin-gated; a participant does NOT hold the
// admin key, so without --admin-assertion the registrar returns 401 (self-serve
// registration goes through the Accounts service via --accounts). --admin-assertion
// is the operator path: a base64 Trust-admin assertion forwarded as
// X-Holder-Assertion.
func cmdRegister(args []string, stdout, stderr io.Writer) int {
	// --accounts routes to the two-step Accounts-service path; the direct
	// --registrar operator path below is unchanged.
	if hasFlag(args, "accounts") {
		return cmdRegisterAccounts(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(stderr)
	role := fs.String("role", "", "holder role: provider|payer|facility|phg (required)")
	name := fs.String("name", "", "holder id (required)")
	baseURL := fs.String("base-url", "", "holder externally reachable base URL (required)")
	registrar := fs.String("registrar", "", "registrar base URL to POST /register to")
	portal := fs.String("portal", "", "deprecated alias — use --accounts (the Accounts service)")
	adminAssertion := fs.String("admin-assertion", "", "base64 Trust-admin assertion (operator path); forwarded as X-Holder-Assertion. Without it the registrar returns 401 — self-serve registration is via --accounts (the Accounts service).")
	out := fs.String("out", ".", "key directory (loaded if present, else generated)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *portal != "" {
		fmt.Fprintln(stderr, "shn register: --portal is deprecated; use `shn register --accounts <url>` (the Accounts service)")
		return 2
	}
	if *role == "" || *name == "" {
		fmt.Fprintln(stderr, "shn register: --role and --name are required")
		return 2
	}
	if *baseURL == "" {
		fmt.Fprintln(stderr, "shn register: --base-url is required (it is signed into the proof-of-possession and stored as the holder's reachable endpoint)")
		return 2
	}
	if *registrar == "" {
		fmt.Fprintln(stderr, "shn register: --registrar is required (or use --accounts for self-serve registration)")
		return 2
	}

	bu := *baseURL
	id, err := loadOrGenIdentity(*out, *name, *role, bu, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "shn register: load/generate identity: %v\n", err)
		return 1
	}

	reg := id.Registration(*role, bu)
	body, err := json.Marshal(reg)
	if err != nil {
		fmt.Fprintf(stderr, "shn register: marshal registration: %v\n", err)
		return 1
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, *registrar+"/register", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "shn register: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	if *adminAssertion != "" {
		req.Header.Set("X-Holder-Assertion", *adminAssertion)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "shn register: POST /register: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes))

	fmt.Fprintf(stdout, "registrar responded %d\n%s\n", resp.StatusCode, respBody)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if *adminAssertion == "" {
			fmt.Fprintln(stderr, "shn register: registrar rejected the request. POST /register is Trust-admin-gated; pass --admin-assertion (operator path) or use --accounts for self-serve registration.")
		}
		return 1
	}
	return 0
}

// cmdEligibility implements `shn eligibility`: run a coverage-eligibility
// round-trip through the Hub via shnsdk.Identity.RunEligibility and print coverage.
func cmdEligibility(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eligibility", flag.ContinueOnError)
	fs.SetOutput(stderr)
	member := fs.String("member", "", "member id (required)")
	dob := fs.String("dob", "", "patient date of birth YYYY-MM-DD (required)")
	family := fs.String("family", "", "patient family name (required)")
	npi := fs.String("npi", "", "provider NPI (optional)")
	hub := fs.String("hub", "", "Hub base URL (required)")
	authz := fs.String("authz", "", "Authorization Framework base URL (required)")
	payerID := fs.String("payer-id", "", "payer holder id (required)")
	payerEnc := fs.String("payer-enc", "", "base64 payer X25519 encryption public key (required)")
	authzPub := fs.String("authz-pub", "", "base64 Authorization Framework ed25519 verifying key (required)")
	name := fs.String("name", "", "originating holder id (defaults to the manifest in --out)")
	out := fs.String("out", ".", "key directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	missing := []string{}
	for n, v := range map[string]string{"member": *member, "dob": *dob, "family": *family, "hub": *hub, "authz": *authz, "payer-id": *payerID, "payer-enc": *payerEnc, "authz-pub": *authzPub} {
		if v == "" {
			missing = append(missing, "--"+n)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "shn eligibility: missing required flags: %v\n", missing)
		return 2
	}

	encPub, err := decodeEncPub(*payerEnc)
	if err != nil {
		fmt.Fprintf(stderr, "shn eligibility: --payer-enc: %v\n", err)
		return 2
	}
	authzKey, err := decodeEd25519Pub(*authzPub)
	if err != nil {
		fmt.Fprintf(stderr, "shn eligibility: --authz-pub: %v\n", err)
		return 2
	}

	holderID := *name
	if holderID == "" {
		if man, merr := readManifest(*out); merr == nil {
			holderID = man.ID
		}
	}
	if holderID == "" {
		fmt.Fprintln(stderr, "shn eligibility: --name is required (no manifest in --out to infer it)")
		return 2
	}
	id, err := loadIdentity(*out, holderID)
	if err != nil {
		fmt.Fprintf(stderr, "shn eligibility: load identity: %v\n", err)
		return 1
	}

	covered, reason, err := id.RunEligibility(context.Background(), http.DefaultClient,
		shnsdk.Endpoints{HubURL: *hub, AuthzURL: *authz},
		shnsdk.Payer{ID: *payerID, EncPub: encPub, AuthzPub: authzKey},
		*npi, *member, *dob, *family)
	if err != nil {
		fmt.Fprintf(stderr, "shn eligibility: %v\n", err)
		return 1
	}
	if covered {
		fmt.Fprintln(stdout, "covered: true")
	} else {
		fmt.Fprintf(stdout, "covered: false\nreason: %s\n", reason)
	}
	return 0
}

// readManifest loads the public manifest snippet from dir.
func readManifest(dir string) (shnsdk.Manifest, error) {
	mb, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return shnsdk.Manifest{}, err
	}
	var man shnsdk.Manifest
	if err := json.Unmarshal(mb, &man); err != nil {
		return shnsdk.Manifest{}, err
	}
	return man, nil
}

// decodeEncPub decodes a std-base64 32-byte X25519 public key.
func decodeEncPub(s string) (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not std-base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("X25519 public key must be 32 bytes, got %d", len(raw))
	}
	var k [32]byte
	copy(k[:], raw)
	return &k, nil
}

// decodeEd25519Pub decodes a std-base64 ed25519 public key.
func decodeEd25519Pub(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not std-base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
