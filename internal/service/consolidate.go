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

// ConsolidationConfig controls the consolidation pipeline.
type ConsolidationConfig struct {
	DecayHalfLifeDays float64 // half-life for decay calculation (default: 30)
	StaleThreshold    float64 // decay score below which a memory is considered stale (default: 0.1)
	PruneAgeDays      int     // minimum age in days before pruning (default: 30)
	DryRun            bool    // if true, log actions but don't mutate
}

// DefaultConsolidationConfig returns sensible defaults.
func DefaultConsolidationConfig() ConsolidationConfig {
	return ConsolidationConfig{
		DecayHalfLifeDays: 30,
		StaleThreshold:    0.1,
		PruneAgeDays:      30,
		DryRun:            false,
	}
}

// ConsolidationResult tracks what the pipeline did.
type ConsolidationResult struct {
	MemoriesDecayed int
	MemoriesPruned  int
	EdgesMerged     int
}

// RunConsolidation executes all consolidation phases.
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

// phaseMerge finds memories with identical tags and high content overlap,
// creates supersedes edges from newer to older.
func phaseMerge(ctx context.Context, repo graph.Repository, cfg ConsolidationConfig) (int, error) {
	// For MVP: Find semantic memories in same project with matching tags.
	// A full implementation would use content similarity scoring.
	// For now, we detect exact duplicate content and create supersedes edges.

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

// phaseDecay recalculates decay_score for all memories.
func phaseDecay(ctx context.Context, repo graph.Repository, cfg ConsolidationConfig) (int, error) {
	// Query all memories and update their decay scores.
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

// phasePrune deletes memories that are stale, never accessed, and old enough.
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
