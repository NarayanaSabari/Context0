#!/usr/bin/env python3
"""Measure the write path: POST /v1/memories under load.

Store does considerably more work per call than Query does per read --
CreateMemory, StoreEmbedding, contradiction detection (which runs a full
QueryMemories), and tag auto-linking (another query plus an edge write per
match) -- so its cost is worth measuring separately rather than inferring from
read latency.

Reports p50/p95 per memory type, because only semantic memories trigger
contradiction detection, and with/without tags, because only tagged memories
trigger auto-linking. That isolates which stage dominates.
"""
import argparse
import json
import statistics
import time
import urllib.request

API = "http://localhost:8080"
KEY = "ctx0_dev_key_1"

TYPE_EPISODIC, TYPE_SEMANTIC, TYPE_PROCEDURAL = 1, 2, 3


def post(path, body):
    req = urllib.request.Request(
        API + path, method="POST", data=json.dumps(body).encode(),
        headers={"X-API-Key": KEY, "Content-Type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=60) as r:
        r.read()
    return (time.perf_counter() - t0) * 1000


def bench(label, project, mem_type, tags, reps):
    samples = []
    for i in range(reps):
        samples.append(post("/v1/memories", {
            "content": f"{label} write probe {i} about database tooling and deployment",
            "type": mem_type,
            "project_id": project,
            "tags": tags,
        }))
    samples.sort()
    p50 = statistics.median(samples)
    p95 = samples[min(int(len(samples) * 0.95), len(samples) - 1)]
    print(f"  {label:<38} p50={p50:7.2f}ms  p95={p95:7.2f}ms")
    return p50


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--reps", type=int, default=40)
    args = ap.parse_args()
    stamp = int(time.time())

    print("\nWrite latency by stage (each project starts empty and grows)\n")
    # Episodic skips contradiction detection entirely.
    bench("episodic, no tags", f"w-ep-{stamp}", TYPE_EPISODIC, [], args.reps)
    bench("episodic, tagged", f"w-ept-{stamp}", TYPE_EPISODIC, ["db", "ops"], args.reps)
    # Semantic runs detectAndSupersede on every call.
    bench("semantic, no tags", f"w-se-{stamp}", TYPE_SEMANTIC, [], args.reps)
    bench("semantic, tagged", f"w-set-{stamp}", TYPE_SEMANTIC, ["db", "ops"], args.reps)

    print("\nDoes write cost grow with the project's existing size?\n")
    project = f"w-grow-{stamp}"
    for batch in range(5):
        p50 = bench(f"after {batch * args.reps} existing memories",
                    project, TYPE_SEMANTIC, ["db"], args.reps)
    print()


if __name__ == "__main__":
    main()
