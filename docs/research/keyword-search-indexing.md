# Keyword search cannot be indexed through AGE's `CONTAINS`

Status: **resolved, 2026-08-27**, by taking option 2 below. Originally
investigated and rejected 2026-08-18, measured on a live cluster holding 44,809
`:Memory` vertices.

Keyword retrieval now runs as SQL full-text search against the `Memory` table
and joins back to the graph by id, in `internal/graph/fts.go`. The findings
below stand unchanged and are what motivated the move; what follows is what
actually shipped.

## What shipped

`to_tsvector`/`ts_rank_cd` with a GIN index over the same property-access
expression this document showed a trigram index could not be used for. Verified
against a live instance:

- The query plans as a `Bitmap Index Scan on memory_content_fts_idx`, which is
  exactly what `CONTAINS` could not do at any cost.
- `go` no longer matches `mango` or `algorithm`, and does match `going`,
  because `to_tsvector` lexes into words before comparing.
- Rare terms outweigh common ones, which needed more than `ts_rank_cd`. It
  measures term frequency and cover density and has **no inverse document
  frequency at all**: a term appearing in 1 document of the corpus and one
  appearing in 1,775 both rank 0.1 on an equivalent document. The apparent
  weighting from a query for `the` is the stop-word dictionary, not weighting,
  and it says nothing about the ordinary words a question is made of -- `said`
  appears in 1,775 of 4,638 memories in this corpus and no dictionary removes
  it. So IDF is computed explicitly, with BM25's
  `ln(1 + (N - df + 0.5) / (df + 0.5))`, and each term's `ts_rank_cd` is
  weighted by it. Measured: `biscuit` (386 documents) scores 2.49 against
  `said` (1,775) at 0.96. Each `df` is one Bitmap Index Scan against the same
  GIN index.
- Tags remain searchable. The `CONTAINS` retriever matched `content OR tags`,
  so the index is built over both; dropping tags would have made a memory
  findable by every word of its prose but not by the label someone attached to
  it.

The raw `ts_rank_cd` scale was measured rather than assumed, because it decides
how the score is normalised. On one document, varying only how many OR-ed query
terms match:

| terms matched | 1 | 2 | 3 | 5 | 8 | 12 | 20 |
|---|---|---|---|---|---|---|---|
| `ts_rank_cd` | 0.1 | 0.2 | 0.3 | 0.5 | 0.8 | 1.2 | 1.7 |

Roughly 0.1 per matched term, so a five-word question scores five times higher
than a one-word one for the same quality of match. That is why
`ranking.NormalizeBM25` adapts its sigmoid to the query length, and why Mem0's
published midpoints of 5.0-12.0 could not be copied: those are for their own
BM25 implementation, and every real `ts_rank_cd` score would land on the flat
bottom of that curve.

`QueryMemories` keeps its `CONTAINS` branch. It is no longer the search path,
but it still serves queries carrying no searchable terms, and profile and
consolidation enumeration that passes no keywords at all.

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
