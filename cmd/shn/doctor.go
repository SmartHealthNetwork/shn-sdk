package main

// doctor.go implements `shn doctor`: the one-command self-validate a developer runs
// to confirm "am I wired up + does my eligibility round-trip conform". It fetches the
// network discovery descriptor and runs eligibility against the seeded covered/
// not-covered personas using the dev's OWN registered identity.
//
// Probes are ATTRIBUTION-ORDERED: network-health checks (not the dev's fault) run
// FIRST and fail with exitNetworkHealth; the wire-version check runs before any
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
// failure: a 10 is the network operator's problem, a 30 is the dev's registration,
// a 40 is a genuine conformance mismatch.
const (
	exitOK              = 0
	exitNetworkHealth   = 10 // discovery/authz/registrar/payer unreachable or missing
	exitVersionUnsup    = 20 // descriptor wire version not supported by this CLI
	exitDevRegistration = 30 // the dev's own client not in /holders
	exitOutcome         = 40 // an eligibility run returned the wrong coverage
	exitUsage           = 2
)

// doctorClock supplies the clock for doctor's eligibility legs. It is nil in
// production (RunEligibility falls back to time.Now); tests set it so assertions/
// tokens land inside the fixed-clock fake substrate's skew window.
var doctorClock func() time.Time

// skewWarnThreshold is how far the local clock may drift from the network's Date
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

	// ───────── Phase A — network health (failures are NOT the dev's fault) ─────────

	// A1. Discovery descriptor.
	disc, discDate, err := fetchDiscovery(ctx, c, discURL)
	if err != nil {
		return fail(exitNetworkHealth, "network discovery unreachable/malformed: %v", err)
	}
	pass("network discovery reachable (%s)", discURL)

	// A2. Wire-version check — FIRST real check, BEFORE any eligibility leg.
	if disc.WireProtocolVersion != shnsdk.WireProtocolVersion {
		return fail(exitVersionUnsup,
			"network speaks wire %q; this shn CLI speaks %q — upgrade your SDK/CLI",
			disc.WireProtocolVersion, shnsdk.WireProtocolVersion)
	}
	pass("wire protocol %q supported", disc.WireProtocolVersion)

	// A3. Authz verifying key.
	authzPub, err := fetchAuthzPub(ctx, c, disc.AuthzPublicKeyURL)
	if err != nil {
		return fail(exitNetworkHealth, "network authz public key unreachable/malformed (%s): %v", disc.AuthzPublicKeyURL, err)
	}
	pass("authz verifying key fetched")

	// A4. Registrar /holders feed.
	if disc.Endpoints.Registrar == "" {
		return fail(exitNetworkHealth, "network discovery has no registrar endpoint")
	}
	holdersURL := strings.TrimRight(disc.Endpoints.Registrar, "/") + "/holders"
	holders, holdersDate, err := fetchHolders(ctx, c, holdersURL)
	if err != nil {
		return fail(exitNetworkHealth, "network registrar /holders unreachable/malformed (%s): %v", holdersURL, err)
	}
	pass("registrar /holders feed fetched (%d holders)", len(holders))

	// A5. Resolve each advertised persona's test counterparty from the directory
	// (persona payerId → holder-attested payerIds; legacy descriptors fall back to
	// demoResponders — R4). Runs over ALL advertised personas: network health is
	// a property of the advertised fixture set, independent of --persona filtering.
	payerFor := map[string]holderEntry{} // member id → resolved payer
	payerEnc := map[string]*[32]byte{}   // holder id → decoded enc key
	legacy := false
	for _, p := range disc.DemoPersonas {
		h, viaLegacy, diag := resolvePersonaPayer(disc, p.PayerID, holders)
		if diag != "" {
			return fail(exitNetworkHealth, "%s", diag)
		}
		legacy = legacy || viaLegacy
		if _, done := payerEnc[h.ID]; !done {
			enc, err := decodeEncPub(h.EncPub)
			if err != nil {
				return fail(exitNetworkHealth, "test payer %q has a malformed encPub: %v", h.ID, err)
			}
			payerEnc[h.ID] = enc
		}
		payerFor[p.MemberID] = h
	}
	if legacy {
		fmt.Fprintln(stdout, "⚠ test counterparty resolved via demoResponders — network predates persona payerId")
		pass("test counterparty resolved via legacy responders (%d payer(s))", len(payerEnc))
	} else {
		pass("test counterparties resolve in the directory (%d payer(s))", len(payerEnc))
	}

	// A6. Clock-skew WARNING (never fails). Use any network Date header we captured.
	if d := firstNonZero(holdersDate, discDate); !d.IsZero() {
		if skew := absDuration(doctorNow().Sub(d)); skew > skewWarnThreshold {
			fmt.Fprintf(stdout, "⚠ your clock is ~%s off the network; assertions may be rejected (skew limit ~5m)\n", skew.Round(time.Second))
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
	personas := disc.DemoPersonas
	if *persona != "" {
		filtered := personas[:0:0]
		for _, p := range personas {
			if p.MemberID == *persona {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			return fail(exitUsage, "no test persona with member id %q", *persona)
		}
		personas = filtered
	}
	if len(personas) == 0 {
		return fail(exitNetworkHealth, "network advertises no personas to test")
	}

	ep := shnsdk.Endpoints{HubURL: disc.Endpoints.Hub, AuthzURL: disc.Endpoints.Authz}

	for _, p := range personas {
		h := payerFor[p.MemberID]
		payer := shnsdk.Payer{ID: h.ID, EncPub: payerEnc[h.ID], AuthzPub: authzPub}
		covered, _, err := devID.RunEligibility(ctx, c, ep, payer, "", p.MemberID, p.DOB, p.Family)
		if err != nil {
			return fail(exitNetworkHealth, "%s: eligibility round-trip failed: %v", p.MemberID, err)
		}
		want := p.ExpectedEligibility == "covered"
		if covered != want {
			return fail(exitOutcome, "%s: got covered=%v want %v", p.MemberID, covered, want)
		}
		pass("%s: covered=%v (expected %q)", p.MemberID, covered, p.ExpectedEligibility)
	}

	// B3. Prior-authorization — runs AFTER the eligibility loop (eligibility-first).
	// For each persona advertising an expected PA outcome, run the CRD→DTR→PAS round-trip
	// and assert the outcome. T14 fix round 9 (ruling 2026-08-24): the order comes from
	// the PERSONA'S OWN descriptor entry, not a single generic fill — "a payer verdict
	// is a function of the ORDER CODE... a descriptor that advertises a per-persona
	// VERDICT while leaving the ORDER generic is incomplete by construction." A pended
	// persona additionally resumes with a named supplemental report and asserts the
	// post-amend outcome. ProceedOnNotCovered so a persona advertising a denial is
	// carried to the payer's FORMAL determination, not stopped at the card.
	for _, p := range personas {
		if p.ExpectedPriorAuth == "" {
			continue
		}
		if p.Order == nil {
			return fail(exitOutcome, "priorauth %s: advertises expectedPriorAuth %q but no order (network descriptor is incomplete)", p.MemberID, p.ExpectedPriorAuth)
		}
		h := payerFor[p.MemberID]
		payer := shnsdk.Payer{ID: h.ID, EncPub: payerEnc[h.ID], AuthzPub: authzPub}
		res, err := devID.RunPriorAuth(ctx, c, ep, payer, shnsdk.PriorAuthRequest{
			Member:           p.MemberID,
			DOB:              p.DOB,
			Family:           p.Family,
			NPI:              "",
			ProcedureSystem:  p.Order.System,
			ProcedureCPT:     p.Order.Code,
			ProcedureDisplay: p.Order.Display,
			DiagnosisICD10:   p.Order.Diagnosis,

			ProceedOnNotCovered: true,
		})
		if err != nil {
			return fail(exitNetworkHealth, "priorauth %s: round-trip failed: %v", p.MemberID, err)
		}
		if res.Outcome != p.ExpectedPriorAuth {
			return fail(exitOutcome, "priorauth %s: got %s want %s", p.MemberID, res.Outcome, p.ExpectedPriorAuth)
		}
		pass("priorauth %s: %s", p.MemberID, res.Outcome)

		// Resume stage (pended→amend): if the persona advertises a post-amend outcome,
		// the pended result must carry needed items + a resume handle; resume with a
		// supplemental report attributed to the SAME order the persona pended on
		// (never a fixed/hardcoded family — a persona's amend evidence must match the
		// order it is evidence for) and assert the post-amend outcome.
		if p.ExpectedAfterAmend == "" {
			continue
		}
		if res.Outcome != "pended" || len(res.NeededItems) == 0 || res.Resume == nil {
			return fail(exitOutcome, "priorauth %s: expected a pended result with needed items + resume handle, got %+v", p.MemberID, res)
		}
		supp := shnsdk.SupplementalReport{
			ReportID:        "dr-" + p.MemberID + "-operative",
			CPT:             p.Order.Code,
			Display:         p.Order.Display,
			ProvenanceAgent: "Organization/provider",
		}
		amended, err := devID.ResumePriorAuth(ctx, c, ep, payer, *res.Resume, supp)
		if err != nil {
			return fail(exitNetworkHealth, "priorauth %s: resume round-trip failed: %v", p.MemberID, err)
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
	ID       string                   `json:"id"`
	Role     string                   `json:"role"`
	EncPub   string                   `json:"encPub"`
	SignPub  string                   `json:"signPub"`
	BaseURL  string                   `json:"baseURL"`
	PayerIDs []shnsdk.PayerIdentifier `json:"payerIds"`
}

// resolvePersonaPayer resolves the test counterparty for one persona.
// payerId present → directory resolution over the /holders rows by holder-attested
// payerIds (FR-G41 semantics; unique by AI-G12): exactly one match, role payer.
// Zero or multiple matches REFUSE (returned diagnostic) — never pick silently,
// never fall back on this branch (fallback would mask an AI-G12 regression).
// payerId absent → legacy path: DemoResponders[0] (legacy=true; caller prints
// the visibility line).
func resolvePersonaPayer(disc shnsdk.Discovery, pid *shnsdk.PayerIdentifier, holders map[string]holderEntry) (holderEntry, bool, string) {
	if pid != nil {
		var matches []holderEntry
		for _, h := range holders {
			for _, hp := range h.PayerIDs {
				if hp == *pid {
					matches = append(matches, h)
					break
				}
			}
		}
		switch len(matches) {
		case 1:
			if matches[0].Role != "payer" {
				return holderEntry{}, false, fmt.Sprintf("test counterparty %s|%s resolves to holder %q with role %q (want payer)", pid.System, pid.Value, matches[0].ID, matches[0].Role)
			}
			return matches[0], false, ""
		case 0:
			return holderEntry{}, false, fmt.Sprintf("test counterparty %s|%s resolves to no holder in the directory", pid.System, pid.Value)
		default:
			return holderEntry{}, false, fmt.Sprintf("test counterparty %s|%s resolves to %d holders — payer-identity uniqueness (AI-G12) violated", pid.System, pid.Value, len(matches))
		}
	}
	if len(disc.DemoResponders) == 0 {
		return holderEntry{}, false, "network advertises no test counterparty (no persona payerId, no demoResponders)"
	}
	h, ok := holders[disc.DemoResponders[0].HolderID]
	if !ok {
		return holderEntry{}, false, fmt.Sprintf("test payer %q not registered in /holders", disc.DemoResponders[0].HolderID)
	}
	return h, true, ""
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
