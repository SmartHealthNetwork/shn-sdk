package metrics

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func testNow() time.Time { return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC) }

// lockedBuf lets the concurrency test share one writer across goroutines.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func emitLine(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("emitted line is not one JSON object: %v (%q)", err, out)
	}
	return doc
}

func TestEmitGauge_EMFShape(t *testing.T) {
	var buf lockedBuf
	e := New(&buf, "SHN/Preview", map[string]string{"Env": "shn-preview"}, testNow)
	e.EmitGauge("HealthStatus", 1, "None", map[string]string{"Service": "hub"})

	doc := emitLine(t, buf.String())
	aws, ok := doc["_aws"].(map[string]any)
	if !ok {
		t.Fatalf("no _aws block: %v", doc)
	}
	if ts := int64(aws["Timestamp"].(float64)); ts != testNow().UnixMilli() {
		t.Fatalf("Timestamp = %d, want %d", ts, testNow().UnixMilli())
	}
	cwm := aws["CloudWatchMetrics"].([]any)[0].(map[string]any)
	if cwm["Namespace"] != "SHN/Preview" {
		t.Fatalf("Namespace = %v", cwm["Namespace"])
	}
	dims := cwm["Dimensions"].([]any)[0].([]any)
	if len(dims) != 2 || dims[0] != "Env" || dims[1] != "Service" {
		t.Fatalf("Dimensions not sorted [Env Service]: %v", dims)
	}
	m := cwm["Metrics"].([]any)[0].(map[string]any)
	if m["Name"] != "HealthStatus" || m["Unit"] != "None" {
		t.Fatalf("Metrics = %v", m)
	}
	if doc["Service"] != "hub" || doc["Env"] != "shn-preview" || doc["HealthStatus"].(float64) != 1 {
		t.Fatalf("top-level values wrong: %v", doc)
	}
}

func TestEmitCount_And_EmitLatency_Units(t *testing.T) {
	var buf lockedBuf
	e := New(&buf, "SHN/Preview", nil, testNow)
	e.EmitCount("Probes", 3, map[string]string{"Service": "hub"})
	e.EmitLatency("ProbeLatencyMS", 42, map[string]string{"Service": "hub"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	count := emitLine(t, lines[0])
	lat := emitLine(t, lines[1])
	cu := count["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)["Metrics"].([]any)[0].(map[string]any)["Unit"]
	lu := lat["_aws"].(map[string]any)["CloudWatchMetrics"].([]any)[0].(map[string]any)["Metrics"].([]any)[0].(map[string]any)["Unit"]
	if cu != "Count" || lu != "Milliseconds" {
		t.Fatalf("units = %v / %v, want Count / Milliseconds", cu, lu)
	}
	if lat["ProbeLatencyMS"].(float64) != 42 {
		t.Fatalf("latency value = %v", lat["ProbeLatencyMS"])
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestEmit_FireAndForget(t *testing.T) {
	// Write errors are swallowed (spec §6: never block or fail a caller path).
	e := New(errWriter{}, "SHN/Preview", nil, testNow)
	e.EmitGauge("X", 1, "None", nil) // must not panic

	// Nil emitter is a no-op (health.PollerCell nil-safety idiom).
	var nilE *Emitter
	nilE.EmitGauge("X", 1, "None", nil)

	// Non-finite values are dropped, not emitted as invalid EMF.
	var buf lockedBuf
	e2 := New(&buf, "SHN/Preview", nil, testNow)
	e2.EmitGauge("X", math.NaN(), "None", nil)
	if buf.String() != "" {
		t.Fatalf("NaN emitted: %q", buf.String())
	}
}

func TestEmit_ConcurrentLinesDoNotInterleave(t *testing.T) {
	var buf lockedBuf
	e := New(&buf, "SHN/Preview", map[string]string{"Env": "shn-preview"}, testNow)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				e.EmitGauge("HealthStatus", 0, "None", map[string]string{"Service": "hub"})
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 400 {
		t.Fatalf("want 400 lines, got %d", len(lines))
	}
	for _, l := range lines {
		emitLine(t, l) // every line must parse — interleaving corrupts JSON
	}
}
