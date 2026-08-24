#!/usr/bin/env bash
#
# verify_docs.sh -- check that the documentation describes settings that exist.
#
# The repo carries two documentation sets: the Markdown at the root and under
# docs/, and the docsify site under site/public/docs/ that is published to
# kora.sabarinarayana.com/docs/. Nothing connected them. verify_install.sh runs
# the README's install commands for real, so a wrong flag there fails; the site
# docs had no equivalent, and they are the ones a new user actually reads.
#
# The failure this prevents is specific and quiet. Every KORA_* setting falls
# back to a default when unset, so a documented variable with a typo, or one
# left behind by a rename, does not error -- the user sets it, the engine
# ignores it, and the behaviour they wanted silently does not happen. The same
# is true of a `--set` flag naming a chart value that no longer exists: Helm
# accepts unknown values without complaint.
#
# So this compares the documentation against the code rather than against
# itself: every KORA_* name in any doc must be read somewhere in Go, and every
# --set path must resolve in the chart's values.yaml.
#
# Deliberately not checked here: prose, formatting, or whether the two doc sets
# say the same thing in the same words. Only claims that can be mechanically
# falsified.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0
FAIL=0
FAILURES=()

fail() {
    FAIL=$((FAIL + 1))
    FAILURES+=("$1")
    printf '  \033[31mFAIL\033[0m %s\n' "$1"
}

ok() {
    PASS=$((PASS + 1))
    printf '  \033[32mok\033[0m   %s\n' "$1"
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

DOC_FILES=(README.md CONTRIBUTING.md RELEASING.md)
while IFS= read -r f; do DOC_FILES+=("$f"); done < <(
    find docs site/public/docs -name '*.md' 2>/dev/null
)

section "1. Every documented KORA_* variable is read by the code"

# The engine reads its settings through internal/config; the CLI reads three of
# its own directly. Collect both, plus the test-only and e2e names, and treat
# that as the set of variables that do something.
known=$(
    {
        grep -rhoE 'KORA_[A-Z0-9_]+' internal/ cmd/ --include='*.go'
        # Documented as a prefix in the rename guard, not a literal read.
        echo KORA_E2E_API_KEY
        echo KORA_E2E_HTTP
        echo KORA_E2E_ENDPOINT
    } | sort -u
)

documented=$(grep -rhoE 'KORA_[A-Z0-9_]+' "${DOC_FILES[@]}" 2>/dev/null | sort -u)

unknown=""
for v in $documented; do
    # KORA_E and similar are truncations produced by matching inside a longer
    # token such as ${KORA_E2E_...}; skip anything too short to be real.
    [ "${#v}" -le 6 ] && continue
    grep -qx "$v" <<<"$known" || unknown="$unknown $v"
done

if [ -z "$unknown" ]; then
    ok "all documented KORA_* variables are read somewhere in Go"
else
    fail "documented but never read (a user setting these gets silence):$unknown"
fi

section "2. Every documented --set path exists in the chart"

values=charts/kora/values.yaml
paths=$(grep -rhoE '\-\-set [a-zA-Z][a-zA-Z0-9._]*' "${DOC_FILES[@]}" 2>/dev/null |
    sed 's/--set //' | sort -u)

missing=""
for p in $paths; do
    # Walk the dotted path through values.yaml.
    #
    # This parses the indentation directly rather than importing a YAML
    # library: PyYAML happens to be preinstalled on GitHub's runners, but
    # depending on that would make this check fail on a machine where it is
    # not, and a check that cannot run is worse than no check. The chart's
    # values are a plain nested mapping, which is the subset this handles.
    if ! python3 - "$values" "$p" <<'PY'
import sys

want = sys.argv[2].split('.')
depth = 0          # how many parts of `want` are matched so far
indent_at = [-1]   # indentation column each matched part was found at

for raw in open(sys.argv[1]):
    line = raw.rstrip('\n')
    stripped = line.strip()
    if not stripped or stripped.startswith('#') or ':' not in stripped:
        continue
    col = len(line) - len(line.lstrip(' '))
    key = stripped.split(':', 1)[0].strip().strip('"\'')

    # Left the subtree that held the last match, so unwind.
    while depth > 0 and col <= indent_at[depth]:
        depth -= 1

    if key == want[depth] and col > indent_at[depth]:
        depth += 1
        if depth == len(want):
            sys.exit(0)
        indent_at = indent_at[:depth] + [col]

sys.exit(1)
PY
    then
        missing="$missing $p"
    fi
done

if [ -z "$missing" ]; then
    ok "all documented --set paths resolve in $values"
else
    fail "documented --set paths missing from the chart:$missing"
fi

section "3. The documented ports match the code's defaults"

grpc_default=$(grep -oE 'getEnvInt\("KORA_GRPC_PORT", [0-9]+' internal/config/config.go | grep -oE '[0-9]+$')
http_default=$(grep -oE 'getEnvInt\("KORA_HTTP_PORT", [0-9]+' internal/config/config.go | grep -oE '[0-9]+$')

for pair in "gRPC:$grpc_default" "HTTP:$http_default"; do
    name=${pair%%:*}
    port=${pair##*:}
    if [ -z "$port" ]; then
        fail "could not read the $name default port out of internal/config"
    elif grep -rq "$port" site/public/docs/*.md; then
        ok "the $name default port ($port) appears in the published docs"
    else
        fail "the $name default is $port, which the published docs never mention"
    fi
done

section "4. The removed default API key is not documented anywhere"

# docs/research and docs/security-research-2026.md are excluded: they are dated
# investigations that describe the key as a vulnerability they found, which is
# the opposite of offering it. docs/research/README.md marks them historical.
# Comment and quote lines are allowed elsewhere for the same reason; a usable
# value is not.
key_docs=()
for f in "${DOC_FILES[@]}"; do
    case "$f" in
        docs/research/* | docs/security-research-2026.md) continue ;;
    esac
    key_docs+=("$f")
done

stale=$(grep -rn 'ctx0_dev_key_1' "${key_docs[@]}" 2>/dev/null |
    grep -vE ':[0-9]+:[[:space:]]*(#|>|-{2,})' || true)
if [ -z "$stale" ]; then
    ok "no doc offers the removed default key"
else
    fail "the removed default key is still presented as usable: $stale"
fi

printf '\n\033[1m=== %d passed, %d failed ===\033[0m\n' "$PASS" "$FAIL"
for f in "${FAILURES[@]:-}"; do [ -n "$f" ] && printf '  failed: %s\n' "$f"; done
exit $((FAIL > 0 ? 1 : 0))
