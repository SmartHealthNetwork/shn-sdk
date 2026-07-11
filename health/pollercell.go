package health

import (
	"context"
	"sync"
	"time"
)

// maxErrLen bounds the stored error string: /health payloads are public on
// some services, and error chains can drag long URLs along. Coarse beats
// complete here (spec §5.1 non-sensitivity rule).
const maxErrLen = 200

// PollerCell is the shared status cell for background feed pollers (design
// spec §5.2): the three registrar-poller code paths record attempts into a
// cell and register cell.Check on their service's health Registry. All record
// methods are NIL-SAFE so call sites can thread an optional *PollerCell
// without branching.
type PollerCell struct {
	name       string
	staleAfter time.Duration

	mu          sync.Mutex
	lastAttempt time.Time
	lastSuccess time.Time
	lastError   string
	holderCount int
}

// NewPollerCell creates a cell. staleAfter is how old the last success may be
// before the check reports degraded (pick ~10x the poll interval).
func NewPollerCell(name string, staleAfter time.Duration) *PollerCell {
	return &PollerCell{name: name, staleAfter: staleAfter}
}

// RecordAttempt marks the start of a poll tick.
func (c *PollerCell) RecordAttempt() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt = time.Now()
}

// RecordSuccess marks a converged tick with the observed feed size.
func (c *PollerCell) RecordSuccess(holderCount int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSuccess = time.Now()
	c.lastError = ""
	c.holderCount = holderCount
}

// RecordError marks a failed tick. The stored string is truncated (coarse,
// non-sensitive). The last good lastSuccess/holderCount stay visible.
func (c *PollerCell) RecordError(err error) {
	if c == nil {
		return
	}
	msg := err.Error()
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastError = msg
}

// Check reports the cell as a health Check. Never-attempted is ok (a booting
// service is not degraded); a failed last attempt or a stale last success is
// degraded.
func (c *PollerCell) Check(_ context.Context) Check {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Check{Name: c.name, Status: StatusOK, HolderCount: c.holderCount, LastError: c.lastError}
	if !c.lastSuccess.IsZero() {
		out.LastSuccess = c.lastSuccess.UTC().Format(time.RFC3339)
	}
	switch {
	case c.lastAttempt.IsZero():
		// never attempted: booting, ok
	case c.lastError != "":
		out.Status = StatusDegraded
	case c.lastSuccess.IsZero():
		// First fetch in flight: ok until the boot window (staleAfter from the
		// first attempt) expires — a health scrape during the very first tick
		// must not record a false red at every service restart.
		if time.Since(c.lastAttempt) > c.staleAfter {
			out.Status = StatusDegraded
		}
	case time.Since(c.lastSuccess) > c.staleAfter:
		out.Status = StatusDegraded
	}
	return out
}
