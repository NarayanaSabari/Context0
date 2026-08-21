#!/usr/bin/env python3
"""Seed a corpus through the public API so embeddings are generated.

Seeding directly into AGE with Cypher is much faster, but it leaves
public.memory_embeddings empty, so SearchByVector matches nothing and the
hybrid retrieval path never executes. Any benchmark built on such a corpus
silently measures only the graph half of the engine.

Usage: scripts/seed_corpus.py [--count N] [--project-count P] [--workers W]
"""
import os
import argparse
import json
import queue
import threading
import time
import urllib.request

# Overridable so these can point at a port-forward on a non-default port, or
# at a remote deployment. soak.py already took --url; these did not.
API = os.environ.get("KORA_HTTP_URL", "http://localhost:8080")
# The key is read lazily by require_key() rather than at import, so --help
# still works without credentials. It has no default: ctx0_dev_key_1 used to
# be one, but that key was published in a public repo and removed from the
# chart in a83af5a, so the default only ever produced 401s.
def require_key():
    key = os.environ.get("KORA_API_KEY", "")
    if not key:
        raise SystemExit(
            "KORA_API_KEY is not set.\n"
            "  export KORA_API_KEY=$(. ./.dev-credentials && echo $DEV_API_KEY)"
        )
    return key

TOPICS = [
    "postgresql database migration", "kubernetes deployment rollout",
    "golang concurrency patterns", "integration testing strategy",
    "prometheus metrics collection", "api rate limiting",
    "vector embedding search", "graph traversal performance",
    "connection pool tuning", "tls certificate rotation",
]


def post(path, body):
    req = urllib.request.Request(
        API + path, method="POST", data=json.dumps(body).encode(),
        headers={"X-API-Key": require_key(), "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--count", type=int, default=5000)
    ap.add_argument("--project-count", type=int, default=20)
    ap.add_argument("--workers", type=int, default=8)
    args = ap.parse_args()

    work = queue.Queue()
    for i in range(args.count):
        work.put(i)

    errors = []
    done = [0]
    lock = threading.Lock()

    def worker():
        while True:
            try:
                i = work.get_nowait()
            except queue.Empty:
                return
            topic = TOPICS[i % len(TOPICS)]
            try:
                post("/v1/memories", {
                    "content": f"Note {i} covering {topic}: the team settled on this "
                               f"approach for tier {i % 7} after review",
                    "type": 2,
                    "project_id": f"proj-{i % args.project_count}",
                    "tags": [topic.split()[0], f"tier{i % 7}"],
                })
            except Exception as e:
                with lock:
                    errors.append(repr(e)[:80])
            with lock:
                done[0] += 1
                if done[0] % 500 == 0:
                    print(f"  {done[0]}/{args.count}", flush=True)
            work.task_done()

    start = time.perf_counter()
    threads = [threading.Thread(target=worker) for _ in range(args.workers)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    elapsed = time.perf_counter() - start

    print(f"\nstored {done[0] - len(errors)}/{args.count} in {elapsed:.1f}s "
          f"({(done[0] - len(errors)) / elapsed:.0f}/s)")
    if errors:
        from collections import Counter
        for e, c in Counter(errors).most_common(3):
            print(f"  {c}x {e}")


if __name__ == "__main__":
    main()
