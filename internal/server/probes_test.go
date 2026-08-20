package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubPinger lets a test drive the database check without a database.
type stubPinger struct {
	err   error
	delay time.Duration
	calls int
}

func (s *stubPinger) Ping(ctx context.Context) error {
	s.calls++
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

func get(t *testing.T, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

// TestLiveness_NeverTouchesTheDatabase is the point of the whole split. A
// liveness probe that depends on Postgres fails on every replica at once during
// a database blip, and Kubernetes responds by restarting the entire fleet --
// which cannot fix a remote database and drops every warm connection pool.
func TestLiveness_NeverTouchesTheDatabase(t *testing.T) {
	pool := &stubPinger{err: errors.New("database is down")}
	p := NewProbes(pool)
	p.MarkStarted()

	rec := get(t, p.Live)

	if rec.Code != http.StatusOK {
		t.Errorf("liveness = %d with the database down, want %d", rec.Code, http.StatusOK)
	}
	if pool.calls != 0 {
		t.Errorf("liveness pinged the database %d times, want 0", pool.calls)
	}
}

// TestLiveness_PassesBeforeStartup keeps liveness independent of startup state:
// the startup probe is what suppresses traffic during initialization, and
// liveness must not restart a container that is merely still booting.
func TestLiveness_PassesBeforeStartup(t *testing.T) {
	p := NewProbes(&stubPinger{})
	if rec := get(t, p.Live); rec.Code != http.StatusOK {
		t.Errorf("liveness = %d before startup, want %d", rec.Code, http.StatusOK)
	}
}

func TestStartup(t *testing.T) {
	p := NewProbes(&stubPinger{})

	if rec := get(t, p.Startup); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("startup = %d before init, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	p.MarkStarted()

	if rec := get(t, p.Startup); rec.Code != http.StatusOK {
		t.Errorf("startup = %d after init, want %d", rec.Code, http.StatusOK)
	}
}

func TestReadiness(t *testing.T) {
	tests := []struct {
		name     string
		started  bool
		draining bool
		pingErr  error
		want     int
	}{
		{"before startup", false, false, nil, http.StatusServiceUnavailable},
		{"ready", true, false, nil, http.StatusOK},
		{"draining", true, true, nil, http.StatusServiceUnavailable},
		{"database unreachable", true, false, errors.New("connection refused"), http.StatusServiceUnavailable},
		// Draining wins even when the database is fine: the pod is going away.
		{"draining with healthy database", true, true, nil, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProbes(&stubPinger{err: tt.pingErr})
			if tt.started {
				p.MarkStarted()
			}
			if tt.draining {
				p.MarkDraining()
			}

			if rec := get(t, p.Ready); rec.Code != tt.want {
				t.Errorf("readiness = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// TestReadiness_PingIsBounded guards the timeout. A probe that can block longer
// than its own period stacks up connections against a database that is already
// struggling, turning slowness into an outage.
func TestReadiness_PingIsBounded(t *testing.T) {
	p := NewProbes(&stubPinger{delay: 30 * time.Second})
	p.MarkStarted()

	start := time.Now()
	rec := get(t, p.Ready)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness = %d against a hung database, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if elapsed > 5*time.Second {
		t.Errorf("readiness blocked for %s; it must give up near %s", elapsed, readinessTimeout)
	}
}

// TestDrainingIsMonotonic: once shutdown starts the pod must never advertise
// itself as ready again, or Kubernetes could route traffic back to it mid-drain.
func TestDrainingIsMonotonic(t *testing.T) {
	p := NewProbes(&stubPinger{})
	p.MarkStarted()
	p.MarkDraining()

	for i := 0; i < 3; i++ {
		if rec := get(t, p.Ready); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("readiness recovered to %d after draining began", rec.Code)
		}
	}
}

// TestRegisterWiresAllThree checks the probes are reachable at the paths the
// Helm chart references. A rename here silently breaks every probe in the
// cluster, so the paths are pinned.
func TestRegisterWiresAllThree(t *testing.T) {
	p := NewProbes(&stubPinger{})
	p.MarkStarted()

	mux := http.NewServeMux()
	p.Register(mux)

	for _, path := range []string{"/livez", "/readyz", "/startupz"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s is not registered", path)
		}
	}
}

// blockingPinger blocks for a fixed delay, honouring context cancellation the
// way a real pool does.
type blockingPinger struct{ delay time.Duration }

func (b blockingPinger) Ping(ctx context.Context) error {
	select {
	case <-time.After(b.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestReadyIgnoresCallerCancellation covers a readiness answer that described
// the caller rather than the database.
//
// The database check was bounded by r.Context(), so a kubelet that hung up
// mid-request -- its probe timeoutSeconds elapsing, which defaults to 1s
// against a readinessTimeout of 1s, so the two race on a loaded node --
// produced "database unreachable" while the database was perfectly healthy.
// The pod is then removed from Service endpoints for a reason that never
// happened, and the message sends whoever investigates to the wrong component.
func TestReadyIgnoresCallerCancellation(t *testing.T) {
	p := NewProbes(blockingPinger{delay: 10 * time.Millisecond})
	p.MarkStarted()

	// A caller that has already gone away.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	p.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Errorf("a healthy pod answered %d %q because the caller disconnected; "+
			"readiness must describe the database, not the client that asked",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}

// TestReadyStillFailsOnASlowDatabase keeps the fix above from being bought by
// ignoring the database entirely: the handler's own bound must still apply.
func TestReadyStillFailsOnASlowDatabase(t *testing.T) {
	// Well past readinessTimeout.
	p := NewProbes(blockingPinger{delay: readinessTimeout * 5})
	p.MarkStarted()

	start := time.Now()
	rec := httptest.NewRecorder()
	p.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a database that never answers produced %d; the pod would keep "+
			"taking traffic it cannot serve", rec.Code)
	}
	// The handler must give up on its own deadline rather than waiting for the
	// database, or a hung database holds the probe open until the kubelet
	// times out and the reason is lost.
	if elapsed > readinessTimeout*2 {
		t.Errorf("the handler took %s to answer against a %s bound; it is "+
			"waiting on the database instead of its own deadline",
			elapsed, readinessTimeout)
	}
	// The two failures call for different action, so they must read
	// differently: a timeout means slow or gone, anything else is the
	// connection itself.
	if body := rec.Body.String(); !strings.Contains(body, "did not respond") {
		t.Errorf("a timeout was reported as %q; it should be distinguishable "+
			"from a connection failure", strings.TrimSpace(body))
	}
}

// TestReadyReportsConnectionFailureDistinctly: the other half of the pair.
func TestReadyReportsConnectionFailureDistinctly(t *testing.T) {
	p := NewProbes(failingPinger{})
	p.MarkStarted()

	rec := httptest.NewRecorder()
	p.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unreachable database produced %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "database unreachable" {
		t.Errorf("connection failure reported as %q, want \"database unreachable\"", body)
	}
}

// failingPinger fails immediately, as a refused connection does.
type failingPinger struct{}

func (failingPinger) Ping(context.Context) error {
	return errors.New("connection refused")
}
