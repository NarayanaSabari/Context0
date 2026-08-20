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
DB_NAME="${DB_NAME:-context0}"
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

# Select a Ready pod, not merely the first one listed. During a rollout the
# first item is often the old Terminating pod, and exec'ing into it fails with
# exit 6 -- which reads as a fleet of unrelated failures rather than a race in
# this script. Wait briefly for readiness so the suite can run immediately
# after a `helm upgrade`.
api_pod() {
  for _ in $(seq 1 60); do
    local p
    p=$(kubectl get pod -n "$NS" -l app=context0-api \
      -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name} {.status.containerStatuses[0].ready} {.metadata.deletionTimestamp}{"\n"}{end}' \
      2>/dev/null | awk '$2=="true" && $3=="" {print $1; exit}')
    if [[ -n "$p" ]]; then printf '%s' "$p"; return 0; fi
    sleep 2
  done
  kubectl get pod -n "$NS" -l app=context0-api -o jsonpath='{.items[0].metadata.name}'
}

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
# Asserts that the chart passes the value through, not what the value is:
# pinning the number here means every retune of the default breaks the suite for
# no reason. The default itself is pinned by a unit test against measured
# service cost.
check "rate limit is configurable, not hardcoded" "configured" \
  "$([[ "$(kubectl exec -n "$NS" "$(api_pod)" -- printenv CONTEXT0_RATE_LIMIT_PER_MINUTE)" =~ ^[0-9]+$ ]] \
    && echo configured || echo missing)"

section "5. Lifecycle: drain outlives the grace period"
check "preStop hook present" "sleep" \
  "$(jq -r '.spec.template.spec.containers[0].lifecycle.preStop.exec.command | join(" ")' <<<"$spec")"
grace=$(jq -r '.spec.template.spec.terminationGracePeriodSeconds' <<<"$spec")
check "terminationGracePeriodSeconds exceeds the 15s in-process drain" "true" \
  "$([[ "$grace" -gt 15 ]] && echo true || echo false)"
check "rollout never drops below the replica count" "0" \
  "$(jq -r '.spec.strategy.rollingUpdate.maxUnavailable' <<<"$spec")"

section "6. Postgres tuning applied to the running server"
# Expected values are read from the chart rather than repeated here. Hardcoding
# them duplicated the chart's numbers, and when the memory limit was raised to
# stop Postgres being OOM-killed, this section failed on the *correct* new
# values -- a check reporting a fix as a regression.
#
# What matters is that what the chart asks for is what the server is running,
# not what any particular number is.
for name in shared_buffers work_mem maintenance_work_mem effective_cache_size; do
  # sharedBuffers, workMem, maintenanceWorkMem, effectiveCacheSize
  key=$(awk -v n="$name" 'BEGIN{
    split(n, p, "_"); out=p[1];
    for (i=2; i<=length(p); i++) out = out toupper(substr(p[i],1,1)) substr(p[i],2);
    print out
  }')
  want=$(awk -v k="$key:" '$1==k {print $2; exit}' charts/context0/values.yaml)
  [[ -n "$want" ]] || { printf '  \033[33mSKIP\033[0m  %s not found in values.yaml\n' "$key"; continue; }
  check "$name matches the chart ($key: $want)" "$want" \
    "$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc "SHOW $name;" 2>/dev/null | tr -d '[:space:]')"
done

# The reason the limit was raised: /dev/shm is medium: Memory, so it counts
# against the pod limit alongside shared_buffers and per-connection work_mem.
# At 1Gi those did not fit and a six-worker soak OOM-killed Postgres six times.
mem_limit=$(kubectl get sts postgres-age -n "$NS" -o jsonpath='{.spec.template.spec.containers[0].resources.limits.memory}' 2>/dev/null)
shm_mb=$(kubectl exec -n "$NS" postgres-age-0 -- sh -c "df -m /dev/shm | awk 'NR==2{print \$2}'" 2>/dev/null)
sb_mb=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "SELECT setting::bigint*8/1024 FROM pg_settings WHERE name='shared_buffers';" 2>/dev/null | tr -d '[:space:]')
limit_mb=$(awk -v l="$mem_limit" 'BEGIN{ gsub(/Gi/,"",l); if (l ~ /Mi/) { gsub(/Mi/,"",l); print l } else { print l*1024 } }')
printf '  observed: limit %s, /dev/shm %sMi, shared_buffers %sMB\n' "$mem_limit" "${shm_mb:-?}" "${sb_mb:-?}"
check "shm + shared_buffers leave room under the memory limit" "ok" \
  "$(awk -v s="${shm_mb:-0}" -v b="${sb_mb:-0}" -v l="${limit_mb:-1024}" \
     'BEGIN{ print ((s+b) < l*0.55) ? "ok" : "too tight" }')"

check "postgres has not been OOM-killed" "0" \
  "$(kubectl get pod postgres-age-0 -n "$NS" \
     -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null)"

# The cgroup's own counter, not the restart count: restarts only show a kill
# that already happened, while this shows whether the limit was ever reached.
# Reading total memory.current instead would be misleading -- most of it is
# reclaimable page cache, and after a 15-minute soak it sat at 1946MB of 2048MB
# while oom_kill stayed 0 and anonymous memory was only 410MB.
check "postgres never reached its memory limit" "0" \
  "$(kubectl exec -n "$NS" postgres-age-0 -- \
     awk '/^oom_kill /{print $2}' /sys/fs/cgroup/memory.events 2>/dev/null | tr -d '[:space:]')"

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

section "11. Credentials and authentication"
# The chart used to ship a working database password and two API keys as
# defaults, which means every install that did not override them shared a
# published credential. These checks assert the deployed state, not the chart
# source, so a regression in either place is caught.
pgpw=$(kubectl get secret -n "$NS" postgres-age-secret -o jsonpath='{.data.password}' | base64 -d)
check "the database password is not a shipped default" "notdefault" \
  "$([[ "$pgpw" == "context0-dev-password" ]] && echo isdefault || echo notdefault)"
check "no shipped default API key is accepted" "401" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
    "wget -q -S -O /dev/null --header='X-API-Key: ctx0_dev_key_1' \
     'http://localhost:8080/v1/memories/query?query=x&project_id=verify' 2>&1" \
    | awk '/HTTP\//{print $2; exit}')"

# The password must reach the container through a Secret reference, never
# inlined into the Deployment spec: `kubectl get deploy -o yaml` is readable by
# far more people than Secrets are.
check "the password is absent from the Deployment spec" "0" \
  "$(kubectl get deploy -n "$NS" context0-api -o yaml | grep -c -- "$pgpw" || true)"
check "the password reaches the pod via secretKeyRef" "POSTGRES_PASSWORD" \
  "$(kubectl get deploy -n "$NS" context0-api -o jsonpath='{.spec.template.spec.containers[0].env[?(@.valueFrom.secretKeyRef)].name}')"

# Deny-by-default: before this, anything outside /v1/ was served without a key,
# so any future route -- an admin surface, a mistakenly mounted profiler -- was
# public the moment it was added.
for path in /debug/pprof/ /admin /v2/memories /; do
  check "unauthenticated $path is denied" "401" \
    "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
      "wget -q -S -O /dev/null 'http://localhost:8080$path' 2>&1" | awk '/HTTP\//{print $2; exit}')"
done

# The allowlist matches r.URL.Path exactly, so the question is whether a caller
# can reach a public path -- or slip past a protected one -- by spelling it
# differently. These have to go over a raw socket: wget, curl and every HTTP
# library clean the path before sending it, so a client-side test cannot
# actually deliver "/v1/../metrics" to the server and proves nothing.
#
# The pairing matters. A request that normalises ONTO a public path must not
# become public (an unclean spelling is not in the allowlist, so it is denied),
# and a request that normalises onto a protected path must stay denied.
# The socket is held open for a moment after the request rather than closed
# immediately. nc closes its write side as soon as printf ends, Go's net/http
# reads that half-close as the client going away, and it cancels the request
# context: /readyz then answers 503 from its own cancelled pool ping and a
# gateway route answers 499 "context canceled". Both are correct responses to a
# client that left, but they say nothing about the path being tested, and a
# check written without the delay reports a healthy endpoint as broken.
raw_status() {
  kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
    "(printf 'GET $1 HTTP/1.1\r\nHost: localhost\r\n\r\n'; sleep 1) | nc localhost 8080 2>/dev/null | head -1" \
    2>/dev/null | tr -d '\r' | awk '{print $2}'
}

for path in /./metrics /v1/../metrics //metrics /metrics/ /v1/./health //v1/health; do
  check "unclean path $path does not reach a public endpoint" "401" "$(raw_status "$path")"
done

for path in /v1/../v1/memories /./v1/memories /v1/./memories /metrics/../v1/memories; do
  check "unclean path $path stays denied" "401" "$(raw_status "$path")"
done

# The control: the exact spellings must still work over the same raw socket, or
# the checks above would pass simply because everything is broken.
check "the exact public path is served" "200" "$(raw_status /metrics)"
check "the exact probe path is served" "200" "$(raw_status /readyz)"

# Readiness must describe the database, not the client that asked. This closes
# the socket immediately, which is what a kubelet whose probe timeout elapses
# looks like: the handler used to answer "database unreachable" while the
# database was healthy, removing the pod from Service endpoints for a reason
# that never happened.
check "readiness does not blame the database for a departed caller" "200" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
    "printf 'GET /readyz HTTP/1.1\r\nHost: localhost\r\n\r\n' | nc localhost 8080 2>/dev/null | head -1" \
    2>/dev/null | tr -d '\r' | awk '{print $2}')"

# The kubelet's own timeout must sit above the handler's 1s database bound, or
# the two race and whichever fires first decides the answer.
check "the readiness probe timeout exceeds the handler's database bound" "yes" \
  "$(t=$(kubectl get deploy -n "$NS" context0-api -o jsonpath='{.spec.template.spec.containers[0].readinessProbe.timeoutSeconds}' 2>/dev/null); \
    [[ -n "$t" && "$t" -gt 1 ]] && echo yes || echo no)"

# Keys are stored hashed, so the running process cannot hand back a credential
# even if it is compromised or dumped.
# /v1/health answers without a credential because probes cannot present one,
# but it must not volunteer what is running and how much data is in it. Found
# via the CLI: `context0 stats` with no API key returned the version, node
# count and edge count.
check "an anonymous caller gets no graph statistics" "0" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- wget -q -O- http://localhost:8080/v1/health 2>/dev/null \
    | grep -o '"nodeCount":"[0-9]*"' | grep -o '[0-9]*')"
check "an anonymous caller gets no version" '"version":""' \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- wget -q -O- http://localhost:8080/v1/health 2>/dev/null \
    | grep -o '"version":"[^"]*"')"
# ...and an authenticated caller still gets them, or this is a regression
# dressed up as a fix.
check "an authenticated caller still gets the statistics" "ok" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
    "wget -q -O- --header='X-API-Key: $key' http://localhost:8080/v1/health" 2>/dev/null \
    | grep -qE '"nodeCount":"[1-9]' && echo ok || echo missing)"

# A deployment's behaviour has to be attributable to its configuration. Before
# this the server logged nothing about auth, the rate limit or the embedding
# provider, so a setting that had been silently replaced by a default -- which
# is what an unparseable value used to do -- left no trace anywhere.
# The whole log, not a tail: this line is written once at startup, so on a pod
# that has been serving for a while it sits thousands of lines back. A --tail
# window made the check report a missing line as a missing feature.
cfg_line=$(kubectl logs -n "$NS" deploy/context0-api 2>/dev/null \
  | grep '"msg":"configuration"' | head -1)
check "the server logs its effective configuration" "1" \
  "$([[ -n "$cfg_line" ]] && echo 1 || echo 0)"
check "the configuration log reports the rate limit in force" "6000" \
  "$(grep -oE '"rate_limit_per_minute":[0-9]+' <<<"$cfg_line" | cut -d: -f2)"
# The key count, never the keys.
check "the configuration log counts keys without printing them" "0" \
  "$(grep -c "$key" <<<"$cfg_line" || true)"

check "stored keys are hashes, not the plaintext key" "0" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c 'cat /proc/1/environ 2>/dev/null | tr "\0" "\n" | grep -c "^CONTEXT0_API_KEYS=$key$"' 2>/dev/null || echo 0)"
check "the API key never appears in logs" "0" \
  "$(kubectl logs -n "$NS" deploy/context0-api --tail=500 2>/dev/null | grep -c "$key" || true)"

section "12. Pod identity and network isolation"
# Every pod used to run as the namespace `default` service account with its
# token mounted, and none of these workloads call the Kubernetes API. A token
# that is never used is a credential available to steal: it turns code execution
# in a container into an authenticated identity in the cluster.
check "no service account token is mounted in the API pod" "absent" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
    'test -d /var/run/secrets/kubernetes.io/serviceaccount && echo present || echo absent' 2>/dev/null)"
check "the API runs as its own service account, not default" "context0-api-sa" \
  "$(kubectl get pod -n "$NS" "$(api_pod)" -o jsonpath='{.spec.serviceAccountName}')"

# The rule that matters: before this, a pod in an unrelated namespace connected
# straight to Postgres and read every memory row, bypassing the API's
# authentication and rate limiting entirely.
#
# NetworkPolicy is enforced by the CNI, not by Kubernetes, so this asserts
# behaviour rather than the existence of the object -- on a CNI that ignores
# policy the resource applies cleanly and does nothing.
if kubectl get networkpolicy -n "$NS" postgres-age >/dev/null 2>&1; then
  reached=$(kubectl run netpol-probe-$$ --rm -i --restart=Never -n default \
    --image=busybox:1.36 --quiet --command -- \
    timeout 8 nc -z -w 5 postgres-age."$NS".svc.cluster.local 5432 2>/dev/null \
    && echo reachable || echo blocked)
  check "Postgres is unreachable from another namespace" "blocked" "$reached"
fi

# ...and the allowed path must still work, or the policy has simply broken the
# deployment rather than secured it.
# Every workload must satisfy the Pod Security "restricted" profile, not just
# the API. Labelling the namespace revealed that Postgres ran as root with full
# capabilities and the web UI ran as root, because only the API had ever been
# given a securityContext.
for wl in "deploy/context0-api" "statefulset/postgres-age" "deploy/context0-web"; do
  uid=$(kubectl get "$wl" -n "$NS" -o jsonpath='{.spec.template.spec.securityContext.runAsUser}' 2>/dev/null)
  check "$wl runs as a non-root uid" "nonroot" \
    "$([[ -n "$uid" && "$uid" != "0" ]] && echo nonroot || echo "root(${uid:-unset})")"
  check "$wl drops all capabilities" "ALL" \
    "$(kubectl get "$wl" -n "$NS" -o jsonpath='{.spec.template.spec.containers[0].securityContext.capabilities.drop[0]}' 2>/dev/null)"
  check "$wl sets a seccomp profile" "RuntimeDefault" \
    "$(kubectl get "$wl" -n "$NS" -o jsonpath='{.spec.template.spec.securityContext.seccompProfile.type}' 2>/dev/null)"
done

# The web UI must still actually serve after being moved off port 80 to run
# unprivileged: hardening that breaks the product is not hardening.
web_np=$(kubectl get svc context0-web -n "$NS" -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null)
if [[ -n "$web_np" ]]; then
  check "the web UI still serves through its NodePort as non-root" "200" \
    "$(docker exec "${KIND_NODE:-context0-dev-control-plane}" \
      curl -s -o /dev/null -w '%{http_code}' "http://localhost:$web_np/" 2>/dev/null)"
fi

# The UI accepts an API key as a ?key= URL parameter for convenience. A
# credential in a URL ends up in browser history, Referer headers, and every
# access log on the path -- verified before the fix by loading /?key=ctx0_...
# and finding it verbatim in the web pod's log.
web_np_leak=$(kubectl get svc context0-web -n "$NS" -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null)
if [[ -n "$web_np_leak" ]]; then
  probe_secret="ctx0_verify_probe_$$"
  docker exec "${KIND_NODE:-context0-dev-control-plane}" \
    curl -s -o /dev/null "http://localhost:$web_np_leak/?key=$probe_secret" 2>/dev/null || true
  sleep 2
  check "the web server does not log API keys from URLs" "0" \
    "$(kubectl logs -n "$NS" deploy/context0-web --tail=30 2>/dev/null | grep -c "$probe_secret" || true)"
  check "the web server sets a Referrer-Policy" "same-origin" \
    "$(docker exec "${KIND_NODE:-context0-dev-control-plane}" \
      curl -sI "http://localhost:$web_np_leak/" 2>/dev/null \
      | awk -F': ' 'tolower($1)=="referrer-policy"{print $2}' | tr -d '\r')"

  # The API key lives in a JavaScript variable in the page and is never
  # persisted, which is the right design -- but it makes the page itself the
  # credential store, so what protects the page protects the key. Verified
  # absent against the running deployment before these were added: Chrome
  # loaded the UI inside a foreign iframe with no complaint.
  web_hdr() {
    docker exec "${KIND_NODE:-context0-dev-control-plane}" \
      curl -sI "http://localhost:$web_np_leak$2" 2>/dev/null \
      | awk -F': ' -v h="$1" 'tolower($1)==h{print $2}' | tr -d '\r'
  }

  check "the UI refuses to be framed" "DENY" "$(web_hdr x-frame-options /)"
  check "the UI sets a Content-Security-Policy" "present" \
    "$([[ -n "$(web_hdr content-security-policy /)" ]] && echo present || echo absent)"
  # frame-ancestors is what a modern browser actually enforces; Chrome blocks
  # the load with "violates the following Content Security Policy directive".
  check "the CSP forbids framing" "yes" \
    "$(grep -q "frame-ancestors 'none'" <<<"$(web_hdr content-security-policy /)" && echo yes || echo no)"
  # Without this, a served asset can be reinterpreted as script from sniffed
  # content rather than its declared type.
  check "the UI sets X-Content-Type-Options" "nosniff" \
    "$(web_hdr x-content-type-options /)"
  # The version is not a secret, but publishing the exact build in every
  # response saves an attacker working out which CVEs apply.
  check "the web server does not advertise its version" "no" \
    "$(grep -qE '[0-9]+\.[0-9]+' <<<"$(web_hdr server /)" && echo yes || echo no)"
  # Headers must be present on the SPA fallback too: a 404 rendered by the app
  # is the same origin holding the same key.
  check "hardening headers survive the SPA fallback" "DENY" \
    "$(web_hdr x-frame-options /no-such-route)"

  # Hardening that breaks the product is not hardening. The bundle and
  # stylesheet must still load, with the MIME types nosniff now enforces.
  ui_assets=$(docker exec "${KIND_NODE:-context0-dev-control-plane}" \
    curl -s "http://localhost:$web_np_leak/" 2>/dev/null | grep -oE '/assets/[^"]+' || true)
  check "the page references its built assets" "yes" \
    "$([[ -n "$ui_assets" ]] && echo yes || echo no)"
  for asset in $ui_assets; do
    want=css; case "$asset" in *.js) want=javascript;; esac
    check "asset $asset is served with a $want content-type" "yes" \
      "$(grep -qi "$want" <<<"$(web_hdr content-type "$asset")" && echo yes || echo no)"
  done
fi

check "the API can still reach Postgres through the policy" "200" \
  "$(kubectl exec -n "$NS" "$(api_pod)" -- wget -q -S -O /dev/null http://localhost:8080/readyz 2>&1 \
    | awk '/HTTP\//{print $2; exit}')"

section "13. Metrics are usable, not merely present"
mx=$(kubectl exec -n "$NS" "$(api_pod)" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null)

# Pool exhaustion is this service's most likely saturation point and used to be
# invisible: a deadlock here once presented as uniformly slow requests with no
# metric naming the cause.
check "connection pool occupancy is exposed" "context0_pool_connections" \
  "$(grep -o 'context0_pool_connections' <<<"$mx" | head -1)"
check "pool acquire wait is exposed" "context0_pool_acquire_wait_seconds_total" \
  "$(grep -o 'context0_pool_acquire_wait_seconds_total' <<<"$mx" | head -1)"

# RED for every method, not just the two that were instrumented by hand: a
# failing Extract or GetProfile previously produced no metric at all.
check "per-method request counters exist" "context0_requests_total" \
  "$(grep -o 'context0_requests_total' <<<"$mx" | head -1)"
check "request counters are labelled by status code" "code" \
  "$(grep -o 'context0_requests_total{code=' <<<"$mx" | head -1 | grep -o 'code')"

# The histogram must be able to tell p50 from p99. With the default buckets,
# both landed in [0.1, 0.25] along with 79% of all samples.
lo=$(grep -c 'context0_request_duration_seconds_bucket{.*le="0.125"' <<<"$mx" || true)
check "latency buckets resolve the range this service operates in" "present" \
  "$([[ "$lo" -gt 0 ]] && echo present || echo missing)"

section "14. Session lifecycle accounting"
# context0_active_sessions is a gauge decremented by EndSession. EndSession
# used to accept a repeat end, so a retried request -- a client timeout, an
# at-least-once queue -- decremented it again and drove the gauge negative:
# one start plus three ends left it at -2. Anything alerting or scaling on it
# was then reading a number that could not occur.
key=$(kubectl get secret -n "$NS" context0-api-keys -o jsonpath='{.data}' \
  | python3 -c "import sys,json,base64; d=json.load(sys.stdin); print(base64.b64decode(list(d.values())[0]).decode().split(',')[0])")

gauge() {
  kubectl exec -n "$NS" "$(api_pod)" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null \
    | awk '/^context0_active_sessions /{print $2; exit}'
}

before_gauge=$(gauge)
sid=$(kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
  "wget -q -O- --header='X-API-Key: $key' --header='Content-Type: application/json' \
   --post-data='{\"project_id\":\"verify-session\",\"agent_id\":\"verify\"}' \
   http://localhost:8080/v1/sessions 2>/dev/null" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['session']['id'])" 2>/dev/null)

check "a session can be started" "started" \
  "$([[ -n "$sid" ]] && echo started || echo failed)"

end_status() {
  kubectl exec -n "$NS" "$(api_pod)" -- sh -c \
    "wget -q -S -O /dev/null --header='X-API-Key: $key' --header='Content-Type: application/json' \
     --post-data='{}' 'http://localhost:8080/v1/sessions/$1/end' 2>&1" 2>/dev/null \
    | awk '/HTTP\//{print $2; exit}'
}

check "the first end succeeds" "200" "$(end_status "$sid")"
# 409 Conflict, not 400: the request is well-formed and merely late, and a
# client cannot tell a retry-to-ignore from a bug it must fix if both are 400.
check "a repeated end is rejected as a conflict" "409" "$(end_status "$sid")"
check "a third end is still rejected" "409" "$(end_status "$sid")"
check "ending an unknown session is not found" "404" \
  "$(end_status 6f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8)"

# The invariant that actually matters: after one start and three ends the
# gauge is exactly where it began.
check "the active-session gauge returns to its starting value" "$before_gauge" "$(gauge)"

section "15. CLI behaviour against the live engine"
# The CLI is how an operator inspects a deployment, so a wrong answer here is
# acted on. Every failure found in it had the same shape: bad input produced a
# confident, successful-looking result.
#
# `context0 stats` with a rejected key printed "Nodes: 0" and exited 0. Health
# deliberately answers without a credential -- probes cannot present one -- and
# withholds statistics from callers it cannot authenticate, so a rejected key
# comes back as a successful response full of zeros. Rendered verbatim, a typo
# in CONTEXT0_API_KEY was indistinguishable from an empty database, and no
# script would catch it.
cli_bin="$(mktemp -d)/context0"
if go build -o "$cli_bin" ./cmd/cli 2>/dev/null; then
  grpc_pf_port=15099
  kubectl port-forward -n "$NS" svc/context0-api "$grpc_pf_port:50051" >/dev/null 2>&1 &
  cli_pf_pid=$!
  trap 'kill "$cli_pf_pid" 2>/dev/null || true' EXIT
  sleep 4

  cli() { CONTEXT0_ENDPOINT="localhost:$grpc_pf_port" CONTEXT0_PROJECT=verify-cli "$@"; }

  # A rejected key must fail loudly rather than render zeros as data.
  cli_out=$(cli env CONTEXT0_API_KEY=definitely-not-a-real-key "$cli_bin" stats 2>&1 || true)
  # Capture the status immediately; $? reflects only the previous command, and
  # any check in between would overwrite it.
  cli env CONTEXT0_API_KEY=definitely-not-a-real-key "$cli_bin" stats >/dev/null 2>&1
  cli_bad_status=$?
  check "a rejected API key makes the CLI exit non-zero" "nonzero" \
    "$([[ $cli_bad_status -ne 0 ]] && echo nonzero || echo zero)"
  check "a rejected API key is named as the cause" "rejected" \
    "$(grep -qi 'rejected\|API key' <<<"$cli_out" && echo rejected || echo silent)"
  check "a rejected key does not render zero counts as data" "no" \
    "$(grep -qE '^ *Nodes: +0' <<<"$cli_out" && echo yes || echo no)"

  # And a valid key still works, or the check above is a regression in disguise.
  cli_ok=$(cli env CONTEXT0_API_KEY="$key" "$cli_bin" stats 2>&1 || true)
  check "a valid API key still returns real statistics" "ok" \
    "$(grep -qE 'Nodes: +[1-9]' <<<"$cli_ok" && echo ok || echo missing)"

  # An unrecognised type used to fall through to semantic, filing the memory
  # under the wrong type with no indication.
  cli_type=$(cli env CONTEXT0_API_KEY="$key" "$cli_bin" store "verify-cli probe" --type=bogus 2>&1 || true)
  check "an unknown memory type is rejected" "rejected" \
    "$(grep -qi 'unknown memory type' <<<"$cli_type" && echo rejected || echo accepted)"

  # Silence the shell's job-termination notice, which otherwise prints a
  # "Terminated" line into the middle of the report.
  kill "$cli_pf_pid" 2>/dev/null || true
  wait "$cli_pf_pid" 2>/dev/null || true
  trap - EXIT
else
  printf '  skipped: go toolchain unavailable, cannot build the CLI\n'
fi

section "16. Multi-replica behaviour (only when replicas > 1)"
replicas=$(kubectl get deploy context0-api -n "$NS" -o jsonpath='{.spec.replicas}')
if [[ "${replicas:-1}" -gt 1 ]]; then
  check "a PodDisruptionBudget exists" "context0-api" \
    "$(kubectl get pdb context0-api -n "$NS" -o jsonpath='{.metadata.name}' 2>/dev/null)"

  # The PDB must actually gate evictions, not merely exist. `kubectl delete`
  # bypasses PDBs entirely, so this uses the eviction API -- which is what a
  # node drain uses, and the only thing that exercises the guarantee.
  # Read into an array without mapfile: macOS ships bash 3.2, where mapfile
  # does not exist and the array silently stays empty.
  pods=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && pods+=("$line")
  done < <(kubectl get pods -n "$NS" -l app=context0-api \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  evict() {
    kubectl create -f - --raw "/api/v1/namespaces/$NS/pods/$1/eviction" >/dev/null 2>&1 <<EOF && echo allowed || echo blocked
{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":"$1","namespace":"$NS"}}
EOF
  }
  if [[ "${#pods[@]}" -lt 2 ]]; then
    check "at least two running pods to evict" "2+" "${#pods[@]}"
  else
    check "the first eviction is allowed" "allowed" "$(evict "${pods[0]}")"
    check "the second concurrent eviction is refused" "blocked" "$(evict "${pods[1]}")"
  fi

  # Wait for the fleet to come back before anything else runs.
  kubectl rollout status deploy/context0-api -n "$NS" --timeout=180s >/dev/null 2>&1
else
  printf '  skipped: replicas=%s (PDB and topology spread are gated on >1)\n' "${replicas:-1}"
fi

section "17. Recoverability"
# /dev/shm defaults to 64Mi in Kubernetes, which is not enough to build the
# pgvector HNSW index in one allocation: at 94k embeddings the build asked for
# 131MB and failed. The live database never hits this, because its index grew a
# row at a time -- it only appears when the index is rebuilt, which is exactly
# what restoring a backup does. pg_restore then reports success having skipped
# the index, and the API treats a failed index build as fatal at startup, so the
# deployment cannot come up on the recovered data.
shm_mb=$(kubectl exec -n "$NS" postgres-age-0 -- sh -c \
  "df -m /dev/shm | awk 'NR==2{print \$2}'" 2>/dev/null)
check "/dev/shm is larger than the 64Mi default" "ok" \
  "$([[ "${shm_mb:-0}" -gt 64 ]] && echo ok || echo "only ${shm_mb:-?}Mi")"

# A backup is only a backup if it restores. This asserts the index survives the
# round trip, because that is the part that fails silently.
check "the HNSW vector index can be rebuilt at the current data size" "ok" \
  "$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d "$DB_NAME" -tAc \
    "SET maintenance_work_mem='128MB';
     CREATE INDEX IF NOT EXISTS shm_probe_idx ON public.memory_embeddings
       USING hnsw (embedding vector_cosine_ops);
     DROP INDEX IF EXISTS shm_probe_idx;" >/dev/null 2>&1 && echo ok || echo "index build failed")"

printf '\n\033[1m%s\033[0m\n' "=== $PASS passed, $FAIL failed ==="
for f in "${FAILURES[@]:-}"; do [[ -n "$f" ]] && printf '  failed: %s\n' "$f"; done
exit $((FAIL > 0 ? 1 : 0))
