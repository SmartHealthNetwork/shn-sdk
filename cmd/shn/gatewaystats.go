package main

// gatewaystats.go implements `shn gateway stats`: it reads GET /internal/stats
// from one or more Smart Gateway replicas and merges their answers into one
// table by exchange (MON-R32). Each replica answers for itself; quantiles
// cannot be averaged, so the merge adds the replicas' latency sketches,
// bucket by bucket, and reads the quantiles from the sum: the same quantiles
// one gateway would report had it answered all the replicas' traffic
// (MON-R46). Stdlib only.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// gatewayStatsPath is the gateway's stats endpoint, under a replica's base URL.
const gatewayStatsPath = "/internal/stats"

// statsMaxBody bounds an answer: a full answer is well under 1 MiB.
const statsMaxBody = 32 << 20

// statsClient reads the replicas. It follows no redirect: the bearer token
// goes only to the URL given, never to wherever a redirect points, nor over
// plain HTTP after HTTPS.
var statsClient = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// statsAnswer is the part of a /internal/stats answer the merge reads. A
// gateway may add fields in a minor release; they are ignored.
type statsAnswer struct {
	Replica     string      `json:"replica"`
	Since       time.Time   `json:"since"`
	GeneratedAt time.Time   `json:"generatedAt"`
	Sketch      *sketchSpec `json:"sketch"`
	Windows     []struct {
		Window    string          `json:"window"`
		Start     time.Time       `json:"start"`
		Exchanges []statsExchange `json:"exchanges"`
		Backends  []statsBackend  `json:"backends"`
	} `json:"windows"`
}

// sketchSpec is a latency sketch's bucketing. Two replicas' sketches merge
// exactly only when their specs are equal.
type sketchSpec struct {
	RelativeError float64 `json:"relativeError"`
	MinSeconds    float64 `json:"minSeconds"`
	MaxSeconds    float64 `json:"maxSeconds"`
}

// latencySketch is a sketch as the gateway reports it: the count of values
// at or below minSeconds, each non-empty bucket as [index, count] in
// ascending index order, and the largest value recorded.
type latencySketch struct {
	Zero    uint64     `json:"zero"`
	Buckets [][2]int64 `json:"buckets"`
	Max     float64    `json:"max"`
}

type statsLatency struct {
	Sketch latencySketch `json:"sketch"`
}

type statsExchange struct {
	Direction string            `json:"direction"`
	Exchange  string            `json:"exchange"`
	Count     uint64            `json:"count"`
	Rate      float64           `json:"rate"`
	Outcomes  map[string]uint64 `json:"outcomes"`
	Latency   statsLatency      `json:"latency"`
}

type statsBackend struct {
	Exchange string       `json:"exchange"`
	Count    uint64       `json:"count"`
	Latency  statsLatency `json:"latency"`
}

// The merged answer, as --json prints it.
type (
	mergedStats struct {
		Window   string          `json:"window"`
		Start    time.Time       `json:"start"`
		End      time.Time       `json:"end"`
		Sketch   sketchSpec      `json:"sketch"`
		Replicas []mergedReplica `json:"replicas"`
		// Exchanges by direction and exchange kind, Backends by exchange kind,
		// each the sum over the replicas.
		Exchanges []mergedExchange `json:"exchanges"`
		Backends  []mergedBackend  `json:"backends"`
	}
	mergedReplica struct {
		URL         string    `json:"url"`
		Replica     string    `json:"replica"`
		Since       time.Time `json:"since"`
		GeneratedAt time.Time `json:"generatedAt"`
		Start       time.Time `json:"start"`
		// Partial is set when the replica's window starts after the window's
		// full span would (it started inside the window): it covers only
		// from Start.
		Partial bool `json:"partial"`
	}
	mergedExchange struct {
		Direction string            `json:"direction"`
		Exchange  string            `json:"exchange"`
		Count     uint64            `json:"count"`
		Rate      float64           `json:"rate"`
		Outcomes  map[string]uint64 `json:"outcomes"`
		Latency   mergedLatency     `json:"latency"`
	}
	mergedBackend struct {
		Exchange string        `json:"exchange"`
		Count    uint64        `json:"count"`
		Latency  mergedLatency `json:"latency"`
	}
	mergedLatency struct {
		P50    *float64      `json:"p50,omitempty"`
		P95    *float64      `json:"p95,omitempty"`
		P99    *float64      `json:"p99,omitempty"`
		Max    *float64      `json:"max,omitempty"`
		Sketch latencySketch `json:"sketch"`
	}
)

// urlList is a repeatable --url.
type urlList []string

func (u *urlList) String() string { return strings.Join(*u, ",") }
func (u *urlList) Set(v string) error {
	*u = append(*u, v)
	return nil
}

// cmdGatewayStats implements `shn gateway stats`. Exit 0 prints the merged
// table, 1 is a replica that could not be read or merged, 2 a usage error.
func cmdGatewayStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var urls urlList
	fs.Var(&urls, "url", "a gateway replica's base URL, serving GET /internal/stats (repeatable; at least one)")
	tokenFile := fs.String("token-file", "", "file holding the gateway's CHECKS_TOKEN, sent as a bearer token (omit for a gateway on this host without one)")
	window := fs.String("window", "5m", "the window to merge: 1m, 5m or 1h")
	asJSON := fs.Bool("json", false, "print the merged answer as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(urls) == 0 || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "shn gateway stats: at least one --url is required, and no arguments")
		return 2
	}
	if !slices.Contains(statsWindows, *window) {
		fmt.Fprintf(stderr, "shn gateway stats: --window %q: the gateway reports %s\n", *window, strings.Join(statsWindows, ", "))
		return 2
	}
	var token string
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			fmt.Fprintf(stderr, "shn gateway stats: --token-file: %v\n", err)
			return 2
		}
		if token = strings.TrimSpace(string(b)); token == "" {
			fmt.Fprintf(stderr, "shn gateway stats: --token-file %s is empty\n", *tokenFile)
			return 2
		}
		// The token is the gateway's CHECKS_TOKEN: it crosses the network
		// only over HTTPS, or in plain HTTP only to this host.
		for _, u := range urls {
			if p, err := url.Parse(u); err == nil && p.Scheme == "http" && !loopbackHost(p.Hostname()) {
				fmt.Fprintf(stderr, "shn gateway stats: --url %s: the token goes over plain HTTP only to this host (localhost, 127.0.0.1 or ::1); use https\n", u)
				return 2
			}
		}
	}
	answers := make([]statsAnswer, len(urls))
	for i, u := range urls {
		a, err := fetchStats(u, token)
		if err != nil {
			fmt.Fprintf(stderr, "shn gateway stats: %s: %v\n", u, err)
			return 1
		}
		answers[i] = a
	}
	m, err := mergeStats(urls, answers, *window)
	if err != nil {
		fmt.Fprintf(stderr, "shn gateway stats: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m); err != nil {
			fmt.Fprintf(stderr, "shn gateway stats: %v\n", err)
			return 1
		}
		return 0
	}
	printStats(stdout, m)
	return 0
}

// statsWindows are the windows a gateway reports.
var statsWindows = []string{"1m", "5m", "1h"}

// loopbackHost reports whether host names this machine: localhost or a
// loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// fetchStats reads one replica's answer.
func fetchStats(base, token string) (statsAnswer, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return statsAnswer{}, errors.New("not an http or https base URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + gatewayStatsPath
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return statsAnswer{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := statsClient.Do(req)
	if err != nil {
		return statsAnswer{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return statsAnswer{}, errors.New("401: the gateway needs its CHECKS_TOKEN (--token-file), and this one was missing or wrong")
	case http.StatusForbidden:
		return statsAnswer{}, errors.New("403: the gateway has no CHECKS_TOKEN, so it answers only on its own host")
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return statsAnswer{}, fmt.Errorf("answered %d, a redirect, which this command does not follow: give the replica's own base URL", resp.StatusCode)
		}
		return statsAnswer{}, fmt.Errorf("answered %d", resp.StatusCode)
	}
	var a statsAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, statsMaxBody)).Decode(&a); err != nil {
		return statsAnswer{}, fmt.Errorf("not a stats answer: %v", err)
	}
	return a, nil
}

// span is a sketch spec's valid bucket indexes, [lo, hi], and its gamma.
func (s sketchSpec) span() (lo, hi int64, gamma float64, err error) {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !finite(s.RelativeError) || !finite(s.MinSeconds) || !finite(s.MaxSeconds) ||
		s.RelativeError <= 0 || s.RelativeError >= 1 || s.MinSeconds <= 0 || s.MaxSeconds <= s.MinSeconds {
		return 0, 0, 0, fmt.Errorf("sketch parameters %+v are not a sketch's", s)
	}
	gamma = (1 + s.RelativeError) / (1 - s.RelativeError)
	lg := math.Log(gamma)
	return int64(math.Ceil(math.Log(s.MinSeconds) / lg)), int64(math.Ceil(math.Log(s.MaxSeconds) / lg)), gamma, nil
}

// mergeStats merges the replicas' answers over the named window. It refuses
// what it cannot merge exactly: a malformed answer, sketches bucketed
// differently, one replica answering twice, or a window an answer lacks.
func mergeStats(urls []string, answers []statsAnswer, window string) (mergedStats, error) {
	m := mergedStats{Window: window, Exchanges: []mergedExchange{}, Backends: []mergedBackend{}}
	type exKey struct{ direction, exchange string }
	exchanges := map[exKey]*mergedExchange{}
	backends := map[string]*mergedBackend{}
	exSketch := map[exKey]latencySketch{}
	beSketch := map[string]latencySketch{}
	seen := map[string]string{}
	var lo, hi int64
	var gamma float64
	for i, a := range answers {
		u := urls[i]
		if a.Replica == "" {
			return m, fmt.Errorf("%s: the answer names no replica", u)
		}
		if first, dup := seen[a.Replica]; dup {
			return m, fmt.Errorf("%s and %s both answered as replica %s: one replica, counted twice if merged (a load balancer, or two gateways given one service.instance.id)", first, u, a.Replica)
		}
		seen[a.Replica] = u
		if a.Since.IsZero() || a.GeneratedAt.IsZero() {
			return m, fmt.Errorf("%s: the answer does not state since and generatedAt", u)
		}
		if a.Sketch == nil {
			return m, fmt.Errorf("%s: the answer has no sketch parameters", u)
		}
		if i == 0 {
			var err error
			if lo, hi, gamma, err = a.Sketch.span(); err != nil {
				return m, fmt.Errorf("%s: %v", u, err)
			}
			m.Sketch = *a.Sketch
		} else if *a.Sketch != m.Sketch {
			return m, fmt.Errorf("%s and %s bucket their sketches differently (%+v, %+v): they cannot be merged exactly", urls[0], u, m.Sketch, *a.Sketch)
		}
		w := -1
		for j := range a.Windows {
			if a.Windows[j].Window == window {
				if w >= 0 {
					return m, fmt.Errorf("%s: the answer has window %s twice", u, window)
				}
				w = j
			}
		}
		if w < 0 {
			return m, fmt.Errorf("%s: the answer has no window %s", u, window)
		}
		win := a.Windows[w]
		if win.Start.IsZero() {
			return m, fmt.Errorf("%s: window %s does not state its start", u, window)
		}
		r := mergedReplica{URL: u, Replica: a.Replica, Since: a.Since, GeneratedAt: a.GeneratedAt, Start: win.Start}
		if d, err := time.ParseDuration(window); err == nil {
			r.Partial = win.Start.After(a.GeneratedAt.Truncate(time.Minute).Add(-d))
		}
		m.Replicas = append(m.Replicas, r)
		if m.Start.IsZero() || win.Start.Before(m.Start) {
			m.Start = win.Start
		}
		if a.GeneratedAt.After(m.End) {
			m.End = a.GeneratedAt
		}
		inAnswer := map[exKey]bool{}
		for _, e := range win.Exchanges {
			k := exKey{e.Direction, e.Exchange}
			if inAnswer[k] {
				return m, fmt.Errorf("%s: the answer lists %s %s twice", u, e.Direction, e.Exchange)
			}
			inAnswer[k] = true
			if err := e.Latency.Sketch.check(lo, hi); err != nil {
				return m, fmt.Errorf("%s: %s %s: %v", u, e.Direction, e.Exchange, err)
			}
			me := exchanges[k]
			if me == nil {
				me = &mergedExchange{Direction: e.Direction, Exchange: e.Exchange, Outcomes: map[string]uint64{}}
				exchanges[k] = me
			}
			me.Count += e.Count
			me.Rate += e.Rate
			for o, n := range e.Outcomes {
				me.Outcomes[o] += n
			}
			exSketch[k] = exSketch[k].merge(e.Latency.Sketch)
		}
		beInAnswer := map[string]bool{}
		for _, b := range win.Backends {
			if beInAnswer[b.Exchange] {
				return m, fmt.Errorf("%s: the answer lists backend %s twice", u, b.Exchange)
			}
			beInAnswer[b.Exchange] = true
			if err := b.Latency.Sketch.check(lo, hi); err != nil {
				return m, fmt.Errorf("%s: backend %s: %v", u, b.Exchange, err)
			}
			mb := backends[b.Exchange]
			if mb == nil {
				mb = &mergedBackend{Exchange: b.Exchange}
				backends[b.Exchange] = mb
			}
			mb.Count += b.Count
			beSketch[b.Exchange] = beSketch[b.Exchange].merge(b.Latency.Sketch)
		}
	}
	for k, e := range exchanges {
		e.Latency = exSketch[k].latency(gamma)
		m.Exchanges = append(m.Exchanges, *e)
	}
	for k, b := range backends {
		b.Latency = beSketch[k].latency(gamma)
		m.Backends = append(m.Backends, *b)
	}
	sort.Slice(m.Exchanges, func(i, j int) bool {
		if m.Exchanges[i].Direction != m.Exchanges[j].Direction {
			return m.Exchanges[i].Direction < m.Exchanges[j].Direction
		}
		return m.Exchanges[i].Exchange < m.Exchanges[j].Exchange
	})
	sort.Slice(m.Backends, func(i, j int) bool { return m.Backends[i].Exchange < m.Backends[j].Exchange })
	return m, nil
}

// check reports a sketch the gateway cannot have sent: a bucket outside the
// spec's indexes, out of ascending order, or empty.
func (s latencySketch) check(lo, hi int64) error {
	for i, b := range s.Buckets {
		switch {
		case b[0] < lo || b[0] > hi:
			return fmt.Errorf("sketch bucket %d is outside the sketch's buckets [%d, %d]", b[0], lo, hi)
		case i > 0 && b[0] <= s.Buckets[i-1][0]:
			return fmt.Errorf("sketch buckets are not in ascending order at %d", b[0])
		case b[1] <= 0:
			return fmt.Errorf("sketch bucket %d holds %d", b[0], b[1])
		}
	}
	if math.IsNaN(s.Max) || s.Max < 0 {
		return fmt.Errorf("sketch max %v is not a duration", s.Max)
	}
	return nil
}

// merge adds o into s: the sketch of both value sets, exactly.
func (s latencySketch) merge(o latencySketch) latencySketch {
	out := latencySketch{Zero: s.Zero + o.Zero, Buckets: [][2]int64{}, Max: math.Max(s.Max, o.Max)}
	i, j := 0, 0
	for i < len(s.Buckets) || j < len(o.Buckets) {
		switch {
		case j == len(o.Buckets) || (i < len(s.Buckets) && s.Buckets[i][0] < o.Buckets[j][0]):
			out.Buckets = append(out.Buckets, s.Buckets[i])
			i++
		case i == len(s.Buckets) || o.Buckets[j][0] < s.Buckets[i][0]:
			out.Buckets = append(out.Buckets, o.Buckets[j])
			j++
		default:
			out.Buckets = append(out.Buckets, [2]int64{s.Buckets[i][0], s.Buckets[i][1] + o.Buckets[j][1]})
			i, j = i+1, j+1
		}
	}
	return out
}

// quantile reads the q-quantile as the gateway does: the first bucket, the
// zero bucket first and then in index order, at which the running count
// reaches q·n; 0 for the zero bucket, else 2·γ^i/(γ+1), never above max.
// ok is false for an empty sketch.
func (s latencySketch) quantile(q, gamma float64) (v float64, ok bool) {
	n := s.Zero
	for _, b := range s.Buckets {
		n += uint64(b[1])
	}
	if n == 0 {
		return 0, false
	}
	rank := q * float64(n)
	seen := s.Zero
	if seen > 0 && float64(seen) >= rank {
		return 0, true
	}
	for _, b := range s.Buckets {
		seen += uint64(b[1])
		if float64(seen) >= rank {
			return math.Min(2*math.Pow(gamma, float64(b[0]))/(gamma+1), s.Max), true
		}
	}
	return s.Max, true
}

// latency is the merged sketch with its quantiles.
func (s latencySketch) latency(gamma float64) mergedLatency {
	if s.Buckets == nil {
		s.Buckets = [][2]int64{}
	}
	l := mergedLatency{Sketch: s}
	q := func(p float64) *float64 {
		if v, ok := s.quantile(p, gamma); ok {
			return &v
		}
		return nil
	}
	if l.P50 = q(0.50); l.P50 != nil {
		l.P95, l.P99 = q(0.95), q(0.99)
		m := s.Max
		l.Max = &m
	}
	return l
}

// printStats prints the merged answer as tables.
func printStats(w io.Writer, m mergedStats) {
	fmt.Fprintf(w, "window %s: %s to %s, %d replica(s)\n", m.Window, m.Start.UTC().Format(time.RFC3339), m.End.UTC().Format(time.RFC3339), len(m.Replicas))
	for _, r := range m.Replicas {
		note := ""
		if r.Partial {
			note = ", covers only since " + r.Start.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "  replica %s (%s), since %s%s\n", r.Replica, r.URL, r.Since.UTC().Format(time.RFC3339), note)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nDIRECTION\tEXCHANGE\tCOUNT\tRATE/S\tP50\tP95\tP99\tMAX\tOUTCOMES")
	for _, e := range m.Exchanges {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Direction, e.Exchange, e.Count, strconv.FormatFloat(e.Rate, 'f', 3, 64),
			seconds(e.Latency.P50), seconds(e.Latency.P95), seconds(e.Latency.P99), seconds(e.Latency.Max), outcomes(e.Outcomes))
	}
	if len(m.Exchanges) == 0 {
		fmt.Fprintln(tw, "(no exchanges in the window)")
	}
	if len(m.Backends) > 0 {
		fmt.Fprintln(tw, "\nBACKEND\tCOUNT\tP50\tP95\tP99\tMAX")
		for _, b := range m.Backends {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", b.Exchange, b.Count, seconds(b.Latency.P50), seconds(b.Latency.P95), seconds(b.Latency.P99), seconds(b.Latency.Max))
		}
	}
	tw.Flush()
}

// seconds formats a latency, "-" when none was recorded.
func seconds(v *float64) string {
	switch {
	case v == nil:
		return "-"
	case *v < 1:
		return strconv.FormatFloat(*v*1000, 'f', 1, 64) + "ms"
	default:
		return strconv.FormatFloat(*v, 'f', 2, 64) + "s"
	}
}

// outcomes formats the outcome counts, by outcome name.
func outcomes(o map[string]uint64) string {
	names := make([]string, 0, len(o))
	for k := range o {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s=%d", k, o[k])
	}
	return strings.Join(parts, " ")
}
