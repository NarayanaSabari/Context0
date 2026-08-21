// Consolidate is a batch job that performs memory graph maintenance. It merges
// duplicate edges, applies time-based decay to memory weights, and prunes
// stale memories that have fallen below the relevance threshold.
//
// This command is designed to run as a periodic cron job or Kubernetes
// CronJob, separate from the main server process.
//
// # Environment Variables
//
// All KORA_* variables from the config package are supported (only
// KORA_DATABASE_URL is required). Additionally:
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

	"github.com/NarayanaSabari/Kora/internal/config"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/logging"
	"github.com/NarayanaSabari/Kora/internal/service"
)

func main() {
	cfg := config.Load()

	logging.Setup(logging.Options{
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
		Version: cfg.Version,
	})
	if err := cfg.Validate(); err != nil {
		fatal("invalid configuration", err)
	}

	slog.Info("consolidation job starting")

	ctx := context.Background()

	// Start with default consolidation settings, then apply any
	// environment variable overrides.
	consolCfg := service.DefaultConsolidationConfig()

	// An unparseable or out-of-range override is fatal, not ignored.
	//
	// These values gate DeleteMemory: StaleThreshold and PruneAgeDays decide
	// which memories are pruned. Silently falling back to the default meant an
	// operator who raised PruneAgeDays to protect data, and mistyped it, got
	// the default instead and lost exactly the memories they were protecting
	// -- while the job logged "consolidation complete" and exited 0.
	//
	// Refusing to start is the safe failure: the CronJob is marked Failed, the
	// maintenance is visibly skipped, and nothing is deleted on a
	// configuration nobody verified.
	if v := os.Getenv("CONSOLIDATION_DECAY_HALF_LIFE_DAYS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			fatal("CONSOLIDATION_DECAY_HALF_LIFE_DAYS is not a number", err,
				slog.String("value", v))
		}
		if f <= 0 {
			fatalMsg("CONSOLIDATION_DECAY_HALF_LIFE_DAYS must be positive",
				slog.String("value", v))
		}
		consolCfg.DecayHalfLifeDays = f
	}
	if v := os.Getenv("CONSOLIDATION_STALE_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			fatal("CONSOLIDATION_STALE_THRESHOLD is not a number", err,
				slog.String("value", v))
		}
		// Decay scores live in [0, 1]. A threshold above 1 prunes every
		// unaccessed memory past the age gate; a negative one prunes none,
		// which silently disables maintenance.
		if f < 0 || f > 1 {
			fatalMsg("CONSOLIDATION_STALE_THRESHOLD must be within [0, 1]",
				slog.String("value", v))
		}
		consolCfg.StaleThreshold = f
	}
	if v := os.Getenv("CONSOLIDATION_PRUNE_AGE_DAYS"); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			fatal("CONSOLIDATION_PRUNE_AGE_DAYS is not an integer", err,
				slog.String("value", v))
		}
		// Negative means every memory is old enough to prune, which turns a
		// maintenance job into a deletion job.
		if i < 0 {
			fatalMsg("CONSOLIDATION_PRUNE_AGE_DAYS must not be negative",
				slog.String("value", v))
		}
		consolCfg.PruneAgeDays = i
	}

	slog.Info("consolidation configuration",
		slog.Float64("decay_half_life_days", consolCfg.DecayHalfLifeDays),
		slog.Float64("stale_threshold", consolCfg.StaleThreshold),
		slog.Int("prune_age_days", consolCfg.PruneAgeDays))

	// Configuration is resolved before the database is touched.
	//
	// Validation used to run after connecting and after InitSchema, so a bad
	// value was only reported if the database happened to be reachable -- and
	// the operator saw a connection error instead of the typo that actually
	// stopped the job. Settings that decide what gets deleted should be
	// checked before anything else can fail.

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
//
// The offending value goes in attrs as a slog attribute rather than being
// concatenated into msg. That keeps msg a constant, which matters for two
// reasons: a log aggregator can group on it instead of seeing every bad value
// as a distinct message, and an environment variable containing a newline
// cannot forge a second log line by being spliced into the text.
func fatal(msg string, err error, attrs ...slog.Attr) {
	slog.LogAttrs(context.Background(), slog.LevelError, msg,
		append([]slog.Attr{slog.Any("error", err)}, attrs...)...)
	os.Exit(1)
}

// fatalMsg reports a configuration value that parsed but is out of range.
func fatalMsg(msg string, attrs ...slog.Attr) {
	slog.LogAttrs(context.Background(), slog.LevelError, msg, attrs...)
	os.Exit(1)
}
