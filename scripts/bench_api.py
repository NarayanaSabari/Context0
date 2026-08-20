#!/usr/bin/env python3
"""Measure Kora query latency through the public REST API.

Run against a corpus large enough that plan choice matters (the 50k seed used
in docs/research/performance-audit-2026-08.md). Uses persistent connections and
in-process timing so it measures the server, not curl's process spawn.
"""
import argparse
import json
import statistics
import time
import urllib.request

API = "http://localhost:8080"
KEY = "ctx0_dev_key_1"


def call(path, method="GET", body=None):
    req = urllib.request.Request(
        API + path, method=method,
        data=json.dumps(body).encode() if body else None,
        headers={"X-API-Key": KEY, "Content-Type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req) as r:
        payload = r.read()
    return (time.perf_counter() - t0) * 1000, payload


def measure(label, path, reps):
    call(path)  # warm
    samples = []
    for _ in range(reps):
        # Small pause between samples so a burst does not trip the per-key rate
        # limiter and turn this into a measurement of the rejection path.
        time.sleep(0.01)
        samples.append(call(path)[0])
    samples.sort()
    p50 = statistics.median(samples)
    p95 = samples[min(int(len(samples) * 0.95), len(samples) - 1)]
    print(f"  {label:<28} p50={p50:8.2f}ms  p95={p95:8.2f}ms")
    return p50


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--project", default="proj-7")
    ap.add_argument("--reps", type=int, default=25)
    args = ap.parse_args()

    print(f"\nQuery latency via REST (project={args.project}, reps={args.reps})")
    measure("query (project filter)", f"/v1/memories/query?project_id={args.project}&top_k=5", args.reps)
    measure("query (keyword)", f"/v1/memories/query?query=topic+5&project_id={args.project}&top_k=5", args.reps)
    measure("profile", f"/v1/profiles/{args.project}", args.reps)

    print("\nProbe latency (these run on every kubelet check)")
    for p in ("livez", "readyz", "startupz"):
        measure(f"/{p}", f"/{p}", args.reps)

    print("\nHealth RPC (full graph counts - deliberately NOT a probe)")
    measure("/v1/health", "/v1/health", args.reps)
    print()


if __name__ == "__main__":
    main()
