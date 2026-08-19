#!/usr/bin/env bash
#
# verify_perf.sh -- re-measure every performance claim this project makes,
# against the current build, and report the observed value next to the claim.
#
# Why this exists: the performance work in this repository was measured as each
# change was made, at whatever graph size existed at the time. That is how one
# claim silently became false -- the UNWIND form in GetContextEdges genuinely
# planned an index scan at 50k vertices and had stopped by 64k -- and it is why
# "measured once" is not the same as "true now". A plan is a property of the
# data, not of the query.
#
# Every check below states the claim, the requirement it comes from, and the
# threshold that would make it false. Thresholds are deliberately loose relative
# to the measured value: this must catch a regression, not a busy laptop.
#
# Usage: scripts/verify_perf.sh
set -uo pipefail

NS="${NS:-context0}"
KEY="${CONTEXT0_API_KEY:?set CONTEXT0_API_KEY}"

PASS=0
FAIL=0
FAILURES=()

api_pod() {
  kubectl get pod -n "$NS" -l app=context0-api \
    -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name} {.status.containerStatuses[0].ready}{"\n"}{end}' \
    2>/dev/null | awk '$2=="true"{print $1; exit}'
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# report NAME CLAIM OBSERVED THRESHOLD COMPARISON
#   comparison: "below" (observed must be < threshold) or "above"
report() {
  local name="$1" claim="$2" observed="$3" threshold="$4" cmp="$5"
  # Force numeric comparison. awk compares two strings lexically when either
  # side is not a number, so "55.2ms" < "150" evaluates false because "5" > "1"
  # -- which reported a passing measurement as a failure. +0 coerces, and the
  # strip keeps the unit in the printed output where it belongs.
  local n="${observed%%[a-z]*}"
  local ok
  if [[ "$cmp" == "below" ]]; then
    ok=$(awk -v o="$n" -v t="$threshold" 'BEGIN{print ((o+0)<(t+0))?"1":"0"}')
  else
    ok=$(awk -v o="$n" -v t="$threshold" 'BEGIN{print ((o+0)>(t+0))?"1":"0"}')
  fi
  if [[ "$ok" == "1" ]]; then
    printf '  \033[32mPASS\033[0m  %s\n        claim: %s\n        observed: %s (limit: %s %s)\n' \
      "$name" "$claim" "$observed" "$cmp" "$threshold"
    PASS=$((PASS + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n        claim: %s\n        observed: %s (limit: %s %s)\n' \
      "$name" "$claim" "$observed" "$cmp" "$threshold"
    FAIL=$((FAIL + 1))
    FAILURES+=("$name")
  fi
}

POD=$(api_pod)
[[ -n "$POD" ]] || { echo "no ready API pod in $NS" >&2; exit 1; }

VERTICES=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   SELECT count(*) FROM cypher('context0', \$\$ MATCH (m:Memory) RETURN m \$\$) AS (m agtype);" 2>/dev/null | tail -1)
EDGES=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   SELECT count(*) FROM cypher('context0', \$\$ MATCH ()-[e]->() RETURN e \$\$) AS (e agtype);" 2>/dev/null | tail -1)

printf '\033[1mMeasured against the current build: %s vertices, %s edges\033[0m\n' "$VERTICES" "$EDGES"

# Mean latency of one RPC, from the RED histogram: (sum after - sum before) /
# (count after - count before). Reading the histogram rather than timing the
# client removes the client and the network from the number.
rpc_mean_ms() {
  local method="$1" script="$2"
  local before after
  before=$(kubectl exec -n "$NS" "$POD" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null \
    | grep -E "context0_request_duration_seconds_(sum|count)\{method=\"$method\"" | awk '{printf "%s ", $2}')
  kubectl exec -n "$NS" "$POD" -- sh "$script" >/dev/null 2>&1
  after=$(kubectl exec -n "$NS" "$POD" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null \
    | grep -E "context0_request_duration_seconds_(sum|count)\{method=\"$method\"" | awk '{printf "%s ", $2}')
  awk -v b="$before" -v a="$after" 'BEGIN{
    split(b, bb, " "); split(a, aa, " ");
    d = aa[2] - bb[2];
    if (d <= 0) { print "0"; exit }
    printf "%.1f", (aa[1] - bb[1]) / d * 1000
  }'
}

# --- Query loop -------------------------------------------------------------
cat > /tmp/vp_query.sh <<SH
i=0
while [ \$i -lt 20 ]; do
  wget -q -O /dev/null --header="X-API-Key: $KEY" \\
    "http://localhost:8080/v1/memories/query?query=prometheus&project_id=soak-67jn8n-0&top_k=10"
  i=\$((i+1))
done
SH
kubectl cp /tmp/vp_query.sh "$NS/$POD:/tmp/vp_query.sh" >/dev/null 2>&1

cat > /tmp/vp_store.sh <<SH
i=0
while [ \$i -lt 20 ]; do
  wget -q -O /dev/null --header="X-API-Key: $KEY" --header="Content-Type: application/json" \\
    --post-data="{\"content\":\"perf probe \$i prometheus metrics\",\"project_id\":\"perfcheck\",\"type\":\"MEMORY_TYPE_SEMANTIC\",\"tags\":[\"metrics\"]}" \\
    http://localhost:8080/v1/memories
  i=\$((i+1))
done
SH
kubectl cp /tmp/vp_store.sh "$NS/$POD:/tmp/vp_store.sh" >/dev/null 2>&1

cat > /tmp/vp_health.sh <<'SH'
i=0
while [ $i -lt 20 ]; do
  wget -q -O /dev/null http://localhost:8080/v1/health
  i=$((i+1))
done
SH
kubectl cp /tmp/vp_health.sh "$NS/$POD:/tmp/vp_health.sh" >/dev/null 2>&1

section "1. Query latency does not track total graph size (a661212)"
# Requirement: replacing UNWIND with literal id lists made a scoped query
# 90.9ms -> 16.2ms at 64k vertices. The property that matters is not the exact
# number but that it stays bounded as the graph grows: the defect being fixed
# was cost scaling with the size of the database rather than the request.
q=$(rpc_mean_ms "/context0.v1.Context0/Query" /tmp/vp_query.sh)
report "scoped query, idle" \
  "90.9ms -> 16.2ms at 64k vertices; must stay bounded as the graph grows" \
  "${q}ms" "60" "below"

section "2. Store latency (a661212, and the maxSupersedesPerStore cap)"
# Store is the whole pipeline: create, embed, contradiction detection, edge
# writes, tag auto-linking. The cap on supersedes edges exists to stop this
# growing with writes x candidates.
s=$(rpc_mean_ms "/context0.v1.Context0/Store" /tmp/vp_store.sh)
report "tagged store, idle" \
  "~38ms at 94k vertices; the cap prevents the ~469ms uncapped case" \
  "${s}ms" "150" "below"

section "3. /v1/health is cached (e317412)"
# Requirement: /v1/health did two full graph scans per call on an
# unauthenticated endpoint. 2196ms p50 under load -> 2.8ms.
h=$(rpc_mean_ms "/context0.v1.HealthService/Health" /tmp/vp_health.sh)
report "health, 20 serial calls" \
  "2196ms -> 2.8ms p50; counts cached for 5s, reachability never cached" \
  "${h}ms" "60" "below"

section "4. Index usage, re-checked at the current size (d5b2e72, a661212)"
# These are plan assertions, not timings: the defect they guard against is AGE
# silently choosing a sequential scan, which is what happened when the graph
# grew past the size the original measurement was taken at.
plan_uses_index() {
  kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -c \
    "LOAD 'age'; SET search_path=ag_catalog,public;
     EXPLAIN SELECT * FROM cypher('context0', \$\$ $1 \$\$) AS (x agtype);" 2>/dev/null \
    | grep -cE "Index Scan using memory_id_idx|Bitmap Index Scan on memory_id_idx"
}
report "id lookup uses memory_id_idx" \
  "property indexes turn an id lookup from a sequential scan into an index scan" \
  "$(plan_uses_index "MATCH (m:Memory) WHERE m.id = 'x' RETURN m")" "0" "above"

report "literal IN list uses the index" \
  "literal IN plans as an index scan; both parameterized forms scan the label" \
  "$(plan_uses_index "MATCH (m:Memory)-[e]->(n:Memory) WHERE m.id IN ['a','b'] RETURN e")" "0" "above"

# The regression that started this: the same query as a parameter must NOT be
# used, which is why the code inlines. Asserted so that if a future AGE version
# fixes it, this check tells us the workaround can be removed.
unwind_scans=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -c \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   EXPLAIN SELECT * FROM cypher('context0', \$\$ UNWIND ['a','b'] AS w MATCH (m:Memory) WHERE m.id = w RETURN m \$\$) AS (x agtype);" 2>/dev/null \
  | grep -c "Seq Scan")
# Informational, not pass/fail. The parameterized form reaches the index while
# statistics are fresh and collapses to a full scan when they are not, so
# whichever plan appears here is correct for the current state of the database
# rather than evidence for or against the workaround. Section 5b checks the
# thing that actually decides it.
if [[ "$unwind_scans" -gt 0 ]]; then
  printf '  \033[33mNOTE\033[0m  parameterized UNWIND is planning a sequential scan.\n        Expected when statistics are stale; the literal-list form does not\n        depend on them. See section 5b.\n'
else
  printf '  \033[33mNOTE\033[0m  parameterized UNWIND is currently planning an index scan.\n        Expected with fresh statistics. The literal form is kept because it\n        holds up when they are stale (158.7ms vs 0.23ms measured). See 5b.\n'
fi

section "5. Undirected traversal still degrades (getEdgesAround)"
# 71.5ms vs 0.05ms at 50k; 734ms vs 14ms at 94k. The gap widens with the graph,
# which is the signature of a full label scan.
und=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   EXPLAIN (ANALYZE, TIMING OFF) SELECT * FROM cypher('context0', \$\$ MATCH (c)-[e]-(o:Memory) WHERE c.id = 'nonexistent' RETURN e \$\$) AS (e agtype);" 2>/dev/null \
  | grep "Execution Time" | grep -oE "[0-9.]+")
dir=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   EXPLAIN (ANALYZE, TIMING OFF) SELECT * FROM cypher('context0', \$\$ MATCH (c)-[e]->(o:Memory) WHERE c.id = 'nonexistent' RETURN e \$\$) AS (e agtype);" 2>/dev/null \
  | grep "Execution Time" | grep -oE "[0-9.]+")
printf '  observed: undirected %sms vs directed %sms\n' "$und" "$dir"
report "two directed matches beat one undirected" \
  "AGE cannot drive an undirected pattern from the edge indexes" \
  "$(awk -v u="$und" -v d="$dir" 'BEGIN{ if (d<=0) d=0.001; printf "%.1f", u/d }')" "2" "above"

section "5b. The literal-list workaround is still earning its place"
# The parameterized form reaches the index while statistics are fresh, so the
# original "AGE cannot use the index" reading was incomplete -- it was really
# observing stale statistics. The literal form is kept because it does not
# depend on statistics at all, and the divergence appears exactly when it hurts
# (after a bulk import or a restore, before autoanalyze catches up).
#
# This reports whether statistics are currently fresh, so a future reader knows
# which regime any timing above was measured in.
mod=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "SELECT n_mod_since_analyze FROM pg_stat_user_tables WHERE relname='Memory';" 2>/dev/null | tail -1)
live=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U context0 -d context0 -tAc \
  "SELECT greatest(n_live_tup,1) FROM pg_stat_user_tables WHERE relname='Memory';" 2>/dev/null | tail -1)
printf '  observed: %s rows modified since the last ANALYZE, of %s live\n' "${mod:-?}" "${live:-?}"
report "planner statistics are being maintained" \
  "autoanalyze must keep expression-index stats fresh; the literal form survives when it does not" \
  "$(awk -v m="${mod:-0}" -v l="${live:-1}" 'BEGIN{printf "%.2f", m/l}')" "0.5" "below"

section "6. Rate limit permits real throughput (e317412)"
# The default was 100/min (1.6/s), which had never run because rate limiting
# only engages once a key is configured. Enabling auth would have throttled
# every deployment to a crawl.
rl=$(kubectl exec -n "$NS" "$POD" -- printenv CONTEXT0_RATE_LIMIT_PER_MINUTE 2>/dev/null)
report "configured rate limit" \
  "6000/min (100/s), sized against a ~4ms store; 100/min was unusable" \
  "${rl:-0}" "1000" "above"

section "7. Latency buckets resolve the operating range (46c642e)"
# With DefBuckets, p50/p95/p99 all landed in [0.1, 0.25] alongside 79% of
# samples, so no percentile could be distinguished from another.
mx=$(kubectl exec -n "$NS" "$POD" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null)
b125=$(grep -c 'context0_request_duration_seconds_bucket{.*le="0.125"' <<<"$mx" || true)
report "sub-decade bucket boundaries present" \
  "buckets must separate p50 from p99 in this service's range" \
  "$b125" "0" "above"

section "8. Connection pool is not saturated at rest"
acq=$(grep '^context0_pool_connections{state="acquired"}' <<<"$mx" | awk '{print $2}')
max=$(grep '^context0_pool_connections{state="max"}' <<<"$mx" | awk '{print $2}')
printf '  observed: %s of %s connections acquired\n' "${acq:-?}" "${max:-?}"
report "pool has headroom at rest" \
  "every request waits on this pool; acquired == max is a stall, not load" \
  "$(awk -v a="${acq:-0}" -v m="${max:-1}" 'BEGIN{printf "%.2f", a/m}')" "0.9" "below"

printf '\n\033[1m%s\033[0m\n' "=== $PASS passed, $FAIL failed ==="
for f in "${FAILURES[@]:-}"; do [[ -n "$f" ]] && printf '  failed: %s\n' "$f"; done
exit $((FAIL > 0 ? 1 : 0))
