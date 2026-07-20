// Package metrics is a tiny, vendor-neutral CloudWatch Embedded Metric Format
// (EMF) emitter: one JSON line per metric to an io.Writer (stdout in prod).
// CloudWatch extracts metrics from the log stream on ingestion — no agent, no
// PutMetricData, no IAM beyond the log delivery every task already has
// (design spec §6). Fire-and-forget by design: emitting must never block or
// fail a caller's path, so all errors are swallowed.
//
// Stdlib-only by policy (enforced by this package's deps test, same gate as
// sdk/health): EMF is structured-log-shaped, so a later OTel/Prometheus move
// (Tier 3) is additive — nothing here is a one-way door.
package metrics

import (
	"encoding/json"
	"io"
	"math"
	"sort"
	"sync"
	"time"
)

// Emitter writes EMF lines with a fixed namespace and base dimension set.
// Nil-safe (like health.PollerCell): a nil *Emitter is a silent no-op, so
// callers can thread an optional emitter without guards. Safe for concurrent
// use — each line is a single Write under a mutex.
type Emitter struct {
	mu        sync.Mutex
	w         io.Writer
	namespace string
	baseDims  map[string]string
	now       func() time.Time
}

// New creates an Emitter. baseDims are merged under every emit's dims (the
// per-emit dims win on key collision). now==nil ⇒ time.Now.
func New(w io.Writer, namespace string, baseDims map[string]string, now func() time.Time) *Emitter {
	if now == nil {
		now = time.Now
	}
	e := &Emitter{w: w, namespace: namespace, now: now, baseDims: map[string]string{}}
	for k, v := range baseDims {
		e.baseDims[k] = v
	}
	return e
}

// EmitCount emits a Count-unit metric (occurrence counters).
func (e *Emitter) EmitCount(name string, value float64, dims map[string]string) {
	e.emit(name, value, "Count", dims)
}

// EmitGauge emits a point-in-time value with an explicit CloudWatch unit
// ("None", "Bytes", ...).
func (e *Emitter) EmitGauge(name string, value float64, unit string, dims map[string]string) {
	e.emit(name, value, unit, dims)
}

// EmitLatency emits a Milliseconds-unit metric.
func (e *Emitter) EmitLatency(name string, ms float64, dims map[string]string) {
	e.emit(name, ms, "Milliseconds", dims)
}

func (e *Emitter) emit(name string, value float64, unit string, dims map[string]string) {
	if e == nil || e.w == nil {
		return
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return // EMF rejects non-finite values; dropping beats a poisoned line
	}
	merged := make(map[string]string, len(e.baseDims)+len(dims))
	for k, v := range e.baseDims {
		merged[k] = v
	}
	for k, v := range dims {
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic dimension-set order

	doc := make(map[string]any, len(merged)+2)
	for k, v := range merged {
		doc[k] = v
	}
	doc["_aws"] = map[string]any{
		"Timestamp": e.now().UnixMilli(),
		"CloudWatchMetrics": []any{map[string]any{
			"Namespace":  e.namespace,
			"Dimensions": []any{keys},
			"Metrics":    []any{map[string]any{"Name": name, "Unit": unit}},
		}},
	}
	doc[name] = value // last: the metric value wins if a dim key collides with name
	line, err := json.Marshal(doc)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(append(line, '\n'))
}
