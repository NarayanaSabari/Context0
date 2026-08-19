package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

// TestConsolidationResultReportsDegradedRuns covers the specific defect this
// accounting was added for.
//
// The first real run of the CronJob against a live cluster logged
// "consolidation complete, decayed: 949" and exited 0, while 51 decay updates
// had failed against a database that was refusing connections. Kubernetes
// marked the job Complete. Nothing anywhere indicated that maintenance had not
// happened -- and this job is the only thing that decays, merges, and prunes,
// so a silent failure means the data quietly stops being maintained.
//
// The threshold is a ratio, not zero: individual failures are normal under
// contention, and a job that exits non-zero for one of them gets ignored.
func TestConsolidationResultReportsDegradedRuns(t *testing.T) {
	cases := []struct {
		name     string
		result   ConsolidationResult
		degraded bool
	}{
		{
			name:     "a clean run",
			result:   ConsolidationResult{EdgesMerged: 3, MemoriesDecayed: 1000},
			degraded: false,
		},
		{
			name: "a few failures under contention are tolerated",
			result: ConsolidationResult{
				MemoriesDecayed: 990,
				DecayFailures:   10,
			},
			degraded: false,
		},
		{
			name: "the run that actually happened: 949 decayed, 51 failed",
			result: ConsolidationResult{
				EdgesMerged:     3,
				MemoriesDecayed: 949,
				DecayFailures:   51,
			},
			degraded: false, // 51/1003 = 5.1%, under the threshold
		},
		{
			name: "a database outage: almost everything failed",
			result: ConsolidationResult{
				MemoriesDecayed: 20,
				DecayFailures:   980,
			},
			degraded: true,
		},
		{
			name: "every item failed",
			result: ConsolidationResult{
				MergeFailures: 5,
				DecayFailures: 500,
				PruneFailures: 2,
			},
			degraded: true,
		},
		{
			name:     "an empty database is not a failure",
			result:   ConsolidationResult{},
			degraded: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Calls the real decision function, not a reimplementation of it:
			// this is what turns a CronJob green or red.
			err := tc.result.degradedErr()
			got := err != nil

			if got != tc.degraded {
				attempted := tc.result.Attempted()
				t.Errorf("degraded = %v (err=%v), want %v (%d of %d failed)",
					got, err, tc.degraded, tc.result.Failures(), attempted)
			}
			if got && err != nil && !strings.Contains(err.Error(), "did not happen") {
				t.Errorf("the degraded error should say the work did not happen: %v", err)
			}
		})
	}
}

// TestConsolidationResultAccounting: Attempted must include failures, or the
// ratio is computed against successes alone and a run where everything failed
// divides by zero and reports healthy.
func TestConsolidationResultAccounting(t *testing.T) {
	r := ConsolidationResult{
		EdgesMerged: 1, MemoriesDecayed: 2, MemoriesPruned: 3,
		MergeFailures: 4, DecayFailures: 5, PruneFailures: 6,
	}
	if got := r.Failures(); got != 15 {
		t.Errorf("Failures() = %d, want 15", got)
	}
	if got := r.Attempted(); got != 21 {
		t.Errorf("Attempted() = %d, want 21 (successes + failures)", got)
	}

	// The case that would divide by zero if Attempted counted only successes.
	allFailed := ConsolidationResult{DecayFailures: 100}
	if got := allFailed.Attempted(); got != 100 {
		t.Errorf("Attempted() = %d for an all-failed run, want 100", got)
	}
}

// failingRepo returns memories to process and fails every write.
type failingRepo struct {
	memories []model.MemoryWithContext
	writeErr error
}

func (r *failingRepo) QueryMemories(context.Context, graph.QueryFilter) ([]model.MemoryWithContext, error) {
	return r.memories, nil
}
func (r *failingRepo) CreateEdge(context.Context, model.Edge) (model.Edge, error) {
	return model.Edge{}, r.writeErr
}
func (r *failingRepo) UpdateDecayScore(context.Context, uuid.UUID, float64) error {
	return r.writeErr
}
func (r *failingRepo) DeleteMemory(context.Context, uuid.UUID) error { return r.writeErr }

// TestRunConsolidationReturnsDegradedError drives the real pipeline, not the
// arithmetic in isolation.
//
// degradedErr can be entirely correct while RunConsolidation ignores it, which
// is precisely the original defect: failures were counted and then dropped, so
// the job logged "consolidation complete, decayed: 949" with 51 failures and
// exited 0, and Kubernetes showed the CronJob as Complete.
func TestRunConsolidationReturnsDegradedError(t *testing.T) {
	mems := make([]model.MemoryWithContext, 50)
	for i := range mems {
		mems[i] = model.MemoryWithContext{Memory: model.Memory{
			ID:        uuid.New(),
			ProjectID: "p",
			Content:   "content",
			CreatedAt: time.Now().Add(-24 * time.Hour),
		}}
	}

	repo := &failingRepo{memories: mems, writeErr: errors.New("connection refused")}
	result, err := RunConsolidation(context.Background(), repo, DefaultConsolidationConfig())

	if err == nil {
		t.Fatal("a run where every write failed reported success; " +
			"the CronJob would show Complete having done nothing")
	}
	if !strings.Contains(err.Error(), "degraded") {
		t.Errorf("error should identify the run as degraded: %v", err)
	}
	if result.Failures() == 0 {
		t.Error("failures were not counted")
	}
	if result.MemoriesDecayed != 0 {
		t.Errorf("decayed = %d, want 0: every write failed", result.MemoriesDecayed)
	}
}

// TestRunConsolidationSucceedsWhenWritesSucceed is the counterpart: the
// threshold must not turn a healthy run red, or it gets ignored.
func TestRunConsolidationSucceedsWhenWritesSucceed(t *testing.T) {
	mems := make([]model.MemoryWithContext, 50)
	for i := range mems {
		mems[i] = model.MemoryWithContext{Memory: model.Memory{
			ID:        uuid.New(),
			ProjectID: "p",
			Content:   fmt.Sprintf("unique content %d", i),
			CreatedAt: time.Now().Add(-24 * time.Hour),
		}}
	}

	repo := &failingRepo{memories: mems} // writeErr nil: writes succeed
	result, err := RunConsolidation(context.Background(), repo, DefaultConsolidationConfig())
	if err != nil {
		t.Fatalf("a healthy run reported failure: %v", err)
	}
	if result.Failures() != 0 {
		t.Errorf("failures = %d, want 0", result.Failures())
	}
	if result.MemoriesDecayed != len(mems) {
		t.Errorf("decayed = %d, want %d", result.MemoriesDecayed, len(mems))
	}
}
