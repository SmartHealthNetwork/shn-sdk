package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// sendTestCase is one scenario probe: POST {gateway}/scenario/{path} with body, then assert pred
// against the decoded JSON response.
type sendTestCase struct {
	label string
	path  string
	body  string
	pred  func(map[string]any) bool
	// scenario names the route's persona in the network's discovery descriptor
	// (scenarioPersonas): the route's path for its default branch, or
	// "<path>-<branch>" for a branch that selects another persona.
	scenario string
	// amend is true for a route whose flow sends an amendment: the request
	// states the amendment's certificationType.
	amend bool
}

func strField(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func boolField(m map[string]any, k string) bool  { b, _ := m[k].(bool); return b }

// sendTestCases are the 8 prior-auth UCs (both branches for uc01/uc05), asserted against the real
// /scenario response structs (gateway/engine/originate*.go): covered/paRequired/authNumber/
// attested/consentDenied/denied. Mirrors gateway/deploy/eval/smoke.sh's provider-data assertions.
func sendTestCases() []sendTestCase {
	return []sendTestCase{
		{"uc01 covered", "uc01", `{"branch":"covered"}`, func(m map[string]any) bool { return boolField(m, "covered") }, "uc01", false},
		{"uc01 notcovered", "uc01", `{"branch":"notcovered"}`, func(m map[string]any) bool { return !boolField(m, "covered") }, "uc01-notcovered", false},
		{"uc02", "uc02", `{}`, func(m map[string]any) bool { return !boolField(m, "paRequired") }, "uc02", false},
		{"uc03", "uc03", `{}`, func(m map[string]any) bool { return boolField(m, "paRequired") && strField(m, "authNumber") != "" }, "uc03", false},
		{"uc04", "uc04", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" }, "uc04", true},
		{"uc05", "uc05", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" }, "uc05", true},
		{"uc05 noconsent", "uc05", `{"branch":"noconsent"}`, func(m map[string]any) bool { return boolField(m, "consentDenied") }, "uc05-noconsent", true},
		{"uc06", "uc06", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" && boolField(m, "attested") }, "uc06", true},
		{"uc07", "uc07", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" && boolField(m, "attested") }, "uc07", true},
		{"uc08", "uc08", `{}`, func(m map[string]any) bool { return boolField(m, "denied") }, "uc08", false},
	}
}

type sendTestResult struct {
	Label  string `json:"label"`
	Pass   bool   `json:"pass"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Outcome is pass, fail or pended. A pend is the payer's answer, not a
	// failure: pended means the payer had not decided within the run's follow
	// bound (--follow), which Detail states with the continuation to resume.
	Outcome string `json:"outcome"`
}

// The outcomes a scenario reports.
const (
	sendTestPass   = "pass"
	sendTestFail   = "fail"
	sendTestPended = "pended"
)

func sendTestFailed(label string, status int, detail string) sendTestResult {
	return sendTestResult{Label: label, Status: status, Detail: detail, Outcome: sendTestFail}
}

// sendTestFollowDefault is how long, in seconds, send-test follows a pended
// scenario to the payer's decision unless --follow says otherwise.
const sendTestFollowDefault = 60

// sendTestInquireWaitMax is the longest wait send-test asks of one
// POST /scenario/pa/inquire: the gateway's own bound (shn-gateway's
// PASWaitMax), past which it would only clamp.
const sendTestInquireWaitMax = 30

// sendTestFollow is how a run follows a pend: for how long, by which clock.
type sendTestFollow struct {
	budget time.Duration
	now    func() time.Time
}

// sendTestSigner signs send-test's /scenario requests as a caller the gateway
// admits: its label and key, for the gateway's holder id.
type sendTestSigner struct {
	label, gateway string
	priv           ed25519.PrivateKey
}

// runOneCase POSTs the scenario and evaluates the predicate. A transport error, a non-200, or a
// non-JSON body is a fail (never a crash) — send-test is a diagnostic, so it always finishes the run.
//
// personas is the network's discovery descriptor when it states the routes'
// personas (scenarioPersonas), else nil: each case then sends its own body,
// exactly as earlier releases did.
//
// discoveryGiven is whether the run read a descriptor at all, which words the
// hint after a refusal.
//
// signer, when set, signs the request (a gateway that names its /scenario
// callers refuses every other); nil sends it unsigned.
//
// A scenario the payer pended, with a continuation, is followed to the payer's
// decision through POST /scenario/pa/inquire for at most follow.budget
// (followPend), and the case's predicate is then evaluated on the decision. A
// pend still undecided at the bound is reported as pended, with the
// continuation, not as a pass or a failure.
func runOneCase(client *http.Client, gateway string, personas *shnsdk.Discovery, discoveryGiven bool, signer *sendTestSigner, follow sendTestFollow, c sendTestCase) sendTestResult {
	body := []byte(c.body)
	if personas != nil {
		b, err := sendTestBody(*personas, sendTestLane, c)
		if err != nil {
			return sendTestFailed(c.label, 0, err.Error())
		}
		body = b
	}
	status, raw, failure := postScenario(client, strings.TrimRight(gateway, "/")+"/scenario/"+c.path, body, signer, follow.now)
	if failure != "" {
		return sendTestFailed(c.label, 0, failure)
	}
	if status != http.StatusOK {
		detail := fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(raw)))
		if status == http.StatusUnauthorized && strings.Contains(string(raw), "unauthorized caller") {
			if signer == nil {
				detail += sendTestUnsignedHint
			} else {
				detail += fmt.Sprintf(sendTestRefusedSignerHint, signer.label, signer.gateway)
			}
		}
		if personas == nil && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) {
			if discoveryGiven {
				detail += sendTestNoPersonasHint
			} else {
				detail += sendTestDiscoveryHint
			}
		}
		return sendTestFailed(c.label, status, detail)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return sendTestFailed(c.label, status, "non-JSON body: "+strings.TrimSpace(string(raw)))
	}
	if cont := strField(m, "continuation"); strField(m, "decision") == "pended" && cont != "" {
		decided, still, failure := followPend(client, gateway, signer, follow, cont)
		if failure != "" {
			return sendTestFailed(c.label, status, failure)
		}
		if decided == nil {
			if still == nil {
				still = raw
			}
			return sendTestResult{Label: c.label, Status: status, Outcome: sendTestPended,
				Detail: fmt.Sprintf("pended, not decided within %d s (the payer's answer is a pend; continue it with POST /scenario/pa/inquire {\"continuation\":%q}): %s",
					int(follow.budget/time.Second), cont, strings.TrimSpace(string(still)))}
		}
		for _, k := range []string{"decision", "authNumber", "validUntil", "denied", "rationale", "pendedItems"} {
			delete(m, k)
			if v, ok := decided[k]; ok {
				m[k] = v
			}
		}
		m["pended"] = false
		raw, _ = json.Marshal(m)
	}
	if !c.pred(m) {
		return sendTestFailed(c.label, status, "assertion failed: "+strings.TrimSpace(string(raw)))
	}
	return sendTestResult{Label: c.label, Pass: true, Status: status, Outcome: sendTestPass}
}

// postScenario POSTs body to url, signed when signer is set, and returns the
// status and body, or why the request could not be made.
func postScenario(client *http.Client, url string, body []byte, signer *sendTestSigner, now func() time.Time) (int, []byte, string) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, "request: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if signer != nil {
		if err := signScenarioRequest(req, signer.label, signer.gateway, signer.priv, now()); err != nil {
			return 0, nil, "sign the request: " + err.Error()
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "request failed: " + err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, ""
}

// followPend follows a pended scenario to the payer's decision: it asks the
// gateway's POST /scenario/pa/inquire to follow the continuation, each time for
// the rest of the bound (at most sendTestInquireWaitMax, the gateway's own),
// until the payer decides or the bound is reached. The gateway makes the
// inquiries, under its own schedule and cap; send-test only states how long it
// is willing to wait. It returns the decision, or nil and the last pended
// answer at the bound, or why the follow failed.
func followPend(client *http.Client, gateway string, signer *sendTestSigner, follow sendTestFollow, cont string) (map[string]any, []byte, string) {
	deadline := follow.now().Add(follow.budget)
	var last []byte
	// The gateway spends each asked wait; a gateway that answers at once, still
	// pended, cannot hold the run in a loop past this many asks.
	for asks, maxAsks := 0, int(follow.budget/(sendTestInquireWaitMax*time.Second))+2; asks < maxAsks; asks++ {
		left := deadline.Sub(follow.now())
		if left <= 0 {
			break
		}
		wait := min(int(math.Ceil(left.Seconds())), sendTestInquireWaitMax)
		body, _ := json.Marshal(map[string]any{"continuation": cont, "waitSeconds": wait})
		status, raw, failure := postScenario(client, strings.TrimRight(gateway, "/")+"/scenario/pa/inquire", body, signer, follow.now)
		if failure != "" {
			return nil, nil, "follow the pend: " + failure
		}
		if status != http.StatusOK {
			return nil, nil, fmt.Sprintf("follow the pend: HTTP %d: %s", status, strings.TrimSpace(string(raw)))
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, nil, "follow the pend: non-JSON body: " + strings.TrimSpace(string(raw))
		}
		if strField(d, "decision") != "pended" {
			return d, nil, ""
		}
		last = raw
	}
	return nil, last, ""
}

// sendTestDiscoveryHint follows a 400 or 422 from a run that sent no persona:
// from shn-gateway v0.62.0 a scenario route takes the persona's member, order
// and claim facts in its request (a request naming no member is refused 400,
// one missing a fact 422), which the network's descriptor states.
const sendTestDiscoveryHint = " (from shn-gateway v0.62.0 a scenario route takes the persona's member and claim facts: pass --discovery <accounts-url> so each scenario sends the persona the network's descriptor states)"

// sendTestUnsignedHint follows a 401 "unauthorized caller" to an unsigned
// run: the gateway names its /scenario callers and admits only their signed
// requests.
const sendTestUnsignedHint = " (the gateway authenticates its /scenario callers: create a caller with `shn scenario-key --bundle <the gateway's bundle>`, restart the gateway, and pass --scenario-key <its key file> --bundle <the gateway's bundle>)"

// sendTestRefusedSignerHint follows the same refusal to a signed run: the
// gateway does not admit this caller, or the requests are for another gateway.
const sendTestRefusedSignerHint = " (the gateway refused the caller %q signing for %q: check that its callers file names this caller with this key, that it was restarted since, and that %[2]q is its holder id)"

// sendTestNoPersonasHint follows the same refusal from a run that read a
// descriptor stating no scenario personas: there is no persona to send yet.
const sendTestNoPersonasHint = " (from shn-gateway v0.62.0 a scenario route takes the persona's member and claim facts, and the network's discovery descriptor states no scenario personas yet)"

// sendTestLane is the origination lane send-test drives: the one the
// shn-gateway evaluation bundles run (ORIGINATION_PROFILE=provider-data), whose
// assertions sendTestCases states. It selects which of the descriptor's
// personas each scenario sends; it states no fact of its own.
const sendTestLane = "provider-data"

// cmdSendTest drives a provider gateway's 8 /scenario/ucNN routes and tabulates per-UC pass/fail.
// Fencing is inherent: it drives a provider gateway the caller already runs, which only originates
// to its own configured payer.
func cmdSendTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("send-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: shn send-test --gateway <url> [flags]\n\n%s\n\n", sendTestExitStatus)
		fs.PrintDefaults()
	}
	gateway := fs.String("gateway", "", "provider gateway base URL, e.g. http://127.0.0.1:8080 (required)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON results")
	discovery := fs.String("discovery", "", "optional: Accounts service base URL (serves GET /discovery), as for shn priorauth. When the network's descriptor states the scenario routes' personas, each scenario sends its persona (member, order and claim facts), which a gateway from shn-gateway v0.62.0 requires; otherwise each scenario sends what earlier releases sent")
	keyFile := fs.String("scenario-key", "", "sign each /scenario request with this caller key file (shn scenario-key writes it); else "+scenarioKeyEnv+" or "+scenarioKeyFileEnv+". A gateway that names its /scenario callers refuses unsigned requests")
	label := fs.String("label", defaultScenarioLabel, "the caller's label, as the gateway's callers file names it")
	gatewayID := fs.String("gateway-id", "", "the gateway's holder id, which the requests are signed for; else "+scenarioGatewayEnv+", else --bundle's manifest")
	bundle := fs.String("bundle", "", "the gateway's bundle directory (its SHN_SECRETS), whose manifest names its holder id")
	followSecs := fs.Int("follow", sendTestFollowDefault, "seconds to follow each scenario the payer pended to its decision, through the gateway's POST /scenario/pa/inquire; 0 reports each pend as the payer answered it. A scenario still pended at the bound is reported as pended, with its continuation (exit status 3)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gateway == "" {
		fmt.Fprintln(stderr, "shn send-test: --gateway <url> is required")
		return 2
	}
	if *followSecs < 0 {
		fmt.Fprintln(stderr, "shn send-test: --follow must not be negative")
		return 2
	}
	follow := sendTestFollow{budget: time.Duration(*followSecs) * time.Second, now: time.Now}
	var signer *sendTestSigner
	priv, err := scenarioKeyFrom(*keyFile, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "shn send-test: %v\n", err)
		return 1
	}
	if priv != nil {
		holder, err := scenarioGatewayFrom(*gatewayID, *bundle, os.Getenv)
		if err != nil {
			fmt.Fprintf(stderr, "shn send-test: %v\n", err)
			return 1
		}
		if holder == "" {
			fmt.Fprintf(stderr, "shn send-test: a caller key needs the gateway it signs for: pass --gateway-id <holder id> or --bundle <the gateway's bundle>, or set %s\n", scenarioGatewayEnv)
			return 2
		}
		signer = &sendTestSigner{label: *label, gateway: holder, priv: priv}
		fmt.Fprintf(stderr, "shn send-test: signing /scenario requests as %s for %s\n", *label, holder)
	}
	// Room for one inquiry's longest wait, which the gateway spends before it answers.
	client := &http.Client{Timeout: (sendTestInquireWaitMax + 15) * time.Second}
	var personas *shnsdk.Discovery
	if *discovery != "" {
		// The descriptor is read exactly as shn priorauth reads it.
		disc, _, err := fetchDiscovery(context.Background(), client, strings.TrimRight(*discovery, "/")+"/discovery")
		if err != nil {
			fmt.Fprintf(stderr, "shn send-test: network discovery unreachable/malformed: %v\n", err)
			return 1
		}
		if len(disc.ScenarioPersonas) > 0 {
			personas = &disc
		}
	}
	if follow.budget > 0 {
		fmt.Fprintf(stderr, "shn send-test: following each pended scenario to the payer's decision for up to %d s\n", *followSecs)
	}
	cases := sendTestCases()
	results := make([]sendTestResult, 0, len(cases))
	failed, pended := 0, 0
	for _, c := range cases {
		r := runOneCase(client, *gateway, personas, *discovery != "", signer, follow, c)
		results = append(results, r)
		switch r.Outcome {
		case sendTestFail:
			failed++
		case sendTestPended:
			pended++
		}
	}
	allPass := failed == 0 && pended == 0
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"allPass": allPass, "failed": failed, "pended": pended, "followSeconds": *followSecs, "results": results})
	} else {
		for _, r := range results {
			mark := map[string]string{sendTestPass: "✓", sendTestFail: "✗", sendTestPended: "…"}[r.Outcome]
			line := mark + " " + r.Label
			if r.Outcome != sendTestPass {
				line += "  — " + r.Detail
			}
			fmt.Fprintln(stdout, line)
		}
		switch {
		case allPass:
			fmt.Fprintln(stdout, "ALL SCENARIOS GREEN")
		case failed > 0:
			fmt.Fprintln(stdout, "SEND-TEST FAILED")
		default:
			fmt.Fprintf(stdout, "SEND-TEST PENDED: %d scenario(s) not decided within %d s\n", pended, *followSecs)
		}
	}
	switch {
	case failed > 0:
		return 1
	case pended > 0:
		return sendTestExitPended
	}
	return 0
}

// sendTestExitPended is send-test's exit status when no scenario failed but at
// least one was still pended at the follow bound: not a pass, and not a failure.
const sendTestExitPended = 3

// sendTestExitStatus is the exit statuses as --help states them; scripts key on them.
const sendTestExitStatus = "Exit status: 0 every scenario passed; 1 a scenario failed, or the run could not start; 2 the command was misused; " +
	"3 no scenario failed, but at least one was still pended at the --follow bound."

// sendTestBody is a case's request: its own keys plus the persona the
// descriptor states for its route on lane — member, order and claim facts.
// A route the descriptor states no persona for, or a persona order stating no
// claim type, priority or service category, is refused: the probe states none.
func sendTestBody(disc shnsdk.Discovery, lane string, c sendTestCase) ([]byte, error) {
	p, ok := disc.ScenarioPersona(c.scenario, lane, "")
	if !ok || p.MemberID == "" {
		return nil, fmt.Errorf("the discovery descriptor states no persona for %s on lane %s", c.scenario, lane)
	}
	body := map[string]any{}
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		return nil, err
	}
	body["member"] = p.MemberID
	if p.Order == nil && c.amend {
		return nil, fmt.Errorf("the descriptor's %s persona %s states no order to amend", c.scenario, p.MemberID)
	}
	if p.Order != nil {
		facts, err := p.Order.ClaimFacts()
		if err != nil {
			return nil, err
		}
		if facts.ClaimType == nil || facts.Priority == nil || facts.ServiceCategory == nil {
			return nil, fmt.Errorf("the descriptor's %s persona %s states no claim type, priority or service category", c.scenario, p.MemberID)
		}
		body["order"] = map[string]string{"system": p.Order.System, "code": p.Order.Code}
		body["claimFacts"] = facts
		if c.amend {
			// The certificationType is the persona's own statement for its
			// amendment; a persona that states none is refused.
			if p.Amendment == nil || p.Amendment.CertificationType == nil || p.Amendment.CertificationType.Code == "" {
				return nil, fmt.Errorf("the descriptor's %s persona %s states no certificationType for its amendment", c.scenario, p.MemberID)
			}
			body["certificationType"] = p.Amendment.CertificationType
		}
	}
	return json.Marshal(body)
}
