package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pendingGateway is a provider gateway whose payer pends UC03–UC07 on submit, as the reference
// payer does (A4), and decides on the decideAt-th inquiry of each continuation (A1); decideAt 0
// never decides. Each inquiry spends the wait it asks for on clock, as the gateway's follow does.
// When pub is set, every request must be signed by it for gateway "my-provider".
type pendingGateway struct {
	t        *testing.T
	decideAt int
	clock    *fakeClock
	pub      ed25519.PublicKey
	inquire  int // status the inquiry route answers; 0 is 200

	mu    sync.Mutex
	asks  map[string]int
	waits []int
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (g *pendingGateway) serve() *httptest.Server {
	g.asks = map[string]int{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if g.pub != nil {
			verifyScenario(g.t, r.Header.Get(scenarioHeader), r.Method, r.URL.RequestURI(), body, "my-provider", g.pub)
		}
		uc := strings.TrimPrefix(r.URL.Path, "/scenario/")
		switch uc {
		case "uc01":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"covered":%v}`, !strings.Contains(string(body), "notcovered"))))
		case "uc02":
			_, _ = w.Write([]byte(`{"paRequired":false}`))
		case "uc08":
			_, _ = w.Write([]byte(`{"paRequired":true,"decision":"denied","denied":true}`))
		case "uc05":
			if strings.Contains(string(body), "noconsent") {
				_, _ = w.Write([]byte(`{"consentDenied":true}`))
				return
			}
			fallthrough
		case "uc03", "uc04", "uc06", "uc07":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"paRequired":true,"attested":true,"decision":"pended","pended":true,"pendedItems":["Q"],"continuation":"c-%s","continuationDurable":false}`, uc)))
		case "pa/inquire":
			if g.inquire != 0 {
				w.WriteHeader(g.inquire)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
				return
			}
			var req struct {
				Continuation string `json:"continuation"`
				WaitSeconds  int    `json:"waitSeconds"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				g.t.Errorf("inquiry body %q: %v", body, err)
			}
			g.mu.Lock()
			g.asks[req.Continuation]++
			n := g.asks[req.Continuation]
			g.waits = append(g.waits, req.WaitSeconds)
			g.mu.Unlock()
			if g.decideAt != 0 && n >= g.decideAt {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"decision":"approved","continuation":%q,"authNumber":"AUTH-%d","inquiries":%d}`, req.Continuation, n, n)))
				return
			}
			if g.clock != nil {
				g.clock.advance(time.Duration(req.WaitSeconds) * time.Second)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"decision":"pended","continuation":%q,"pendedItems":["Q"],"inquiries":%d}`, req.Continuation, n)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	g.t.Cleanup(s.Close)
	return s
}

func (g *pendingGateway) totalAsks() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, a := range g.asks {
		n += a
	}
	return n
}

// A payer that pends on submit and decides on a later inquiry is followed to its decision: the
// run is green, and every follow is an inquiry the gateway makes within its own wait bound. The
// same payer without a follow (--follow 0) is not a pass: each pend is reported as pended, with
// its continuation, as the bounded outcome it is.
func TestSendTest_FollowsAPendToThePayersDecision(t *testing.T) {
	g := &pendingGateway{t: t, decideAt: 2}
	gw := g.serve()

	var out, errb strings.Builder
	if code := cmdSendTest([]string{"--gateway", gw.URL}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, want 0:\n%s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "ALL SCENARIOS GREEN") || !strings.Contains(errb.String(), "for up to 60 s") {
		t.Fatalf("a payer deciding on the second inquiry is not green, or the bound is not printed:\n%s%s", out.String(), errb.String())
	}
	if got, want := g.totalAsks(), 2*5; got != want {
		t.Fatalf("%d inquiries, want %d (two for each of the five pended scenarios)", got, want)
	}
	for _, w := range g.waits {
		if w < 1 || w > sendTestInquireWaitMax {
			t.Fatalf("an inquiry asked to wait %d s, outside 1..%d", w, sendTestInquireWaitMax)
		}
	}

	g = &pendingGateway{t: t, decideAt: 2}
	gw = g.serve()
	out.Reset()
	errb.Reset()
	code := cmdSendTest([]string{"--gateway", gw.URL, "--follow", "0"}, &out, &errb)
	if code != sendTestExitPended {
		t.Fatalf("no follow: exit %d, want %d:\n%s", code, sendTestExitPended, out.String())
	}
	if g.totalAsks() != 0 {
		t.Fatalf("--follow 0 made %d inquiries", g.totalAsks())
	}
	for _, uc := range []string{"uc03", "uc04", "uc05", "uc06", "uc07"} {
		if !strings.Contains(out.String(), "… "+uc+"  — pended, not decided within 0 s") || !strings.Contains(out.String(), `{"continuation":"c-`+uc+`"}`) {
			t.Fatalf("%s is not reported pended with its continuation:\n%s", uc, out.String())
		}
	}
	if strings.Contains(out.String(), "✗") || !strings.Contains(out.String(), "SEND-TEST PENDED: 5 scenario(s) not decided within 0 s") {
		t.Fatalf("a pend is reported as a failure, or the summary is missing:\n%s", out.String())
	}

	g = &pendingGateway{t: t, decideAt: 2}
	gw = g.serve()
	out.Reset()
	cmdSendTest([]string{"--gateway", gw.URL, "--follow", "0", "--json"}, &out, &errb)
	var res struct {
		AllPass bool             `json:"allPass"`
		Failed  int              `json:"failed"`
		Pended  int              `json:"pended"`
		Results []sendTestResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatalf("--json: %v\n%s", err, out.String())
	}
	if res.AllPass || res.Failed != 0 || res.Pended != 5 {
		t.Fatalf("--json: allPass=%v failed=%d pended=%d, want false, 0, 5", res.AllPass, res.Failed, res.Pended)
	}
	pendedLabels := map[string]bool{"uc03": true, "uc04": true, "uc05": true, "uc06": true, "uc07": true}
	for _, r := range res.Results {
		want := sendTestPass
		if pendedLabels[r.Label] {
			want = sendTestPended
		}
		if r.Outcome != want {
			t.Fatalf("--json: %s outcome %q, want %q", r.Label, r.Outcome, want)
		}
	}
}

// A payer that never decides is followed for the bound and no longer: the gateway spends each
// asked wait, the asks add up to the bound, and the scenario is reported pended at it.
func TestSendTest_ANeverDecidingPayerIsPendedAtTheBound(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC)}
	g := &pendingGateway{t: t, clock: clock}
	gw := g.serve()
	follow := sendTestFollow{budget: 70 * time.Second, now: clock.Now}
	c := sendTestCaseLabelled(t, "uc04")
	r := runOneCase(&http.Client{}, gw.URL, nil, false, nil, follow, c)
	if r.Outcome != sendTestPended || r.Pass {
		t.Fatalf("outcome %q pass=%v, want pended", r.Outcome, r.Pass)
	}
	if !strings.Contains(r.Detail, "pended, not decided within 70 s") || !strings.Contains(r.Detail, `"c-uc04"`) {
		t.Fatalf("detail %q does not state the bound and the continuation", r.Detail)
	}
	if want := []int{30, 30, 10}; fmt.Sprint(g.waits) != fmt.Sprint(want) {
		t.Fatalf("asked waits %v, want %v", g.waits, want)
	}

	// A gateway that answers at once, still pended, without spending the wait, cannot hold the
	// run: the asks stop at the cap.
	still := &fakeClock{now: clock.Now()}
	g = &pendingGateway{t: t}
	gw = g.serve()
	r = runOneCase(&http.Client{}, gw.URL, nil, false, nil, sendTestFollow{budget: 60 * time.Second, now: still.Now}, c)
	if r.Outcome != sendTestPended || g.totalAsks() != 60/sendTestInquireWaitMax+2 {
		t.Fatalf("outcome %q after %d asks, want pended after %d", r.Outcome, g.totalAsks(), 60/sendTestInquireWaitMax+2)
	}
}

// An inquiry the gateway refuses fails the scenario, naming the follow.
func TestSendTest_ARefusedFollowFails(t *testing.T) {
	g := &pendingGateway{t: t, decideAt: 1, inquire: http.StatusNotFound}
	gw := g.serve()
	r := runOneCase(&http.Client{}, gw.URL, nil, false, nil, sendTestFollow{budget: time.Minute, now: time.Now}, sendTestCaseLabelled(t, "uc03"))
	if r.Outcome != sendTestFail || !strings.Contains(r.Detail, "follow the pend: HTTP 404") {
		t.Fatalf("outcome %q detail %q, want a failure naming the follow", r.Outcome, r.Detail)
	}
}

// The follow is signed as the scenarios are: a gateway that names its callers admits the
// inquiries too.
func TestSendTest_SignsItsFollows(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "k.sign")
	if err := writeKeyFile(keyPath, priv, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(scenarioKeyEnv, "")
	t.Setenv(scenarioKeyFileEnv, "")
	t.Setenv(scenarioGatewayEnv, "")
	g := &pendingGateway{t: t, decideAt: 1, pub: pub}
	gw := g.serve()
	var out, errb bytes.Buffer
	if code := cmdSendTest([]string{"--gateway", gw.URL, "--scenario-key", keyPath, "--gateway-id", "my-provider"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out.String(), errb.String())
	}
	if g.totalAsks() != 5 {
		t.Fatalf("%d signed inquiries, want 5", g.totalAsks())
	}
}

func sendTestCaseLabelled(t *testing.T, label string) sendTestCase {
	t.Helper()
	for _, c := range sendTestCases() {
		if c.label == label {
			return c
		}
	}
	t.Fatalf("no send-test case %q", label)
	return sendTestCase{}
}
