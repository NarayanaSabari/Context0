package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	bin := filepath.Join(t.TempDir(), "context0")
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
		"CONTEXT0_ENDPOINT=127.0.0.1:1", // nothing listens on port 1
		"CONTEXT0_PROJECT=cli-test",
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
			t.Errorf("`context0 %s` exited 0 with missing or invalid arguments",
				strings.Join(args, " "))
		}
		if stderr == "" {
			t.Errorf("`context0 %s` failed silently on stderr",
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
		_, stderr, code := run(t, bin, []string{"CONTEXT0_API_KEY=some-key"}, args...)
		if code == 0 {
			t.Errorf("`context0 %s` exited 0 against an unreachable engine",
				strings.Join(args, " "))
		}
		if stderr == "" {
			t.Errorf("`context0 %s` reported nothing on stderr", strings.Join(args, " "))
		}
	}
}
