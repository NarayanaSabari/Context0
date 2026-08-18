package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
