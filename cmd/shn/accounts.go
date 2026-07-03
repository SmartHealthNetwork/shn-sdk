package main

// accounts.go implements the developer client-management commands that talk to
// the Accounts service (accounts.<apex>): `register --accounts` (two-step
// assign-id → sign-PoP → submit), `clients` (list), `revoke`, plus `rotate`
// (CLI-direct holder-self against the registrar, not the Accounts service).
//
// Dep-purity: this file uses ONLY stdlib + shnsdk + x/crypto. The Accounts API
// client (accounts.Client) is plain net/http; the substrate's internal/ packages
// are never imported.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
	acct "github.com/SmartHealthNetwork/shn-sdk/accounts"
)

// splitPositional pulls a single leading positional argument (e.g. the client/holder
// id for `revoke`/`rotate`) out of args so flags may appear before or after it. If the
// first arg starts with "-" it is treated as a flag and no positional is taken. Returns
// the positional ("" if none) and the remaining args to hand to flag.Parse.
func splitPositional(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// notLoggedIn prints the standard "run login" message and returns exit code 1. It is
// the single place the login prompt is worded, so register/clients/revoke agree.
func notLoggedIn(stderr io.Writer, accounts string) int {
	fmt.Fprintf(stderr, "shn: not logged in — run `shn login --accounts %s` first\n", accounts)
	return 1
}

// cmdRegisterAccounts is the `register --accounts` path: two-step registration via the
// Accounts service. It load-or-generates the keypair into -out, POSTs /clients to get a
// server-assigned id, sets that id as the identity's HolderID BEFORE building the PoP
// (so the proof signs the right id), then POSTs /clients/{id}/pop.
func cmdRegisterAccounts(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accounts := fs.String("accounts", "", "Accounts service base URL")
	defaultCache := filepath.Join(homeDir(), ".shn", "credentials")
	cache := fs.String("cache", defaultCache, "credential cache path")
	role := fs.String("role", "", "holder role: provider|payer|facility|phg (required)")
	name := fs.String("name", "", "client display name (required)")
	baseURL := fs.String("base-url", "", "holder externally reachable base URL (required)")
	out := fs.String("out", ".", "key directory (loaded if present, else generated)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *accounts == "" {
		fmt.Fprintln(stderr, "shn register: --accounts <url> is required")
		return 2
	}
	if *role == "" || *name == "" || *baseURL == "" {
		fmt.Fprintln(stderr, "shn register: --role, --name and --base-url are required")
		return 2
	}

	tok, ok := loadToken(*cache, *accounts)
	if !ok {
		return notLoggedIn(stderr, *accounts)
	}

	// Load-or-gen the keypair. The holder id is server-assigned, so the local id is a
	// placeholder derived from --name until POST /clients returns the real one.
	id, err := loadOrGenIdentity(*out, *name, *role, *baseURL, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "shn register: load/generate identity: %v\n", err)
		return 1
	}

	c := acct.NewClient(*accounts, tok)
	encPub := base64.StdEncoding.EncodeToString(id.EncPub[:])
	signPub := base64.StdEncoding.EncodeToString(id.SignPub)

	assignedID, err := c.Create(context.Background(), *name, *role, encPub, signPub, *baseURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn register: %v\n", err)
		return 1
	}

	// Set the server-assigned id BEFORE building the PoP so the proof signs it.
	id.HolderID = assignedID
	reg := id.Registration(*role, *baseURL)
	if err := c.SubmitPoP(context.Background(), assignedID, reg); err != nil {
		fmt.Fprintf(stderr, "shn register: %v\n", err)
		return 1
	}

	// Persist the manifest under the assigned id so subsequent ops resolve it.
	if err := writeIdentity(*out, id, *role, *baseURL); err != nil {
		fmt.Fprintf(stderr, "shn register: persist identity: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Registered %s. Keys in %s.\n", assignedID, *out)
	return 0
}

// cmdClients is `shn clients --accounts URL --cache C`: list the developer's clients
// in an aligned table.
func cmdClients(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clients", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accounts := fs.String("accounts", "", "Accounts service base URL (required)")
	defaultCache := filepath.Join(homeDir(), ".shn", "credentials")
	cache := fs.String("cache", defaultCache, "credential cache path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *accounts == "" {
		fmt.Fprintln(stderr, "shn clients: --accounts is required")
		return 2
	}
	tok, ok := loadToken(*cache, *accounts)
	if !ok {
		return notLoggedIn(stderr, *accounts)
	}
	rows, err := acct.NewClient(*accounts, tok).List(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "shn clients: %v\n", err)
		return 1
	}

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tROLE\tSTATUS\tCREATED\tSIGN-FP\tENC-FP")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.ID, r.Role, r.Status, r.CreatedAt, r.SignPubFp, r.EncPubFp)
	}
	_ = tw.Flush()
	return 0
}

// cmdRevoke is `shn revoke <id> --accounts URL --cache C`: revoke a client.
func cmdRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	accounts := fs.String("accounts", "", "Accounts service base URL (required)")
	defaultCache := filepath.Join(homeDir(), ".shn", "credentials")
	cache := fs.String("cache", defaultCache, "credential cache path")
	id, rest := splitPositional(args)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "shn revoke: usage: shn revoke <id> --accounts <url>")
		return 2
	}
	if *accounts == "" {
		fmt.Fprintln(stderr, "shn revoke: --accounts is required")
		return 2
	}
	tok, ok := loadToken(*cache, *accounts)
	if !ok {
		return notLoggedIn(stderr, *accounts)
	}
	if err := acct.NewClient(*accounts, tok).Revoke(context.Background(), id); err != nil {
		fmt.Fprintf(stderr, "shn revoke: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "revoked %s\n", id)
	return 0
}

// cmdRotate is `shn rotate <id> --registrar URL -out <keys>`: CLI-DIRECT holder-self
// key rotation against the registrar's RFC 7592 PUT /register/{id} (NOT the Accounts
// service). It authenticates with a holder-self assertion signed by the CURRENT key
// (audience "registrar", HolderID == id) and submits a body carrying NEW keys plus a
// proof-of-possession over the NEW signing key. On success the new keys replace the
// old ones in -out. The registrar verifies current-key auth before new-key PoP.
func cmdRotate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registrar := fs.String("registrar", "", "registrar base URL (required)")
	out := fs.String("out", ".", "key directory holding the CURRENT keys (overwritten with the new keys)")
	id, rest := splitPositional(args)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "shn rotate: usage: shn rotate <id> --registrar <url> -out <keys>")
		return 2
	}
	if *registrar == "" {
		fmt.Fprintln(stderr, "shn rotate: --registrar is required")
		return 2
	}

	// Load the CURRENT identity (authenticates the rotation).
	cur, err := loadIdentity(*out, id)
	if err != nil {
		fmt.Fprintf(stderr, "shn rotate: load current identity: %v\n", err)
		return 1
	}
	// role/baseURL come from the local manifest (rotation re-keys only — they must
	// match the existing record). A separate `loaded` bool avoids a sentinel that
	// would misfire for a holder whose role string happens to equal its id.
	var role, baseURL string
	loaded := false
	if man, merr := readManifest(*out); merr == nil {
		role, baseURL, loaded = man.Role, man.BaseURL, true
	}
	if !loaded || role == "" || baseURL == "" {
		fmt.Fprintf(stderr, "shn rotate: need a manifest in %s with role+baseURL (rotation re-keys only; role/baseURL must match the existing record)\n", *out)
		return 1
	}

	// Holder-self assertion signed by the CURRENT key (audience "registrar").
	hdr, err := cur.Assertion("registrar", time.Now(), shnsdk.MaxAssertionTTL)
	if err != nil {
		fmt.Fprintf(stderr, "shn rotate: build holder assertion: %v\n", err)
		return 1
	}

	// Generate the NEW identity (same id) and build the new-key PoP body.
	next, err := shnsdk.GenerateIdentity(id)
	if err != nil {
		fmt.Fprintf(stderr, "shn rotate: generate new keys: %v\n", err)
		return 1
	}
	reg := next.Registration(role, baseURL)
	body, err := json.Marshal(reg)
	if err != nil {
		fmt.Fprintf(stderr, "shn rotate: marshal rotation body: %v\n", err)
		return 1
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, strings.TrimRight(*registrar, "/")+"/register/"+id, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "shn rotate: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Holder-Assertion", hdr)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "shn rotate: PUT /register/%s: %v\n", id, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(stderr, "shn rotate: registrar responded %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	// Persist the NEW keys only after the registrar accepted them, so a rejected
	// rotation does not strand the holder with keys the registrar never recorded.
	if err := writeIdentity(*out, next, role, baseURL); err != nil {
		fmt.Fprintf(stderr, "shn rotate: persist new keys: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "rotated %s. New keys in %s.\n", id, *out)
	return 0
}
