package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all configuration for the Context0 engine.
type Config struct {
	// Server
	GRPCPort int
	HTTPPort int

	// Database (PostgreSQL + AGE)
	DatabaseURL string

	// Auth
	APIKeys []string

	// Embedding
	EmbeddingProvider string // "bag-of-words" | "ollama" | "openai" | "google"
	EmbeddingModel    string // model name (e.g. "nomic-embed-text", "text-embedding-3-small")
	EmbeddingAPIKey   string // API key for cloud providers (OpenAI, Google)
	EmbeddingBaseURL  string // base URL override (Ollama endpoint, or custom OpenAI-compatible)
	EmbeddingDim      int    // vector dimension (auto-detected if 0)

	// LLM (for extraction)
	LLMProvider string // "rule-based" | "ollama" | "openai"
	LLMModel    string
	LLMAPIKey   string
	LLMBaseURL  string

	// Version
	Version string
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		GRPCPort:    getEnvInt("CONTEXT0_GRPC_PORT", 50051),
		HTTPPort:    getEnvInt("CONTEXT0_HTTP_PORT", 8080),
		DatabaseURL: getEnv("CONTEXT0_DATABASE_URL", "postgres://context0:context0@localhost:5432/context0?sslmode=disable"),
		APIKeys:     splitEnv("CONTEXT0_API_KEYS", ","),

		EmbeddingProvider: getEnv("CONTEXT0_EMBEDDING_PROVIDER", "bag-of-words"),
		EmbeddingModel:    getEnv("CONTEXT0_EMBEDDING_MODEL", ""),
		EmbeddingAPIKey:   getEnv("CONTEXT0_EMBEDDING_API_KEY", ""),
		EmbeddingBaseURL:  getEnv("CONTEXT0_EMBEDDING_BASE_URL", ""),
		EmbeddingDim:      getEnvInt("CONTEXT0_EMBEDDING_DIM", 0),

		LLMProvider: getEnv("CONTEXT0_LLM_PROVIDER", "rule-based"),
		LLMModel:    getEnv("CONTEXT0_LLM_MODEL", ""),
		LLMAPIKey:   getEnv("CONTEXT0_LLM_API_KEY", ""),
		LLMBaseURL:  getEnv("CONTEXT0_LLM_BASE_URL", ""),

		Version: getEnv("CONTEXT0_VERSION", "0.1.0-dev"),
	}
}

func (c Config) GRPCAddr() string {
	return fmt.Sprintf(":%d", c.GRPCPort)
}

func (c Config) HTTPAddr() string {
	return fmt.Sprintf(":%d", c.HTTPPort)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func splitEnv(key, sep string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var parts []string
	for _, s := range splitString(v, sep) {
		s = trimSpace(s)
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

func splitString(s, sep string) []string {
	if sep == "" {
		return []string{s}
	}
	var result []string
	for {
		i := indexOf(s, sep)
		if i < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:i])
		s = s[i+len(sep):]
	}
	return result
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
