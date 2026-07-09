package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sendTestCase is one scenario probe: POST {gateway}/scenario/{path} with body, then assert pred
// against the decoded JSON response.
type sendTestCase struct {
	label string
	path  string
	body  string
	pred  func(map[string]any) bool
}

func strField(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func boolField(m map[string]any, k string) bool  { b, _ := m[k].(bool); return b }

// sendTestCases are the 8 prior-auth UCs (both branches for uc01/uc05), asserted against the real
// /scenario response structs (gateway/engine/originate*.go): covered/paRequired/authNumber/
// attested/consentDenied/denied. Mirrors gateway/deploy/eval/smoke.sh's provider-data assertions.
func sendTestCases() []sendTestCase {
	return []sendTestCase{
		{"uc01 covered", "uc01", `{"branch":"covered"}`, func(m map[string]any) bool { return boolField(m, "covered") }},
		{"uc01 notcovered", "uc01", `{"branch":"notcovered"}`, func(m map[string]any) bool { return !boolField(m, "covered") }},
		{"uc02", "uc02", `{}`, func(m map[string]any) bool { return !boolField(m, "paRequired") }},
		{"uc03", "uc03", `{}`, func(m map[string]any) bool { return boolField(m, "paRequired") && strField(m, "authNumber") != "" }},
		{"uc04", "uc04", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" }},
		{"uc05", "uc05", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" }},
		{"uc05 noconsent", "uc05", `{"branch":"noconsent"}`, func(m map[string]any) bool { return boolField(m, "consentDenied") }},
		{"uc06", "uc06", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" && boolField(m, "attested") }},
		{"uc07", "uc07", `{}`, func(m map[string]any) bool { return strField(m, "authNumber") != "" && boolField(m, "attested") }},
		{"uc08", "uc08", `{}`, func(m map[string]any) bool { return boolField(m, "denied") }},
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
func runOneCase(client *http.Client, gateway string, c sendTestCase) sendTestResult {
	url := strings.TrimRight(gateway, "/") + "/scenario/" + c.path
	resp, err := client.Post(url, "application/json", bytes.NewBufferString(c.body))
	if err != nil {
		return sendTestResult{c.label, false, 0, "request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return sendTestResult{c.label, false, resp.StatusCode, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))}
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

// cmdSendTest drives a provider gateway's 8 /scenario/ucNN routes and tabulates per-UC pass/fail.
// Fencing is inherent: it drives a provider gateway the caller already runs, which only originates
// to its own configured payer (spec §7).
func cmdSendTest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("send-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	gateway := fs.String("gateway", "", "provider gateway base URL, e.g. http://127.0.0.1:8080 (required)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON results")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gateway == "" {
		fmt.Fprintln(stderr, "shn send-test: --gateway <url> is required")
		return 2
	}
	client := &http.Client{Timeout: 30 * time.Second}
	cases := sendTestCases()
	results := make([]sendTestResult, 0, len(cases))
	allPass := true
	for _, c := range cases {
		r := runOneCase(client, *gateway, c)
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
