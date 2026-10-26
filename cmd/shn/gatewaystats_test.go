package main

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The gateway's sketch parameters, as /internal/stats states them.
const (
	tRelErr = 0.02
	tMin    = 1e-4
	tMax    = 300.0
)

var tGamma = (1 + tRelErr) / (1 - tRelErr)

// tSketch buckets vs as the gateway does, independently of the code under
// test: a value at or below tMin counts in the zero bucket, any other in
// bucket ceil(ln v / ln γ), clamped to the top bucket.
func tSketch(vs []float64) map[string]any {
	lg := math.Log(tGamma)
	hi := int64(math.Ceil(math.Log(tMax) / lg))
	counts := map[int64]int64{}
	var zero uint64
	var max float64
	for _, v := range vs {
		if v <= tMin {
			zero++
		} else {
			counts[min(int64(math.Ceil(math.Log(v)/lg)), hi)]++
		}
		max = math.Max(max, v)
	}
	idx := make([]int64, 0, len(counts))
	for i := range counts {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool { return idx[a] < idx[b] })
	buckets := []any{}
	for _, i := range idx {
		buckets = append(buckets, []any{i, counts[i]})
	}
	return map[string]any{"zero": zero, "buckets": buckets, "max": max}
}

// tQuantile is the nearest-rank q-quantile of vs read through the sketch's
// buckets: the reading one gateway gives for all of vs.
func tQuantile(vs []float64, q float64) float64 {
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	lg := math.Log(tGamma)
	hi := math.Ceil(math.Log(tMax) / lg)
	for i, v := range s {
		if float64(i+1) >= q*float64(len(s)) {
			if v <= tMin {
				return 0
			}
			b := math.Min(math.Ceil(math.Log(v)/lg), hi)
			return math.Min(2*math.Pow(tGamma, b)/(tGamma+1), s[len(s)-1])
		}
	}
	return s[len(s)-1]
}

var tNow = time.Date(2026, 10, 25, 23, 41, 30, 0, time.UTC)

// tAnswer is one replica's answer with a 5m window holding the given
// exchange series and backends, and a 1h window that is empty.
func tAnswer(replica string, exchanges, backends []any) map[string]any {
	return map[string]any{
		"replica":     replica,
		"since":       tNow.Add(-2 * time.Hour).Format(time.RFC3339),
		"generatedAt": tNow.Format(time.RFC3339),
		"sketch":      map[string]any{"relativeError": tRelErr, "minSeconds": tMin, "maxSeconds": tMax},
		"windows": []any{
			map[string]any{"window": "5m", "start": tNow.Truncate(time.Minute).Add(-5 * time.Minute).Format(time.RFC3339), "exchanges": exchanges, "backends": backends},
			map[string]any{"window": "1h", "start": tNow.Truncate(time.Minute).Add(-time.Hour).Format(time.RFC3339), "exchanges": []any{}, "backends": []any{}},
		},
	}
}

func tExchange(direction, exchange string, count uint64, rate float64, outcomes map[string]uint64, vs []float64) map[string]any {
	return map[string]any{"direction": direction, "exchange": exchange, "count": count, "rate": rate, "outcomes": outcomes,
		"latency": map[string]any{"sketch": tSketch(vs)}}
}

func tBackend(exchange string, count uint64, vs []float64) map[string]any {
	return map[string]any{"exchange": exchange, "count": count, "latency": map[string]any{"sketch": tSketch(vs)}}
}

// tServe serves answer as a replica's /internal/stats, demanding token when
// it is set, as /internal/checks' access rule does.
func tServe(t *testing.T, answer any, token string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/stats" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func tTokenFile(t *testing.T, token string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tLatencies(r *rand.Rand, n int, mu, sigma float64) []float64 {
	vs := make([]float64, n)
	for i := range vs {
		vs[i] = math.Exp(mu + sigma*r.NormFloat64())
	}
	return vs
}

// MON-R32, MON-R46: two replicas' answers merge into the table one gateway
// would give for both replicas' traffic: counts, rates and outcomes add, and
// each quantile is the one read from all the durations together, not an
// average of the replicas' quantiles.
func TestGatewayStats_MergesReplicasExactly_MON_R32_R46(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	a1 := tLatencies(r, 700, math.Log(0.05), 0.6)
	a2 := tLatencies(r, 300, math.Log(0.4), 0.4) // a slower replica
	a1 = append(a1, 400)                         // above the top bucket: the max is the first replica's
	a2 = append(a2, 0, 5e-5)                     // the zero bucket
	b1 := tLatencies(r, 50, math.Log(0.02), 0.3)
	only2 := tLatencies(r, 9, math.Log(1), 0.2)
	token := "t0k3n-for-stats"
	u1 := tServe(t, tAnswer("replica-1",
		[]any{tExchange("inbound", "pas-claim", 701, 2.5, map[string]uint64{"ok": 691, "refused": 10}, a1)},
		[]any{tBackend("pas-claim", 50, b1)}), token)
	u2 := tServe(t, tAnswer("replica-2",
		[]any{
			tExchange("inbound", "pas-claim", 302, 1.0, map[string]uint64{"ok": 297, "a-future-outcome": 5}, a2),
			tExchange("outbound", "crd-order-sign", 9, 0.03, map[string]uint64{"ok": 9}, only2),
		},
		[]any{}), token)

	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u1, "--url", u2 + "/", "--token-file", tTokenFile(t, token), "--window", "5m", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var m mergedStats
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Exchanges) != 2 || len(m.Backends) != 1 || len(m.Replicas) != 2 {
		t.Fatalf("merged %d exchanges, %d backends, %d replicas", len(m.Exchanges), len(m.Backends), len(m.Replicas))
	}
	pas := m.Exchanges[0]
	if pas.Direction != "inbound" || pas.Exchange != "pas-claim" || pas.Count != 1003 || math.Abs(pas.Rate-3.5) > 1e-12 {
		t.Errorf("pas-claim %+v", pas)
	}
	if want := map[string]uint64{"ok": 988, "refused": 10, "a-future-outcome": 5}; !reflect.DeepEqual(pas.Outcomes, want) {
		t.Errorf("outcomes %v, want %v", pas.Outcomes, want)
	}
	all := append(append([]float64(nil), a1...), a2...)
	for _, c := range []struct {
		name string
		got  *float64
		q    float64
	}{{"p50", pas.Latency.P50, 0.5}, {"p95", pas.Latency.P95, 0.95}, {"p99", pas.Latency.P99, 0.99}} {
		if want := tQuantile(all, c.q); c.got == nil || *c.got != want {
			t.Errorf("%s %v, want %g (all durations together)", c.name, c.got, want)
		}
	}
	if pas.Latency.Max == nil || *pas.Latency.Max != 400 {
		t.Errorf("max %v, want 400", pas.Latency.Max)
	}
	if want := tSketch(all); !sameSketch(t, pas.Latency.Sketch, want) {
		t.Errorf("merged sketch %+v, want the sketch of all durations %v", pas.Latency.Sketch, want)
	}
	if crd := m.Exchanges[1]; crd.Exchange != "crd-order-sign" || crd.Count != 9 || *crd.Latency.P95 != tQuantile(only2, 0.95) {
		t.Errorf("a series only one replica answered %+v", crd)
	}
	if be := m.Backends[0]; be.Count != 50 || *be.Latency.P50 != tQuantile(b1, 0.5) {
		t.Errorf("backend %+v", be)
	}
	if m.Replicas[0].Replica != "replica-1" || m.Replicas[1].Replica != "replica-2" || m.Replicas[0].Partial {
		t.Errorf("replicas %+v", m.Replicas)
	}
	if strings.Contains(out.String()+errb.String(), token) {
		t.Error("the token was printed")
	}
}

// sameSketch compares a merged sketch with tSketch's, through JSON.
func sameSketch(t *testing.T, got latencySketch, want map[string]any) bool {
	t.Helper()
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	var g, w any
	_ = json.Unmarshal(gb, &g)
	_ = json.Unmarshal(wb, &w)
	return reflect.DeepEqual(g, w)
}

// The table names the window, each replica, and every series.
func TestGatewayStats_Table(t *testing.T) {
	u := tServe(t, tAnswer("replica-1",
		[]any{tExchange("inbound", "pas-claim", 3, 0.01, map[string]uint64{"ok": 2, "refused": 1}, []float64{0.010, 0.020, 2})},
		[]any{tBackend("pas-claim", 1, []float64{0.5})}), "")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	p50 := tQuantile([]float64{0.010, 0.020, 2}, 0.5) * 1000
	for _, want := range []string{"window 5m:", "replica replica-1 (" + u + ")", "inbound", "pas-claim", "ok=2 refused=1", "BACKEND", "2.00s",
		strconv.FormatFloat(p50, 'f', 1, 64) + "ms", "500.0ms"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}

// A window with no traffic prints no series; a series with no duration
// recorded has no quantiles.
func TestGatewayStats_EmptyWindowAndNoDurations(t *testing.T) {
	u := tServe(t, tAnswer("replica-1", []any{tExchange("inbound", "pas-claim", 4, 0.1, map[string]uint64{"ok": 4}, nil)}, []any{}), "")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u, "--window", "1h"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "(no exchanges in the window)") {
		t.Fatalf("exit %d: %s%s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"gateway", "stats", "--url", u, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var m mergedStats
	_ = json.Unmarshal(out.Bytes(), &m)
	if l := m.Exchanges[0].Latency; l.P50 != nil || l.P95 != nil || l.P99 != nil || l.Max != nil || l.Sketch.Buckets == nil {
		t.Errorf("latency %+v", l)
	}
}

// A quantile in the zero bucket reads 0; the max still states the value.
func TestGatewayStats_ZeroBucketReadsZero(t *testing.T) {
	u := tServe(t, tAnswer("replica-1", []any{tExchange("inbound", "pas-claim", 3, 0.1, nil, []float64{0, 5e-5, 1})}, []any{}), "")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var m mergedStats
	_ = json.Unmarshal(out.Bytes(), &m)
	if l := m.Exchanges[0].Latency; *l.P50 != 0 || *l.Max != 1 || *l.P99 != math.Min(2*math.Pow(tGamma, 0)/(tGamma+1), 1) {
		t.Errorf("latency p50 %v p99 %v max %v", *l.P50, *l.P99, *l.Max)
	}
	// Where the zero bucket's count is exactly q·n, the quantile is still 0.
	u = tServe(t, tAnswer("replica-1", []any{tExchange("inbound", "pas-claim", 2, 0.1, nil, []float64{0, 1})}, []any{}), "")
	out.Reset()
	if code := run([]string{"gateway", "stats", "--url", u, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	m = mergedStats{}
	_ = json.Unmarshal(out.Bytes(), &m)
	if p50 := *m.Exchanges[0].Latency.P50; p50 != 0 {
		t.Errorf("p50 of [0 1] reads %g, want 0", p50)
	}
}

// The quantile is the nearest rank: where q·n is a whole number, the bucket
// at which the running count reaches it, not the next one up.
func TestGatewayStats_NearestRankWhereQNIsWhole(t *testing.T) {
	vs := []float64{0.1, 0.2, 0.3, 0.4}
	u := tServe(t, tAnswer("replica-1", []any{tExchange("inbound", "pas-claim", 4, 0.1, nil, vs)}, []any{}), "")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var m mergedStats
	_ = json.Unmarshal(out.Bytes(), &m)
	if got, want := *m.Exchanges[0].Latency.P50, tQuantile(vs, 0.5); got != want || math.Abs(got-0.2) > 0.2*tRelErr {
		t.Errorf("p50 of %v: %g, want %g (0.2 within the relative error)", vs, got, want)
	}
}

// Fields a later gateway adds, and windows this CLI does not know, are
// ignored (decode tolerantly).
func TestGatewayStats_DecodesTolerantly(t *testing.T) {
	a := tAnswer("replica-1", []any{tExchange("inbound", "pas-claim", 1, 0.1, nil, []float64{0.1})}, []any{})
	a["aNewField"] = map[string]any{"x": 1}
	a["windows"] = append(a["windows"].([]any), map[string]any{"window": "24h", "start": tNow.Format(time.RFC3339), "somethingElse": true})
	a["windows"].([]any)[0].(map[string]any)["exchanges"].([]any)[0].(map[string]any)["refusals"] = map[string]any{"consent": 1}
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", tServe(t, a, "")}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

// A replica that started inside the window covers only part of it, and the
// table says so; the merged window runs from the earliest start.
func TestGatewayStats_ReplicaStartedInsideTheWindow_MON_R46(t *testing.T) {
	late := tAnswer("replica-2", []any{tExchange("inbound", "pas-claim", 1, 0.1, nil, []float64{0.1})}, []any{})
	started := tNow.Add(-90 * time.Second)
	late["since"] = started.Format(time.RFC3339)
	late["windows"].([]any)[0].(map[string]any)["start"] = started.Format(time.RFC3339)
	u1 := tServe(t, tAnswer("replica-1", []any{}, []any{}), "")
	u2 := tServe(t, late, "")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u1, "--url", u2}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "covers only since "+started.Format(time.RFC3339)) ||
		!strings.Contains(out.String(), "window 5m: "+tNow.Truncate(time.Minute).Add(-5*time.Minute).Format(time.RFC3339)+" to "+tNow.Format(time.RFC3339)) {
		t.Errorf("table:\n%s", out.String())
	}
}

// MON-R32, MON-R46 rejection rows: an answer the CLI cannot merge exactly is
// refused, naming the replica, never merged in part.
func TestGatewayStats_RefusesWhatItCannotMergeExactly_MON_R32_R46(t *testing.T) {
	lg := math.Log(tGamma)
	lo := int64(math.Ceil(math.Log(tMin) / lg))
	hi := int64(math.Ceil(math.Log(tMax) / lg))
	win := func(a map[string]any) map[string]any { return a["windows"].([]any)[0].(map[string]any) }
	firstSketch := func(a map[string]any) map[string]any {
		return win(a)["exchanges"].([]any)[0].(map[string]any)["latency"].(map[string]any)["sketch"].(map[string]any)
	}
	for _, c := range []struct {
		name   string
		mutate func(a map[string]any) // applied to the second replica's answer
		want   string
	}{
		{"no replica named", func(a map[string]any) { delete(a, "replica") }, "names no replica"},
		{"the same replica twice", func(a map[string]any) { a["replica"] = "replica-1" }, "both answered as replica replica-1"},
		{"no sketch parameters", func(a map[string]any) { delete(a, "sketch") }, "no sketch parameters"},
		{"sketches bucketed differently", func(a map[string]any) { a["sketch"].(map[string]any)["relativeError"] = 0.01 }, "bucket their sketches differently"},
		{"another minimum", func(a map[string]any) { a["sketch"].(map[string]any)["minSeconds"] = 1e-3 }, "bucket their sketches differently"},
		{"another maximum", func(a map[string]any) { a["sketch"].(map[string]any)["maxSeconds"] = 600 }, "MaxSeconds:600"},
		{"no since", func(a map[string]any) { delete(a, "since") }, "does not state since and generatedAt"},
		{"no generatedAt", func(a map[string]any) { delete(a, "generatedAt") }, "does not state since and generatedAt"},
		{"a window without its start", func(a map[string]any) { delete(win(a), "start") }, "window 5m does not state its start"},
		{"no such window", func(a map[string]any) { win(a)["window"] = "15m" }, "no window 5m"},
		{"the window twice", func(a map[string]any) { a["windows"] = append(a["windows"].([]any), win(a)) }, "window 5m twice"},
		{"a bucket below the lowest", func(a map[string]any) { firstSketch(a)["buckets"] = []any{[]any{lo - 1, 1}} }, "outside the sketch's buckets"},
		{"a bucket above the highest", func(a map[string]any) { firstSketch(a)["buckets"] = []any{[]any{hi + 1, 1}} }, "outside the sketch's buckets"},
		{"buckets out of order", func(a map[string]any) { firstSketch(a)["buckets"] = []any{[]any{5, 1}, []any{4, 1}} }, "not in ascending order"},
		{"a bucket repeated", func(a map[string]any) { firstSketch(a)["buckets"] = []any{[]any{5, 1}, []any{5, 1}} }, "not in ascending order"},
		{"an empty bucket", func(a map[string]any) { firstSketch(a)["buckets"] = []any{[]any{5, 0}} }, "holds 0"},
		{"a negative max", func(a map[string]any) { firstSketch(a)["max"] = -1 }, "not a duration"},
		{"a series twice", func(a map[string]any) {
			win(a)["exchanges"] = append(win(a)["exchanges"].([]any), win(a)["exchanges"].([]any)[0])
		}, "lists inbound pas-claim twice"},
		{"a backend twice", func(a map[string]any) {
			win(a)["backends"] = []any{tBackend("pas-claim", 1, nil), tBackend("pas-claim", 1, nil)}
		}, "backend pas-claim twice"},
		{"a backend bucket outside", func(a map[string]any) {
			b := tBackend("pas-claim", 1, nil)
			b["latency"].(map[string]any)["sketch"].(map[string]any)["buckets"] = []any{[]any{hi + 1, 1}}
			win(a)["backends"] = []any{b}
		}, "backend pas-claim: sketch bucket"},
	} {
		t.Run(c.name, func(t *testing.T) {
			second := tAnswer("replica-2", []any{tExchange("inbound", "pas-claim", 2, 0.1, nil, []float64{0.1, 0.2})}, []any{})
			c.mutate(second)
			u1 := tServe(t, tAnswer("replica-1", []any{tExchange("inbound", "pas-claim", 1, 0.1, nil, []float64{0.1})}, []any{}), "")
			u2 := tServe(t, second, "")
			var out, errb bytes.Buffer
			code := run([]string{"gateway", "stats", "--url", u1, "--url", u2, "--json"}, &out, &errb)
			if code != 1 || !strings.Contains(errb.String(), c.want) || !strings.Contains(errb.String(), u2) || out.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 1 naming %s and %q", code, out.String(), errb.String(), u2, c.want)
			}
			// Sketches bucketed differently name both replicas and both
			// parameter sets.
			if strings.Contains(c.want, "differently") || strings.HasPrefix(c.want, "MaxSeconds") {
				for _, w := range []string{u1, "RelativeError:0.02 MinSeconds:0.0001 MaxSeconds:300}"} {
					if !strings.Contains(errb.String(), w) {
						t.Errorf("stderr %q does not name %q", errb.String(), w)
					}
				}
			}
		})
	}
	// Parameters that are not a sketch's are refused on a lone replica too.
	for _, c := range []struct {
		name, field string
		v           float64
	}{
		{"a maximum at the minimum", "maxSeconds", tMin},
		{"no relative error", "relativeError", 0},
		{"a relative error of 1", "relativeError", 1},
		{"a negative relative error", "relativeError", -0.02},
		{"a minimum of 0", "minSeconds", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := tAnswer("replica-1", []any{}, []any{})
			a["sketch"].(map[string]any)[c.field] = c.v
			var out, errb bytes.Buffer
			if code := run([]string{"gateway", "stats", "--url", tServe(t, a, "")}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "are not a sketch's") {
				t.Errorf("exit %d, %q", code, errb.String())
			}
		})
	}
}

// A replica that cannot be read is refused, naming it; the token is never
// printed.
func TestGatewayStats_RefusesAReplicaItCannotRead(t *testing.T) {
	token := "the-right-token"
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	notJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	t.Cleanup(notJSON.Close)
	// A redirect is not followed: the token would go to wherever it points.
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.RedirectHandler(target.URL+"/internal/stats", http.StatusFound))
	t.Cleanup(redirect.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"a wrong token", []string{"--url", tServe(t, tAnswer("r", nil, nil), token), "--token-file", tTokenFile(t, "a-wrong-token")}, "401"},
		{"no token where one is needed", []string{"--url", tServe(t, tAnswer("r", nil, nil), token)}, "401"},
		{"a gateway answering only its own host", []string{"--url", status(http.StatusForbidden)}, "403"},
		{"another status", []string{"--url", status(http.StatusBadGateway)}, "answered 502"},
		{"a redirect, not followed", []string{"--url", redirect.URL, "--token-file", tTokenFile(t, token)}, "a redirect, which this command does not follow"},
		{"not a stats answer", []string{"--url", notJSON.URL}, "not a stats answer"},
		{"unreachable", []string{"--url", goneURL}, goneURL},
		{"not an http URL", []string{"--url", "ftp://example.test"}, "not an http or https base URL"},
		{"no host", []string{"--url", "http://"}, "not an http or https base URL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(append([]string{"gateway", "stats"}, c.args...), &out, &errb)
			if code != 1 || !strings.Contains(errb.String(), c.want) || out.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 1 naming %q", code, out.String(), errb.String(), c.want)
			}
			if strings.Contains(errb.String(), "a-wrong-token") || strings.Contains(errb.String(), token) {
				t.Error("the token was printed")
			}
		})
	}
	if followed {
		t.Error("the redirect was followed")
	}
}

// The token goes over plain HTTP to this host's names and loopback
// addresses, and over HTTPS anywhere.
func TestGatewayStats_TokenOverPlainHTTPOnlyToThisHost(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "127.0.0.9": true, "::1": true,
		"gw.example.test": false, "192.0.2.7": false, "localhost.example.test": false, "0.0.0.0": false, "": false} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
	// The served replicas are on 127.0.0.1: the token goes to them.
	u := tServe(t, tAnswer("replica-1", []any{}, []any{}), "a-token")
	var out, errb bytes.Buffer
	if code := run([]string{"gateway", "stats", "--url", u, "--token-file", tTokenFile(t, "a-token")}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

// Usage errors exit 2.
func TestGatewayStats_UsageErrors(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := tTokenFile(t, "a-token")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no command after gateway", []string{"gateway"}, "shn gateway stats"},
		{"another command after gateway", []string{"gateway", "status"}, "shn gateway stats"},
		{"no --url", []string{"gateway", "stats"}, "at least one --url"},
		{"an argument", []string{"gateway", "stats", "--url", "http://x.test", "extra"}, "no arguments"},
		{"an unknown flag", []string{"gateway", "stats", "--nope"}, "nope"},
		{"a token file that is not there", []string{"gateway", "stats", "--url", "http://x.test", "--token-file", filepath.Join(t.TempDir(), "absent")}, "--token-file"},
		{"an empty token file", []string{"gateway", "stats", "--url", "http://x.test", "--token-file", empty}, "is empty"},
		{"a window the gateway does not report", []string{"gateway", "stats", "--url", "http://127.0.0.1:1", "--window", "2m"}, `--window "2m"`},
		{"the token over plain HTTP to another host", []string{"gateway", "stats", "--url", "http://127.0.0.1:1", "--url", "http://gw.example.test", "--token-file", token}, "--url http://gw.example.test: the token goes over plain HTTP only to this host"},
		{"the token over plain HTTP to an address off loopback", []string{"gateway", "stats", "--url", "http://192.0.2.7:8080", "--token-file", token}, "only to this host"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(c.args, &out, &errb); code != 2 || !strings.Contains(errb.String(), c.want) {
				t.Fatalf("exit %d, stderr %q; want exit 2 naming %q", code, errb.String(), c.want)
			}
		})
	}
}
