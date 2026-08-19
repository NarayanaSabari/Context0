#!/usr/bin/env python3
"""mutate.py -- check whether the test suite actually fails when the code is wrong.

Coverage says a line was executed. It does not say anything was asserted about
it. Nine checks in this repository passed while measuring nothing, and every one
was caught by hand-mutating the thing it claimed to guard -- never by watching
it go green, and never by a coverage number.

This automates that: apply a small semantic change to the source, run the tests,
and report any mutation the suite fails to notice. A surviving mutation is a
statement about the tests, not the code -- it means that behaviour is
unprotected.

Mutations are deliberately conservative. Each is a change that alters behaviour
in a way a correct test should catch, rather than a random edit that might not
compile or might be semantically identical.

Usage:
    scripts/mutate.py                     # audit the fast unit-testable packages
    scripts/mutate.py internal/auth       # one package
    scripts/mutate.py --list              # show the mutation operators
"""

import argparse
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parent.parent

# (name, pattern, replacement, description)
#
# Ordered roughly by how often each has caught something real here. Boundary
# and boolean flips came first because the defects in this repo clustered
# there: a rate limit that never engaged, a threshold that reported a healthy
# run as failed, an allowlist that let everything through.
MUTATIONS = [
    # `true && (cond)` and `false && (cond)` are NOT mutations: the first is
    # semantically identical to the original, and the second is just an
    # unreachable branch that most tests legitimately never enter. Both showed
    # up as false "survivors" in the first run of this script, which would have
    # sent someone hunting for a test gap that did not exist.
    #
    # Replacing the condition outright is the real mutation.
    ("cond-true", r"\bif ([a-zA-Z_][\w.]*\s*[!=<>]=\s*[^{;]+?) \{", r"if true /*MUT*/ {",
     "replace a condition with true"),
    ("cond-false", r"\bif ([a-zA-Z_][\w.]*\s*[!=<>]=\s*[^{;]+?) \{", r"if false /*MUT*/ {",
     "replace a condition with false"),
    ("negate-eq", r"([\w\.\)\]]) == ", r"\1 != ", "flip == to !="),
    ("negate-neq", r"([\w\.\)\]]) != ", r"\1 == ", "flip != to =="),
    ("boundary-lt", r"([\w\.\)\]]) < ", r"\1 <= ", "widen < to <="),
    ("boundary-gt", r"([\w\.\)\]]) > ", r"\1 >= ", "widen > to >="),
    ("and-or", r" && ", r" || ", "flip && to ||"),
    ("or-and", r" \|\| ", r" && ", "flip || to &&"),
    ("drop-return-err", r"\n(\s+)return ([a-z]\w*), (err|fmt\.Errorf[^\n]+)\n",
     r"\n\1return \2, nil /*MUT*/\n", "swallow a returned error"),

    # The comparison-based patterns above only match conditions containing
    # ==, !=, < or >. That silently skipped whole families of guard:
    #
    #   if !p.started.Load() {          -- a negated boolean
    #   if p.draining.Load() {          -- a bare boolean call
    #   if err := f(); err != nil {     -- an inline assignment
    #
    # internal/server is built almost entirely from those forms, so it
    # reported one mutant across the package and looked fully covered when
    # nothing had actually been mutated. A blind spot in the harness reads
    # exactly like an absence of gaps, which is the failure mode this whole
    # exercise exists to catch.
    ("bool-negate", r"\bif !([a-zA-Z_][\w.]*(?:\([^()]*\))?) \{",
     r"if \1 /*MUT*/ {", "drop a negation from a boolean guard"),
    ("bool-true", r"\bif ([a-zA-Z_][\w.]*(?:\([^()]*\))?) \{",
     r"if true /*MUT*/ {", "replace a boolean guard with true"),
    ("bool-false", r"\bif ([a-zA-Z_][\w.]*(?:\([^()]*\))?) \{",
     r"if false /*MUT*/ {", "replace a boolean guard with false"),
    # if err := f(); err != nil { ... } -- keep the call, skip the branch.
    ("inline-err-false", r"\bif (\w+ :?= [^;{]+); (\w+ != nil) \{",
     r"if \1; false /*MUT*/ {", "ignore an inline error check"),
]

SKIP_DIRS = {"testdata", "gen", "node_modules", ".git"}


def go_files(pkg: pathlib.Path):
    for p in sorted(pkg.rglob("*.go")):
        if p.name.endswith("_test.go"):
            continue
        if any(part in SKIP_DIRS for part in p.parts):
            continue
        yield p


def run_tests(pkg_path: str, timeout: int) -> bool:
    """True if the tests pass."""
    try:
        r = subprocess.run(
            ["go", "test", f"./{pkg_path}/...", "-count=1", "-failfast"],
            cwd=REPO, capture_output=True, timeout=timeout,
        )
        return r.returncode == 0
    except subprocess.TimeoutExpired:
        # A mutation that hangs is caught, in the sense that it does not
        # silently pass.
        return False



def comment_and_string_spans(src: str):
    """Byte ranges covered by comments or string/rune literals.

    Mutating inside a comment changes nothing, so the mutant always survives
    and is reported as a test gap. A comment reading "acquired == max" in
    internal/metrics produced exactly that: a survivor pointing at prose.
    Mutating inside a string literal is worse than useless -- it can change an
    SQL query or an error message into something that no longer compiles or
    silently alters behaviour unrelated to the guard under test.
    """
    spans = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c == '/' and i + 1 < n and src[i + 1] == '/':
            j = src.find('\n', i)
            j = n if j == -1 else j
            spans.append((i, j))
            i = j
        elif c == '/' and i + 1 < n and src[i + 1] == '*':
            j = src.find('*/', i + 2)
            j = n if j == -1 else j + 2
            spans.append((i, j))
            i = j
        elif c in '"`\'':
            quote = c
            j = i + 1
            while j < n:
                if src[j] == '\\' and quote != '`':
                    j += 2
                    continue
                if src[j] == quote:
                    j += 1
                    break
                if src[j] == '\n' and quote != '`':
                    break
                j += 1
            spans.append((i, min(j, n)))
            i = min(j, n)
        else:
            i += 1
    return spans


def in_spans(pos: int, spans) -> bool:
    return any(lo <= pos < hi for lo, hi in spans)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("packages", nargs="*", default=None)
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--timeout", type=int, default=120)
    ap.add_argument("--max-per-file", type=int, default=6,
                    help="cap mutations per file so one large file cannot dominate a run")
    args = ap.parse_args()

    if args.list:
        for name, _, _, desc in MUTATIONS:
            print(f"  {name:18s} {desc}")
        return 0

    packages = args.packages or [
        "internal/auth", "internal/extraction", "internal/ranking",
        "internal/service", "internal/logging", "internal/metrics",
        "internal/config", "internal/server",
    ]

    total = killed = survived = 0
    survivors = []

    for pkg in packages:
        pkg_path = REPO / pkg
        if not pkg_path.is_dir():
            print(f"skip {pkg}: not a directory", file=sys.stderr)
            continue

        # A suite that is already red tells us nothing about mutations.
        if not run_tests(pkg, args.timeout):
            print(f"SKIP {pkg}: tests already failing", file=sys.stderr)
            continue

        print(f"\n=== {pkg} ===")
        for src in go_files(pkg_path):
            original = src.read_text()
            applied = 0

            skip_spans = comment_and_string_spans(original)

            for name, pattern, repl, _ in MUTATIONS:
                if applied >= args.max_per_file:
                    break
                for m in list(re.finditer(pattern, original)):
                    if applied >= args.max_per_file:
                        break
                    # Only mutate real code.
                    if in_spans(m.start(), skip_spans):
                        continue
                    mutated = original[:m.start()] + re.sub(pattern, repl, m.group(0), count=1) + original[m.end():]
                    if mutated == original:
                        continue

                    src.write_text(mutated)
                    try:
                        build = subprocess.run(
                            ["go", "build", f"./{pkg}/..."],
                            cwd=REPO, capture_output=True, timeout=args.timeout,
                        )
                        if build.returncode != 0:
                            continue  # not a valid program; not a real mutation
                        applied += 1
                        total += 1
                        if run_tests(pkg, args.timeout):
                            survived += 1
                            line = original[:m.start()].count("\n") + 1
                            survivors.append((str(src.relative_to(REPO)), line, name,
                                              m.group(0).strip()[:70]))
                            print(f"  SURVIVED  {src.relative_to(REPO)}:{line}  [{name}]")
                        else:
                            killed += 1
                    finally:
                        src.write_text(original)

    print(f"\n=== {killed} killed, {survived} survived, {total} total ===")
    if survivors:
        print("\nUnprotected behaviour (the tests do not notice these changes):")
        for path, line, name, snippet in survivors:
            print(f"  {path}:{line}  [{name}]  {snippet}")
    return 1 if survived else 0


if __name__ == "__main__":
    sys.exit(main())
