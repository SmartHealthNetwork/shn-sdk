package main

// doctor.go implements `shn doctor`: the one-command self-validate a developer runs
// to confirm "am I wired up + does my eligibility round-trip conform". It fetches the
// sandbox discovery descriptor and runs eligibility against the seeded covered/
// not-covered personas using the dev's OWN registered identity.
//
// Probes are ATTRIBUTION-ORDERED: sandbox-health checks (not the dev's fault) run
// FIRST and fail with exitSandboxHealth; the wire-version check runs before any
// eligibility leg and fails with exitVersionUnsup; only then do dev-attributed checks
// (your client registered? your eligibility outcomes correct?) run. Each phase has a
// STABLE exit code so a script can branch on whose problem it is.
//
// The bar is wire-correctness + expected outcome — NOT IG validation. doctor calls NO
// FHIR/IG validator; the substrate validates server-side. Dep-purity: stdlib + shnsdk
// + x/crypto only (no internal/).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// Stable per-phase exit codes. A wrapper script branches on these to attribute a
// failure: a 10 is the sandbox operator's problem, a 30 is the dev's registration,
// a 40 is a genuine conformance mismatch.
const (
	exitOK              = 0
	exitSandboxHealth   = 10 // discovery/authz/registrar/payer unreachable or missing
	exitVersionUnsup    = 20 // descriptor wire version not supported by this CLI
	exitDevRegistration = 30 // the dev's own client not in /holders
	exitOutcome         = 40 // an eligibility run returned the wrong coverage
	exitUsage           = 2
)

// doctorClock supplies the clock for doctor's eligibility legs. It is nil in
// production (RunEligibility falls back to time.Now); tests set it so assertions/
// tokens land inside the fixed-clock fake substrate's skew window.
var doctorClock func() time.Time

// skewWarnThreshold is how far the local clock may drift from the sandbox's Date
// header before doctor WARNS (the substrate's hard skew limit is ~5m, so warn a bit
// inside it). A warning never fails the run.
const skewWarnThreshold = 4 * time.Minute

// cmdDoctor runs the attribution-ordered self-validate and returns a phase exit code.
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	discovery := fs.String("discovery", "", "Accounts service base URL (serves GET /discovery) (required)")
	id := fs.String("id", "", "your registered holder id (required)")
	keys := fs.String("keys", "", "key directory holding your signing+encryption keys")
	out := fs.String("out", ".", "alias for --keys (key directory)")
	persona := fs.String("persona", "", "run only the persona with this member id (default: all seeded personas)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	keysDir := *keys
	if keysDir == "" {
		keysDir = *out
	}
	if *discovery == "" || *id == "" {
		fmt.Fprintln(stderr, "shn doctor: --discovery and --id are required")
		return exitUsage
	}

	ctx := context.Background()
	c := http.DefaultClient

	pass := func(format string, a ...any) { fmt.Fprintf(stdout, "✓ "+format+"\n", a...) }
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stdout, "✗ "+format+"\n", a...)
		fmt.Fprintln(stdout, "FAIL")
		return code
	}

	discURL := strings.TrimRight(*discovery, "/") + "/discovery"

	// ───────── Phase A — sandbox health (failures are NOT the dev's fault) ─────────

	// A1. Discovery descriptor.
	disc, discDate, err := fetchDiscovery(ctx, c, discURL)
	if err != nil {
		return fail(exitSandboxHealth, "sandbox discovery unreachable/malformed: %v", err)
	}
	pass("sandbox discovery reachable (%s)", discURL)

	// A2. Wire-version check — FIRST real check, BEFORE any eligibility leg.
	if disc.WireProtocolVersion != shnsdk.WireProtocolVersion {
		return fail(exitVersionUnsup,
			"sandbox speaks wire %q; this shn CLI speaks %q — upgrade your SDK/CLI",
			disc.WireProtocolVersion, shnsdk.WireProtocolVersion)
	}
	pass("wire protocol %q supported", disc.WireProtocolVersion)

	// A3. Authz verifying key.
	authzPub, err := fetchAuthzPub(ctx, c, disc.AuthzPublicKeyURL)
	if err != nil {
		return fail(exitSandboxHealth, "sandbox authz public key unreachable/malformed (%s): %v", disc.AuthzPublicKeyURL, err)
	}
	pass("authz verifying key fetched")

	// A4. Registrar /holders feed.
	if disc.Endpoints.Registrar == "" {
		return fail(exitSandboxHealth, "sandbox discovery has no registrar endpoint")
	}
	holdersURL := strings.TrimRight(disc.Endpoints.Registrar, "/") + "/holders"
	holders, holdersDate, err := fetchHolders(ctx, c, holdersURL)
	if err != nil {
		return fail(exitSandboxHealth, "sandbox registrar /holders unreachable/malformed (%s): %v", holdersURL, err)
	}
	pass("registrar /holders feed fetched (%d holders)", len(holders))

	// A5. Each sandbox responder (the payer) must be registered with a decodable encPub.
	if len(disc.SandboxResponders) == 0 {
		return fail(exitSandboxHealth, "sandbox advertises no responders")
	}
	payerEnc := map[string]*[32]byte{}
	for _, resp := range disc.SandboxResponders {
		h, ok := holders[resp.HolderID]
		if !ok {
			return fail(exitSandboxHealth, "sandbox payer %q not registered in /holders", resp.HolderID)
		}
		enc, err := decodeEncPub(h.EncPub)
		if err != nil {
			return fail(exitSandboxHealth, "sandbox payer %q has a malformed encPub: %v", resp.HolderID, err)
		}
		payerEnc[resp.HolderID] = enc
	}
	pass("sandbox responder(s) registered with encryption keys")

	// A6. Clock-skew WARNING (never fails). Use any sandbox Date header we captured.
	if d := firstNonZero(holdersDate, discDate); !d.IsZero() {
		if skew := absDuration(doctorNow().Sub(d)); skew > skewWarnThreshold {
			fmt.Fprintf(stdout, "⚠ your clock is ~%s off the sandbox; assertions may be rejected (skew limit ~5m)\n", skew.Round(time.Second))
		}
	}

	// ───────── Phase B — dev-attributed ─────────

	// B1. The dev's own client present (and active) in /holders.
	if _, ok := holders[*id]; !ok {
		return fail(exitDevRegistration,
			"your client %q is not registered/active — run `shn register`, or it was revoked", *id)
	}
	pass("your client %q is registered", *id)

	// Load the dev identity once (the eligibility origin).
	devID, err := loadIdentity(keysDir, *id)
	if err != nil {
		return fail(exitDevRegistration, "load your identity from %q: %v", keysDir, err)
	}
	if doctorClock != nil {
		devID.Clock = doctorClock
	}

	// B2. Run eligibility per seeded persona; assert the coverage outcome.
	personas := disc.SandboxPersonas
	if *persona != "" {
		filtered := personas[:0:0]
		for _, p := range personas {
			if p.MemberID == *persona {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			return fail(exitUsage, "no seeded persona with member id %q", *persona)
		}
		personas = filtered
	}
	if len(personas) == 0 {
		return fail(exitSandboxHealth, "sandbox advertises no personas to test")
	}

	// One responder drives every persona (UC-01 has a single payer).
	resp := disc.SandboxResponders[0]
	payer := shnsdk.Payer{ID: resp.HolderID, EncPub: payerEnc[resp.HolderID], AuthzPub: authzPub}
	ep := shnsdk.Endpoints{HubURL: disc.Endpoints.Hub, AuthzURL: disc.Endpoints.Authz}

	for _, p := range personas {
		covered, _, err := devID.RunEligibility(ctx, c, ep, payer, "", p.MemberID, p.DOB, p.Family)
		if err != nil {
			return fail(exitSandboxHealth, "%s: eligibility round-trip failed: %v", p.MemberID, err)
		}
		want := p.ExpectedEligibility == "covered"
		if covered != want {
			return fail(exitOutcome, "%s: got covered=%v want %v", p.MemberID, covered, want)
		}
		pass("%s: covered=%v (expected %q)", p.MemberID, covered, p.ExpectedEligibility)
	}

	// B3. Prior-authorization — runs AFTER the eligibility loop (eligibility-first).
	// For each persona advertising an expected PA outcome, run the CRD→DTR→PAS round-trip
	// and assert the outcome. The clinical ANSWERS (per-scenario, via SandboxContextFor)
	// drive the outcome — not the member id (FR-35). A pended persona additionally
	// resumes with the sandbox supplemental and asserts the post-amend outcome.
	cpt, display, icd := shnsdk.SandboxUC03Order()
	for _, p := range personas {
		if p.ExpectedPriorAuth == "" {
			continue
		}
		cc, ok := shnsdk.SandboxContextFor(p.MemberID)
		if !ok {
			return fail(exitSandboxHealth, "priorauth %s: no sandbox clinical context", p.MemberID)
		}
		res, err := devID.RunPriorAuth(ctx, c, ep, payer, shnsdk.PriorAuthRequest{
			Member:           p.MemberID,
			DOB:              p.DOB,
			Family:           p.Family,
			NPI:              "",
			Clinical:         cc,
			ProcedureCPT:     cpt,
			ProcedureDisplay: display,
			DiagnosisICD10:   icd,
		})
		if err != nil {
			return fail(exitSandboxHealth, "priorauth %s: round-trip failed: %v", p.MemberID, err)
		}
		if res.Outcome != p.ExpectedPriorAuth {
			return fail(exitOutcome, "priorauth %s: got %s want %s", p.MemberID, res.Outcome, p.ExpectedPriorAuth)
		}
		pass("priorauth %s: %s", p.MemberID, res.Outcome)

		// Resume stage (UC-04): if the persona advertises a post-amend outcome, the
		// pended result must carry needed items + a resume handle; resume with the
		// sandbox supplemental and assert the post-amend outcome.
		if p.ExpectedAfterAmend == "" {
			continue
		}
		if res.Outcome != "pended" || len(res.NeededItems) == 0 || res.Resume == nil {
			return fail(exitOutcome, "priorauth %s: expected a pended result with needed items + resume handle, got %+v", p.MemberID, res)
		}
		amended, err := devID.ResumePriorAuth(ctx, c, ep, payer, *res.Resume, shnsdk.SandboxUC04Report())
		if err != nil {
			return fail(exitSandboxHealth, "priorauth %s: resume round-trip failed: %v", p.MemberID, err)
		}
		if amended.Outcome != p.ExpectedAfterAmend {
			return fail(exitOutcome, "priorauth %s: after amend got %s want %s", p.MemberID, amended.Outcome, p.ExpectedAfterAmend)
		}
		pass("priorauth %s: after amend %s", p.MemberID, amended.Outcome)
	}

	fmt.Fprintln(stdout, "PASS")
	return exitOK
}

// doctorNow / doctorClock indirection: clock-skew compares against the local wall clock
// (or the injected test clock).
func doctorNow() time.Time {
	if doctorClock != nil {
		return doctorClock()
	}
	return time.Now()
}

// holderEntry is one row of the registrar /holders feed (subset doctor needs).
type holderEntry struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	EncPub  string `json:"encPub"`
	SignPub string `json:"signPub"`
	BaseURL string `json:"baseURL"`
}

// fetchDiscovery GETs and decodes the discovery descriptor, returning it plus the
// server's Date header (for clock-skew).
func fetchDiscovery(ctx context.Context, c *http.Client, url string) (shnsdk.Discovery, time.Time, error) {
	body, date, err := getBounded(ctx, c, url)
	if err != nil {
		return shnsdk.Discovery{}, time.Time{}, err
	}
	var disc shnsdk.Discovery
	if err := json.Unmarshal(body, &disc); err != nil {
		return shnsdk.Discovery{}, time.Time{}, fmt.Errorf("decode descriptor: %w", err)
	}
	if disc.WireProtocolVersion == "" {
		return shnsdk.Discovery{}, time.Time{}, fmt.Errorf("descriptor missing wireProtocolVersion")
	}
	return disc, date, nil
}

// fetchAuthzPub GETs {url} and decodes {"pubkey": <std-base64 ed25519>} — the same
// encoding the substrate's authz /pubkey serves and RunEligibility verifies against.
func fetchAuthzPub(ctx context.Context, c *http.Client, url string) (ed25519.PublicKey, error) {
	if url == "" {
		return nil, fmt.Errorf("descriptor has no authzPublicKeyURL")
	}
	body, _, err := getBounded(ctx, c, url)
	if err != nil {
		return nil, err
	}
	var out struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode pubkey response: %w", err)
	}
	return decodeEd25519Pub(out.Pubkey)
}

// fetchHolders GETs the registrar /holders feed and indexes it by holder id, returning
// the map plus the server Date header.
func fetchHolders(ctx context.Context, c *http.Client, url string) (map[string]holderEntry, time.Time, error) {
	body, date, err := getBounded(ctx, c, url)
	if err != nil {
		return nil, time.Time{}, err
	}
	var rows []holderEntry
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode holders feed: %w", err)
	}
	out := make(map[string]holderEntry, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, date, nil
}

// getBounded GETs url, returning the bounded body and the parsed Date header (zero if
// absent/unparseable). A transport error or non-2xx status is an error.
func getBounded(ctx context.Context, c *http.Client, url string) ([]byte, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, time.Time{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var date time.Time
	if d := resp.Header.Get("Date"); d != "" {
		if t, perr := http.ParseTime(d); perr == nil {
			date = t
		}
	}
	return body, date, nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func firstNonZero(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}
