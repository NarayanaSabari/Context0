package config

import (
	"os"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env vars that might be set.
	os.Unsetenv("CONTEXT0_GRPC_PORT")
	os.Unsetenv("CONTEXT0_HTTP_PORT")
	os.Unsetenv("CONTEXT0_DATABASE_URL")

	cfg := Load()

	if cfg.GRPCPort != 50051 {
		t.Errorf("GRPCPort = %d, want 50051", cfg.GRPCPort)
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("HTTPPort = %d, want 8080", cfg.HTTPPort)
	}
	if cfg.GRPCAddr() != ":50051" {
		t.Errorf("GRPCAddr() = %q, want ':50051'", cfg.GRPCAddr())
	}
	if cfg.HTTPAddr() != ":8080" {
		t.Errorf("HTTPAddr() = %q, want ':8080'", cfg.HTTPAddr())
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("CONTEXT0_GRPC_PORT", "9090")
	t.Setenv("CONTEXT0_HTTP_PORT", "9091")
	t.Setenv("CONTEXT0_DATABASE_URL", "postgres://test:test@db:5432/test")
	t.Setenv("CONTEXT0_API_KEYS", "key1,key2,key3")
	t.Setenv("CONTEXT0_VERSION", "1.2.3")

	cfg := Load()

	if cfg.GRPCPort != 9090 {
		t.Errorf("GRPCPort = %d, want 9090", cfg.GRPCPort)
	}
	if cfg.HTTPPort != 9091 {
		t.Errorf("HTTPPort = %d, want 9091", cfg.HTTPPort)
	}
	if cfg.DatabaseURL != "postgres://test:test@db:5432/test" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if len(cfg.APIKeys) != 3 {
		t.Errorf("APIKeys count = %d, want 3", len(cfg.APIKeys))
	}
	if cfg.Version != "1.2.3" {
		t.Errorf("Version = %q, want '1.2.3'", cfg.Version)
	}
}
