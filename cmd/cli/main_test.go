package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The CLI had no tests at all. It is a published interface -- a hand-maintained
// argument parser in front of the same API the SDK wraps -- and the failures
// found here were all of the same shape: a wrong input produced a confident,
// successful-looking answer instead of an error.
//
// These tests build the binary and run it as a subprocess, because the
// behaviour under test includes the exit code and what lands on stderr. A test
// calling the functions directly cannot observe either: fatalf calls os.Exit.

// cliBinary builds the CLI once per test binary.
func cliBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kora")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cli: %v\n%s", err, out)
	}
	return bin
}

// run executes the CLI with an endpoint that refuses connections, so anything
// reaching the network fails loudly rather than depending on a live server.
// Argument validation happens before any dialling, which is what these cover.
func run(t *testing.T, bin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"KORA_ENDPOINT=127.0.0.1:1", // nothing listens on port 1
		"KORA_PROJECT=cli-test",
	)
	cmd.Env = append(cmd.Env, env...)

	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()

	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cli: %v", err)
	}
	return out.String(), errBuf.String(), code
}

// TestUnknownMemoryTypeIsRejected: a typo in --type used to fall through to
// SEMANTIC. The write succeeded, the CLI printed success, and the memory was
// filed under the wrong type -- invisible until a later type-filtered query
// failed to return it. Guessing is worse than refusing, because the caller
// never learns they were guessed at.
func TestUnknownMemoryTypeIsRejected(t *testing.T) {
	bin := cliBinary(t)

	for _, bad := range []string{"bogus", "semanticc", "EPISODIK", "fact"} {
		_, stderr, code := run(t, bin, nil, "store", "some content", "--type="+bad)
		if code == 0 {
			t.Errorf("store --type=%q exited 0; an unrecognised type must not be "+
				"silently stored as semantic", bad)
		}
		if !strings.Contains(stderr, "unknown memory type") {
			t.Errorf("store --type=%q stderr = %q, want it to name the problem", bad, stderr)
		}
		// The message has to say what is accepted, or the user is left guessing.
		for _, valid := range []string{"semantic", "episodic", "procedural"} {
			if !strings.Contains(stderr, valid) {
				t.Errorf("the error for --type=%q does not mention %q as a valid option: %q",
					bad, valid, stderr)
			}
		}
	}
}

// TestValidMemoryTypesAreAccepted keeps the guard above from being satisfied by
// rejecting everything. These must get past parsing and fail at the network
// instead.
func TestValidMemoryTypesAreAccepted(t *testing.T) {
	bin := cliBinary(t)

	for _, valid := range []string{"semantic", "episodic", "procedural", "SEMANTIC", "Episodic"} {
		_, stderr, _ := run(t, bin, nil, "store", "some content", "--type="+valid)
		if strings.Contains(stderr, "unknown memory type") {
			t.Errorf("store --type=%q was rejected: %q", valid, stderr)
		}
	}
}

// TestUnknownRelationshipTypeIsRejected: the same silent default applied to
// edges, and it matters more there. Supersedes edges are followed when
// resolving which fact is current, so a mistyped "supersedes" that became
// "relates_to" leaves the superseded fact live alongside its replacement.
func TestUnknownRelationshipTypeIsRejected(t *testing.T) {
	bin := cliBinary(t)

	for _, bad := range []string{"bogus", "supersede", "relates-to", "causedby"} {
		_, stderr, code := run(t, bin, nil, "connect", "id-a", "id-b", bad)
		if code == 0 {
			t.Errorf("connect with relationship %q exited 0; an unrecognised "+
				"relationship must not silently become relates_to", bad)
		}
		if !strings.Contains(stderr, "unknown relationship type") {
			t.Errorf("connect %q stderr = %q, want it to name the problem", bad, stderr)
		}
		for _, valid := range []string{"relates_to", "supersedes", "caused_by"} {
			if !strings.Contains(stderr, valid) {
				t.Errorf("the error for %q does not mention %q: %q", bad, valid, stderr)
			}
		}
	}
}

func TestValidRelationshipTypesAreAccepted(t *testing.T) {
	bin := cliBinary(t)

	for _, valid := range []string{"relates_to", "supersedes", "caused_by", "SUPERSEDES"} {
		_, stderr, _ := run(t, bin, nil, "connect", "id-a", "id-b", valid)
		if strings.Contains(stderr, "unknown relationship type") {
			t.Errorf("connect with %q was rejected: %q", valid, stderr)
		}
	}
}

// TestMissingArgumentsExitNonZero: a CLI that exits 0 on a usage error breaks
// every script that checks the status.
func TestMissingArgumentsExitNonZero(t *testing.T) {
	bin := cliBinary(t)

	cases := [][]string{
		{"store"},
		{"query"},
		{"connect"},
		{"connect", "only-one-id"},
		{"delete"},
		{"session-end"},
		{"not-a-command"},
	}
	for _, args := range cases {
		_, stderr, code := run(t, bin, nil, args...)
		if code == 0 {
			t.Errorf("`kora %s` exited 0 with missing or invalid arguments",
				strings.Join(args, " "))
		}
		if stderr == "" {
			t.Errorf("`kora %s` failed silently on stderr",
				strings.Join(args, " "))
		}
	}
}

// TestNoArgumentsPrintsUsage: bare invocation is how a user discovers the tool,
// so it must explain itself rather than failing obscurely.
func TestNoArgumentsPrintsUsage(t *testing.T) {
	bin := cliBinary(t)

	stdout, stderr, code := run(t, bin, nil)
	combined := stdout + stderr
	if code == 0 {
		t.Error("running with no arguments exited 0; nothing was done")
	}
	for _, cmd := range []string{"store", "query", "connect", "stats", "session-start"} {
		if !strings.Contains(combined, cmd) {
			t.Errorf("usage output does not mention the %q command: %q", cmd, combined)
		}
	}
}

// TestUnreachableEngineIsReported: the endpoint is configuration, and a wrong
// one must not look like an empty database.
func TestUnreachableEngineIsReported(t *testing.T) {
	bin := cliBinary(t)

	for _, args := range [][]string{
		{"stats"},
		{"query", "anything"},
		{"store", "anything"},
	} {
		_, stderr, code := run(t, bin, []string{"KORA_API_KEY=some-key"}, args...)
		if code == 0 {
			t.Errorf("`kora %s` exited 0 against an unreachable engine",
				strings.Join(args, " "))
		}
		if stderr == "" {
			t.Errorf("`kora %s` reported nothing on stderr", strings.Join(args, " "))
		}
	}
}

// The tests above all run against an unreachable endpoint, so they exercise
// argument handling and nothing else. That leaves the success path unproven:
// mutation testing forced every RPC error branch to fire and the suite did not
// notice, because from its view every call already failed.
//
// These run against a real engine. KORA_CLI_ENDPOINT is separate from the
// unreachable default so the two groups cannot be confused, and the tests skip
// when it is absent rather than silently proving nothing.

func liveEndpoint(t *testing.T) (endpoint, key string) {
	t.Helper()
	endpoint = os.Getenv("KORA_CLI_ENDPOINT")
	key = os.Getenv("KORA_API_KEY")
	if endpoint == "" || key == "" {
		t.Skip("KORA_CLI_ENDPOINT and KORA_API_KEY not set")
	}
	return endpoint, key
}

// runLive executes the CLI against a reachable engine.
func runLive(t *testing.T, bin, project string, args ...string) (stdout string, code int) {
	t.Helper()
	endpoint, key := liveEndpoint(t)

	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"KORA_ENDPOINT="+endpoint,
		"KORA_API_KEY="+key,
		"KORA_PROJECT="+project,
	)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()

	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cli: %v", err)
	}
	if code != 0 {
		t.Logf("stderr: %s", errBuf.String())
	}
	return out.String(), code
}

// TestLiveRoundTrip proves the CLI can actually do its job: store a memory,
// find it again, connect two, and delete one. Without this, every RPC error
// branch could fire unconditionally and no test would object.
func TestLiveRoundTrip(t *testing.T) {
	bin := cliBinary(t)
	project := fmt.Sprintf("cli-live-%d", time.Now().UnixNano())

	// Store.
	out, code := runLive(t, bin, project, "store", "the deploy target is production", "--tags", "cli,live")
	if code != 0 {
		t.Fatalf("store exited %d against a reachable engine: %s", code, out)
	}
	var stored struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &stored); err != nil {
		t.Fatalf("store output is not the memory JSON: %v\n%s", err, out)
	}
	if stored.ID == "" {
		t.Fatalf("store returned no id: %s", out)
	}
	if stored.Content != "the deploy target is production" {
		t.Errorf("stored content = %q, want what was passed", stored.Content)
	}

	// Query finds it. Retried briefly: the memory is committed before the
	// response, but its embedding is written after, so vector search can lag.
	var found bool
	for attempt := 0; attempt < 10 && !found; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		qout, qcode := runLive(t, bin, project, "query", "deploy target")
		if qcode != 0 {
			t.Fatalf("query exited %d: %s", qcode, qout)
		}
		found = strings.Contains(qout, stored.ID)
	}
	if !found {
		t.Error("query did not return the memory that was just stored")
	}

	// Connect two memories.
	out2, code2 := runLive(t, bin, project, "store", "the rollback target is staging")
	if code2 != 0 {
		t.Fatalf("second store exited %d: %s", code2, out2)
	}
	var second struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out2), &second); err != nil {
		t.Fatalf("second store output is not JSON: %v", err)
	}

	cout, ccode := runLive(t, bin, project, "connect", stored.ID, second.ID, "supersedes")
	if ccode != 0 {
		t.Fatalf("connect exited %d: %s", ccode, cout)
	}
	if !strings.Contains(cout, stored.ID) {
		t.Errorf("connect output does not reference the source memory: %s", cout)
	}

	// Delete.
	dout, dcode := runLive(t, bin, project, "delete", stored.ID)
	if dcode != 0 {
		t.Fatalf("delete exited %d: %s", dcode, dout)
	}
}

// TestLiveStatsReportsRealNumbers: `kora stats` is how an operator checks a
// deployment, so it has to report the engine's actual state rather than the
// zeros an unauthenticated caller receives.
func TestLiveStatsReportsRealNumbers(t *testing.T) {
	bin := cliBinary(t)

	out, code := runLive(t, bin, "cli-live-stats", "stats")
	if code != 0 {
		t.Fatalf("stats exited %d: %s", code, out)
	}
	if !strings.Contains(out, "Status:") || !strings.Contains(out, "Nodes:") {
		t.Fatalf("stats output is not the expected report: %s", out)
	}
	// A version and a non-zero node count are what distinguish an
	// authenticated answer from the anonymous one.
	if strings.Contains(out, "Engine v\n") {
		t.Errorf("stats reported an empty version, which is the anonymous "+
			"response: %s", out)
	}
	if strings.Contains(out, "Nodes:      0\n") {
		t.Errorf("stats reported zero nodes against a populated engine, which "+
			"is the anonymous response: %s", out)
	}
}

// TestLiveSessionLifecycle covers the session commands end to end, including
// the repeat-end rejection the server added.
func TestLiveSessionLifecycle(t *testing.T) {
	bin := cliBinary(t)
	project := fmt.Sprintf("cli-live-session-%d", time.Now().UnixNano())

	out, code := runLive(t, bin, project, "session-start")
	if code != 0 {
		t.Fatalf("session-start exited %d: %s", code, out)
	}
	id := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).FindString(out)
	if id == "" {
		t.Fatalf("session-start printed no session id: %s", out)
	}

	if _, code := runLive(t, bin, project, "session-end", id); code != 0 {
		t.Fatalf("the first session-end exited %d", code)
	}
	// The server rejects a repeat end; the CLI must surface that as a failure
	// rather than printing a duration computed from a rejected response.
	if _, code := runLive(t, bin, project, "session-end", id); code == 0 {
		t.Error("a repeated session-end exited 0; the CLI reported success for " +
			"a request the server rejected")
	}
}

// TestRenamedEnvVarsWarn covers the gap the Context0 -> Kora rename left in the
// CLI. Unlike the server, the CLI reads the environment directly instead of
// going through internal/config, so the startup check there does not protect
// it -- and every setting has a fallback, so a stale variable is silently wrong
// rather than an error.
//
// CONTEXT0_ENDPOINT is the clearest case: the CLI ignores it and talks to
// localhost, so a user pointed at a remote engine quietly queries a different
// machine. CONTEXT0_PROJECT is worse in a subtle way, because querying the
// wrong project returns an empty result that reads as "no memories" rather
// than as a misconfiguration.
func TestRenamedEnvVarsWarn(t *testing.T) {
	bin := cliBinary(t)

	for _, old := range []string{
		"CONTEXT0_ENDPOINT",
		"CONTEXT0_API_KEY",
		"CONTEXT0_PROJECT",
	} {
		t.Run(old, func(t *testing.T) {
			_, stderr, _ := run(t, bin, []string{old + "=some-value"}, "query", "anything")

			if !strings.Contains(stderr, old) {
				t.Errorf("%s was set and the CLI said nothing about it; "+
					"the setting is silently ignored.\nstderr: %s", old, stderr)
			}
			// Naming the replacement is the point: a warning that something is
			// wrong without saying what to set is barely better than silence.
			want := "KORA_" + strings.TrimPrefix(old, "CONTEXT0_")
			if !strings.Contains(stderr, want) {
				t.Errorf("the warning does not name the replacement %s.\nstderr: %s",
					want, stderr)
			}
		})
	}
}

// TestUnrelatedEnvVarsDoNotWarn keeps the check above from being satisfied by
// warning about everything it sees.
func TestUnrelatedEnvVarsDoNotWarn(t *testing.T) {
	bin := cliBinary(t)
	_, stderr, _ := run(t, bin, []string{"CONTEXTUAL_THING=x"}, "query", "anything")

	if strings.Contains(stderr, "CONTEXTUAL_THING") {
		t.Errorf("an unrelated variable was reported as renamed.\nstderr: %s", stderr)
	}
}
