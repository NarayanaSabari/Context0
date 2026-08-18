// probes.go implements the Kubernetes probe endpoints.
//
// These are deliberately separate from the Health RPC. `HealthService.Health`
// reports node and edge counts, which are two full graph scans -- ~17ms at 50k
// vertices, growing with the corpus. That is fine for a human or a dashboard
// asking "how big is the graph", and completely wrong for something kubelet
// calls every few seconds on every pod.
//
// The three probes answer three different questions:
//
//   - /livez   Is this process wedged? Answered without touching the database,
//     because a restart cannot fix a remote database, and a probe
//     that says otherwise turns a brief Postgres blip into a
//     simultaneous fleet-wide restart and a cold connection pool.
//   - /readyz  Can this pod serve a request right now? A bounded pool ping,
//     plus a draining flag so a terminating pod leaves the Service
//     endpoints before it stops accepting connections.
//   - /startupz Has initialization finished? InitSchema creates the AGE graph
//     and its indexes, which is slow on a cold database. A startup
//     probe covers that window so liveness can stay aggressive
//     afterwards.
package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// readinessTimeout bounds the database check. A probe that can block longer
// than its own period stacks up connections against a database that is already
// struggling.
const readinessTimeout = 1 * time.Second

// Pinger is the subset of the connection pool the probes need. Narrow by
// design: probes must not be able to run graph queries.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Probes tracks process state and serves the three Kubernetes probe endpoints.
// The zero value is not usable; call NewProbes.
type Probes struct {
	pool Pinger

	// started flips once initialization has completed.
	started atomic.Bool

	// draining flips on SIGTERM, before the servers begin shutting down, so
	// readiness fails while in-flight requests are still being served.
	draining atomic.Bool
}

// NewProbes returns probes bound to the given pool, in the not-yet-started,
// not-draining state.
func NewProbes(pool Pinger) *Probes {
	return &Probes{pool: pool}
}

// MarkStarted records that initialization finished and the process can serve.
func (p *Probes) MarkStarted() { p.started.Store(true) }

// MarkDraining records that shutdown has begun. Readiness fails from this
// point on, which is what removes the pod from Service endpoints.
func (p *Probes) MarkDraining() { p.draining.Store(true) }

// Register wires the probe endpoints onto a mux.
func (p *Probes) Register(mux *http.ServeMux) {
	mux.HandleFunc("/livez", p.Live)
	mux.HandleFunc("/readyz", p.Ready)
	mux.HandleFunc("/startupz", p.Startup)
}

// Live reports whether the process itself is healthy. It deliberately performs
// no I/O: reaching this handler at all proves the HTTP server is accepting
// connections and its goroutines are scheduled, which is the only thing a
// container restart could actually repair.
func (p *Probes) Live(w http.ResponseWriter, _ *http.Request) {
	writePlain(w, http.StatusOK, "ok")
}

// Startup reports whether initialization has finished.
func (p *Probes) Startup(w http.ResponseWriter, _ *http.Request) {
	if !p.started.Load() {
		writePlain(w, http.StatusServiceUnavailable, "starting")
		return
	}
	writePlain(w, http.StatusOK, "started")
}

// Ready reports whether this pod should receive traffic: initialization done,
// not draining, and the database reachable within readinessTimeout.
func (p *Probes) Ready(w http.ResponseWriter, r *http.Request) {
	if !p.started.Load() {
		writePlain(w, http.StatusServiceUnavailable, "starting")
		return
	}
	if p.draining.Load() {
		writePlain(w, http.StatusServiceUnavailable, "draining")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	if err := p.pool.Ping(ctx); err != nil {
		// Unreadiness removes the pod from Service endpoints without
		// restarting it, so the pod recovers on its own once the database does.
		writePlain(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}

	writePlain(w, http.StatusOK, "ready")
}

func writePlain(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}
