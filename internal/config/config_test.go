package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env vars that might be set.
	os.Unsetenv("KORA_GRPC_PORT")
	os.Unsetenv("KORA_HTTP_PORT")
	os.Unsetenv("KORA_DATABASE_URL")

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
	t.Setenv("KORA_GRPC_PORT", "9090")
	t.Setenv("KORA_HTTP_PORT", "9091")
	t.Setenv("KORA_DATABASE_URL", "postgres://test:test@db:5432/test")
	t.Setenv("KORA_API_KEYS", "key1,key2,key3")
	t.Setenv("KORA_VERSION", "1.2.3")

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

// TestSplitEnv_DiscardsEmptySegments guards the parsing of KORA_API_KEYS.
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
			t.Setenv("KORA_TEST_SPLIT", tc.value)
			got := splitEnv("KORA_TEST_SPLIT", ",")

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
	if got := splitEnv("KORA_DEFINITELY_UNSET_VAR", ","); got != nil {
		t.Errorf("splitEnv on an unset variable = %#v, want nil", got)
	}
}

// TestValidateRejectsUnparseableIntegers covers the silent fallback in
// getEnvInt.
//
// A value that was set but could not be parsed used to be discarded, leaving
// the default in place with nothing anywhere saying the setting was ignored.
// An operator who typed KORA_RATE_LIMIT_PER_MINUTE=6OOO -- letter O --
// got the default limit and no way to tell. The same pattern in the
// consolidation job silently deleted memories on default thresholds.
func TestValidateRejectsUnparseableIntegers(t *testing.T) {
	cases := []struct {
		env   string
		value string
	}{
		{"KORA_RATE_LIMIT_PER_MINUTE", "6OOO"},
		{"KORA_GRPC_PORT", "50051x"},
		{"KORA_HTTP_PORT", "eighty-eighty"},
		{"KORA_EMBEDDING_DIM", "384.0"},
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
			env:  map[string]string{"KORA_HTTP_PORT": "80800"},
			why:  "net.Listen reports this only for the listener that failed, after the other is already serving",
		},
		{
			name: "port zero",
			env:  map[string]string{"KORA_GRPC_PORT": "0"},
			why:  "port 0 binds an arbitrary free port, so nothing can reach the service",
		},
		{
			name: "negative port",
			env:  map[string]string{"KORA_GRPC_PORT": "-1"},
			why:  "cannot bind",
		},
		{
			name: "both listeners on one port",
			env:  map[string]string{"KORA_GRPC_PORT": "9000", "KORA_HTTP_PORT": "9000"},
			why:  "one of the two listeners would fail to bind",
		},
		{
			name: "graph signals set to neither on nor off",
			env:  map[string]string{"KORA_GRAPH_SIGNALS": "of"},
			why: "a typo silently running the full engine would invalidate the ablation " +
				"measurement in the direction that flatters the graph",
		},
		{
			name: "zero rate limit",
			env:  map[string]string{"KORA_RATE_LIMIT_PER_MINUTE": "0"},
			why:  "a bucket that never refills rejects every request, which looks like an outage",
		},
		{
			name: "negative rate limit",
			env:  map[string]string{"KORA_RATE_LIMIT_PER_MINUTE": "-100"},
			why:  "same, and the negative is not visible anywhere at runtime",
		},
		{
			name: "negative embedding dimension",
			env:  map[string]string{"KORA_EMBEDDING_DIM": "-384"},
			why:  "this width is handed to the pgvector column definition",
		},
		{
			name: "embedding dimension that wraps uint32",
			env:  map[string]string{"KORA_EMBEDDING_DIM": "4294967296"},
			why: "2^32 converts to uint32(0) in the bag-of-words hash. Postgres " +
				"rejects the column width first, so the server never reached " +
				"the panic, but it died with SQLSTATE 22003 from the database " +
				"instead of a message naming the variable",
		},
		{
			name: "absurd embedding dimension",
			env:  map[string]string{"KORA_EMBEDDING_DIM": "100000000"},
			why:  "allocates a vector per embed that no real model would need",
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
		{"KORA_RATE_LIMIT_PER_MINUTE": "1"},
		{"KORA_RATE_LIMIT_PER_MINUTE": "100000"},
		{"KORA_GRPC_PORT": "1", "KORA_HTTP_PORT": "65535"},
		{"KORA_EMBEDDING_DIM": "0"}, // 0 means auto-detect
		{"KORA_EMBEDDING_DIM": "1536"},
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
	t.Setenv("KORA_RATE_LIMIT_PER_MINUTE", "notanumber")
	t.Setenv("KORA_HTTP_PORT", "99999")
	envProblems = nil
	t.Cleanup(func() { envProblems = nil })

	err := Load().Validate()
	if err == nil {
		t.Fatal("two invalid settings were accepted")
	}
	for _, want := range []string{"KORA_RATE_LIMIT_PER_MINUTE", "KORA_HTTP_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error reports only some problems, missing %s: %v", want, err)
		}
	}
}

// TestRenamedEnvVarsAreRejected covers the failure the Context0 -> Kora rename
// created: every setting falls back to a default when its variable is unset,
// so a deployment left on the old names does not fail, it comes up
// misconfigured. The worst case is authentication. An empty key list disables
// auth, so a pod still passing CONTEXT0_API_KEYS would start with
// auth_enabled=false and serve every stored memory unauthenticated.
func TestRenamedEnvVarsAreRejected(t *testing.T) {
	for _, old := range []string{
		"CONTEXT0_API_KEYS",
		"CONTEXT0_DATABASE_URL",
		"CONTEXT0_GRPC_PORT",
		"CONTEXT0_EMBEDDING_PROVIDER",
	} {
		t.Run(old, func(t *testing.T) {
			t.Setenv(old, "some-value")
			envProblems = nil
			t.Cleanup(func() { envProblems = nil })

			err := Load().Validate()
			if err == nil {
				t.Fatalf("%s was set and startup was allowed to continue; "+
					"the setting is silently ignored", old)
			}
			// The message has to name the replacement, or the operator is
			// told something is wrong without being told what to set.
			want := "KORA_" + strings.TrimPrefix(old, "CONTEXT0_")
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not name the replacement %s: %v", want, err)
			}
		})
	}
}

// TestRenamedEnvCheckIgnoresUnrelatedVars keeps the check above from being
// satisfied by rejecting every environment it sees.
func TestRenamedEnvCheckIgnoresUnrelatedVars(t *testing.T) {
	t.Setenv("CONTEXTUAL_SOMETHING", "x") // shares a prefix up to "CONTEXT"
	t.Setenv("KORA_API_KEYS", "ctx0_aaaa_bbbb")
	envProblems = nil
	t.Cleanup(func() { envProblems = nil })

	if err := Load().Validate(); err != nil {
		t.Errorf("an unrelated variable was treated as a renamed one: %v", err)
	}
}

// TestDefaultVersionIsLinkerStampable guards the release build's version
// stamping.
//
// The release workflow builds every binary with
// -X ...internal/config.DefaultVersion=$VERSION. The Go linker's -X flag is
// silent when it cannot apply: against a missing symbol, a constant, or a
// non-string it does nothing and the build still succeeds. The workflow had
// been passing that flag to a symbol that was never declared, so released
// binaries reported the fallback version while the release was tagged
// something else, and nothing failed to say so.
//
// This asserts the two properties -X needs. It cannot execute the linker, so
// the real end-to-end check is in the release workflow itself; what this
// prevents is someone turning DefaultVersion into a const or inlining the
// literal back into Load(), which would re-break stamping invisibly.
func TestDefaultVersionIsLinkerStampable(t *testing.T) {
	// The compile-time half of the check, and it asserts more than it appears
	// to. reflect.TypeOf on a pointer to DefaultVersion does not compile
	// against a const, and comparing the element kind catches the type
	// changing, so turning DefaultVersion into either fails here rather than
	// silently disabling -X at release time.
	if k := reflect.TypeOf(&DefaultVersion).Elem().Kind(); k != reflect.String {
		t.Errorf("DefaultVersion is %s, want string; -X only stamps string vars", k)
	}

	if DefaultVersion == "" {
		t.Error("DefaultVersion is empty; an unstamped build would report no version")
	}

	// Load must read DefaultVersion rather than a literal, or stamping the
	// variable would change nothing that reaches the health endpoint.
	original := DefaultVersion
	t.Cleanup(func() { DefaultVersion = original })

	DefaultVersion = "9.9.9-test"
	envProblems = nil
	t.Cleanup(func() { envProblems = nil })

	if got := Load().Version; got != "9.9.9-test" {
		t.Errorf("Load().Version = %q, want the stamped value; "+
			"Load is not reading DefaultVersion", got)
	}

	// The environment variable still wins, so an operator can override a
	// stamped build without rebuilding it.
	t.Setenv("KORA_VERSION", "from-env")
	if got := Load().Version; got != "from-env" {
		t.Errorf("Load().Version = %q, want KORA_VERSION to take precedence", got)
	}
}
