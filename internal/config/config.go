// Package config loads and provides access to all Kora engine configuration.
// Every setting is read from environment variables with sensible defaults,
// requiring zero configuration files for local development.
//
// # Environment Variables
//
// Server:
//
//	KORA_GRPC_PORT          gRPC listen port (default: 50051)
//	KORA_HTTP_PORT          HTTP/REST listen port (default: 8080)
//
// Database:
//
//	KORA_DATABASE_URL       PostgreSQL + AGE connection string
//	                            (default: postgres://kora:kora@localhost:5432/kora?sslmode=disable)
//
// Authentication:
//
//	KORA_API_KEYS           Comma-separated list of valid API keys.
//	                            When empty, authentication is disabled.
//	KORA_RATE_LIMIT_PER_MINUTE
//	                            Per-key requests per minute, enforced per replica
//	                            (default: 100)
//
// Embedding:
//
//	KORA_EMBEDDING_PROVIDER Embedding backend: "bag-of-words" | "ollama" | "openai" | "google" (default: bag-of-words)
//	KORA_EMBEDDING_MODEL    Model name, e.g. "nomic-embed-text", "text-embedding-3-small" (default: "")
//	KORA_EMBEDDING_API_KEY  API key for cloud embedding providers (OpenAI, Google) (default: "")
//	KORA_EMBEDDING_BASE_URL Base URL override for Ollama or any OpenAI-compatible endpoint (default: "")
//	KORA_EMBEDDING_DIM      Vector dimension; auto-detected from the provider when 0 (default: 0)
//
// Metadata:
//
//	KORA_VERSION            Reported engine version string (default: "0.1.0-dev")
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration for the Kora engine. Each field maps
// to an environment variable documented in the package comment.
type Config struct {
	// GRPCPort is the TCP port the gRPC server binds to.
	// Env: KORA_GRPC_PORT (default: 50051)
	GRPCPort int

	// HTTPPort is the TCP port the HTTP/REST gateway and metrics endpoint bind to.
	// Env: KORA_HTTP_PORT (default: 8080)
	HTTPPort int

	// DatabaseURL is a PostgreSQL connection string pointing to a database
	// with the Apache AGE extension installed.
	// Env: KORA_DATABASE_URL
	DatabaseURL string

	// APIKeys is the list of accepted API keys. An empty list disables auth.
	// Env: KORA_API_KEYS (comma-separated)
	APIKeys []string

	// RateLimitPerMinute is the per-API-key request budget enforced by each
	// replica's token bucket.
	//
	// The limit is per pod, not cluster-wide: the buckets live in process
	// memory, so N replicas admit roughly N times this rate. Scale it down as
	// replicas go up, or move rate limiting to an ingress or service mesh if
	// you need a true global budget.
	// Env: KORA_RATE_LIMIT_PER_MINUTE (default: 6000)
	RateLimitPerMinute int

	// EmbeddingProvider selects the embedding backend.
	// Accepted values: "bag-of-words", "ollama", "openai", "google".
	// Env: KORA_EMBEDDING_PROVIDER (default: "bag-of-words")
	EmbeddingProvider string

	// EmbeddingModel is the model name passed to the embedding provider.
	// Env: KORA_EMBEDDING_MODEL
	EmbeddingModel string

	// EmbeddingAPIKey is the API key for cloud embedding providers (OpenAI, Google).
	// Env: KORA_EMBEDDING_API_KEY
	EmbeddingAPIKey string

	// EmbeddingBaseURL overrides the default endpoint for the embedding provider.
	// Env: KORA_EMBEDDING_BASE_URL
	EmbeddingBaseURL string

	// EmbeddingDim is the vector dimension. When 0, the provider auto-detects it.
	// Env: KORA_EMBEDDING_DIM (default: 0)
	EmbeddingDim int

	// LogLevel is one of debug, info, warn, error.
	// Env: KORA_LOG_LEVEL (default: info)
	LogLevel string

	// LogFormat is "json" or "text". JSON is the default because the usual
	// destination is a log aggregator; text is for local development.
	// Env: KORA_LOG_FORMAT (default: json)
	LogFormat string

	// Version is the engine version string reported by the health endpoint.
	// Env: KORA_VERSION (default: "0.1.0-dev")
	Version string
}

// Load reads all configuration from environment variables. Missing or empty
// variables fall back to their documented defaults. This function never
// returns an error; invalid numeric values silently use the fallback.
func Load() Config {
	checkRenamedEnv()

	return Config{
		GRPCPort:    getEnvInt("KORA_GRPC_PORT", 50051),
		HTTPPort:    getEnvInt("KORA_HTTP_PORT", 8080),
		DatabaseURL: getEnv("KORA_DATABASE_URL", "postgres://kora:kora@localhost:5432/kora?sslmode=disable"),
		APIKeys:     splitEnv("KORA_API_KEYS", ","),

		RateLimitPerMinute: getEnvInt("KORA_RATE_LIMIT_PER_MINUTE", 6000),

		EmbeddingProvider: getEnv("KORA_EMBEDDING_PROVIDER", "bag-of-words"),
		EmbeddingModel:    getEnv("KORA_EMBEDDING_MODEL", ""),
		EmbeddingAPIKey:   getEnv("KORA_EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:  getEnv("KORA_EMBEDDING_BASE_URL", ""),
		EmbeddingDim:      getEnvInt("KORA_EMBEDDING_DIM", 0),

		LogLevel:  getEnv("KORA_LOG_LEVEL", "info"),
		LogFormat: getEnv("KORA_LOG_FORMAT", "json"),

		Version: getEnv("KORA_VERSION", "0.1.0-dev"),
	}
}

// renamedEnvPrefix is the CONTEXT0_ prefix these variables used before the
// project was renamed to Kora.
const renamedEnvPrefix = "CONTEXT0_"

// maxEmbeddingDim is the largest vector dimension accepted from
// KORA_EMBEDDING_DIM. It mirrors the cap in internal/embedding, which needs
// the dimension to fit safely in a uint32; see the note in Validate.
const maxEmbeddingDim = 65536

// checkRenamedEnv records any CONTEXT0_* variable still present in the
// environment as a configuration problem.
//
// Every setting silently falls back to a default when its variable is unset,
// which makes this rename dangerous in a specific way: a deployment that still
// sets CONTEXT0_API_KEYS would not fail, it would start with KORA_API_KEYS
// empty -- and an empty key list disables authentication entirely. The engine
// would come up reporting auth_enabled=false and serve every stored memory to
// anyone who asked. CONTEXT0_DATABASE_URL is the same shape of problem: the
// default DSN points at localhost, so the server would try a different
// database rather than report the misconfiguration.
//
// Old names are rejected rather than honoured as aliases. Silently accepting
// them would leave two supported spellings indefinitely, and the failure being
// prevented here is precisely the one a silent fallback creates.
func checkRenamedEnv() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, renamedEnvPrefix) {
			continue
		}
		envProblems = append(envProblems, fmt.Sprintf(
			"%s is set but no longer read: the project was renamed to Kora. "+
				"Use %s instead", name,
			"KORA_"+strings.TrimPrefix(name, renamedEnvPrefix)))
	}
}

// GRPCAddr returns the gRPC listen address in ":port" format.
func (c Config) GRPCAddr() string {
	return fmt.Sprintf(":%d", c.GRPCPort)
}

// HTTPAddr returns the HTTP listen address in ":port" format.
func (c Config) HTTPAddr() string {
	return fmt.Sprintf(":%d", c.HTTPPort)
}

// getEnv reads an environment variable, returning fallback when the variable
// is unset or empty.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envProblems collects configuration values that were set but unusable.
//
// Load has no error return and is called before the logger exists, so problems
// are accumulated here and reported by Validate once the caller can act on
// them. A package-level slice is safe because Load runs once, at startup,
// before any goroutine is started.
var envProblems []string

// getEnvInt reads an integer environment variable, returning fallback when the
// variable is unset or empty.
//
// A value that is set but unparseable is recorded as a problem rather than
// silently discarded. Falling back to the default there means an operator who
// typed KORA_RATE_LIMIT_PER_MINUTE=6OOO -- letter O -- gets the default
// limit with nothing anywhere saying their setting was ignored. The same
// pattern in the consolidation job silently deleted memories on default
// thresholds.
func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		envProblems = append(envProblems,
			fmt.Sprintf("%s=%q is not an integer", key, v))
		return fallback
	}
	return i
}

// Validate reports configuration that was supplied but cannot be used.
//
// Separate from Load because Load runs before logging is configured: the
// caller decides how to report, and whether to continue. Returning an error
// rather than exiting keeps the decision at the composition root.
func (c Config) Validate() error {
	problems := append([]string(nil), envProblems...)

	// Ports are checked here rather than left to net.Listen, which reports
	// "invalid port" only for the listener that failed -- after the other one
	// is already accepting traffic.
	for _, p := range []struct {
		name  string
		value int
	}{
		{"KORA_GRPC_PORT", c.GRPCPort},
		{"KORA_HTTP_PORT", c.HTTPPort},
	} {
		if p.value < 1 || p.value > 65535 {
			problems = append(problems,
				fmt.Sprintf("%s=%d is outside the valid port range 1-65535", p.name, p.value))
		}
	}
	if c.GRPCPort == c.HTTPPort {
		problems = append(problems,
			fmt.Sprintf("KORA_GRPC_PORT and KORA_HTTP_PORT are both %d; "+
				"one listener would fail to bind", c.GRPCPort))
	}

	// A non-positive rate limit is a token bucket that can never refill, so
	// every authenticated request is rejected. That looks like an outage, not
	// a configuration mistake.
	if c.RateLimitPerMinute <= 0 {
		problems = append(problems,
			fmt.Sprintf("KORA_RATE_LIMIT_PER_MINUTE=%d must be positive; "+
				"a non-positive limit rejects every request", c.RateLimitPerMinute))
	}

	// A negative dimension would be handed to the pgvector column definition.
	//
	// The upper bound matters for a different reason: the bag-of-words
	// embedder hashes into the vector with `% uint32(dim)`, and a dim that is
	// an exact multiple of 2^32 converts to uint32(0), panicking with a
	// divide-by-zero on the first text embedded. Checking only for negatives
	// let KORA_EMBEDDING_DIM=4294967296 start cleanly and crash on first use.
	//
	// 65536 is far above any real model - the largest embeddings in common use
	// are 3072 - so nothing legitimate is refused here.
	if c.EmbeddingDim < 0 {
		problems = append(problems,
			fmt.Sprintf("KORA_EMBEDDING_DIM=%d must not be negative", c.EmbeddingDim))
	} else if c.EmbeddingDim > maxEmbeddingDim {
		problems = append(problems,
			fmt.Sprintf("KORA_EMBEDDING_DIM=%d exceeds the maximum of %d",
				c.EmbeddingDim, maxEmbeddingDim))
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
}

// splitEnv reads an environment variable and splits it by sep, trimming
// whitespace from each part and discarding empty segments. Returns nil
// when the variable is unset or empty.
func splitEnv(key, sep string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var parts []string
	for _, s := range strings.Split(v, sep) {
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}
