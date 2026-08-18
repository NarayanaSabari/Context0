# Soak testing against a live cluster

The short soaks that gate a change (2-3 minutes) catch correctness bugs and
gross regressions. They do not catch anything that scales with accumulated
state, because in three minutes the graph barely grows. Two real defects in this
project were only visible over twenty minutes.

## What the long soak found

**Latency tracking total graph size** (fixed in `a661212`). A 20-minute run
showed query p50 drifting 124ms -> 144ms while the graph grew 44k -> 64k
vertices. Isolated at idle, a scoped query had gone from 53ms to 90.9ms on
unchanged code.

The cause was `UNWIND $ids AS wanted MATCH (m:Memory) WHERE m.id = wanted`,
which AGE cannot serve from the id index -- it planned a sequential scan over
every memory, for every hydration call. Cost grew with the size of the database
rather than the size of the request, so it was invisible in any short run and
would have arrived in production as gradual, unexplained decay.

Note this had been measured before and passed: at 50k vertices the same query
planned an index scan. **The plan changed as the graph grew.** A performance
measurement is true of a database state, not of a query.

**A write not readable by its own keyword** (fixed in `d424a1f`). One violation
in ~1,800 writes, which no short run reproduced.

## Running one

The default-deny NetworkPolicy means a load generator needs the
`role: test-client` label to reach the API at all:

```sh
kubectl create configmap soak-script -n context0 \
  --from-file=soak.py=scripts/soak.py --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: soak
  namespace: context0
  labels: {role: test-client}    # required: default-deny blocks egress otherwise
spec:
  restartPolicy: Never
  securityContext:               # required: namespace enforces restricted PSS
    runAsNonRoot: true
    runAsUser: 1000
    seccompProfile: {type: RuntimeDefault}
  containers:
  - name: soak
    image: python:3.12-slim
    securityContext:
      allowPrivilegeEscalation: false
      capabilities: {drop: ["ALL"]}
    command: ["python","/s/soak.py","--url","http://context0-api:8080",
              "--key","<api key>","--workers","6","--minutes","20"]
    volumeMounts: [{name: s, mountPath: /s}]
  volumes: [{name: s, configMap: {name: soak-script}}]
EOF
```

Run it in-cluster, not through `kubectl port-forward`. The forward becomes the
bottleneck under concurrency and produces latency numbers that describe the
tunnel rather than the service.

## Reading the output

**The reported p50 is cumulative, not per-window.** A cumulative p50 that is
still falling can hide a per-window latency that is already rising, and a
cumulative one that rises slowly can mean the recent windows are much worse.
Compare the first and last windows and look at the shape between them:

```sh
kubectl logs soak -n context0 | grep -E "^  query" \
  | sed 's/.*n=\([0-9]*\).*p50= *\([0-9.]*\)ms.*/\1 \2/'
```

Healthy: p50 falls as the run warms up, then holds flat.
Unhealthy: p50 reaches a floor and then climbs for the rest of the run.

**`correctness: no violations` is the line that matters.** Latency regressions
are recoverable; a write that is not readable by its own content is not.

**HTTP 429s are usually the rate limiter working.** The soak drives ~4 requests
per cycle on one key, so at 60+ ops/s it exceeds the 100/s per-key default. That
is the limiter doing its job. To measure the service rather than the limiter,
raise it for the run:

```sh
kubectl set env deploy/context0-api -n context0 CONTEXT0_RATE_LIMIT_PER_MINUTE=60000
# ... and remember to remove it afterwards
kubectl set env deploy/context0-api -n context0 CONTEXT0_RATE_LIMIT_PER_MINUTE-
```

## Checking for leaks

Sample the process metrics across the run. Flat is the requirement; a slope in
any of them over twenty minutes is a leak:

```sh
for i in $(seq 1 9); do
  sleep 110
  kubectl exec -n context0 deploy/context0-api -- wget -q -O- http://localhost:8080/metrics \
    | grep -E "^go_goroutines |^go_memstats_heap_inuse_bytes |^process_open_fds "
done
```

Observed healthy over 18 minutes and ~48,000 operations: goroutines 53-54, heap
5.1-5.9MB, file descriptors constant at 26.

## Measuring one operation in isolation

Aggregate soak numbers include queueing, so they cannot separate "the service
got slower" from "the load generator asked for more". For that, use the RED
histogram's sum and count around a serial loop:

```sh
M() { kubectl exec -n context0 deploy/context0-api -- wget -q -O- http://localhost:8080/metrics \
  | grep -E 'context0_request_duration_seconds_(sum|count)\{method=".*/Query"' | awk '{print $2}' | tr '\n' ' '; }
B=$(M); kubectl exec -n context0 deploy/context0-api -- sh /tmp/loop.sh; A=$(M)
# mean = (sum_after - sum_before) / (count_after - count_before)
```

This is how the 90.9ms -> 16.2ms improvement was attributed to the query change
rather than to a quieter cluster.
