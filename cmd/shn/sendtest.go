package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
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
func runOneCase(client *http.Client, gateway string, personas *shnsdk.Discovery, discoveryGiven bool, c sendTestCase) sendTestResult {
	body := []byte(c.body)
	if personas != nil {
		b, err := sendTestBody(*personas, sendTestLane, c)
		if err != nil {
			return sendTestResult{c.label, false, 0, err.Error()}
		}
		body = b
	}
	url := strings.TrimRight(gateway, "/") + "/scenario/" + c.path
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return sendTestResult{c.label, false, 0, "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		detail := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		if personas == nil && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
			if discoveryGiven {
				detail += sendTestNoPersonasHint
			} else {
				detail += sendTestDiscoveryHint
			}
		}
		return sendTestResult{c.label, false, resp.StatusCode, detail}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return sendTestResult{c.label, false, resp.StatusCode, "non-JSON body: " + strings.TrimSpace(string(raw))}
	}
	if !c.pred(m) {
		return sendTestResult{c.label, false, resp.StatusCode, "assertion failed: " + strings.TrimSpace(string(raw))}
	}
	return sendTestResult{c.label, true, resp.StatusCode, ""}
}

// sendTestDiscoveryHint follows a 400 or 422 from a run that sent no persona:
// from shn-gateway v0.62.0 a scenario route takes the persona's member, order
// and claim facts in its request (a request naming no member is refused 400,
// one missing a fact 422), which the network's descriptor states.
const sendTestDiscoveryHint = " (from shn-gateway v0.62.0 a scenario route takes the persona's member and claim facts: pass --discovery <accounts-url> so each scenario sends the persona the network's descriptor states)"

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
	gateway := fs.String("gateway", "", "provider gateway base URL, e.g. http://127.0.0.1:8080 (required)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON results")
	discovery := fs.String("discovery", "", "optional: Accounts service base URL (serves GET /discovery), as for shn priorauth. When the network's descriptor states the scenario routes' personas, each scenario sends its persona (member, order and claim facts), which a gateway from shn-gateway v0.62.0 requires; otherwise each scenario sends what earlier releases sent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gateway == "" {
		fmt.Fprintln(stderr, "shn send-test: --gateway <url> is required")
		return 2
	}
	client := &http.Client{Timeout: 30 * time.Second}
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
	cases := sendTestCases()
	results := make([]sendTestResult, 0, len(cases))
	allPass := true
	for _, c := range cases {
		r := runOneCase(client, *gateway, personas, *discovery != "", c)
		results = append(results, r)
		if !r.Pass {
			allPass = false
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"allPass": allPass, "results": results})
	} else {
		for _, r := range results {
			mark := "✓"
			if !r.Pass {
				mark = "✗"
			}
			line := mark + " " + r.Label
			if !r.Pass {
				line += "  — " + r.Detail
			}
			fmt.Fprintln(stdout, line)
		}
		if allPass {
			fmt.Fprintln(stdout, "ALL SCENARIOS GREEN")
		} else {
			fmt.Fprintln(stdout, "SEND-TEST FAILED")
		}
	}
	if !allPass {
		return 1
	}
	return 0
}

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
