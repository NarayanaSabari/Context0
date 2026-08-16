// Consolidate is a batch job that performs memory graph maintenance. It merges
// duplicate edges, applies time-based decay to memory weights, and prunes
// stale memories that have fallen below the relevance threshold.
//
// This command is designed to run as a periodic cron job or Kubernetes
// CronJob, separate from the main server process.
//
// # Environment Variables
//
// All CONTEXT0_* variables from the config package are supported (only
// CONTEXT0_DATABASE_URL is required). Additionally:
//
//	CONSOLIDATION_DECAY_HALF_LIFE_DAYS  Half-life for exponential decay (float, days)
//	CONSOLIDATION_STALE_THRESHOLD       Weight threshold below which memories are pruned (float)
//	CONSOLIDATION_PRUNE_AGE_DAYS        Minimum age in days before a memory can be pruned (int)
package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/context0/context0/internal/config"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/service"
)

func main() {
	log.Println("context0 consolidation job starting...")

	cfg := config.Load()

	ctx := context.Background()

	// Connect to the database. The consolidation job only reads and writes
	// graph data; it does not need embedding support.
	pool, err := graph.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	// dim=0 tells the repository to use the existing schema dimension.
	// Consolidation never creates new embeddings, so the actual value
	// does not matter.
	repo := graph.NewAGERepository(pool, 0)
	defer repo.Close()

	if err := repo.InitSchema(ctx); err != nil {
		log.Fatalf("failed to init graph schema: %v", err)
	}

	// Start with default consolidation settings, then apply any
	// environment variable overrides.
	consolCfg := service.DefaultConsolidationConfig()

	if v := os.Getenv("CONSOLIDATION_DECAY_HALF_LIFE_DAYS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			consolCfg.DecayHalfLifeDays = f
		}
	}
	if v := os.Getenv("CONSOLIDATION_STALE_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			consolCfg.StaleThreshold = f
		}
	}
	if v := os.Getenv("CONSOLIDATION_PRUNE_AGE_DAYS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			consolCfg.PruneAgeDays = i
		}
	}

	// Execute the consolidation pipeline: merge, decay, prune.
	result, err := service.RunConsolidation(ctx, repo, consolCfg)
	if err != nil {
		log.Fatalf("consolidation failed: %v", err)
	}

	log.Printf("consolidation complete: merged=%d, decayed=%d, pruned=%d",
		result.EdgesMerged, result.MemoriesDecayed, result.MemoriesPruned)
}
