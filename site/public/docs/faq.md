# FAQ

## Is kora ready to use?

Release v0.1.1 is available, and everything documented here works. It is pre-1.0 and the API may still change, so pin the version and read release notes before upgrading.

## How is this different from a vector database?

A vector store gives you similarity and nothing else. Ask it "what database do we use?" after a migration and it returns both the old fact and the new one, ranked by cosine distance, with no way to tell which is current.

kora stores relationships as data. A migration produces a `SUPERSEDES` edge, so the current answer is unambiguous and the old one stays queryable rather than being overwritten. Retrieval also traverses the graph, so a memory one hop from a strong match surfaces even when its own text matches poorly. See [Concepts](concepts.md).

## Do I need two databases for the graph and the vectors?

No, and that is the point. Apache AGE and pgvector are both PostgreSQL extensions, so both live in one instance. One backup, one failure mode, one set of credentials, and a memory commits atomically with the edges pointing at it. See [Architecture](architecture.md).

## Does it call out to an LLM?

Only if you configure it to. The default embedding provider is `bag-of-words`, which runs locally with no network access, no API key, and no model download, so a first run works entirely offline. Ollama keeps it self-hosted; OpenAI and Google are available if you want them. Extraction is rule-based with an optional LLM path.

Nothing leaves your infrastructure unless you point it at something that is not yours.

## Which embedding provider should I use?

`bag-of-words` for a first run and for tests. Ollama with `nomic-embed-text` for real self-hosted use. An OpenAI-compatible endpoint if you already have one and do not mind the dependency.

Changing provider or dimension on a populated database invalidates existing embeddings: vectors from different models are not comparable. Plan on re-embedding.

## Can I run it without Kubernetes?

Docker Compose works and is what the [Quick start](quickstart.md) uses. It is genuinely fine for a single machine. Kubernetes is what the Helm chart, the consolidation CronJob, the probes, and the network policies are built around, so it is the supported production path.

## Is authentication optional?

An empty key list disables authentication. In the Helm chart this is gated behind an explicit `auth.allowUnauthenticated: true`, so a deployment that means it has to say so, and one that merely forgot its keys fails to install rather than quietly opening to the world.

`/livez`, `/readyz`, `/startupz`, `/metrics`, and `/v1/health` are always public. Health reports graph totals, so keep the service off the public internet.

## I lost my API key. Can I recover it?

No. The server stores only a hash. Generate a new one with `kora keys generate` and update the Secret.

## Why does my memory graph keep growing?

By design: superseded memories are kept, not deleted. Consolidation manages the growth by merging exact duplicates, decaying scores, and pruning memories that are simultaneously stale, never once retrieved, and older than 30 days. See [Operations](operations.md).

## Queries are slow. What do I check first?

CPU on Postgres, before anything else. AGE traversals and pgvector distance calculations are CPU-bound, and a CPU limit throttles silently: the pod is Running, healthy, under its memory limit, never restarting, and every query is uniformly slow. Removing a 1000m limit here took query p50 from 523ms to 45ms.

After that, `kora_pool_acquire_wait_seconds_total`, which rises before latency does when the connection pool saturates. See [Configuration](configuration.md).

## Is my rate limit cluster-wide?

No. Token buckets live in process memory, so N replicas admit roughly N times `rateLimitPerMinute`. Enforce a true global budget at an ingress or service mesh.

## What is the license?

Apache 2.0, and every dependency is OSI-approved. No SSPL, no BSL, no proprietary components, and no hosted-only features. What is in the repository is the whole product.

## I have CONTEXT0_ variables from before the rename. Do they still work?

No, and that is deliberate. The project was called Context0 before it was called kora, and the environment variables moved with it: `CONTEXT0_API_KEYS` is now `KORA_API_KEYS`, and so on throughout.

The engine **refuses to start** when it sees an old name, rather than falling back to a default. The reason is specific: an unset `KORA_API_KEYS` disables authentication entirely, so a silent fallback would have brought the API up serving every stored memory to anyone who asked.

The CLI is gentler, since its failure modes are wrong rather than dangerous: it warns and carries on. That warning still matters. A leftover `CONTEXT0_ENDPOINT` leaves the CLI talking to localhost instead of your server, and `CONTEXT0_PROJECT` queries a project that does not exist and returns nothing, which looks like data loss rather than a typo.

## I am upgrading from Context0. What breaks?

Two things, and a plain `helm upgrade` handles neither.

**Environment variables**, as above: rename `CONTEXT0_*` to `KORA_*` or the engine will not start.

**The Postgres role and database** were renamed from `context0` to `kora`. Those live in your data rather than in the repository, so a new image points at names that do not exist yet and fails with `role "kora" does not exist`. The Postgres StatefulSet's volume survives an upgrade, so Kubernetes deployments hit this too, not only Docker Compose.

Either point the chart back at the names your database already uses, which changes nothing on disk:

```bash
helm upgrade kora ./charts/kora \
  --set postgres.user=context0 --set postgres.database=context0
```

Or rename the role and database to match the new defaults, with the API stopped:

```bash
scripts/migrate_rename.sh
```

The script is idempotent, renames both catalog-only so the cost does not scale with database size, reaps any privileged helper role left behind by an interrupted run, and verifies the graph is still readable afterwards.

The AGE graph and its schema keep the name `context0` either way. Renaming those is a data migration with no functional benefit.

## Where do I report a security issue?

Not in a public issue. See [SECURITY.md](https://github.com/NarayanaSabari/Kora/blob/main/SECURITY.md).
