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

NS="${NS:-kora}"
KEY="${KORA_API_KEY:?set KORA_API_KEY}"

PASS=0
FAIL=0
FAILURES=()

api_pod() {
  kubectl get pod -n "$NS" -l app=kora-api \
    -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name} {.status.containerStatuses[0].ready}{"\n"}{end}' \
    2>/dev/null | awk '$2=="true"{print $1; exit}'
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# report NAME CLAIM OBSERVED THRESHOLD COMPARISON [KIND]
#   comparison: "below" (observed must be < threshold) or "above"
#   kind: "latency" reports without enforcing when the dataset is too small
report() {
  local name="$1" claim="$2" observed="$3" threshold="$4" cmp="$5" kind="${6:-}"

  if [[ "$kind" == "latency" && "$LATENCY_MEANINGFUL" -eq 0 ]]; then
    printf '  \033[33mINFO\033[0m  %s\n        claim: %s\n        observed: %s (not enforced: dataset too small)\n' \
      "$name" "$claim" "$observed"
    return
  fi
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

VERTICES=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   SELECT count(*) FROM cypher('context0', \$\$ MATCH (m:Memory) RETURN m \$\$) AS (m agtype);" 2>/dev/null | tail -1)
EDGES=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   SELECT count(*) FROM cypher('context0', \$\$ MATCH ()-[e]->() RETURN e \$\$) AS (e agtype);" 2>/dev/null | tail -1)

printf '\033[1mMeasured against the current build: %s vertices, %s edges\033[0m\n' "$VERTICES" "$EDGES"

# Latency thresholds only mean something once there is enough data for a
# regression to show. Below this, everything is fast because the database is
# empty, and a pass would be evidence of nothing. The structural checks -- index
# capability, configuration, bucket boundaries -- run at any size.
MIN_VERTICES_FOR_LATENCY="${MIN_VERTICES_FOR_LATENCY:-5000}"
LATENCY_MEANINGFUL=1
if [[ "${VERTICES:-0}" -lt "$MIN_VERTICES_FOR_LATENCY" ]]; then
  LATENCY_MEANINGFUL=0
  printf '\033[33mNote: only %s vertices; latency thresholds are reported but not enforced\n' "$VERTICES"
  printf '      (below %s rows everything is fast and a pass proves nothing).\033[0m\n' "$MIN_VERTICES_FOR_LATENCY"
fi

# Mean latency of one RPC, from the RED histogram: (sum after - sum before) /
# (count after - count before). Reading the histogram rather than timing the
# client removes the client and the network from the number.
rpc_mean_ms() {
  local method="$1" script="$2"
  local before after
  before=$(kubectl exec -n "$NS" "$POD" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null \
    | grep -E "kora_request_duration_seconds_(sum|count)\{method=\"$method\"" | awk '{printf "%s ", $2}')
  kubectl exec -n "$NS" "$POD" -- sh "$script" >/dev/null 2>&1
  after=$(kubectl exec -n "$NS" "$POD" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null \
    | grep -E "kora_request_duration_seconds_(sum|count)\{method=\"$method\"" | awk '{printf "%s ", $2}')
  awk -v b="$before" -v a="$after" 'BEGIN{
    split(b, bb, " "); split(a, aa, " ");
    d = aa[2] - bb[2];
    if (d <= 0) { print "0"; exit }
    printf "%.1f", (aa[1] - bb[1]) / d * 1000
  }'
}

# --- Query loop -------------------------------------------------------------
#
# Seeded into a fresh project of known size rather than pointed at whatever a
# previous soak happened to leave behind.
#
# This probe used to target "soak-67jn8n-0", a leftover from one specific soak
# run hardcoded into the script. Soaks generate a new project id each time, so
# that project only ever grew or went stale, and the measurement tracked how
# many soaks had run rather than how the service performs: 91ms, 122ms and
# 155ms on three consecutive runs against identical data. A latency figure that
# swings 70% run to run cannot support a claim about latency.
#
# The claim being checked is that a scoped query does not track TOTAL graph
# size, so the project needs a fixed number of memories inside a large graph --
# which is exactly what seeding one gives.
QUERY_PROJECT="perfquery-$(date +%s)-$$"
cat > /tmp/vp_seed_query.sh <<SH
i=0
while [ \$i -lt 50 ]; do
  wget -q -O /dev/null --header="X-API-Key: $KEY" --header="Content-Type: application/json" \\
    --post-data="{\"content\":\"prometheus metrics probe \$i for query latency\",\"project_id\":\"$QUERY_PROJECT\",\"type\":\"MEMORY_TYPE_SEMANTIC\",\"tags\":[\"metrics\"]}" \\
    http://localhost:8080/v1/memories
  i=\$((i+1))
done
SH
kubectl cp /tmp/vp_seed_query.sh "$NS/$POD:/tmp/vp_seed_query.sh" >/dev/null 2>&1
kubectl exec -n "$NS" "$POD" -- sh /tmp/vp_seed_query.sh >/dev/null 2>&1

# Let the writes settle before measuring reads.
#
# Each seeded store runs contradiction detection, whose subject-verb-object
# lookup takes over a second on a graph this size. Measuring the query straight
# afterwards timed those writes as much as the query: 67ms, 148ms and 220ms on
# three consecutive runs. Seeding was added to remove variance and introduced
# a different source of it, which is only visible by looking at what the
# database was actually executing.
sleep 3

cat > /tmp/vp_query.sh <<SH
i=0
while [ \$i -lt 20 ]; do
  wget -q -O /dev/null --header="X-API-Key: $KEY" \\
    "http://localhost:8080/v1/memories/query?query=prometheus&project_id=$QUERY_PROJECT&top_k=10"
  i=\$((i+1))
done
SH
kubectl cp /tmp/vp_query.sh "$NS/$POD:/tmp/vp_query.sh" >/dev/null 2>&1

# A fresh project per run.
#
# Store fans out into contradiction detection across the target project's
# existing memories, so writing into a fixed project made every run slower than
# the last: 20 memories per run had accumulated to 520, and this check began
# failing at 241ms against a 150ms limit that was set when the project was
# nearly empty. The service had not regressed -- the measurement had drifted by
# construction, which is the same flaw as the earlier run against zero
# vertices. A per-run project keeps the number reproducible.
STORE_PROJECT="perfcheck-$(date +%s)-$$"

cat > /tmp/vp_store.sh <<SH
i=0
while [ \$i -lt 20 ]; do
  wget -q -O /dev/null --header="X-API-Key: $KEY" --header="Content-Type: application/json" \\
    --post-data="{\"content\":\"perf probe \$i prometheus metrics\",\"project_id\":\"$STORE_PROJECT\",\"type\":\"MEMORY_TYPE_SEMANTIC\",\"tags\":[\"metrics\"]}" \\
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
# A discarded warm-up pass: the first request through a pooled connection pays
# setup the rest do not, and 20 requests is few enough for one cold connection
# to dominate the mean.
kubectl exec -n "$NS" "$POD" -- sh /tmp/vp_query.sh >/dev/null 2>&1
q=$(rpc_mean_ms "/kora.v1.Kora/Query" /tmp/vp_query.sh)
# The threshold defends bounded-ness, not a specific number. 16.2ms was measured
# at 64k vertices; at 256k -- four times the graph -- a seeded 50-memory project
# measures 43-64ms across eight runs, so cost did not scale with the graph. 100
# leaves room for that spread while still failing if the query starts tracking
# total graph size, which is the defect the literal-list form fixed.
report "scoped query, idle" \
  "90.9ms at 64k vertices before the fix; 43-64ms at 256k after, so cost does not track graph size" \
  "${q}ms" "100" "below" "latency"

section "2. Store latency (a661212, and the maxSupersedesPerStore cap)"
# Store is the whole pipeline: create, embed, contradiction detection, edge
# writes, tag auto-linking. The cap on supersedes edges exists to stop this
# growing with writes x candidates.
s=$(rpc_mean_ms "/kora.v1.Kora/Store" /tmp/vp_store.sh)
report "tagged store, idle" \
  "~38ms at 94k vertices; the cap prevents the ~469ms uncapped case" \
  "${s}ms" "150" "below" "latency"

section "3. /v1/health is cached (e317412)"
# Requirement: /v1/health did two full graph scans per call on an
# unauthenticated endpoint. 2196ms p50 under load -> 2.8ms.
h=$(rpc_mean_ms "/kora.v1.HealthService/Health" /tmp/vp_health.sh)
report "health, 20 serial calls" \
  "2196ms -> 2.8ms p50; counts cached for 5s, reachability never cached" \
  "${h}ms" "60" "below" "latency"

section "4. Index usage, re-checked at the current size (d5b2e72, a661212)"
# These are plan assertions, not timings: the defect they guard against is AGE
# silently choosing a sequential scan, which is what happened when the graph
# grew past the size the original measurement was taken at.
# enable_seqscan=off asserts that an index *can* serve the predicate, not that
# the planner currently prefers it. On a small table a sequential scan is the
# correct choice, so a choice-based check fails on a fresh CI database while the
# index is perfectly fine -- the same mistake bbd6eee fixed in verify_k8s.sh.
plan_uses_index() {
  kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -c \
    "LOAD 'age'; SET search_path=ag_catalog,public; SET enable_seqscan=off;
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
unwind_scans=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -c \
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
und=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
  "LOAD 'age'; SET search_path=ag_catalog,public;
   EXPLAIN (ANALYZE, TIMING OFF) SELECT * FROM cypher('context0', \$\$ MATCH (c)-[e]-(o:Memory) WHERE c.id = 'nonexistent' RETURN e \$\$) AS (e agtype);" 2>/dev/null \
  | grep "Execution Time" | grep -oE "[0-9.]+")
dir=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
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
mod=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
  "SELECT n_mod_since_analyze FROM pg_stat_user_tables WHERE relname='Memory';" 2>/dev/null | tail -1)
live=$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
  "SELECT greatest(n_live_tup,1) FROM pg_stat_user_tables WHERE relname='Memory';" 2>/dev/null | tail -1)
printf '  observed: %s rows modified since the last ANALYZE, of %s live\n' "${mod:-?}" "${live:-?}"

# Only meaningful once autovacuum's own threshold is in play. PostgreSQL
# triggers autoanalyze at roughly 10% of the table plus 50 rows, so on a small
# table a high ratio is normal and expected, not a symptom -- checking it there
# produced a failure on a freshly seeded database whose statistics were fine.
# Reported, never failed.
#
# A high ratio here is not a defect: it is the normal state immediately after a
# burst of writes, before autoanalyze catches up. CI seeds ~3,600 memories in
# two minutes and this reads 12,692 modifications against 3,668 live rows -- a
# perfectly healthy database that had just been written to hard.
#
# Failing on it would be actively misleading, because this is precisely the
# window the literal-list form exists to survive. The measurement is worth
# printing because it tells a reader which statistical regime the timings above
# were taken in; it is not a pass/fail signal about the service.
ratio=$(awk -v m="${mod:-0}" -v l="${live:-1}" 'BEGIN{printf "%.2f", m/l}')
if awk -v r="$ratio" 'BEGIN{exit !(r > 0.5)}'; then
  printf '  \033[33mINFO\033[0m  statistics are lagging (ratio %s): recent bulk writes, autoanalyze\n' "$ratio"
  printf '        has not caught up. This is the window the literal-list form in\n'
  printf '        uuidLiteralList is designed to survive -- 158.7ms vs 0.23ms measured.\n'
else
  printf '  \033[32mINFO\033[0m  statistics are current (ratio %s); the timings above were taken\n' "$ratio"
  printf '        with a fresh plan.\n'
fi

# Dead tuples are the other thing that moves the timings above, and unlike
# statistics lag it is invisible in the query plan.
#
# HNSW keeps index entries for deleted rows until VACUUM removes them, and a
# scan spends its budget walking them. Measured on this cluster: the scoped
# query above read 94.0ms with 28,500 dead tuples on memory_embeddings and
# 22,442 on the Memory vertex table, and 55.6ms after VACUUM ANALYZE on the
# same data -- the graph had not changed at all.
#
# Reported rather than failed, for the same reason as the statistics ratio: a
# database that has just absorbed a burst of writes and deletes is in a normal
# state, not a broken one. But a reader comparing two runs of this script needs
# to know which regime each was taken in, or a routine bloat difference reads
# as a performance regression in the service.
printf '\n\033[1m5b. Table bloat at measurement time\033[0m\n'
for tbl in "memory_embeddings" "Memory"; do
  read -r live dead <<<"$(kubectl exec -n "$NS" postgres-age-0 -- psql -U kora -d kora -tAc \
    "SELECT greatest(n_live_tup,1), n_dead_tup FROM pg_stat_user_tables WHERE relname='$tbl';" 2>/dev/null \
    | tail -1 | tr '|' ' ')"
  pct=$(awk -v d="${dead:-0}" -v l="${live:-1}" 'BEGIN{printf "%.1f", 100*d/l}')
  printf '  observed: %s has %s dead tuples against %s live (%s%%)\n' \
    "$tbl" "${dead:-?}" "${live:-?}" "$pct"
  if awk -v p="$pct" 'BEGIN{exit !(p > 10)}'; then
    printf '  \033[33mINFO\033[0m  %s%% dead: the timings above are inflated by index entries for\n' "$pct"
    printf '        rows that no longer exist. VACUUM ANALYZE before comparing this run\n'
    printf '        against another.\n'
  fi
done

section "6. Rate limit permits real throughput (e317412)"
# The default was 100/min (1.6/s), which had never run because rate limiting
# only engages once a key is configured. Enabling auth would have throttled
# every deployment to a crawl.
rl=$(kubectl exec -n "$NS" "$POD" -- printenv KORA_RATE_LIMIT_PER_MINUTE 2>/dev/null)
report "configured rate limit" \
  "6000/min (100/s), sized against a ~4ms store; 100/min was unusable" \
  "${rl:-0}" "1000" "above"

section "7. Latency buckets resolve the operating range (46c642e)"
# With DefBuckets, p50/p95/p99 all landed in [0.1, 0.25] alongside 79% of
# samples, so no percentile could be distinguished from another.
mx=$(kubectl exec -n "$NS" "$POD" -- wget -q -O- http://localhost:8080/metrics 2>/dev/null)
b125=$(grep -c 'kora_request_duration_seconds_bucket{.*le="0.125"' <<<"$mx" || true)
report "sub-decade bucket boundaries present" \
  "buckets must separate p50 from p99 in this service's range" \
  "$b125" "0" "above"

section "8. Connection pool is not saturated at rest"
acq=$(grep '^kora_pool_connections{state="acquired"}' <<<"$mx" | awk '{print $2}')
max=$(grep '^kora_pool_connections{state="max"}' <<<"$mx" | awk '{print $2}')
printf '  observed: %s of %s connections acquired\n' "${acq:-<absent>}" "${max:-<absent>}"

# Absent metrics must fail, not pass. The gauges are sampled on a 5s ticker, so
# they are missing for the first few seconds after startup -- and defaulting a
# missing value to 0 made this report a healthy pool that was not being measured
# at all, which is the opposite of what the check is for.
if [[ -z "$max" || -z "$acq" ]]; then
  printf '  \033[31mFAIL\033[0m  pool metrics are not being exported\n        (kora_pool_connections absent; the sampler may not be running)\n'
  FAIL=$((FAIL + 1))
  FAILURES+=("pool metrics exported")
else
  report "pool has headroom at rest" \
    "every request waits on this pool; acquired == max is a stall, not load" \
    "$(awk -v a="$acq" -v m="$max" 'BEGIN{printf "%.2f", a/m}')" "0.9" "below"
fi

printf '\n\033[1m%s\033[0m\n' "=== $PASS passed, $FAIL failed ==="
if [[ "$LATENCY_MEANINGFUL" -eq 0 ]]; then
  printf '\033[33mLatency was measured but not enforced at %s vertices. A pass here means\n' "$VERTICES"
  printf 'the structural guarantees hold, NOT that the service is fast: run this\n'
  printf 'against a loaded cluster (>= %s vertices) for that.\033[0m\n' "$MIN_VERTICES_FOR_LATENCY"
fi
for f in "${FAILURES[@]:-}"; do [[ -n "$f" ]] && printf '  failed: %s\n' "$f"; done
exit $((FAIL > 0 ? 1 : 0))
