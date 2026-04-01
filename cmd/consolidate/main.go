package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/context0/context0/internal/config"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/service"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log.Println("context0 consolidation job starting...")

	cfg := config.Load()

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("failed to ping database: %v", err)
	}

	repo := graph.NewAGERepository(pool)
	defer repo.Close()

	if err := repo.InitSchema(ctx); err != nil {
		log.Fatalf("failed to init graph schema: %v", err)
	}

	consolCfg := service.DefaultConsolidationConfig()

	// Override from environment.
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
	if os.Getenv("CONSOLIDATION_DRY_RUN") == "true" {
		consolCfg.DryRun = true
	}

	result, err := service.RunConsolidation(ctx, repo, consolCfg)
	if err != nil {
		log.Fatalf("consolidation failed: %v", err)
	}

	log.Printf("consolidation complete: merged=%d, decayed=%d, pruned=%d",
		result.EdgesMerged, result.MemoriesDecayed, result.MemoriesPruned)
}
