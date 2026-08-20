package service

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/metrics"
)

// sessionTestService builds a SessionService against the real database. The
// session lifecycle is a database state transition, so a stub would test the
// stub rather than the behaviour that broke.
func sessionTestService(t *testing.T) (*SessionService, context.Context) {
	t.Helper()

	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return NewSessionService(repo), ctx
}

// TestEndSessionIsNotRepeatable covers the gauge corruption caused by ending a
// session more than once.
//
// EndSession matched a Session vertex by ID and set ended_at unconditionally,
// so a second call overwrote the original timestamp, returned success, and ran
// metrics.ActiveSessions.Dec() again. Reproduced against the live API: one
// StartSession followed by three EndSession calls left
// kora_active_sessions at -2. A negative gauge silently breaks any alert
// or dashboard built on it, and losing the first ended_at also corrupts
// session duration.
//
// Found by mutation testing, which showed nothing asserted on the EndSession
// error path at all.
func TestEndSessionIsNotRepeatable(t *testing.T) {
	svc, ctx := sessionTestService(t)

	before := testutil.ToFloat64(metrics.ActiveSessions)

	start, err := svc.StartSession(ctx, &pb.StartSessionRequest{
		ProjectId: "session-lifecycle-test",
		AgentId:   "test-agent",
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	id := start.Session.Id

	if got := testutil.ToFloat64(metrics.ActiveSessions); got != before+1 {
		t.Fatalf("after StartSession the gauge is %v, want %v", got, before+1)
	}

	first, err := svc.EndSession(ctx, &pb.EndSessionRequest{Id: id})
	if err != nil {
		t.Fatalf("first EndSession: %v", err)
	}
	firstEndedAt := first.Session.EndedAt.AsTime()

	if got := testutil.ToFloat64(metrics.ActiveSessions); got != before {
		t.Fatalf("after the first EndSession the gauge is %v, want %v", got, before)
	}

	// Every subsequent end must be rejected and must not move the gauge.
	for i := 2; i <= 4; i++ {
		_, err := svc.EndSession(ctx, &pb.EndSessionRequest{Id: id})
		if err == nil {
			t.Fatalf("EndSession call #%d succeeded; ending a session twice "+
				"decrements ActiveSessions twice and drives the gauge negative", i)
		}
		if got := status.Code(err); got != codes.Aborted {
			t.Errorf("EndSession call #%d returned code %v, want Aborted (409 Conflict)", i, got)
		}
		if got := testutil.ToFloat64(metrics.ActiveSessions); got != before {
			t.Fatalf("EndSession call #%d moved the gauge to %v, want %v", i, got, before)
		}
	}

	// The original end timestamp is asserted in the graph package, where the
	// session vertex can be read back directly; see
	// TestEndSessionPreservesOriginalTimestamp.
	_ = firstEndedAt
}

// TestEndSessionUnknownID: an ID that was never issued is NotFound, which must
// stay distinct from the already-ended case so a client can tell a stale retry
// from a bad ID.
func TestEndSessionUnknownID(t *testing.T) {
	svc, ctx := sessionTestService(t)

	before := testutil.ToFloat64(metrics.ActiveSessions)

	_, err := svc.EndSession(ctx, &pb.EndSessionRequest{
		Id: "6f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8",
	})
	if err == nil {
		t.Fatal("ending an unknown session succeeded")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("got code %v, want NotFound", got)
	}
	if got := testutil.ToFloat64(metrics.ActiveSessions); got != before {
		t.Errorf("a failed EndSession moved the gauge from %v to %v", before, got)
	}
}

// TestEndSessionRejectsMalformedID keeps argument validation ahead of any
// database work, and off the gauge.
func TestEndSessionRejectsMalformedID(t *testing.T) {
	svc, ctx := sessionTestService(t)

	before := testutil.ToFloat64(metrics.ActiveSessions)

	for _, id := range []string{"", "not-a-uuid", "12345"} {
		_, err := svc.EndSession(ctx, &pb.EndSessionRequest{Id: id})
		if err == nil {
			t.Errorf("EndSession(%q) succeeded, want InvalidArgument", id)
			continue
		}
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("EndSession(%q) returned code %v, want InvalidArgument", id, got)
		}
	}
	if got := testutil.ToFloat64(metrics.ActiveSessions); got != before {
		t.Errorf("rejected requests moved the gauge from %v to %v", before, got)
	}
}

// TestStartSessionValidatesRequiredFields: both fields identify who the
// session belongs to, and neither is recoverable after the fact.
func TestStartSessionValidatesRequiredFields(t *testing.T) {
	svc, ctx := sessionTestService(t)

	before := testutil.ToFloat64(metrics.ActiveSessions)

	cases := []struct {
		name      string
		projectID string
		agentID   string
	}{
		{"no project", "", "agent-1"},
		{"no agent", "proj-1", ""},
		{"neither", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.StartSession(ctx, &pb.StartSessionRequest{
				ProjectId: tc.projectID,
				AgentId:   tc.agentID,
			})
			if err == nil {
				t.Fatal("StartSession succeeded with a missing required field")
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("got code %v, want InvalidArgument", got)
			}
		})
	}

	if got := testutil.ToFloat64(metrics.ActiveSessions); got != before {
		t.Errorf("rejected StartSession calls moved the gauge from %v to %v: "+
			"a session that was never created must not count as active", before, got)
	}
}

// TestEndSessionErrorsAreDistinguishable pins the sentinel errors themselves,
// since the gRPC codes above are only correct if these stay distinct.
func TestEndSessionErrorsAreDistinguishable(t *testing.T) {
	if errors.Is(graph.ErrSessionAlreadyEnded, graph.ErrSessionNotFound) {
		t.Error("ErrSessionAlreadyEnded matches ErrSessionNotFound; " +
			"a retry of a completed session would be reported as a bad ID")
	}
	if errors.Is(graph.ErrSessionNotFound, graph.ErrSessionAlreadyEnded) {
		t.Error("ErrSessionNotFound matches ErrSessionAlreadyEnded")
	}
}

// TestConcurrentEndSessionDecrementsOnce: the guard lives in the MATCH clause
// so exactly one of N racing callers can transition the session.
//
// A read-then-write in Go would let two callers both observe ended_at as null
// and both decrement ActiveSessions, which is the same gauge corruption the
// sequential case produced but only under load.
func TestConcurrentEndSessionDecrementsOnce(t *testing.T) {
	svc, ctx := sessionTestService(t)

	start, err := svc.StartSession(ctx, &pb.StartSessionRequest{
		ProjectId: "session-race-test",
		AgentId:   "test-agent",
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	const racers = 8
	var wg sync.WaitGroup
	var succeeded atomic.Int32

	before := testutil.ToFloat64(metrics.ActiveSessions)

	start2 := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start2 // release all goroutines together
			if _, err := svc.EndSession(ctx, &pb.EndSessionRequest{Id: start.Session.Id}); err == nil {
				succeeded.Add(1)
			}
		}()
	}
	close(start2)
	wg.Wait()

	if got := succeeded.Load(); got != 1 {
		t.Errorf("%d of %d concurrent EndSession calls succeeded, want exactly 1: "+
			"each success decrements ActiveSessions", got, racers)
	}
	if got := testutil.ToFloat64(metrics.ActiveSessions); got != before-1 {
		t.Errorf("gauge moved from %v to %v, want %v: concurrent ends "+
			"decremented more than once", before, got, before-1)
	}
}
