package config

import (
	"fmt"
	"os"
	"strings"
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

// TestValidateRejectsUnparseableIntegers covers the silent fallback in
// getEnvInt.
//
// A value that was set but could not be parsed used to be discarded, leaving
// the default in place with nothing anywhere saying the setting was ignored.
// An operator who typed CONTEXT0_RATE_LIMIT_PER_MINUTE=6OOO -- letter O --
// got the default limit and no way to tell. The same pattern in the
// consolidation job silently deleted memories on default thresholds.
func TestValidateRejectsUnparseableIntegers(t *testing.T) {
	cases := []struct {
		env   string
		value string
	}{
		{"CONTEXT0_RATE_LIMIT_PER_MINUTE", "6OOO"},
		{"CONTEXT0_GRPC_PORT", "50051x"},
		{"CONTEXT0_HTTP_PORT", "eighty-eighty"},
		{"CONTEXT0_EMBEDDING_DIM", "384.0"},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			envProblems = nil
			t.Cleanup(func() { envProblems = nil })

			err := Load().Validate()
			if err == nil {
				t.Fatalf("%s=%q was accepted; an unparseable value must not "+
					"silently leave the default in place", tc.env, tc.value)
			}
			if !strings.Contains(err.Error(), tc.env) {
				t.Errorf("the error does not name %s: %v", tc.env, err)
			}
			// The rejected value has to appear, or an operator cannot see what
			// was wrong with what they set.
			if !strings.Contains(err.Error(), tc.value) {
				t.Errorf("the error does not echo the rejected value %q: %v", tc.value, err)
			}
		})
	}
}

// TestValidateRejectsOutOfRangeValues: these parse but cannot work, and each
// one fails in a way that looks like something other than a config mistake.
func TestValidateRejectsOutOfRangeValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		why  string
	}{
		{
			name: "port above the valid range",
			env:  map[string]string{"CONTEXT0_HTTP_PORT": "80800"},
			why:  "net.Listen reports this only for the listener that failed, after the other is already serving",
		},
		{
			name: "port zero",
			env:  map[string]string{"CONTEXT0_GRPC_PORT": "0"},
			why:  "port 0 binds an arbitrary free port, so nothing can reach the service",
		},
		{
			name: "negative port",
			env:  map[string]string{"CONTEXT0_GRPC_PORT": "-1"},
			why:  "cannot bind",
		},
		{
			name: "both listeners on one port",
			env:  map[string]string{"CONTEXT0_GRPC_PORT": "9000", "CONTEXT0_HTTP_PORT": "9000"},
			why:  "one of the two listeners would fail to bind",
		},
		{
			name: "zero rate limit",
			env:  map[string]string{"CONTEXT0_RATE_LIMIT_PER_MINUTE": "0"},
			why:  "a bucket that never refills rejects every request, which looks like an outage",
		},
		{
			name: "negative rate limit",
			env:  map[string]string{"CONTEXT0_RATE_LIMIT_PER_MINUTE": "-100"},
			why:  "same, and the negative is not visible anywhere at runtime",
		},
		{
			name: "negative embedding dimension",
			env:  map[string]string{"CONTEXT0_EMBEDDING_DIM": "-384"},
			why:  "this width is handed to the pgvector column definition",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			envProblems = nil
			t.Cleanup(func() { envProblems = nil })

			if err := Load().Validate(); err == nil {
				t.Errorf("configuration %v was accepted: %s", tc.env, tc.why)
			}
		})
	}
}

// TestValidateAcceptsWorkingConfiguration keeps the guards above from being
// satisfied by rejecting everything.
func TestValidateAcceptsWorkingConfiguration(t *testing.T) {
	envProblems = nil
	t.Cleanup(func() { envProblems = nil })

	// Defaults alone must be valid, or the chart's own install fails.
	if err := Load().Validate(); err != nil {
		t.Errorf("the default configuration is invalid: %v", err)
	}

	for _, env := range []map[string]string{
		{"CONTEXT0_RATE_LIMIT_PER_MINUTE": "1"},
		{"CONTEXT0_RATE_LIMIT_PER_MINUTE": "100000"},
		{"CONTEXT0_GRPC_PORT": "1", "CONTEXT0_HTTP_PORT": "65535"},
		{"CONTEXT0_EMBEDDING_DIM": "0"}, // 0 means auto-detect
		{"CONTEXT0_EMBEDDING_DIM": "1536"},
	} {
		t.Run(fmt.Sprint(env), func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			envProblems = nil
			if err := Load().Validate(); err != nil {
				t.Errorf("valid configuration %v was rejected: %v", env, err)
			}
		})
	}
}

// TestValidateReportsEveryProblemAtOnce: fixing configuration one error per
// restart is slow when each restart is a rollout.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("CONTEXT0_RATE_LIMIT_PER_MINUTE", "notanumber")
	t.Setenv("CONTEXT0_HTTP_PORT", "99999")
	envProblems = nil
	t.Cleanup(func() { envProblems = nil })

	err := Load().Validate()
	if err == nil {
		t.Fatal("two invalid settings were accepted")
	}
	for _, want := range []string{"CONTEXT0_RATE_LIMIT_PER_MINUTE", "CONTEXT0_HTTP_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error reports only some problems, missing %s: %v", want, err)
		}
	}
}
