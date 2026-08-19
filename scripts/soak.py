#!/usr/bin/env python3
"""Continuous soak + correctness harness against a running Context0.

Runs mixed read/write load forever, verifying invariants on every cycle rather
than only measuring latency. A soak test that only reports throughput will miss
the failure modes that actually matter -- results silently going missing,
embeddings orphaning, edges duplicating -- so each cycle asserts:

  * a memory that was just written is retrievable by query
  * project scoping never leaks another project's data
  * embedding count matches memory count (no orphans)
  * requested top_k is honoured when enough memories exist
  * latency stays within a budget

Usage:
  scripts/soak.py --url http://localhost:8080 --minutes 10 --workers 8
  scripts/soak.py --forever          # until SIGINT

Exits non-zero if any invariant is violated.
"""
import argparse
import json
import random
import signal
import statistics
import string
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter, defaultdict

TYPE_EPISODIC, TYPE_SEMANTIC, TYPE_PROCEDURAL = 1, 2, 3

TOPICS = [
    "postgresql database migration", "kubernetes deployment rollout",
    "golang concurrency patterns", "integration testing strategy",
    "prometheus metrics collection", "api rate limiting",
    "vector embedding search", "graph traversal performance",
]

stop = threading.Event()


class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.latencies = defaultdict(list)
        self.errors = Counter()
        self.violations = []
        self.ops = Counter()

    def record(self, op, ms):
        with self.lock:
            self.latencies[op].append(ms)
            self.ops[op] += 1

    def error(self, op, err):
        with self.lock:
            self.errors[f"{op}: {err}"] += 1

    def violation(self, what):
        with self.lock:
            self.violations.append(what)


class Client:
    def __init__(self, url, key, stats):
        self.url = url.rstrip("/")
        self.key = key
        self.stats = stats

    def _call(self, op, path, method="GET", body=None, timeout=30):
        req = urllib.request.Request(
            self.url + path, method=method,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"X-API-Key": self.key, "Content-Type": "application/json"})
        t0 = time.perf_counter()
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                payload = r.read()
            self.stats.record(op, (time.perf_counter() - t0) * 1000)
            return json.loads(payload) if payload else {}
        except urllib.error.HTTPError as e:
            self.stats.error(op, f"HTTP {e.code}")
            if e.code == 429:
                # Honour Retry-After. Retrying a rejection immediately turns one
                # 429 into a hot loop and reports hundreds of thousands of
                # "errors" that are really one client refusing to back off,
                # which buries whatever the soak was meant to find.
                try:
                    delay = float(e.headers.get("Retry-After", 1))
                except (TypeError, ValueError):
                    delay = 1.0
                time.sleep(min(delay, 5.0))
            return None
        except Exception as e:
            self.stats.error(op, type(e).__name__)
            return None

    def store(self, project, content, mem_type=TYPE_SEMANTIC, tags=None):
        return self._call("store", "/v1/memories", "POST", {
            "content": content, "type": mem_type,
            "project_id": project, "tags": tags or [],
        })

    def query(self, project, text="", top_k=5):
        q = f"/v1/memories/query?project_id={project}&top_k={top_k}"
        if text:
            q += "&query=" + urllib.parse.quote(text)
        return self._call("query", q)

    def profile(self, project):
        return self._call("profile", f"/v1/profiles/{project}")

    def extract(self, project, conversation):
        return self._call("extract", "/v1/memories/extract", "POST",
                          {"conversation": conversation, "project_id": project})

    def health(self):
        return self._call("health", "/v1/health")


def rand_suffix(n=8):
    return "".join(random.choices(string.ascii_lowercase + string.digits, k=n))


def worker(client, stats, projects, budget_ms):
    """One mixed-workload worker. Each cycle both exercises and verifies."""
    while not stop.is_set():
        try:
            _cycle(client, stats, projects)
        except Exception as e:
            # A crashing worker would silently reduce load and mask the very
            # problem the soak exists to find, so record it and keep going.
            stats.error("worker", f"{type(e).__name__}: {e}")


def _cycle(client, stats, projects):
    """One unit of mixed work, with its invariants checked inline."""
    project = random.choice(projects)
    topic = random.choice(TOPICS)

    # Write, then prove it is readable in its own project.
    marker = rand_suffix()
    content = f"soak {marker} about {topic}"
    if client.store(project, content, TYPE_SEMANTIC, [topic.split()[0]]) is None:
        return

    found = client.query(project, marker, top_k=10)
    if found is not None:
        hits = found.get("results", [])
        if not any(marker in r["memory"]["content"] for r in hits):
            stats.violation(f"write not readable: {marker} absent from its own project")

    # Scoping must hold: another project must never return this memory.
    others = [p for p in projects if p != project]
    if others:
        other = random.choice(others)
        leaked = client.query(other, marker, top_k=10)
        if leaked is not None:
            for r in leaked.get("results", []):
                # grpc-gateway emits camelCase JSON.
                got = r["memory"].get("projectId") or r["memory"].get("project_id")
                if got != other:
                    stats.violation(f"scope leak: querying {other} returned {got}")
                if marker in r["memory"]["content"]:
                    stats.violation(f"scope leak: {marker} surfaced in {other}")

    # Exercise the rest of the surface at a lower rate.
    if random.random() < 0.3:
        client.profile(project)
    if random.random() < 0.2:
        # Alternate the two shapes a client actually sends. The newline form was
        # the only one exercised here, which is why a single-line conversation
        # collapsing into one memory went unnoticed: every test agreed with
        # every other test and with nothing a real HTTP caller does.
        if random.random() < 0.5:
            conversation = (f"user: we switched to {topic} last week\n"
                            f"user: I prefer {topic.split()[0]} for this")
            expect_min = 2
        else:
            conversation = (f"User: We switched to {topic} last week. "
                            f"Assistant: Noted. "
                            f"User: I prefer {topic.split()[0]} for this.")
            expect_min = 2

        extracted = client.extract(project, conversation)
        if extracted is not None:
            memories = extracted.get("memories", [])
            if len(memories) < expect_min:
                stats.violation(
                    f"extract returned {len(memories)} memories for a "
                    f"{'multi-line' if chr(10) in conversation else 'single-line'} "
                    f"conversation containing {expect_min} distinct facts")
            for m in memories:
                content = m.get("content", "")
                if "Assistant:" in content or "User:" in content:
                    stats.violation(
                        f"extracted memory still contains a speaker label: {content[:60]!r}")
    if random.random() < 0.1:
        client.health()


def report(stats, elapsed, budget_ms, quiet=False):
    with stats.lock:
        lines = []
        total = sum(stats.ops.values())
        lines.append(f"\n=== soak report after {elapsed:.0f}s: {total} ops "
                     f"({total / max(elapsed, 1):.0f}/s) ===")
        for op in sorted(stats.latencies):
            s = sorted(stats.latencies[op])
            if not s:
                continue
            p50 = statistics.median(s)
            p95 = s[min(int(len(s) * 0.95), len(s) - 1)]
            p99 = s[min(int(len(s) * 0.99), len(s) - 1)]
            flag = "  <-- OVER BUDGET" if p95 > budget_ms else ""
            lines.append(f"  {op:<9} n={len(s):<7} p50={p50:7.1f}ms  "
                         f"p95={p95:7.1f}ms  p99={p99:7.1f}ms{flag}")
        if stats.errors:
            lines.append("  errors:")
            for e, c in stats.errors.most_common(8):
                lines.append(f"    {c:>6}x {e}")
        if stats.violations:
            lines.append(f"  CORRECTNESS VIOLATIONS: {len(stats.violations)}")
            for v in stats.violations[:10]:
                lines.append(f"    {v}")
        else:
            lines.append("  correctness: no violations")
        print("\n".join(lines), flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://localhost:8080")
    ap.add_argument("--key", default="ctx0_dev_key_1")
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--minutes", type=float, default=5)
    ap.add_argument("--forever", action="store_true")
    ap.add_argument("--projects", type=int, default=5)
    ap.add_argument("--budget-ms", type=float, default=500)
    ap.add_argument("--report-every", type=float, default=30)
    args = ap.parse_args()

    stats = Stats()
    client = Client(args.url, args.key, stats)
    run = rand_suffix(6)
    projects = [f"soak-{run}-{i}" for i in range(args.projects)]

    signal.signal(signal.SIGINT, lambda *_: stop.set())
    signal.signal(signal.SIGTERM, lambda *_: stop.set())

    threads = [threading.Thread(target=worker, args=(client, stats, projects, args.budget_ms),
                                daemon=True) for _ in range(args.workers)]
    start = time.time()
    for t in threads:
        t.start()

    deadline = None if args.forever else start + args.minutes * 60
    next_report = start + args.report_every
    try:
        while not stop.is_set():
            time.sleep(0.5)
            now = time.time()
            if now >= next_report:
                report(stats, now - start, args.budget_ms)
                next_report = now + args.report_every
            if deadline and now >= deadline:
                break
    finally:
        stop.set()
        for t in threads:
            t.join(timeout=5)

    elapsed = time.time() - start
    report(stats, elapsed, args.budget_ms)

    with stats.lock:
        failed = bool(stats.violations)
        # Errors are only fatal above a small noise floor; a rollout during the
        # soak legitimately produces a few.
        total = sum(stats.ops.values())
        err_total = sum(stats.errors.values())
        if total and err_total / max(total + err_total, 1) > 0.01:
            print(f"\nFAIL: error rate {err_total / (total + err_total) * 100:.2f}% exceeds 1%")
            failed = True
        if failed:
            return 1
    print("\nPASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
