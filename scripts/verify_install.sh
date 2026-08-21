#!/usr/bin/env bash
#
# verify_install.sh -- run the install paths this project documents, as a new
# user would, against a real cluster.
#
# This exists because of a specific failure. Removing the chart's default
# password and API key was the right change, but it broke `make deploy`, the
# README's `helm install`, and the README's E2E snippet, and none of that was
# noticed for several commits. Every other check in this repository passed
# throughout: the unit tests, the 64 cluster checks, the soak, and CI all
# exercise a *running* deployment, and all of them started from a deployment
# that was already up. Nothing started from scratch the way a new user does.
#
# `helm template` is not sufficient. It catches a chart that cannot render, but
# not a Makefile that forgets to pass credentials, a README that names a flag
# that no longer exists, or an install that renders and then crash-loops.
#
# Usage:
#   scripts/verify_install.sh            # uses the current kube context
#   KEEP=1 scripts/verify_install.sh     # leave the namespaces behind
set -uo pipefail

PASS=0
FAIL=0
FAILURES=()

check() {
  local name="$1" want="$2" got="$3"
  if [[ "$got" == *"$want"* ]]; then
    printf '  \033[32mPASS\033[0m  %s\n' "$name"
    PASS=$((PASS + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n' "$name"
    printf '        expected: %s\n        actual:   %s\n' "$want" "${got:-<empty>}"
    FAIL=$((FAIL + 1))
    FAILURES+=("$name")
  fi
}

# check_empty asserts that something was NOT found.
#
# Deliberately separate from check(): check() tests substring containment, and
# every string contains the empty string, so `check "..." "" "$got"` passes
# unconditionally. Two of the doc checks were written that way and silently
# asserted nothing -- caught by mutating the README and watching them pass.
check_empty() {
  local name="$1" got="$2"
  if [[ -z "$got" ]]; then
    printf '  \033[32mPASS\033[0m  %s\n' "$name"
    PASS=$((PASS + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n' "$name"
    printf '        found:    %s\n' "$got"
    FAIL=$((FAIL + 1))
    FAILURES+=("$name")
  fi
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  [[ -n "${KEEP:-}" ]] && { echo "KEEP set; leaving namespaces in place"; return; }
  # Guard the expansion: bash 3.2 treats "${arr[@]}" on an empty array as an
  # unbound variable under `set -u`, so an early exit (a missing image, say)
  # would fail inside the trap and bury the real error message.
  [[ "${#NAMESPACES[@]}" -eq 0 ]] && return
  for ns in "${NAMESPACES[@]}"; do
    helm uninstall "$(basename "$ns")" -n "$ns" >/dev/null 2>&1
    kubectl delete ns "$ns" --wait=false >/dev/null 2>&1
  done
}
NAMESPACES=()
trap cleanup EXIT

# Remove a verified deployment immediately rather than leaving it up until the
# script exits. Each install requests 1.5 cores for Postgres (sized from
# measurement, see values.yaml), so three concurrent ones cannot be scheduled on
# a 2-core CI runner: the later installs sat Pending until helm timed out, which
# reads as "the documented install is broken" rather than "the runner is full".
teardown() {
  helm uninstall kora -n "$1" >/dev/null 2>&1
  # Synchronous: the PersistentVolumeClaim is released when the namespace
  # finishes deleting, and the next install needs that capacity back.
  kubectl delete ns "$1" --wait=true --timeout=90s >/dev/null 2>&1
}

# The images the chart references must exist in the cluster, or every install
# waits out its timeout on ImagePullBackOff for reasons that have nothing to do
# with the docs. Overridable because CI tags images differently from local dev,
# and a hardcoded tag here means the script hangs rather than fails.
API_REPO="${API_IMAGE_REPO:-kora-api}"
API_TAG="${API_IMAGE_TAG:-dev}"
PG_REPO="${PG_IMAGE_REPO:-kora/postgres-age-vector}"
PG_TAG="${PG_IMAGE_TAG:-dev}"

IMAGE_ARGS=(
  --set "api.image.repository=$API_REPO"
  --set "api.image.tag=$API_TAG"
  --set api.image.pullPolicy=IfNotPresent
  --set "postgres.image.repository=$PG_REPO"
  --set "postgres.image.tag=$PG_TAG"
  --set postgres.image.pullPolicy=IfNotPresent
  --set web.enabled=false
  --set postgres.storage=1Gi
)

# Fail fast rather than waiting out five minutes of ImagePullBackOff per
# install: a missing image is a setup error, not a documentation defect, and it
# should say so immediately.
for img in "$API_REPO:$API_TAG" "$PG_REPO:$PG_TAG"; do
  if ! docker image inspect "$img" >/dev/null 2>&1; then
    echo "error: image $img not found locally." >&2
    echo "       Build it first, or set API_IMAGE_TAG / PG_IMAGE_TAG." >&2
    exit 1
  fi
done

# Shorter than the 5m default: these installs are two pods on a local cluster
# with images already present. Anything slower is stuck, and waiting longer
# only delays the report.
WAIT_TIMEOUT="${WAIT_TIMEOUT:-3m}"

section "1. The chart refuses to install without credentials"
# The guard that started all this. If it regresses, every install below would
# still pass while silently shipping an empty password.
out=$(helm install guard-test ./charts/kora -n guard-test --dry-run=client 2>&1)
check "a credential-less install is refused" "postgres.password is required" "$out"
check "the refusal says how to fix it" "existingSecret" "$out"

section "2. The README's 'Install with Helm' works"
# Run the README's own commands, not an approximation of them.
key=$(go run ./cmd/cli keys generate 2>/dev/null)
check "cmd/cli keys generate prints a usable key" "ctx0_" "$key"

ns=readme-install
NAMESPACES+=("$ns")
kubectl delete ns "$ns" --ignore-not-found --wait=true >/dev/null 2>&1
out=$(helm install kora ./charts/kora -n "$ns" --create-namespace \
  --set postgres.password="$(openssl rand -base64 24 | tr -d '/+=')" \
  --set auth.apiKeys="$key" \
  "${IMAGE_ARGS[@]}" --wait --timeout "$WAIT_TIMEOUT" 2>&1)
check "the documented helm install succeeds" "STATUS: deployed" "$out"

# Rendering is not working. A chart that installs and then crash-loops has
# still failed the user.
check "the API pod becomes ready" "true" \
  "$(kubectl get pod -n "$ns" -l app=kora-api \
    -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null)"

# And the key the user was told to generate must actually authenticate.
stored=$(kubectl exec -n "$ns" deploy/kora-api -- sh -c \
  "wget -q -O- --header='X-API-Key: $key' --header='Content-Type: application/json' \
   --post-data='{\"content\":\"install verification\",\"project_id\":\"verify\",\"type\":\"MEMORY_TYPE_SEMANTIC\"}' \
   http://localhost:8080/v1/memories" 2>/dev/null)
check "the generated key authenticates a write" "install verification" "$stored"
teardown "$ns"

section "3. The README's 'bring your own Secrets' path works"
ns=readme-existing
NAMESPACES+=("$ns")
kubectl delete ns "$ns" --ignore-not-found --wait=true >/dev/null 2>&1
kubectl create ns "$ns" >/dev/null 2>&1
kubectl create secret generic my-postgres-secret -n "$ns" \
  --from-literal=password="$(openssl rand -hex 16)" >/dev/null 2>&1
kubectl create secret generic my-api-keys -n "$ns" \
  --from-literal=keys="$(go run ./cmd/cli keys generate 2>/dev/null)" >/dev/null 2>&1

out=$(helm install kora ./charts/kora -n "$ns" \
  --set postgres.existingSecret=my-postgres-secret \
  --set auth.existingSecret=my-api-keys \
  "${IMAGE_ARGS[@]}" --wait --timeout "$WAIT_TIMEOUT" 2>&1)
check "the existingSecret install succeeds" "STATUS: deployed" "$out"
check "the API pod becomes ready with operator-managed Secrets" "true" \
  "$(kubectl get pod -n "$ns" -l app=kora-api \
    -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null)"

# The chart must not have created Secrets of its own on this path, or it is
# managing credentials the operator believes they control.
check "the chart created no Secret of its own" "0" \
  "$(kubectl get secret -n "$ns" -l app.kubernetes.io/managed-by=Helm --no-headers 2>/dev/null | wc -l | tr -d ' ')"
teardown "$ns"

section "4. make deploy works from nothing"
# The exact regression this script exists for: `make deploy` passed no
# credentials at all, and only appeared to work because helm reuses values from
# an existing release.
rm -f .dev-credentials
make .dev-credentials >/dev/null 2>&1
check "make generates dev credentials" "DEV_API_KEY=ctx0_" "$(cat .dev-credentials 2>/dev/null)"
check "the credentials file is gitignored" ".dev-credentials" "$(git check-ignore .dev-credentials 2>/dev/null)"

# Run the real target, not an approximation of it. The original bug was that
# `make deploy` passed no credentials, so a check that reimplements the helm
# call here would have passed while the actual target stayed broken.
#
# The recipe is extracted and run against a scratch namespace so this does not
# disturb an existing dev deployment, but the command itself comes from the
# Makefile, so dropping the --set flags fails this check.
# shellcheck disable=SC1091
. ./.dev-credentials
ns=make-deploy
NAMESPACES+=("$ns")
kubectl delete ns "$ns" --ignore-not-found --wait=true >/dev/null 2>&1

# make --dry-run prints the recipe with its line continuations intact, so the
# continuations must be joined before matching: grepping line by line truncates
# the command at the first backslash and hides exactly the --set flags this is
# checking for. (awk rather than sed: the GNU branch syntax is not portable.)
recipe=$(make --dry-run --always-make deploy 2>/dev/null \
  | awk '{ if (sub(/\\$/, "")) { printf "%s", $0 } else { print } }' \
  | grep 'helm upgrade' || true)
check "make deploy passes credentials to helm" "auth.apiKeys" "$recipe"
check "make deploy passes a database password to helm" "postgres.password" "$recipe"

out=$(eval "${recipe//-n kora /-n $ns }" \
  "${IMAGE_ARGS[*]}" --wait --timeout "$WAIT_TIMEOUT" 2>&1 || true)
check "make deploy's install succeeds from scratch" "STATUS: deployed" "$out"
check "the generated key works against the deployment" "make deploy verification" \
  "$(kubectl exec -n "$ns" deploy/kora-api -- sh -c \
    "wget -q -O- --header='X-API-Key: $DEV_API_KEY' --header='Content-Type: application/json' \
     --post-data='{\"content\":\"make deploy verification\",\"project_id\":\"v\",\"type\":\"MEMORY_TYPE_SEMANTIC\"}' \
     http://localhost:8080/v1/memories" 2>/dev/null)"
teardown "$ns"

section "4b. docker compose, the other documented quickstart"
# Not started here -- building and running four containers is slow and this
# script already builds a cluster -- but the parts that broke are checked:
# whether the file still ships a credential, and whether it refuses to start
# without one. Both were true until this was looked at.
# Only non-comment lines count: the comment explaining why the key was removed
# necessarily names it, and matching that made this fail on its own explanation.
check_empty "docker-compose.yaml ships no default API key" \
  "$(grep -n 'ctx0_dev_key' docker-compose.yaml 2>/dev/null \
    | grep -vE '^[0-9]+:[[:space:]]*#' || true)"
check "compose requires an API key to be supplied" "required variable" \
  "$(env -u KORA_API_KEYS -u POSTGRES_PASSWORD docker compose --env-file /dev/null config 2>&1 | tail -1)"

# The web container listens on 8080 since it moved to nginx-unprivileged, so a
# port mapping of :80 silently serves nothing.
check "the web port mapping matches the container's listen port" "8080" \
  "$(grep -oE '\$\{WEB_PORT:-[0-9]+\}:[0-9]+' docker-compose.yaml | awk -F'}:' '{print $2}')"

section "5. The docs do not reference things that no longer exist"
# Cheap, and it is exactly the class of rot that broke the README: a flag or a
# file named in the docs that was renamed or deleted.
for f in charts/kora/values-local.yaml charts/kora/values-production.yaml; do
  # Only lines that tell a reader to *use* the file matter. An unchecked
  # roadmap item ("- [ ] values-local.yaml") names a file that is meant not to
  # exist yet, and failing on that would train people to ignore this check.
  refs=$(grep -rn "$(basename "$f")" README.md docs/*.md 2>/dev/null \
    | grep -v research | grep -v security- \
    | grep -E '(helm|-f |install|apply)' || true)
  [[ -f "$f" ]] && refs=""
  check_empty "no doc instructs using the missing $(basename "$f")" "$refs"
done

# The deleted default key must not survive anywhere a user would copy from.
#
# scripts/ is included because it was the gap: the key lived on as a working
# default in demo.sh -- which CONTRIBUTING.md tells new contributors to run --
# and in four Python scripts, long after a83af5a removed it from the chart.
# This check did not look there.
#
# Comment lines are excluded rather than the whole file, so a comment may
# explain the history while a reintroduced value is still caught. verify_k8s.sh
# is excluded because it presents the key deliberately and asserts a 401.
stale=$(grep -rn "ctx0_dev_key_1" README.md docs/*.md Makefile test/ scripts/ 2>/dev/null \
  | grep -v security-research | grep -v keyword-search \
  | grep -v 'verify_install.sh' \
  | grep -v 'verify_k8s.sh' \
  | grep -vE ':[0-9]+:[[:space:]]*#' \
  | grep -v 'used to live here' || true)
check_empty "no doc still tells users to use the removed default key" "$stale"

printf '\n\033[1m%s\033[0m\n' "=== $PASS passed, $FAIL failed ==="
for f in "${FAILURES[@]:-}"; do [[ -n "$f" ]] && printf '  failed: %s\n' "$f"; done
exit $((FAIL > 0 ? 1 : 0))
