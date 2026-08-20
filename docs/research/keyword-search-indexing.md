# Keyword search cannot be indexed through AGE's `CONTAINS`

Status: investigated and rejected, 2026-08-18. Measured on a live cluster
holding 44,809 `:Memory` vertices.

## The problem

The graph retriever filters candidates with Cypher `CONTAINS`:

```cypher
MATCH (m:Memory)
WHERE toLower(m.content) CONTAINS $kw0
RETURN properties(m)
```

Unscoped, that is a sequential scan. Measured with `EXPLAIN (ANALYZE)`:

```
Seq Scan on "Memory" m  (actual rows=5933.00 loops=1)
  Filter: agtype_string_match_contains(
            age_tolower(agtype_access_operator(VARIADIC ARRAY[properties, '"content"'])),
            '"prometheus"')
  Rows Removed by Filter: 38876
Execution Time: 25.256 ms
```

38,876 rows read and discarded, and the cost grows linearly with the size of the
whole graph rather than with the number of matches.

## What was tried

`pg_trgm` is the standard answer for substring search in PostgreSQL, and it is
available in this image. A GIN trigram index was built over the same expression
AGE filters on:

```sql
CREATE EXTENSION pg_trgm;
CREATE INDEX memory_content_trgm_idx ON context0."Memory"
  USING gin ((ag_catalog.agtype_access_operator(
    VARIADIC ARRAY[properties, '"content"'::ag_catalog.agtype])::text) gin_trgm_ops);
ANALYZE context0."Memory";
```

**The index is sound.** Queried directly through SQL it is used and it helps:

```
SET enable_seqscan=off;
SELECT count(*) FROM context0."Memory"
WHERE agtype_access_operator(VARIADIC ARRAY[properties, '"content"'])::text
      ILIKE '%prometheus%';

->  Bitmap Index Scan on memory_content_trgm_idx (actual rows=5933.00)
Execution Time: 7.282 ms      (vs 24.295 ms sequential)
```

**AGE cannot use it.** The same predicate expressed as Cypher `CONTAINS` still
sequential-scans, and it does so even with `enable_seqscan=off`, which forces the
planner to prefer any usable index at almost any cost:

```
SET enable_seqscan=off;
MATCH (m:Memory) WHERE toLower(m.content) CONTAINS 'prometheus' RETURN m

Seq Scan on "Memory" m
  Filter: agtype_string_match_contains(...)
Execution Time: 24.295 ms
```

Refusing to use an index under `enable_seqscan=off` is not a costing decision.
It means no index can satisfy the predicate at all: `agtype_string_match_contains`
is an ordinary function to the planner, with no operator class binding it to
`gin_trgm_ops`, so there is nothing to match against the index. `age_tolower`
wrapping the property access compounds this -- even a matching operator class
would need the index built over the same expression including that call.

The index was therefore dropped rather than shipped. Keeping it would add write
amplification on every memory insert, and a GIN index to VACUUM, in exchange for
nothing.

## Why this is not currently urgent

The unscoped query is not the common path. Scoping by project uses the
`memory_project_id_idx` expression index, and `CONTAINS` then filters a much
smaller set:

| Query | Rows scanned | Time |
|---|---|---|
| Scoped to one project (~1k memories) | 982 | **2.2 ms** |
| Unscoped across 44,809 memories | 44,809 | **30.6 ms** |

Every SDK path and the soak workload scope by `project_id`. The unscoped case is
reachable through the API by omitting `project_id`, and it degrades linearly, so
this is a real ceiling rather than a non-issue -- just not one worth a risky
change today.

## Options, if this becomes the bottleneck

1. **A shadow column.** Store `content` in a plain `text` column alongside the
   vertex and index that. The planner can use it, but it means writing the same
   data twice and keeping the copy consistent, and AGE queries still cannot
   reference it -- the filter would have to move out of Cypher into SQL.

2. **Move keyword retrieval to PostgreSQL full-text search** (`tsvector` +
   GIN), executed as SQL against the `Memory` table and joined back to the graph
   by id. Better recall than substring matching (stemming, ranking via
   `ts_rank`), and it removes the `LIKE '%...%'` semantics that trigram indexes
   only partially rescue. Largest change, best end state.

3. **Lean harder on vector retrieval** and treat keyword matching as a
   re-ranking signal over vector candidates rather than an independent
   retriever. Note this interacts with the relevance tiering added in `d424a1f`:
   lexical evidence currently outranks cosine similarity, and that ordering
   exists because an unfiltered vector hit once displaced a verbatim match.

Option 2 is the recommendation when the unscoped path matters. None of these
should be started before there is a workload that actually needs it.

## Reproducing

```sh
kubectl exec -n kora postgres-age-0 -- psql -U kora -d kora -c "
LOAD 'age'; SET search_path=ag_catalog,public; SET enable_seqscan=off;
EXPLAIN (ANALYZE, TIMING OFF) SELECT * FROM cypher('context0', \$\$
  MATCH (m:Memory) WHERE toLower(m.content) CONTAINS 'prometheus' RETURN m
\$\$) AS (m agtype);"
```

A `Seq Scan` under `enable_seqscan=off` is the signature of a predicate no index
can serve.
