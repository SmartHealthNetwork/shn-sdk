package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// fakeGateway serves canned /scenario/ucNN responses so send-test's tabulation can be tested
// hermetically (no real gateway). Each handler returns the shape the real gateway would.
func fakeGateway(t *testing.T, overrides map[string]string) *httptest.Server {
	t.Helper()
	def := map[string]string{
		"/scenario/uc01": `{"covered":true}`, // default branch handler keys off the request body
		"/scenario/uc02": `{"paRequired":false}`,
		"/scenario/uc03": `{"paRequired":true,"authNumber":"AUTH-3"}`,
		"/scenario/uc04": `{"authNumber":"AUTH-4"}`,
		"/scenario/uc05": `{"authNumber":"AUTH-5"}`,
		"/scenario/uc06": `{"authNumber":"AUTH-6","attested":true}`,
		"/scenario/uc07": `{"authNumber":"AUTH-7","attested":true}`,
		"/scenario/uc08": `{"denied":true}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/scenario/uc01", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "notcovered") {
			w.Write([]byte(`{"covered":false}`))
			return
		}
		w.Write([]byte(def["/scenario/uc01"]))
	})
	mux.HandleFunc("/scenario/uc05", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "noconsent") {
			w.Write([]byte(`{"consentDenied":true}`))
			return
		}
		w.Write([]byte(def["/scenario/uc05"]))
	})
	for path, body := range def {
		if path == "/scenario/uc01" || path == "/scenario/uc05" {
			continue
		}
		b := body
		if o, ok := overrides[path]; ok {
			b = o
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(b)) })
	}
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestSendTestAllGreen(t *testing.T) {
	s := fakeGateway(t, nil)
	var out, errb strings.Builder
	code := cmdSendTest([]string{"--gateway", s.URL}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%q err=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "ALL SCENARIOS GREEN") {
		t.Errorf("missing summary; out=%q", out.String())
	}
	// All 10 checks (uc01 x2, uc05 x2) must show ✓.
	if strings.Contains(out.String(), "✗") {
		t.Errorf("unexpected failure mark; out=%q", out.String())
	}
}

func TestSendTestReportsFailure(t *testing.T) {
	// uc03 returns paRequired:false → its predicate (paRequired && authNumber) fails.
	s := fakeGateway(t, map[string]string{"/scenario/uc03": `{"paRequired":false}`})
	var out, errb strings.Builder
	code := cmdSendTest([]string{"--gateway", s.URL}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "✗ uc03") || !strings.Contains(out.String(), "SEND-TEST FAILED") {
		t.Errorf("expected uc03 failure + summary; out=%q", out.String())
	}
}

func TestSendTestNon200IsFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	var out, errb strings.Builder
	if code := cmdSendTest([]string{"--gateway", s.URL}, &out, &errb); code != 1 {
		t.Fatalf("exit = %d, want 1 (every UC should fail on 500)", code)
	}
}

func TestSendTestRequiresGateway(t *testing.T) {
	var out, errb strings.Builder
	if code := cmdSendTest([]string{}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (missing --gateway)", code)
	}
}

// recordingGateway answers every /scenario route as fakeGateway does and
// records each request it received, in order, as "<method> <path+query>
// <content-type> <body>". A refusing gateway answers what shn-gateway v0.62.0
// answers a request naming no member (400) or missing a claim fact (422).
func recordingGateway(t *testing.T, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var got []string
	ok := fakeGateway(t, nil)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Content-Type")+" "+string(b))
		switch status {
		case http.StatusOK:
		case http.StatusBadRequest:
			http.Error(w, `{"error":"the request names no \"member\": an origination route takes the member it originates for in its request body"}`, status)
			return
		default:
			http.Error(w, `{"error":"Claim.type absent at its source, so the request is not built"}`, status)
			return
		}
		resp, err := http.Post(ok.URL+r.URL.Path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		w.Write(out)
	}))
	t.Cleanup(s.Close)
	return s, &got
}

// v0611SendTestRequests are the requests shn-sdk v0.61.1's send-test sends,
// byte for byte and in order.
var v0611SendTestRequests = []string{
	`POST /scenario/uc01 application/json {"branch":"covered"}`,
	`POST /scenario/uc01 application/json {"branch":"notcovered"}`,
	`POST /scenario/uc02 application/json {}`,
	`POST /scenario/uc03 application/json {}`,
	`POST /scenario/uc04 application/json {}`,
	`POST /scenario/uc05 application/json {}`,
	`POST /scenario/uc05 application/json {"branch":"noconsent"}`,
	`POST /scenario/uc06 application/json {}`,
	`POST /scenario/uc07 application/json {}`,
	`POST /scenario/uc08 application/json {}`,
}

func discoveryServer(t *testing.T, disc shnsdk.Discovery) *httptest.Server {
	t.Helper()
	if disc.WireProtocolVersion == "" {
		disc.WireProtocolVersion = "1"
	}
	b, err := json.Marshal(disc)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discovery" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

// Without --discovery, or with a descriptor that states no scenario personas,
// send-test sends exactly what v0.61.1 sent: the fallback cannot drift.
func TestSendTestFallbackSendsV0611Bytes(t *testing.T) {
	disc := discoveryServer(t, shnsdk.Discovery{})
	for name, args := range map[string][]string{
		"no --discovery":                   {},
		"a descriptor stating no personas": {"--discovery", disc.URL},
	} {
		t.Run(name, func(t *testing.T) {
			gw, got := recordingGateway(t, http.StatusOK)
			var out, errb strings.Builder
			if code := cmdSendTest(append([]string{"--gateway", gw.URL}, args...), &out, &errb); code != 0 {
				t.Fatalf("exit %d; out=%q err=%q", code, out.String(), errb.String())
			}
			if !slices.Equal(*got, v0611SendTestRequests) {
				t.Fatalf("requests sent:\n%s\nwant v0.61.1's:\n%s", strings.Join(*got, "\n"), strings.Join(v0611SendTestRequests, "\n"))
			}
		})
	}
}

func scenarioPersonaFor(scenario, member, code string, amend bool) shnsdk.DiscoveryScenarioPersona {
	p := shnsdk.DiscoveryScenarioPersona{Scenario: scenario, Lane: sendTestLane, MemberID: member, Order: &shnsdk.DiscoveryOrder{
		System: "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", Code: code, Display: "d", Diagnosis: "I63.9",
		ClaimType:       &shnsdk.PASCoding{System: shnsdk.PASSystemClaimType, Code: "professional"},
		Priority:        &shnsdk.PASCoding{System: shnsdk.PASSystemProcessPriority, Code: "normal"},
		ServiceCategory: &shnsdk.PASCoding{System: shnsdk.PASSystemX12ServiceType, Code: "42"},
	}}
	if amend {
		p.Amendment = &shnsdk.DiscoveryAmendment{CertificationType: &shnsdk.PASCoding{System: shnsdk.PASSystemX12CertificationType, Code: "I"}}
	}
	return p
}

func allScenarioPersonas() []shnsdk.DiscoveryScenarioPersona {
	var ps []shnsdk.DiscoveryScenarioPersona
	for i, c := range sendTestCases() {
		p := scenarioPersonaFor(c.scenario, "MBR-"+strings.ToUpper(c.scenario), "G0151", c.amend)
		// Each persona states its own values, so a value the probe made up
		// cannot pass for the persona's.
		p.Order.Priority = &shnsdk.PASCoding{System: shnsdk.PASSystemProcessPriority, Code: []string{"normal", "stat", "deferred"}[i%3]}
		if p.Amendment != nil {
			p.Amendment.CertificationType.Code = []string{"I", "3", "4"}[i%3]
		}
		ps = append(ps, p)
	}
	return ps
}

// With a descriptor stating the scenario routes' personas, each scenario sends
// its persona: its own keys, the member, the order, the claim facts and, on an
// amending route, the certificationType.
func TestSendTestSendsTheDescriptorsPersonas(t *testing.T) {
	disc := discoveryServer(t, shnsdk.Discovery{ScenarioPersonas: allScenarioPersonas()})
	gw, got := recordingGateway(t, http.StatusOK)
	var out, errb strings.Builder
	if code := cmdSendTest([]string{"--gateway", gw.URL, "--discovery", disc.URL}, &out, &errb); code != 0 {
		t.Fatalf("exit %d; out=%q err=%q", code, out.String(), errb.String())
	}
	if len(*got) != len(sendTestCases()) {
		t.Fatalf("%d requests, want %d", len(*got), len(sendTestCases()))
	}
	for i, c := range sendTestCases() {
		head, body, _ := strings.Cut((*got)[i], " {")
		body = "{" + body
		var m map[string]any
		if head != "POST /scenario/"+c.path+" application/json" || json.Unmarshal([]byte(body), &m) != nil {
			t.Fatalf("request %d = %q", i, (*got)[i])
		}
		order, _ := m["order"].(map[string]any)
		if m["member"] != "MBR-"+strings.ToUpper(c.scenario) || m["claimFacts"] == nil ||
			order["system"] != "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets" || order["code"] != "G0151" {
			t.Fatalf("%s sent %s, want its persona's member, order and claim facts", c.label, body)
		}
		p, _ := (shnsdk.Discovery{ScenarioPersonas: allScenarioPersonas()}).ScenarioPersona(c.scenario, sendTestLane, "")
		wantFacts, _ := p.Order.ClaimFacts()
		var sent struct {
			ClaimFacts        shnsdk.PASClaimFacts `json:"claimFacts"`
			CertificationType *shnsdk.PASCoding    `json:"certificationType"`
		}
		_ = json.Unmarshal([]byte(body), &sent)
		if !reflect.DeepEqual(sent.ClaimFacts, wantFacts) {
			t.Fatalf("%s sent claim facts %+v, want the persona's %+v", c.label, sent.ClaimFacts, wantFacts)
		}
		if c.amend {
			if sent.CertificationType == nil || *sent.CertificationType != *p.Amendment.CertificationType {
				t.Fatalf("%s sent certificationType %+v, want the persona's %+v", c.label, sent.CertificationType, p.Amendment.CertificationType)
			}
		} else if _, has := m["certificationType"]; has {
			t.Fatalf("%s sent a certificationType on a route that sends no amendment: %s", c.label, body)
		}
		if strings.Contains(c.body, "branch") && m["branch"] == nil {
			t.Fatalf("%s lost its own keys: %s", c.label, body)
		}
	}
}

// A route the descriptor states no persona for, or an amending route whose
// persona states no certificationType, is refused for that scenario and
// nothing is sent for it: the probe states none of its own.
func TestSendTestRefusesAScenarioTheDescriptorDoesNotState(t *testing.T) {
	ps := allScenarioPersonas()
	for i := range ps {
		switch ps[i].Scenario {
		case "uc04":
			ps[i].Amendment = nil
		case "uc03":
			ps[i].Order.Priority = nil
		case "uc02":
			ps[i].MemberID = ""
		case "uc06":
			ps[i].Order = nil
		}
	}
	ps = slices.DeleteFunc(ps, func(p shnsdk.DiscoveryScenarioPersona) bool { return p.Scenario == "uc08" })
	disc := discoveryServer(t, shnsdk.Discovery{ScenarioPersonas: ps})
	gw, got := recordingGateway(t, http.StatusOK)
	var out, errb strings.Builder
	if code := cmdSendTest([]string{"--gateway", gw.URL, "--discovery", disc.URL}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1; out=%q", code, out.String())
	}
	for _, want := range []string{
		"✗ uc04  — the descriptor's uc04 persona MBR-UC04 states no certificationType",
		"✗ uc08  — the discovery descriptor states no persona for uc08",
		"✗ uc03  — the descriptor's uc03 persona MBR-UC03 states no claim type, priority or service category",
		"✗ uc02  — the discovery descriptor states no persona for uc02",
		"✗ uc06  — the descriptor's uc06 persona MBR-UC06 states no order to amend",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("out=%q, want %q", out.String(), want)
		}
	}
	for _, r := range *got {
		if strings.Contains(r, " /scenario/uc04 ") || strings.Contains(r, " /scenario/uc08 ") || strings.Contains(r, " /scenario/uc03 ") || strings.Contains(r, " /scenario/uc02 ") || strings.Contains(r, " /scenario/uc06 ") {
			t.Fatalf("sent %q for a scenario it refused", r)
		}
	}
}

// A gateway from shn-gateway v0.62.0 refuses a run that sent no persona: 400
// for a request naming no member, 422 for a missing claim fact. Either refusal
// says to pass --discovery; one from a run that did pass it does not, and
// neither does any other status.
func TestSendTestARefusalWithoutDiscoveryNamesTheFlag(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		gw, _ := recordingGateway(t, status)
		var out, errb strings.Builder
		cmdSendTest([]string{"--gateway", gw.URL}, &out, &errb)
		if strings.Count(out.String(), "pass --discovery <accounts-url>") != len(sendTestCases()) {
			t.Fatalf("%d: out=%q, want the --discovery hint on every scenario", status, out.String())
		}
		disc := discoveryServer(t, shnsdk.Discovery{ScenarioPersonas: allScenarioPersonas()})
		out.Reset()
		cmdSendTest([]string{"--gateway", gw.URL, "--discovery", disc.URL}, &out, &errb)
		if strings.Contains(out.String(), "pass --discovery") || strings.Contains(out.String(), "states no scenario personas") {
			t.Fatalf("%d: out=%q: a hint for a run that sent personas", status, out.String())
		}
		// A run that passed --discovery to a descriptor stating no personas
		// is told there are none yet, not to pass the flag it passed.
		none := discoveryServer(t, shnsdk.Discovery{})
		out.Reset()
		cmdSendTest([]string{"--gateway", gw.URL, "--discovery", none.URL}, &out, &errb)
		if strings.Contains(out.String(), "pass --discovery") || strings.Count(out.String(), "states no scenario personas yet") != len(sendTestCases()) {
			t.Fatalf("%d: out=%q, want the no-personas-yet hint on every scenario", status, out.String())
		}
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gw.Close)
	var out, errb strings.Builder
	cmdSendTest([]string{"--gateway", gw.URL}, &out, &errb)
	if strings.Contains(out.String(), "pass --discovery") {
		t.Fatalf("out=%q: the hint on a 500", out.String())
	}
}

// A --discovery that cannot be read stops the run before anything is sent.
func TestSendTestUnreadableDiscoveryStops(t *testing.T) {
	gw, got := recordingGateway(t, http.StatusOK)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) }))
	t.Cleanup(bad.Close)
	var out, errb strings.Builder
	if code := cmdSendTest([]string{"--gateway", gw.URL, "--discovery", bad.URL}, &out, &errb); code != 1 || len(*got) != 0 {
		t.Fatalf("exit %d, %d requests sent; want 1 and none", code, len(*got))
	}
}
