#!/usr/bin/env bash
# Requirement-to-check verification for the Kubernetes work.
#
# Every claim the chart and server changes make, mapped to a check that
# observes the running cluster rather than the rendered YAML. Run against a
# freshly created kind cluster.
#
# Usage: scripts/verify_k8s.sh [namespace]
set -uo pipefail

NS="${1:-context0}"
PASS=0
FAIL=0
declare -a FAILURES

check() {
  local requirement="$1" expected="$2" actual="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    printf '  \033[32mPASS\033[0m  %s\n' "$requirement"
    PASS=$((PASS + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n        expected: %s\n        actual:   %s\n' \
      "$requirement" "$expected" "$actual"
    FAIL=$((FAIL + 1))
    FAILURES+=("$requirement")
  fi
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

api_pod() { kubectl get pod -n "$NS" -l app=context0-api -o jsonpath='{.items[0].metadata.name}'; }

section "1. Probes: three endpoints, three questions"
spec=$(kubectl get deploy -n "$NS" context0-api -o json)
check "startupProbe targets /startupz" "/startupz" \
  "$(jq -r '.spec.template.spec.containers[0].startupProbe.httpGet.path' <<<"$spec")"
check "livenessProbe targets /livez" "/livez" \
  "$(jq -r '.spec.template.spec.containers[0].livenessProbe.httpGet.path' <<<"$spec")"
check "readinessProbe targets /readyz" "/readyz" \
  "$(jq -r '.spec.template.spec.containers[0].readinessProbe.httpGet.path' <<<"$spec")"
check "no probe still points at the graph-counting /v1/health" "false" \
  "$(jq -r '[.spec.template.spec.containers[0]
             | .startupProbe, .livenessProbe, .readinessProbe
             | .httpGet.path] | any(. == "/v1/health")' <<<"$spec")"

section "2. Probe endpoints answer correctly through the Service"
for p in livez readyz startupz; do
  check "GET /$p returns 200" "200" \
    "$(kubectl exec -n "$NS" "$(api_pod)" -- \
       wget -q -S -O /dev/null "http://localhost:8080/$p" 2>&1 | awk '/HTTP\//{print $2; exit}')"
done

section "3. Security context is enforced on the running container"
sc=$(kubectl get pod -n "$NS" "$(api_pod)" -o json | jq -c '.spec.containers[0].securityContext')
check "readOnlyRootFilesystem" "\"readOnlyRootFilesystem\":true" "$sc"
check "allowPrivilegeEscalation disabled" "\"allowPrivilegeEscalation\":false" "$sc"
check "all capabilities dropped" "\"drop\":[\"ALL\"]" "$sc"
check "runs as non-root" "true" \
  "$(kubectl get pod -n "$NS" "$(api_pod)" -o jsonpath='{.spec.securityContext.runAsNonRoot}')"
# The rootfs really is read-only, not just declared so. kubectl exec exits
# non-zero and writes the shell's complaint to stderr, so capture both.
check "writing outside /tmp is refused at runtime" "Read-only" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c 'echo x > /probe' 2>&1 || true)"
check "the mounted /tmp is writable" "ok" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c 'echo ok > /tmp/probe && cat /tmp/probe')"

section "4. Runtime configuration reaches the process"
check "GOMEMLIMIT derived from limits.memory" "536870912" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- printenv GOMEMLIMIT)"
check "pool_max_conns set explicitly in the DSN" "pool_max_conns=10" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- printenv CONTEXT0_DATABASE_URL)"
check "rate limit is configurable, not hardcoded" "100" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- printenv CONTEXT0_RATE_LIMIT_PER_MINUTE)"

section "5. Lifecycle: drain outlives the grace period"
check "preStop hook present" "sleep" \
  "$(jq -r '.spec.template.spec.containers[0].lifecycle.preStop.exec.command | join(" ")' <<<"$spec")"
grace=$(jq -r '.spec.template.spec.terminationGracePeriodSeconds' <<<"$spec")
check "terminationGracePeriodSeconds exceeds the 15s in-process drain" "true" \
  "$([[ "$grace" -gt 15 ]] && echo true || echo false)"
check "rollout never drops below the replica count" "0" \
  "$(jq -r '.spec.strategy.rollingUpdate.maxUnavailable' <<<"$spec")"

section "6. Postgres tuning applied to the running server"
for setting in shared_buffers:256MB work_mem:16MB maintenance_work_mem:128MB effective_cache_size:768MB; do
  name="${setting%%:*}"; want="${setting##*:}"
  check "$name" "$want" \
    "$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc "SHOW $name;" 2>/dev/null | tr -d '[:space:]')"
done

section "7. Performance work reaches the cluster, not just the laptop"
check "property indexes created automatically on first boot" "memory_id_idx" \
  "$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
     "SELECT string_agg(indexname, ',') FROM pg_indexes WHERE schemaname='context0' AND indexname LIKE 'memory%';" 2>/dev/null)"
check "project_id index present too" "memory_project_id_idx" \
  "$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
     "SELECT string_agg(indexname, ',') FROM pg_indexes WHERE schemaname='context0' AND indexname LIKE 'memory%';" 2>/dev/null)"

section "8. Public API works end to end in-cluster"
key=$(kubectl get secret -n "$NS" context0-api-keys -o jsonpath='{.data.keys}' | base64 -d | cut -d, -f1)
stored=$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
  "wget -q -O- --header='Content-Type: application/json' --header='X-API-Key: $key' \
   --post-data='{\"content\":\"verification probe\",\"type\":2,\"project_id\":\"verify\",\"tags\":[\"v\"]}' \
   http://localhost:8080/v1/memories" 2>/dev/null)
check "POST /v1/memories stores a memory" "verification probe" "$stored"
queried=$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
  "wget -q -O- --header='X-API-Key: $key' \
   'http://localhost:8080/v1/memories/query?query=verification&project_id=verify&top_k=3'" 2>/dev/null)
check "GET /v1/memories/query returns it" "verification probe" "$queried"
check "/metrics is scrapeable without auth" "context0" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null | head -40 | tr '\n' ' ')"

section "9. Web UI resolves the API upstream"
check "web deployment sets API_HOST to in-cluster DNS" "context0-api.$NS.svc.cluster.local" \
  "$(kubectl get deploy -n "$NS" context0-web -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="API_HOST")].value}' 2>/dev/null)"
check "web pod is Running, not CrashLoopBackOff" "Running" \
  "$(kubectl get pod -n "$NS" -l app=context0-web -o jsonpath='{.items[0].status.phase}' 2>/dev/null)"

section "10. Failure mode: the database goes away"
# The reason liveness must not touch the database. Before this split, a brief
# Postgres outage failed liveness on every replica at once and Kubernetes
# restarted the whole fleet -- which cannot fix a remote database and discards
# every warm connection pool.
restarts_before=$(kubectl get pod -n "$NS" "$(api_pod)" -o jsonpath='{.status.containerStatuses[0].restartCount}')
kubectl scale statefulset postgres-age -n "$NS" --replicas=0 >/dev/null 2>&1
sleep 25
pod=$(api_pod)
check "liveness still passes with the database down" "200" \
  "$(kubectl exec -n "$NS" "$pod" -- wget -q -S -O /dev/null http://localhost:8080/livez 2>&1 | awk '/HTTP\//{print $2; exit}')"
check "readiness fails with the database down" "503" \
  "$(kubectl exec -n "$NS" "$pod" -- wget -q -S -O /dev/null http://localhost:8080/readyz 2>&1 | awk '/HTTP\//{print $2; exit}')"
check "pod left Service endpoints" "false" \
  "$(kubectl get pod -n "$NS" "$pod" -o jsonpath='{.status.containerStatuses[0].ready}')"

kubectl scale statefulset postgres-age -n "$NS" --replicas=1 >/dev/null 2>&1
for _ in $(seq 1 40); do
  [[ "$(kubectl get pod -n "$NS" "$(api_pod)" -o jsonpath='{.status.containerStatuses[0].ready}')" == "true" ]] && break
  sleep 3
done
check "pod recovers on its own once the database returns" "true" \
  "$(kubectl get pod -n "$NS" "$(api_pod)" -o jsonpath='{.status.containerStatuses[0].ready}')"
check "the outage caused no container restart" "$restarts_before" \
  "$(kubectl get pod -n "$NS" "$(api_pod)" -o jsonpath='{.status.containerStatuses[0].restartCount}')"

printf '\n\033[1m%s\033[0m\n' "=== $PASS passed, $FAIL failed ==="
for f in "${FAILURES[@]:-}"; do [[ -n "$f" ]] && printf '  failed: %s\n' "$f"; done
exit $((FAIL > 0 ? 1 : 0))
