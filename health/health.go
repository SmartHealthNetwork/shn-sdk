// Package health provides the SHN /health contract: a small check registry and
// an HTTP handler emitting the shared JSON shape every SHN service serves
// (design spec §5.1). Payloads are non-sensitive BY CONSTRUCTION — statuses,
// timestamps, counts, coarse error strings only; never key material, config
// values, or internal hostnames. Several services expose /health publicly, so
// every check added anywhere must honor this rule.
//
// Stdlib-only by policy (enforced by this package's deps test): the payload
// contract is mounted on every service including partner-run gateways.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Status is a check's (and by worst-wins rollup, a service's) health state.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
)

// Check is one named health signal in the /health payload.
type Check struct {
	Name        string `json:"name"`
	Status      Status `json:"status"`
	LastSuccess string `json:"lastSuccess,omitempty"` // RFC3339
	LastError   string `json:"lastError,omitempty"`   // coarse, non-sensitive
	HolderCount int    `json:"holderCount,omitempty"` // poller feed size
}

// CheckFunc produces a Check on demand. It must be fast and non-blocking in
// spirit: reading an atomic cell, or a ping bounded by its own short timeout.
type CheckFunc func(ctx context.Context) Check

// Registry holds a service's registered checks and serves the /health payload.
type Registry struct {
	service string
	version string
	start   time.Time

	mu     sync.Mutex
	checks []CheckFunc
}

// New creates a Registry. version comes from the SHN_VERSION env by
// convention (the deploy image SHA in cloud, "dev" in compose); empty is
// omitted from the payload.
func New(service, version string) *Registry {
	return &Registry{service: service, version: version, start: time.Now()}
}

// Register appends a check. Registration order is payload order.
func (r *Registry) Register(fn CheckFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, fn)
}

type payload struct {
	Service       string  `json:"service"`
	Version       string  `json:"version,omitempty"`
	UptimeSeconds int64   `json:"uptimeSeconds"`
	Status        Status  `json:"status"`
	Checks        []Check `json:"checks"`
}

// Handler serves the /health JSON. Zero checks is still liveness: status ok.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		fns := make([]CheckFunc, len(r.checks))
		copy(fns, r.checks)
		r.mu.Unlock()
		p := payload{
			Service:       r.service,
			Version:       r.version,
			UptimeSeconds: int64(time.Since(r.start).Seconds()),
			Status:        StatusOK,
			Checks:        make([]Check, 0, len(fns)), // never JSON null
		}
		for _, fn := range fns {
			c := fn(req.Context())
			if c.Status != StatusOK {
				p.Status = StatusDegraded // worst-wins (two states today)
			}
			p.Checks = append(p.Checks, c)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p)
	})
}

// Wrap mounts GET /health on top of next. Services whose Handler() is built
// elsewhere (every cmd main) wrap once at the top: everything except
// GET /health flows through untouched.
func Wrap(reg *Registry, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", reg.Handler())
	mux.Handle("/", next)
	return mux
}

// DBPing adapts a ping func (e.g. pgxpool.Pool.Ping) into a CheckFunc with
// its own 2s bound so a wedged database can't stall the /health response.
func DBPing(name string, ping func(ctx context.Context) error) CheckFunc {
	return func(ctx context.Context) Check {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := ping(pctx); err != nil {
			// NEVER relay the driver error: connect failures embed DSN details
			// (host, user) and /health is public on some services. A fixed class
			// string is the §5.1-conformant maximum.
			msg := "ping failed"
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "ping timeout"
			}
			return Check{Name: name, Status: StatusDegraded, LastError: msg}
		}
		return Check{Name: name, Status: StatusOK}
	}
}
