// consolidate.go implements the memory consolidation pipeline, a background
// maintenance process that keeps the memory graph healthy over time. It runs
// three sequential phases:
//
//  1. Merge -- detect duplicate semantic memories (exact content match within
//     the same project) and create supersedes edges from the newest copy to all
//     older duplicates.
//  2. Decay -- recalculate each memory's decay score using exponential time decay
//     combined with a logarithmic frequency boost based on access count.
//  3. Prune -- delete memories that have decayed below a staleness threshold,
//     have never been accessed, and are older than a configurable age limit.

package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

// ConsolidationConfig controls the behavior of the consolidation pipeline.
type ConsolidationConfig struct {
	DecayHalfLifeDays float64 // half-life for decay calculation (default: 30)
	StaleThreshold    float64 // decay score below which a memory is considered stale (default: 0.1)
	PruneAgeDays      int     // minimum age in days before pruning (default: 30)
}

// DefaultConsolidationConfig returns production-ready defaults: 30-day half-life,
// 0.1 stale threshold, and 30-day minimum age for pruning.
func DefaultConsolidationConfig() ConsolidationConfig {
	return ConsolidationConfig{
		DecayHalfLifeDays: 30,
		StaleThreshold:    0.1,
		PruneAgeDays:      30,
	}
}

// ConsolidationResult tracks the outcome of a consolidation run, reporting how
// many memories were affected in each phase.
//
// Failures are counted separately from successes, and deliberately so. Each
// phase treats a per-item error as non-fatal and moves on, which is right --
// one unwritable memory should not abandon the other 999. But the first real
// run of this job logged "consolidation complete, decayed: 949" while 51 decay
// updates had failed against a database that was refusing connections, and it
// exited 0. A scheduled job that reports success while silently skipping work
// is how data quietly stops being maintained.
type ConsolidationResult struct {
	MemoriesDecayed int
	MemoriesPruned  int
	EdgesMerged     int

	// Per-item failures, by phase. Non-fatal individually, diagnostic in
	// aggregate: a handful is normal contention, a majority is an outage.
	MergeFailures int
	DecayFailures int
	PruneFailures int
}

// Failures is the total number of items that could not be processed.
func (r ConsolidationResult) Failures() int {
	return r.MergeFailures + r.DecayFailures + r.PruneFailures
}

// Attempted is the total number of items the run tried to process.
func (r ConsolidationResult) Attempted() int {
	return r.MemoriesDecayed + r.MemoriesPruned + r.EdgesMerged + r.Failures()
}

// degradedErr reports whether too large a share of items failed for the run to
// count as successful.
//
// A method rather than an inline condition so the decision itself is testable:
// the arithmetic is what decides whether a CronJob shows Complete or Failed,
// and that is the behaviour worth pinning.
func (r ConsolidationResult) degradedErr() error {
	attempted := r.Attempted()
	if attempted == 0 {
		// An empty database is not a failed run.
		return nil
	}
	failures := r.Failures()
	if float64(failures)/float64(attempted) <= maxFailureRatio {
		return nil
	}
	return fmt.Errorf(
		"consolidation degraded: %d of %d items failed (>%.0f%%); "+
			"the run completed but most work did not happen",
		failures, attempted, maxFailureRatio*100)
}

// maxFailureRatio is the share of failed items above which a run is treated as
// failed rather than merely degraded.
//
// Not zero: individual failures happen under contention and a job that exits
// non-zero for one of them would page someone every night, which trains people
// to ignore it. Not one: a run where most items failed did not consolidate
// anything, and reporting success there is how a maintenance job silently
// stops maintaining. A tenth sits well above normal noise and well below an
// outage.
const maxFailureRatio = 0.1

// RunConsolidation executes all three consolidation phases (merge, decay, prune)
// sequentially, returning a summary result. If any phase fails, the pipeline stops
// and returns the error along with the partial result.
// consolidationRepo is the subset of the repository the pipeline uses.
//
// Narrow by design: it makes the degraded-run path testable without a live
// database, which is the only way to assert that a run where most writes fail
// actually reports failure. That was the original defect -- failures were
// counted and then discarded -- so it needs a test that exercises the pipeline,
// not just the arithmetic.
type consolidationRepo interface {
	QueryMemories(ctx context.Context, filter graph.QueryFilter) ([]model.MemoryWithContext, error)
	CreateEdge(ctx context.Context, edge model.Edge) (model.Edge, error)
	UpdateDecayScore(ctx context.Context, id uuid.UUID, score float64) error
	DeleteMemory(ctx context.Context, id uuid.UUID) error
}

func RunConsolidation(ctx context.Context, repo consolidationRepo, cfg ConsolidationConfig) (ConsolidationResult, error) {
	var result ConsolidationResult

	slog.Info("consolidation: merge phase starting")
	merged, mergeFailed, err := phaseMerge(ctx, repo)
	if err != nil {
		return result, fmt.Errorf("merge phase: %w", err)
	}
	result.EdgesMerged, result.MergeFailures = merged, mergeFailed

	slog.Info("consolidation: decay phase starting")
	decayed, decayFailed, err := phaseDecay(ctx, repo, cfg)
	if err != nil {
		return result, fmt.Errorf("decay phase: %w", err)
	}
	result.MemoriesDecayed, result.DecayFailures = decayed, decayFailed

	slog.Info("consolidation: prune phase starting")
	pruned, pruneFailed, err := phasePrune(ctx, repo, cfg)
	if err != nil {
		return result, fmt.Errorf("prune phase: %w", err)
	}
	result.MemoriesPruned, result.PruneFailures = pruned, pruneFailed

	failures, attempted := result.Failures(), result.Attempted()
	slog.Info("consolidation complete",
		slog.Int("merged", merged), slog.Int("decayed", decayed), slog.Int("pruned", pruned),
		slog.Int("failed", failures), slog.Int("attempted", attempted))

	// Report a run that mostly failed as a failure, so the CronJob's status
	// reflects whether the work happened rather than whether the process ran.
	if err := result.degradedErr(); err != nil {
		return result, err
	}

	return result, nil
}

// phaseMerge detects duplicate semantic memories by grouping on (project_id, content).
// For each group with more than one memory, the most recently created one is kept
// and supersedes edges are created from it to all older duplicates. This prevents
// identical facts from cluttering query results.
//
// NOTE: The current implementation uses exact content matching. A future version
// should incorporate content similarity scoring for near-duplicate detection.
func phaseMerge(ctx context.Context, repo consolidationRepo) (merged, failed int, err error) {
	filter := graph.QueryFilter{
		Types: []model.MemoryType{model.MemoryTypeSemantic},
		TopK:  100,
	}

	results, err := repo.QueryMemories(ctx, filter)
	if err != nil {
		return 0, 0, fmt.Errorf("query for merge: %w", err)
	}

	// Group by content hash (simple dedup).
	type memKey struct {
		projectID string
		content   string
	}
	groups := make(map[memKey][]model.Memory)
	for _, r := range results {
		key := memKey{projectID: r.Memory.ProjectID, content: r.Memory.Content}
		groups[key] = append(groups[key], r.Memory)
	}

	for _, mems := range groups {
		if len(mems) < 2 {
			continue
		}
		// Keep the most recently created one, supersede the rest.
		newest := mems[0]
		for _, m := range mems[1:] {
			if m.CreatedAt.After(newest.CreatedAt) {
				newest = m
			}
		}

		for _, m := range mems {
			if m.ID == newest.ID {
				continue
			}
			edge := model.Edge{
				ID:           uuid.New(),
				FromID:       newest.ID,
				ToID:         m.ID,
				Relationship: model.RelSupersedes,
				Weight:       1.0,
				CreatedAt:    time.Now().UTC(),
			}
			if _, err := repo.CreateEdge(ctx, edge); err != nil {
				slog.Warn("consolidation: creating supersedes edge failed", slog.Any("error", err))
				failed++
				continue
			}
			merged++
		}
	}

	return merged, failed, nil
}

// phaseDecay recalculates the decay_score for every memory using an exponential
// decay formula combined with a frequency boost:
//
//	decay = exp(-0.693 * hoursSinceCreation / halfLifeHours) * (1 + frequencyBoost)
//
// where frequencyBoost = min(1.0, ln(1 + accessCount) / 5.0). This ensures that
// frequently accessed memories decay more slowly. The final score is clamped to [0, 1].
func phaseDecay(ctx context.Context, repo consolidationRepo, cfg ConsolidationConfig) (decayed, failed int, err error) {
	filter := graph.QueryFilter{TopK: 1000}
	results, err := repo.QueryMemories(ctx, filter)
	if err != nil {
		return 0, 0, fmt.Errorf("query for decay: %w", err)
	}

	now := time.Now().UTC()
	halfLifeHours := cfg.DecayHalfLifeDays * 24.0

	for _, r := range results {
		hoursSince := now.Sub(r.Memory.CreatedAt).Hours()
		if hoursSince < 0 {
			hoursSince = 0
		}

		// decay = exp(-0.693 * hours / halfLife) * frequencyBoost * baseImportance
		baseDecay := math.Exp(-0.693 * hoursSince / halfLifeHours)
		frequencyBoost := math.Log1p(float64(r.Memory.AccessCount)) / 5.0
		if frequencyBoost > 1.0 {
			frequencyBoost = 1.0
		}
		newScore := baseDecay * (1.0 + frequencyBoost)
		if newScore > 1.0 {
			newScore = 1.0
		}

		if err := repo.UpdateDecayScore(ctx, r.Memory.ID, newScore); err != nil {
			slog.Warn("consolidation: updating decay score failed", slog.String("memory_id", r.Memory.ID.String()), slog.Any("error", err))
			failed++
			continue
		}
		decayed++
	}

	return decayed, failed, nil
}

// phasePrune removes memories that meet all three pruning criteria simultaneously:
//   - decay_score is below the configured stale threshold.
//   - access_count is zero (the memory was never retrieved by a query).
//   - age exceeds the configured minimum prune age in days.
//
// This conservative approach ensures that only truly abandoned memories are deleted.
func phasePrune(ctx context.Context, repo consolidationRepo, cfg ConsolidationConfig) (pruned, failed int, err error) {
	filter := graph.QueryFilter{TopK: 1000}
	results, err := repo.QueryMemories(ctx, filter)
	if err != nil {
		return 0, 0, fmt.Errorf("query for prune: %w", err)
	}

	now := time.Now().UTC()

	for _, r := range results {
		ageDays := now.Sub(r.Memory.CreatedAt).Hours() / 24.0

		// Prune if: decay_score < threshold AND access_count == 0 AND age > N days.
		if r.Memory.DecayScore < cfg.StaleThreshold &&
			r.Memory.AccessCount == 0 &&
			ageDays > float64(cfg.PruneAgeDays) {

			if err := repo.DeleteMemory(ctx, r.Memory.ID); err != nil {
				slog.Warn("consolidation: pruning memory failed", slog.String("memory_id", r.Memory.ID.String()), slog.Any("error", err))
				failed++
				continue
			}
			pruned++
		}
	}

	return pruned, failed, nil
}
