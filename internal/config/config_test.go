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

// TestSplitEnv_DiscardsEmptySegments guards the parsing of CONTEXT0_API_KEYS.
//
// A trailing comma, a doubled comma, or a stray space is the normal result of
// editing a key list by hand or generating one in a shell loop. Without the
// filtering, "key1,,key2" yields an empty string in the middle of the
// allowlist.
//
// Found by mutation testing: the package reported 100% statement coverage while
// removing this filter changed nothing any test noticed. Coverage says a line
// ran, not that anything was asserted about it.
func TestSplitEnv_DiscardsEmptySegments(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  []string
	}{
		{"trailing comma", "a,b,", []string{"a", "b"}},
		{"doubled comma", "a,,b", []string{"a", "b"}},
		{"leading comma", ",a,b", []string{"a", "b"}},
		{"whitespace around", " a , b ", []string{"a", "b"}},
		{"only separators", ",,,", nil},
		{"only whitespace", "  ,  ", nil},
		{"empty", "", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT0_TEST_SPLIT", tc.value)
			got := splitEnv("CONTEXT0_TEST_SPLIT", ",")

			if len(got) != len(tc.want) {
				t.Fatalf("splitEnv(%q) = %#v, want %#v", tc.value, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("part %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
			// The property that matters: an empty string must never reach the
			// caller, because for API keys it would become an entry in the
			// allowlist.
			for _, part := range got {
				if part == "" {
					t.Errorf("splitEnv(%q) produced an empty segment: %#v", tc.value, got)
				}
			}
		})
	}
}

// TestSplitEnv_UnsetReturnsNil: an unset variable must be indistinguishable
// from an empty one, because the auth layer treats "no keys configured" as a
// deliberate state rather than an error.
func TestSplitEnv_UnsetReturnsNil(t *testing.T) {
	if got := splitEnv("CONTEXT0_DEFINITELY_UNSET_VAR", ","); got != nil {
		t.Errorf("splitEnv on an unset variable = %#v, want nil", got)
	}
}
