package main

// priorauth.go implements `shn priorauth`: the explicit dev-facing PA run, mirroring
// `shn doctor`'s resolution path. It fetches the network discovery descriptor and runs
// a prior-authorization (CRD→DTR→PAS) against the resolved test payer using the dev's
// OWN registered identity, then prints the outcome. Like doctor it calls NO FHIR/IG
// validator — the substrate validates server-side — and depends only on stdlib +
// shnsdk (no internal/).

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// resolveTestPayer resolves Payer{ID,EncPub,AuthzPub} + Endpoints for the given
// payer-identity claim (nil ⇒ legacy demoResponders path — spec R4; the caller
// prints the visibility note when legacy is true). Shared by `shn priorauth` and
// its `resume` subcommand; doctor shares the underlying resolvePersonaPayer.
func resolveTestPayer(ctx context.Context, c *http.Client, disc shnsdk.Discovery, pid *shnsdk.PayerIdentifier, cmd string, stderr io.Writer) (shnsdk.Payer, shnsdk.Endpoints, bool, int) {
	authzPub, err := fetchAuthzPub(ctx, c, disc.AuthzPublicKeyURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn %s: network authz public key unreachable/malformed: %v\n", cmd, err)
		return shnsdk.Payer{}, shnsdk.Endpoints{}, false, 1
	}
	if disc.Endpoints.Registrar == "" {
		fmt.Fprintf(stderr, "shn %s: network discovery has no registrar endpoint\n", cmd)
		return shnsdk.Payer{}, shnsdk.Endpoints{}, false, 1
	}
	holders, _, err := fetchHolders(ctx, c, strings.TrimRight(disc.Endpoints.Registrar, "/")+"/holders")
	if err != nil {
		fmt.Fprintf(stderr, "shn %s: network registrar /holders unreachable/malformed: %v\n", cmd, err)
		return shnsdk.Payer{}, shnsdk.Endpoints{}, false, 1
	}
	h, legacy, diag := resolvePersonaPayer(disc, pid, holders)
	if diag != "" {
		fmt.Fprintf(stderr, "shn %s: %s\n", cmd, diag)
		return shnsdk.Payer{}, shnsdk.Endpoints{}, false, 1
	}
	encPub, err := decodeEncPub(h.EncPub)
	if err != nil {
		fmt.Fprintf(stderr, "shn %s: test payer %q has a malformed encPub: %v\n", cmd, h.ID, err)
		return shnsdk.Payer{}, shnsdk.Endpoints{}, false, 1
	}
	return shnsdk.Payer{ID: h.ID, EncPub: encPub, AuthzPub: authzPub},
		shnsdk.Endpoints{HubURL: disc.Endpoints.Hub, AuthzURL: disc.Endpoints.Authz}, legacy, 0
}

// cmdPriorAuth implements `shn priorauth`: resolve Payer{ID,EncPub,AuthzPub} +
// Endpoints from the discovery descriptor (the same resolution doctor uses), load the
// dev identity from -keys/-out, run the test order for the given member, and print
// the outcome (approved/pended/denied/no-pa-required).
func cmdPriorAuth(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("priorauth", flag.ContinueOnError)
	fs.SetOutput(stderr)
	member := fs.String("member", "", "member id to prior-auth for (required)")
	discovery := fs.String("discovery", "", "Accounts service base URL (serves GET /discovery) (required)")
	id := fs.String("id", "", "your registered holder id (required)")
	keys := fs.String("keys", "", "key directory holding your signing+encryption keys")
	out := fs.String("out", ".", "alias for --keys (key directory)")
	resumeOut := fs.String("resume-out", "shn-resume.json", "where to write the resume handle if the PA pends")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	keysDir := *keys
	if keysDir == "" {
		keysDir = *out
	}
	if *member == "" || *discovery == "" || *id == "" {
		fmt.Fprintln(stderr, "shn priorauth: --member, --discovery and --id are required")
		return exitUsage
	}

	ctx := context.Background()
	c := http.DefaultClient

	discURL := strings.TrimRight(*discovery, "/") + "/discovery"
	disc, _, err := fetchDiscovery(ctx, c, discURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: network discovery unreachable/malformed: %v\n", err)
		return 1
	}
	if disc.WireProtocolVersion != shnsdk.WireProtocolVersion {
		fmt.Fprintf(stderr, "shn priorauth: network speaks wire %q; this CLI speaks %q — upgrade your SDK/CLI\n", disc.WireProtocolVersion, shnsdk.WireProtocolVersion)
		return 1
	}

	// Locate the persona to source DOB/Family from (the order itself is the fixed
	// test order for this member).
	var persona shnsdk.DiscoveryPersona
	for _, p := range disc.DemoPersonas {
		if p.MemberID == *member {
			persona = p
			break
		}
	}
	if persona.MemberID == "" {
		fmt.Fprintf(stderr, "shn priorauth: no test persona with member id %q\n", *member)
		return exitUsage
	}

	payer, ep, legacy, rc := resolveTestPayer(ctx, c, disc, persona.PayerID, "priorauth", stderr)
	if rc != 0 {
		return rc
	}
	if legacy {
		fmt.Fprintln(stderr, "shn priorauth: note: resolved via demoResponders — network predates persona payerId")
	}

	devID, err := loadIdentity(keysDir, *id)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: load your identity from %q: %v\n", keysDir, err)
		return 1
	}
	if doctorClock != nil {
		devID.Clock = doctorClock
	}

	// The persona's OWN advertised order (T14 fix round 9, ruling 2026-08-24): "a payer
	// verdict is a function of the ORDER CODE... a descriptor that advertises a
	// per-persona VERDICT while leaving the ORDER generic is incomplete by
	// construction." No fallback order — a persona advertising expectedPriorAuth with
	// no Order is a descriptor bug, surfaced rather than papered over. Clinical is left
	// zero-value: every mirrored family decides its verdict off the order code alone,
	// never off the QR's answered content. ProceedOnNotCovered so a persona whose plan
	// excludes the service comes back with the payer's FORMAL determination rather than
	// stopping at the advisory card.
	if persona.Order == nil {
		fmt.Fprintf(stderr, "shn priorauth: persona %q advertises no order (network descriptor is incomplete)\n", *member)
		return 1
	}
	req := shnsdk.PriorAuthRequest{
		Member: *member, DOB: persona.DOB, Family: persona.Family, NPI: "",
		ProcedureSystem: persona.Order.System, ProcedureCPT: persona.Order.Code,
		ProcedureDisplay: persona.Order.Display, DiagnosisICD10: persona.Order.Diagnosis,
		ProceedOnNotCovered: true,
	}
	res, err := devID.RunPriorAuth(ctx, c, ep, payer, req)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: %v\n", err)
		return 1
	}
	switch res.Outcome {
	case "approved", "no-pa-required":
		fmt.Fprintf(stdout, "outcome=%s preAuthRef=%s validUntil=%s\n", res.Outcome, res.PreAuthRef, res.ValidUntil)
	case "pended":
		if res.Resume == nil {
			fmt.Fprintln(stderr, "shn priorauth: pended but no resume handle returned")
			return 1
		}
		res.Resume.PayerID = persona.PayerID
		if err := writeResumeHandle(*resumeOut, *res.Resume); err != nil {
			fmt.Fprintf(stderr, "shn priorauth: write resume handle: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "outcome=pended needed=%s resume=%s\n", neededCodes(res.NeededItems), *resumeOut)
		fmt.Fprintf(stdout, "resume with: shn priorauth resume --resume %s --report-id <id> --report-cpt <cpt> --discovery <url> --id <id> -keys <dir>\n", *resumeOut)
	case "denied":
		reasonCode, rationale, appeal := "", "", ""
		if res.Denial != nil {
			reasonCode = res.Denial.ReasonCode
			rationale = res.Denial.Rationale
			if len(res.Denial.AppealNote) > 0 {
				appeal = res.Denial.AppealNote[0]
			}
		}
		fmt.Fprintf(stdout, "outcome=denied reasonCode=%s rationale=%q\n", reasonCode, rationale)
		if appeal != "" {
			fmt.Fprintf(stdout, "appeal: %s\n", appeal)
		}
	default:
		fmt.Fprintf(stdout, "outcome=%s\n", res.Outcome)
	}
	return 0
}

// cmdPriorAuthResume implements `shn priorauth resume`: load a resume handle, run the
// pended→amend ClaimUpdate, print the resumed outcome. The supplemental facts come from
// flags — the caller names the evidence they actually hold (v0.46.0 removed the
// fixture-supplemental shortcut that stamped a canned report instead).
func cmdPriorAuthResume(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("priorauth resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	resumePath := fs.String("resume", "shn-resume.json", "the resume handle file written by a pended `shn priorauth`")
	discovery := fs.String("discovery", "", "Accounts service base URL (serves GET /discovery) (required)")
	id := fs.String("id", "", "your registered holder id (required)")
	keys := fs.String("keys", "", "key directory holding your signing+encryption keys")
	out := fs.String("out", ".", "alias for --keys (key directory)")
	reportID := fs.String("report-id", "", "supplemental DiagnosticReport id (required)")
	cpt := fs.String("report-cpt", "", "supplemental procedure CPT")
	display := fs.String("report-display", "", "supplemental procedure display")
	agent := fs.String("provenance-agent", "", "FR-32 provenance source, e.g. Organization/<id>")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	keysDir := *keys
	if keysDir == "" {
		keysDir = *out
	}
	if *discovery == "" || *id == "" {
		fmt.Fprintln(stderr, "shn priorauth resume: --discovery and --id are required")
		return exitUsage
	}
	handle, err := readResumeHandle(*resumePath)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth resume: read resume handle %q: %v\n", *resumePath, err)
		return 1
	}
	supp := shnsdk.SupplementalReport{ReportID: *reportID, CPT: *cpt, Display: *display, ProvenanceAgent: *agent}

	ctx := context.Background()
	c := http.DefaultClient

	discURL := strings.TrimRight(*discovery, "/") + "/discovery"
	disc, _, err := fetchDiscovery(ctx, c, discURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth resume: network discovery unreachable/malformed: %v\n", err)
		return 1
	}
	if disc.WireProtocolVersion != shnsdk.WireProtocolVersion {
		fmt.Fprintf(stderr, "shn priorauth resume: network speaks wire %q; this CLI speaks %q — upgrade your SDK/CLI\n", disc.WireProtocolVersion, shnsdk.WireProtocolVersion)
		return 1
	}

	payer, ep, legacy, rc := resolveTestPayer(ctx, c, disc, handle.PayerID, "priorauth resume", stderr)
	if rc != 0 {
		return rc
	}
	if legacy {
		fmt.Fprintln(stderr, "shn priorauth resume: note: resolved via demoResponders — network predates persona payerId")
	}

	devID, err := loadIdentity(keysDir, *id)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth resume: load your identity from %q: %v\n", keysDir, err)
		return 1
	}
	if doctorClock != nil {
		devID.Clock = doctorClock
	}
	res, err := devID.ResumePriorAuth(ctx, c, ep, payer, handle, supp)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth resume: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "outcome=%s preAuthRef=%s validUntil=%s\n", res.Outcome, res.PreAuthRef, res.ValidUntil)
	return 0
}

// writeResumeHandle persists a pended PA's resume handle as JSON (a real integration
// reloads it later — the pend→amend gap spans process restarts). The embedded
// json.RawMessage fields (QRJSON/SRJSON) are compacted before marshaling so a
// round-trip through readResumeHandle preserves the original compact bytes.
func writeResumeHandle(path string, h shnsdk.PriorAuthResume) error {
	// Compact nested RawMessage fields so they read back byte-identical.
	if len(h.QRJSON) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, h.QRJSON); err == nil {
			h.QRJSON = json.RawMessage(buf.Bytes())
		}
	}
	if len(h.SRJSON) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, h.SRJSON); err == nil {
			h.SRJSON = json.RawMessage(buf.Bytes())
		}
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// readResumeHandle loads a resume handle written by writeResumeHandle. The embedded
// json.RawMessage fields are compacted on load so callers always see the same compact
// bytes regardless of how the file was formatted on disk.
func readResumeHandle(path string) (shnsdk.PriorAuthResume, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return shnsdk.PriorAuthResume{}, err
	}
	var h shnsdk.PriorAuthResume
	if err := json.Unmarshal(b, &h); err != nil {
		return shnsdk.PriorAuthResume{}, err
	}
	// Compact embedded RawMessage fields to canonical form so callers always see
	// compact bytes regardless of on-disk indentation (mirrors writeResumeHandle).
	if len(h.QRJSON) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, h.QRJSON); err == nil {
			h.QRJSON = json.RawMessage(buf.Bytes())
		}
	}
	if len(h.SRJSON) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, h.SRJSON); err == nil {
			h.SRJSON = json.RawMessage(buf.Bytes())
		}
	}
	return h, nil
}

// neededCodes joins the needed-item codes for one-line output.
func neededCodes(items []shnsdk.NeededItem) string {
	codes := make([]string, 0, len(items))
	for _, it := range items {
		codes = append(codes, it.Code)
	}
	return strings.Join(codes, ",")
}
