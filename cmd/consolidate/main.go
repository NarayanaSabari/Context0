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
	"log/slog"
	"os"
	"strconv"

	"github.com/context0/context0/internal/config"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/logging"
	"github.com/context0/context0/internal/service"
)

func main() {
	cfg := config.Load()

	logging.Setup(logging.Options{
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
		Version: cfg.Version,
	})
	slog.Info("consolidation job starting")

	ctx := context.Background()

	// Connect to the database. The consolidation job only reads and writes
	// graph data; it does not need embedding support.
	pool, err := graph.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("failed to connect to database", err)
	}
	defer pool.Close()

	// dim=0 tells the repository to use the existing schema dimension.
	// Consolidation never creates new embeddings, so the actual value
	// does not matter.
	repo := graph.NewAGERepository(pool, 0)
	defer repo.Close()

	if err := repo.InitSchema(ctx); err != nil {
		fatal("failed to init graph schema", err)
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
	//
	// A degraded run -- one where most items failed -- returns an error, so the
	// process exits non-zero and the CronJob is marked Failed. Before this, a
	// run against a database refusing connections logged "consolidation
	// complete" and exited 0, so nothing anywhere indicated that the
	// maintenance had not happened.
	result, err := service.RunConsolidation(ctx, repo, consolCfg)
	if err != nil {
		slog.Error("consolidation failed",
			slog.Int("merged", result.EdgesMerged),
			slog.Int("decayed", result.MemoriesDecayed),
			slog.Int("pruned", result.MemoriesPruned),
			slog.Int("failed", result.Failures()),
			slog.Any("error", err))
		os.Exit(1)
	}

	slog.Info("consolidation complete",
		slog.Int("merged", result.EdgesMerged),
		slog.Int("decayed", result.MemoriesDecayed),
		slog.Int("pruned", result.MemoriesPruned),
		slog.Int("failed", result.Failures()))
}

// fatal logs a structured error and exits non-zero. See the equivalent in
// cmd/server for why slog has no Fatal of its own.
func fatal(msg string, err error) {
	slog.Error(msg, slog.Any("error", err))
	os.Exit(1)
}
