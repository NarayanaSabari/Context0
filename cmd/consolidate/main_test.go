package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The consolidation job had no tests. It is the only thing that deletes
// memories, and its deletion criteria come from environment variables that
// were parsed with the error discarded: an unparseable value silently kept the
// default and the job logged "consolidation complete".
//
// The tests run the built binary, because the behaviour that matters is
// whether the process refuses to start and what exit status Kubernetes sees.

func consolidateBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "consolidate")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build consolidate: %v\n%s", err, out)
	}
	return bin
}

// runConsolidate executes the job with a database that cannot be reached, so
// nothing is mutated. Configuration is validated before the pool is used,
// which is the behaviour under test: a bad value must be rejected up front
// rather than after the job has started deleting.
func runConsolidate(t *testing.T, bin string, env ...string) (output string, code int) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"CONTEXT0_DATABASE_URL=postgres://nobody@127.0.0.1:1/nothing?sslmode=disable",
	)
	cmd.Env = append(cmd.Env, env...)

	out, err := cmd.CombinedOutput()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run consolidate: %v", err)
	}
	return string(out), code
}

// TestInvalidTuningIsRejected covers the silent fallback.
//
// StaleThreshold and PruneAgeDays gate DeleteMemory. Discarding the parse
// error meant an operator who raised PruneAgeDays to protect data, and
// mistyped it, got the default instead and lost exactly the memories they
// were protecting -- while the job reported success.
func TestInvalidTuningIsRejected(t *testing.T) {
	bin := consolidateBinary(t)

	cases := []struct {
		name string
		env  string
		want string
	}{
		{"prune age typo", "CONSOLIDATION_PRUNE_AGE_DAYS=3O", "PRUNE_AGE_DAYS"},
		{"prune age not a number", "CONSOLIDATION_PRUNE_AGE_DAYS=thirty", "PRUNE_AGE_DAYS"},
		{"threshold typo", "CONSOLIDATION_STALE_THRESHOLD=0.5.0", "STALE_THRESHOLD"},
		{"threshold not a number", "CONSOLIDATION_STALE_THRESHOLD=low", "STALE_THRESHOLD"},
		{"half life typo", "CONSOLIDATION_DECAY_HALF_LIFE_DAYS=7d", "DECAY_HALF_LIFE_DAYS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runConsolidate(t, bin, tc.env)
			if code == 0 {
				t.Errorf("the job started with %s; an unparseable value that "+
					"gates deletion must not fall back to the default", tc.env)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not name the offending variable %q: %s", tc.want, out)
			}
			// The rejected value has to appear, or an operator cannot see what
			// was wrong with what they set.
			value := tc.env[strings.Index(tc.env, "=")+1:]
			if !strings.Contains(out, value) {
				t.Errorf("output does not echo the rejected value %q: %s", value, out)
			}
		})
	}
}

// TestOutOfRangeTuningIsRejected: these values parse but are nonsensical, and
// each one turns maintenance into something more destructive.
func TestOutOfRangeTuningIsRejected(t *testing.T) {
	bin := consolidateBinary(t)

	cases := []struct {
		name string
		env  string
		why  string
	}{
		{
			name: "negative prune age",
			env:  "CONSOLIDATION_PRUNE_AGE_DAYS=-1",
			why:  "every memory is then old enough to prune",
		},
		{
			name: "threshold above one",
			env:  "CONSOLIDATION_STALE_THRESHOLD=5",
			why:  "decay scores live in [0,1], so every unaccessed memory past the age gate is pruned",
		},
		{
			name: "negative threshold",
			env:  "CONSOLIDATION_STALE_THRESHOLD=-1",
			why:  "nothing is ever pruned, so maintenance silently stops",
		},
		{
			name: "zero half life",
			env:  "CONSOLIDATION_DECAY_HALF_LIFE_DAYS=0",
			why:  "a zero half-life makes decay undefined",
		},
		{
			name: "negative half life",
			env:  "CONSOLIDATION_DECAY_HALF_LIFE_DAYS=-7",
			why:  "decay would run backwards",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runConsolidate(t, bin, tc.env)
			if code == 0 {
				t.Errorf("the job started with %s: %s", tc.env, tc.why)
			}
			// A non-zero exit is not enough on its own: the database is
			// deliberately unreachable, so the job would exit 1 anyway and
			// this check would pass while the range guard was removed.
			// Verified by deleting the guard -- the exit code alone still
			// said "pass". The message has to show the value was rejected.
			if !strings.Contains(out, "must ") {
				t.Errorf("%s was not rejected as out of range (%s); the run "+
					"failed for some other reason: %s", tc.env, tc.why, out)
			}
			// And it must have been rejected before anything touched the
			// database, or a bad setting is only caught when the database
			// happens to be up.
			if strings.Contains(out, "failed to connect to database") {
				t.Errorf("%s reached the database connection before being "+
					"validated: %s", tc.env, out)
			}
		})
	}
}

// TestValidTuningIsAccepted keeps the guards above from being satisfied by
// rejecting everything. These must get past configuration and fail later, at
// the unreachable database.
func TestValidTuningIsAccepted(t *testing.T) {
	bin := consolidateBinary(t)

	for _, env := range []string{
		"CONSOLIDATION_PRUNE_AGE_DAYS=30",
		"CONSOLIDATION_PRUNE_AGE_DAYS=0",
		"CONSOLIDATION_STALE_THRESHOLD=0.1",
		"CONSOLIDATION_STALE_THRESHOLD=0",
		"CONSOLIDATION_STALE_THRESHOLD=1",
		"CONSOLIDATION_DECAY_HALF_LIFE_DAYS=7",
		"CONSOLIDATION_DECAY_HALF_LIFE_DAYS=0.5",
	} {
		out, _ := runConsolidate(t, bin, env)
		if strings.Contains(out, "must be") || strings.Contains(out, "is not a") {
			t.Errorf("valid setting %s was rejected: %s", env, out)
		}
	}
}

// TestTuningIsLogged: the values actually in force must be visible, or a
// deployment cannot tell which configuration produced a given pruning run.
func TestTuningIsLogged(t *testing.T) {
	bin := consolidateBinary(t)

	out, _ := runConsolidate(t, bin, "CONSOLIDATION_PRUNE_AGE_DAYS=45")
	if !strings.Contains(out, "45") {
		t.Errorf("the configured prune age does not appear in the output; "+
			"a pruning run cannot be attributed to its settings: %s", out)
	}
	for _, field := range []string{"stale_threshold", "prune_age_days", "decay_half_life_days"} {
		if !strings.Contains(out, field) {
			t.Errorf("the effective %s is not logged: %s", field, out)
		}
	}
}

// TestUnreachableDatabaseExitsNonZero: the CronJob must be marked Failed when
// maintenance did not happen. A run that exits 0 having done nothing is how
// this job silently stopped working before.
func TestUnreachableDatabaseExitsNonZero(t *testing.T) {
	bin := consolidateBinary(t)

	out, code := runConsolidate(t, bin)
	if code == 0 {
		t.Errorf("the job exited 0 against an unreachable database: %s", out)
	}
}
