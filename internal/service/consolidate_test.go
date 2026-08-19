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

// mergeTestRepo is a consolidationRepo that serves a fixed set of memories and
// records the edges the merge phase writes.
type mergeTestRepo struct {
	memories []model.Memory
	edges    []model.Edge
	failEdge bool
}

func (r *mergeTestRepo) QueryMemories(_ context.Context, _ graph.QueryFilter) ([]model.MemoryWithContext, error) {
	out := make([]model.MemoryWithContext, 0, len(r.memories))
	for _, m := range r.memories {
		out = append(out, model.MemoryWithContext{Memory: m})
	}
	return out, nil
}

func (r *mergeTestRepo) CreateEdge(_ context.Context, e model.Edge) (model.Edge, error) {
	if r.failEdge {
		return model.Edge{}, errors.New("edge write refused")
	}
	r.edges = append(r.edges, e)
	return e, nil
}

func (r *mergeTestRepo) UpdateDecayScore(_ context.Context, _ uuid.UUID, _ float64) error {
	return nil
}

func (r *mergeTestRepo) DeleteMemory(_ context.Context, _ uuid.UUID) error { return nil }

// TestPhaseMergeSupersedesOlderDuplicates covers what the merge phase actually
// does, which nothing asserted: the existing consolidation tests only cover how
// results are counted and reported.
//
// The important properties are that the newest duplicate is the survivor and
// that it never supersedes itself. A self-loop would make a memory its own
// replacement, which is both meaningless in the graph and, since supersedes
// edges are followed when resolving current facts, a cycle for any traversal
// that walks them.
func TestPhaseMergeSupersedesOlderDuplicates(t *testing.T) {
	base := time.Now().UTC().Add(-24 * time.Hour)
	mk := func(project, content string, ageMinutes int) model.Memory {
		return model.Memory{
			ID:        uuid.New(),
			ProjectID: project,
			Type:      model.MemoryTypeSemantic,
			Content:   content,
			CreatedAt: base.Add(time.Duration(ageMinutes) * time.Minute),
		}
	}

	oldest := mk("proj-a", "the deploy target is staging", 0)
	middle := mk("proj-a", "the deploy target is staging", 10)
	newest := mk("proj-a", "the deploy target is staging", 20)
	unique := mk("proj-a", "the region is eu-west-1", 5)
	// Same content, different project: projects are isolated, so this must not
	// be merged with the group above.
	otherProject := mk("proj-b", "the deploy target is staging", 30)

	repo := &mergeTestRepo{memories: []model.Memory{oldest, newest, unique, middle, otherProject}}

	merged, failed, err := phaseMerge(context.Background(), repo)
	if err != nil {
		t.Fatalf("phaseMerge: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
	// Two older duplicates superseded; the unique memory and the one in the
	// other project are untouched.
	if merged != 2 {
		t.Errorf("merged = %d, want 2", merged)
	}
	if len(repo.edges) != 2 {
		t.Fatalf("wrote %d edges, want 2", len(repo.edges))
	}

	superseded := map[uuid.UUID]bool{}
	for _, e := range repo.edges {
		if e.FromID != newest.ID {
			t.Errorf("edge originates from %v, want the newest duplicate %v: "+
				"consolidation must keep the most recent fact", e.FromID, newest.ID)
		}
		if e.FromID == e.ToID {
			t.Errorf("memory %v supersedes itself: a self-loop makes a memory "+
				"its own replacement and cycles any traversal of supersedes edges", e.ToID)
		}
		if e.Relationship != model.RelSupersedes {
			t.Errorf("relationship = %v, want %v", e.Relationship, model.RelSupersedes)
		}
		superseded[e.ToID] = true
	}

	for _, want := range []model.Memory{oldest, middle} {
		if !superseded[want.ID] {
			t.Errorf("older duplicate %v was not superseded", want.ID)
		}
	}
	if superseded[unique.ID] {
		t.Error("a memory with unique content was superseded")
	}
	if superseded[otherProject.ID] {
		t.Error("a memory in a different project was superseded: " +
			"identical content across projects is not a duplicate")
	}
	if superseded[newest.ID] {
		t.Error("the newest duplicate was superseded; it must be the survivor")
	}
}

// TestPhaseMergeCountsEdgeFailures: a merge that cannot write its edge is a
// failure, not a silent no-op. The ratio of these is what makes the CronJob
// report a degraded run.
func TestPhaseMergeCountsEdgeFailures(t *testing.T) {
	base := time.Now().UTC()
	mk := func(content string, ageMinutes int) model.Memory {
		return model.Memory{
			ID:        uuid.New(),
			ProjectID: "proj",
			Type:      model.MemoryTypeSemantic,
			Content:   content,
			CreatedAt: base.Add(time.Duration(ageMinutes) * time.Minute),
		}
	}
	repo := &mergeTestRepo{
		memories: []model.Memory{mk("duplicate fact", 0), mk("duplicate fact", 5)},
		failEdge: true,
	}

	merged, failed, err := phaseMerge(context.Background(), repo)
	if err != nil {
		t.Fatalf("phaseMerge: %v", err)
	}
	if merged != 0 {
		t.Errorf("merged = %d, want 0: no edge was written", merged)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1: a refused edge write must be counted, "+
			"or the job reports success while doing nothing", failed)
	}
}

// TestPhaseMergeLeavesSingletonsAlone: a project of distinct facts must produce
// no edges at all.
func TestPhaseMergeLeavesSingletonsAlone(t *testing.T) {
	base := time.Now().UTC()
	var mems []model.Memory
	for i, c := range []string{"fact one", "fact two", "fact three"} {
		mems = append(mems, model.Memory{
			ID:        uuid.New(),
			ProjectID: "proj",
			Type:      model.MemoryTypeSemantic,
			Content:   c,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	repo := &mergeTestRepo{memories: mems}

	merged, failed, err := phaseMerge(context.Background(), repo)
	if err != nil {
		t.Fatalf("phaseMerge: %v", err)
	}
	if merged != 0 || failed != 0 || len(repo.edges) != 0 {
		t.Errorf("merged=%d failed=%d edges=%d, want all zero: distinct facts "+
			"are not duplicates", merged, failed, len(repo.edges))
	}
}
