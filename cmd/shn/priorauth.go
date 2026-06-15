package main

// priorauth.go implements `shn priorauth`: the explicit dev-facing PA run, mirroring
// `shn doctor`'s resolution path. It fetches the sandbox discovery descriptor and runs
// a prior-authorization (CRD→DTR→PAS) against the seeded payer using the dev's
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

// resolveSandboxPayer resolves the descriptor + Payer{ID,EncPub,AuthzPub} + Endpoints
// from the sandbox discovery descriptor — the resolution shared by `shn priorauth` and
// its `resume` subcommand. On failure it writes the diagnostic to stderr and returns a
// non-zero rc.
func resolveSandboxPayer(ctx context.Context, c *http.Client, discovery string, stderr io.Writer) (shnsdk.Discovery, shnsdk.Payer, shnsdk.Endpoints, int) {
	discURL := strings.TrimRight(discovery, "/") + "/discovery"
	disc, _, err := fetchDiscovery(ctx, c, discURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: sandbox discovery unreachable/malformed: %v\n", err)
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	if disc.WireProtocolVersion != shnsdk.WireProtocolVersion {
		fmt.Fprintf(stderr, "shn priorauth: sandbox speaks wire %q; this CLI speaks %q — upgrade your SDK/CLI\n", disc.WireProtocolVersion, shnsdk.WireProtocolVersion)
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	if len(disc.SandboxResponders) == 0 {
		fmt.Fprintln(stderr, "shn priorauth: sandbox advertises no responders")
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	authzPub, err := fetchAuthzPub(ctx, c, disc.AuthzPublicKeyURL)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: sandbox authz public key unreachable/malformed: %v\n", err)
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	if disc.Endpoints.Registrar == "" {
		fmt.Fprintln(stderr, "shn priorauth: sandbox discovery has no registrar endpoint")
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	holders, _, err := fetchHolders(ctx, c, strings.TrimRight(disc.Endpoints.Registrar, "/")+"/holders")
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: sandbox registrar /holders unreachable/malformed: %v\n", err)
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	resp := disc.SandboxResponders[0]
	h, ok := holders[resp.HolderID]
	if !ok {
		fmt.Fprintf(stderr, "shn priorauth: sandbox payer %q not registered in /holders\n", resp.HolderID)
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	encPub, err := decodeEncPub(h.EncPub)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: sandbox payer %q has a malformed encPub: %v\n", resp.HolderID, err)
		return shnsdk.Discovery{}, shnsdk.Payer{}, shnsdk.Endpoints{}, 1
	}
	payer := shnsdk.Payer{ID: resp.HolderID, EncPub: encPub, AuthzPub: authzPub}
	ep := shnsdk.Endpoints{HubURL: disc.Endpoints.Hub, AuthzURL: disc.Endpoints.Authz}
	return disc, payer, ep, 0
}

// cmdPriorAuth implements `shn priorauth`: resolve Payer{ID,EncPub,AuthzPub} +
// Endpoints from the discovery descriptor (the same resolution doctor uses), load the
// dev identity from -keys/-out, run the sandbox order for the given member, and print
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

	disc, payer, ep, rc := resolveSandboxPayer(ctx, c, *discovery, stderr)
	if rc != 0 {
		return rc
	}

	// Locate the persona to source DOB/Family from (the order itself is the fixed
	// sandbox order for this member).
	var persona shnsdk.DiscoveryPersona
	for _, p := range disc.SandboxPersonas {
		if p.MemberID == *member {
			persona = p
			break
		}
	}
	if persona.MemberID == "" {
		fmt.Fprintf(stderr, "shn priorauth: no seeded persona with member id %q\n", *member)
		return exitUsage
	}

	devID, err := loadIdentity(keysDir, *id)
	if err != nil {
		fmt.Fprintf(stderr, "shn priorauth: load your identity from %q: %v\n", keysDir, err)
		return 1
	}
	if doctorClock != nil {
		devID.Clock = doctorClock
	}

	cc, ok := shnsdk.SandboxContextFor(*member)
	if !ok {
		fmt.Fprintf(stderr, "shn priorauth: no sandbox clinical context for member %q\n", *member)
		return exitUsage
	}
	cpt, display, icd := shnsdk.SandboxUC03Order()
	req := shnsdk.PriorAuthRequest{
		Member: *member, DOB: persona.DOB, Family: persona.Family, NPI: "",
		Clinical: cc, ProcedureCPT: cpt, ProcedureDisplay: display, DiagnosisICD10: icd,
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
		if err := writeResumeHandle(*resumeOut, *res.Resume); err != nil {
			fmt.Fprintf(stderr, "shn priorauth: write resume handle: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "outcome=pended needed=%s resume=%s\n", neededCodes(res.NeededItems), *resumeOut)
		fmt.Fprintf(stdout, "resume with: shn priorauth resume --resume %s --sandbox-supplemental --discovery <url> --id <id> -keys <dir>\n", *resumeOut)
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
// pended→amend ClaimUpdate, print the resumed outcome. --sandbox-supplemental supplies
// the SDK's SandboxUC04Report; otherwise the supplemental facts come from flags.
func cmdPriorAuthResume(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("priorauth resume", flag.ContinueOnError)
	fs.SetOutput(stderr)
	resumePath := fs.String("resume", "shn-resume.json", "the resume handle file written by a pended `shn priorauth`")
	discovery := fs.String("discovery", "", "Accounts service base URL (serves GET /discovery) (required)")
	id := fs.String("id", "", "your registered holder id (required)")
	keys := fs.String("keys", "", "key directory holding your signing+encryption keys")
	out := fs.String("out", ".", "alias for --keys (key directory)")
	sandboxSupp := fs.Bool("sandbox-supplemental", false, "use the SDK's SandboxUC04Report supplemental")
	reportID := fs.String("report-id", "", "supplemental DiagnosticReport id (when not --sandbox-supplemental)")
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
	var supp shnsdk.SupplementalReport
	if *sandboxSupp {
		supp = shnsdk.SandboxUC04Report()
	} else {
		supp = shnsdk.SupplementalReport{ReportID: *reportID, CPT: *cpt, Display: *display, ProvenanceAgent: *agent}
	}

	ctx := context.Background()
	c := http.DefaultClient
	_, payer, ep, rc := resolveSandboxPayer(ctx, c, *discovery, stderr)
	if rc != 0 {
		return rc
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
