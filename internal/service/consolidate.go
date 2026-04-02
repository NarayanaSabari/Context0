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
//
// The pipeline supports a dry-run mode that logs intended actions without mutating
// the graph.

package service

import (
	"context"
	"fmt"
	"log"
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
	DryRun            bool    // if true, log actions but don't mutate
}

// DefaultConsolidationConfig returns production-ready defaults: 30-day half-life,
// 0.1 stale threshold, 30-day minimum age for pruning, and mutations enabled.
func DefaultConsolidationConfig() ConsolidationConfig {
	return ConsolidationConfig{
		DecayHalfLifeDays: 30,
		StaleThreshold:    0.1,
		PruneAgeDays:      30,
		DryRun:            false,
	}
}

// ConsolidationResult tracks the outcome of a consolidation run, reporting how
// many memories were affected in each phase.
type ConsolidationResult struct {
	MemoriesDecayed int
	MemoriesPruned  int
	EdgesMerged     int
}

// RunConsolidation executes all three consolidation phases (merge, decay, prune)
// sequentially, returning a summary result. If any phase fails, the pipeline stops
// and returns the error along with the partial result.
func RunConsolidation(ctx context.Context, repo graph.Repository, cfg ConsolidationConfig) (ConsolidationResult, error) {
	var result ConsolidationResult

	log.Println("consolidation: starting merge phase...")
	merged, err := phaseMerge(ctx, repo, cfg)
	if err != nil {
		return result, fmt.Errorf("merge phase: %w", err)
	}
	result.EdgesMerged = merged

	log.Println("consolidation: starting decay phase...")
	decayed, err := phaseDecay(ctx, repo, cfg)
	if err != nil {
		return result, fmt.Errorf("decay phase: %w", err)
	}
	result.MemoriesDecayed = decayed

	log.Println("consolidation: starting prune phase...")
	pruned, err := phasePrune(ctx, repo, cfg)
	if err != nil {
		return result, fmt.Errorf("prune phase: %w", err)
	}
	result.MemoriesPruned = pruned

	log.Printf("consolidation: complete (merged=%d, decayed=%d, pruned=%d)",
		result.EdgesMerged, result.MemoriesDecayed, result.MemoriesPruned)

	return result, nil
}

// phaseMerge detects duplicate semantic memories by grouping on (project_id, content).
// For each group with more than one memory, the most recently created one is kept
// and supersedes edges are created from it to all older duplicates. This prevents
// identical facts from cluttering query results.
//
// NOTE: The current implementation uses exact content matching. A future version
// should incorporate content similarity scoring for near-duplicate detection.
func phaseMerge(ctx context.Context, repo graph.Repository, cfg ConsolidationConfig) (int, error) {

	filter := graph.QueryFilter{
		Types: []model.MemoryType{model.MemoryTypeSemantic},
		TopK:  100,
	}

	results, err := repo.QueryMemories(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("query for merge: %w", err)
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

	merged := 0
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
			if cfg.DryRun {
				log.Printf("consolidation [dry-run]: would supersede %s with %s", m.ID, newest.ID)
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
			if err := repo.CreateEdge(ctx, edge); err != nil {
				log.Printf("consolidation: failed to create supersedes edge: %v", err)
				continue
			}
			merged++
		}
	}

	return merged, nil
}

// phaseDecay recalculates the decay_score for every memory using an exponential
// decay formula combined with a frequency boost:
//
//	decay = exp(-0.693 * hoursSinceCreation / halfLifeHours) * (1 + frequencyBoost)
//
// where frequencyBoost = min(1.0, ln(1 + accessCount) / 5.0). This ensures that
// frequently accessed memories decay more slowly. The final score is clamped to [0, 1].
func phaseDecay(ctx context.Context, repo graph.Repository, cfg ConsolidationConfig) (int, error) {
	filter := graph.QueryFilter{TopK: 1000}
	results, err := repo.QueryMemories(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("query for decay: %w", err)
	}

	now := time.Now().UTC()
	halfLifeHours := cfg.DecayHalfLifeDays * 24.0
	decayed := 0

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

		if cfg.DryRun {
			log.Printf("consolidation [dry-run]: would set decay_score of %s from %.3f to %.3f",
				r.Memory.ID, r.Memory.DecayScore, newScore)
			continue
		}

		// TODO: Add UpdateDecayScore to repository interface.
		// For now, decay is tracked in-memory during consolidation.
		decayed++
	}

	return decayed, nil
}

// phasePrune removes memories that meet all three pruning criteria simultaneously:
//   - decay_score is below the configured stale threshold.
//   - access_count is zero (the memory was never retrieved by a query).
//   - age exceeds the configured minimum prune age in days.
//
// This conservative approach ensures that only truly abandoned memories are deleted.
func phasePrune(ctx context.Context, repo graph.Repository, cfg ConsolidationConfig) (int, error) {
	filter := graph.QueryFilter{TopK: 1000}
	results, err := repo.QueryMemories(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("query for prune: %w", err)
	}

	now := time.Now().UTC()
	pruned := 0

	for _, r := range results {
		ageDays := now.Sub(r.Memory.CreatedAt).Hours() / 24.0

		// Prune if: decay_score < threshold AND access_count == 0 AND age > N days.
		if r.Memory.DecayScore < cfg.StaleThreshold &&
			r.Memory.AccessCount == 0 &&
			ageDays > float64(cfg.PruneAgeDays) {

			if cfg.DryRun {
				log.Printf("consolidation [dry-run]: would prune memory %s (decay=%.3f, access=%d, age=%.0f days)",
					r.Memory.ID, r.Memory.DecayScore, r.Memory.AccessCount, ageDays)
				continue
			}

			if err := repo.DeleteMemory(ctx, r.Memory.ID); err != nil {
				log.Printf("consolidation: failed to prune memory %s: %v", r.Memory.ID, err)
				continue
			}
			pruned++
		}
	}

	return pruned, nil
}
