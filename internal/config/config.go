// Package config loads and provides access to all Context0 engine configuration.
// Every setting is read from environment variables with sensible defaults,
// requiring zero configuration files for local development.
//
// # Environment Variables
//
// Server:
//
//	CONTEXT0_GRPC_PORT          gRPC listen port (default: 50051)
//	CONTEXT0_HTTP_PORT          HTTP/REST listen port (default: 8080)
//
// Database:
//
//	CONTEXT0_DATABASE_URL       PostgreSQL + AGE connection string
//	                            (default: postgres://context0:context0@localhost:5432/context0?sslmode=disable)
//
// Authentication:
//
//	CONTEXT0_API_KEYS           Comma-separated list of valid API keys.
//	                            When empty, authentication is disabled.
//	CONTEXT0_RATE_LIMIT_PER_MINUTE
//	                            Per-key requests per minute, enforced per replica
//	                            (default: 100)
//
// Embedding:
//
//	CONTEXT0_EMBEDDING_PROVIDER Embedding backend: "bag-of-words" | "ollama" | "openai" | "google" (default: bag-of-words)
//	CONTEXT0_EMBEDDING_MODEL    Model name, e.g. "nomic-embed-text", "text-embedding-3-small" (default: "")
//	CONTEXT0_EMBEDDING_API_KEY  API key for cloud embedding providers (OpenAI, Google) (default: "")
//	CONTEXT0_EMBEDDING_BASE_URL Base URL override for Ollama or any OpenAI-compatible endpoint (default: "")
//	CONTEXT0_EMBEDDING_DIM      Vector dimension; auto-detected from the provider when 0 (default: 0)
//
// Metadata:
//
//	CONTEXT0_VERSION            Reported engine version string (default: "0.1.0-dev")
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration for the Context0 engine. Each field maps
// to an environment variable documented in the package comment.
type Config struct {
	// GRPCPort is the TCP port the gRPC server binds to.
	// Env: CONTEXT0_GRPC_PORT (default: 50051)
	GRPCPort int

	// HTTPPort is the TCP port the HTTP/REST gateway and metrics endpoint bind to.
	// Env: CONTEXT0_HTTP_PORT (default: 8080)
	HTTPPort int

	// DatabaseURL is a PostgreSQL connection string pointing to a database
	// with the Apache AGE extension installed.
	// Env: CONTEXT0_DATABASE_URL
	DatabaseURL string

	// APIKeys is the list of accepted API keys. An empty list disables auth.
	// Env: CONTEXT0_API_KEYS (comma-separated)
	APIKeys []string

	// RateLimitPerMinute is the per-API-key request budget enforced by each
	// replica's token bucket.
	//
	// The limit is per pod, not cluster-wide: the buckets live in process
	// memory, so N replicas admit roughly N times this rate. Scale it down as
	// replicas go up, or move rate limiting to an ingress or service mesh if
	// you need a true global budget.
	// Env: CONTEXT0_RATE_LIMIT_PER_MINUTE (default: 100)
	RateLimitPerMinute int

	// EmbeddingProvider selects the embedding backend.
	// Accepted values: "bag-of-words", "ollama", "openai", "google".
	// Env: CONTEXT0_EMBEDDING_PROVIDER (default: "bag-of-words")
	EmbeddingProvider string

	// EmbeddingModel is the model name passed to the embedding provider.
	// Env: CONTEXT0_EMBEDDING_MODEL
	EmbeddingModel string

	// EmbeddingAPIKey is the API key for cloud embedding providers (OpenAI, Google).
	// Env: CONTEXT0_EMBEDDING_API_KEY
	EmbeddingAPIKey string

	// EmbeddingBaseURL overrides the default endpoint for the embedding provider.
	// Env: CONTEXT0_EMBEDDING_BASE_URL
	EmbeddingBaseURL string

	// EmbeddingDim is the vector dimension. When 0, the provider auto-detects it.
	// Env: CONTEXT0_EMBEDDING_DIM (default: 0)
	EmbeddingDim int

	// Version is the engine version string reported by the health endpoint.
	// Env: CONTEXT0_VERSION (default: "0.1.0-dev")
	Version string
}

// Load reads all configuration from environment variables. Missing or empty
// variables fall back to their documented defaults. This function never
// returns an error; invalid numeric values silently use the fallback.
func Load() Config {
	return Config{
		GRPCPort:    getEnvInt("CONTEXT0_GRPC_PORT", 50051),
		HTTPPort:    getEnvInt("CONTEXT0_HTTP_PORT", 8080),
		DatabaseURL: getEnv("CONTEXT0_DATABASE_URL", "postgres://context0:context0@localhost:5432/context0?sslmode=disable"),
		APIKeys:     splitEnv("CONTEXT0_API_KEYS", ","),

		RateLimitPerMinute: getEnvInt("CONTEXT0_RATE_LIMIT_PER_MINUTE", 100),

		EmbeddingProvider: getEnv("CONTEXT0_EMBEDDING_PROVIDER", "bag-of-words"),
		EmbeddingModel:    getEnv("CONTEXT0_EMBEDDING_MODEL", ""),
		EmbeddingAPIKey:   getEnv("CONTEXT0_EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:  getEnv("CONTEXT0_EMBEDDING_BASE_URL", ""),
		EmbeddingDim:      getEnvInt("CONTEXT0_EMBEDDING_DIM", 0),

		Version: getEnv("CONTEXT0_VERSION", "0.1.0-dev"),
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

// getEnvInt reads an integer environment variable, returning fallback when
// the variable is unset, empty, or not a valid integer.
func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
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
