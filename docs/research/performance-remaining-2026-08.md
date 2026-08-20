# Kora: remaining performance optimizations

Research report, 2026-08-18. **Research only - no service code was modified.**

Every number below marked **[M]** was measured in this session against a throwaway
container built from this repo's own image, `kora/postgres-age-vector:dev`
(PostgreSQL 18.4, Apache AGE 1.7.0, pgvector, pg_trgm, btree_gin), loaded with 50k
`:Memory` vertices across 20 projects plus a 20k x 768-dim vector table.
Claims marked **[V]** are verified against primary sources with URLs.
Claims marked **[I]** are inference and are labelled as such.
Claims marked **[U]** could not be verified and say why.

The container was destroyed at the end of the session.

---

## 0. Executive summary: top 5 by expected impact

| # | Optimization | Expected impact | Evidence |
|---|---|---|---|
| 1 | **Do not put PgBouncer in transaction mode in front of this service as it stands** | Prevents a hard, reproducible outage | **[M]** reproduced `type "agtype" does not exist` |
| 2 | **Indexed keyword search: tsvector generated column + GIN** | **3.8x** on the real query shape, and it stops being O(project size) | **[M]** 10.5ms -> 2.8ms |
| 3 | **RRF instead of the current weighted `CombineRelevance`** | Retrieval quality, not latency; removes an unnormalizable score comparison | **[V]** Cormack et al. |
| 4 | **`hnsw.ef_search` tuning + `halfvec`** | 36% smaller vector index | **[M]** 59MB -> 38MB |
| 5 | **Return named fields instead of `properties(m)`; skip `cypher()` on hot reads** | ~5x on first execution, modest steady-state | **[M]** 3.27ms -> 0.55ms |

The single most important finding is #1, and it is a correctness/availability
finding rather than a performance one. Details in section 5.

---

## 1. Indexed text search over AGE vertex properties

### 1.0 Confirming the problem statement

The premise in the brief is **correct, and I verified the mechanism directly** rather
than taking it on faith.

**There is no operator at all backed by AGE's string-match functions [M]:**

```sql
SELECT o.oprname FROM pg_operator o JOIN pg_proc p ON p.oid=o.oprcode
WHERE p.proname LIKE 'agtype_string_match%';
-- (0 rows)
```

`agtype_string_match_contains` exists only as a *function*. PostgreSQL can only
produce an `Index Cond` from an operator that belongs to an operator class, so a
Cypher `CONTAINS` can never be an index condition. The only agtype opclasses that
exist are `agtype_ops_btree`, `agtype_ops_hash`, and `gin_agtype_ops` **[M]** - none
of which implements substring matching.

Measured baseline, unscoped, 50k vertices **[M]**: `Seq Scan`, 50001 rows removed by
filter, **25.0ms**.

Interesting detail the brief did not mention: `agtype_string_match_contains` is
actually marked **IMMUTABLE** (`provolatile = 'i'`), while `starts_with` and
`ends_with` are only STABLE **[M]**. So volatility is not what blocks indexing here.
The blocker is purely the absence of an operator and opclass. This distinction
matters because it means the fix is not "make it immutable" but "route around
Cypher entirely".

### 1.1 Are agtype -> text casts IMMUTABLE? Yes. [M]

This was the explicit question, and the answer is unambiguous:

```
ag_catalog.agtype_out(agtype)                 | i   (IMMUTABLE)
ag_catalog.agtype_to_text(agtype)             | i   (IMMUTABLE)
ag_catalog.agtype_access_operator(agtype[])   | i   (IMMUTABLE)
```

All three are IMMUTABLE, so **expression indexes over agtype property extraction are
legal**. This is what the repo already relies on for its `id` / `project_id` indexes.

**Critical subtlety, and an easy way to ship a silent bug [M].** `agtype::text`
resolves to `agtype_to_text`, with `castcontext = 'e'` (explicit only):

```
castsource | casttarget |        castfunc        | castcontext
agtype     | text       | agtype_to_text(agtype) | e
```

`agtype_to_text` returns the string **unquoted** (`the quokka telemetry subsystem
misbehaves`), whereas `agtype_out` would return it **with JSON quotes**
(`"the quokka..."`). I verified the unquoted behaviour directly **[M]**. Two
consequences:

- Because the cast is explicit-only, the `::text` in your DDL and in your query must
  match *exactly*, or the planner will not match the expression to the index and you
  get a seq scan with no error. This is the classic expression-index footgun.
- A missing property yields SQL `NULL`, not the string `"null"` **[M]**, so
  `coalesce(...)` is required in a generated column or the column becomes NULL.

### 1.2 Option (a): pg_trgm GIN over the agtype expression

**Exact DDL** (verified to build and to be used **[M]**):

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX memory_content_trgm ON context0."Memory"
USING GIN (
  (lower(ag_catalog.agtype_access_operator(
     VARIADIC ARRAY[properties, '"content"'::ag_catalog.agtype])::text))
  gin_trgm_ops
);
```

**Exact query** - plain SQL against the label table, bypassing `cypher()`:

```sql
SELECT properties
FROM context0."Memory"
WHERE lower(ag_catalog.agtype_access_operator(
        VARIADIC ARRAY[properties,'"content"'::ag_catalog.agtype])::text) LIKE $1
  AND ag_catalog.agtype_access_operator(
        VARIADIC ARRAY[properties,'"project_id"'::ag_catalog.agtype]) = $2
LIMIT $3;
-- $1 = '%' || lower(keyword) || '%'
```

Measured, rare term, 50k rows **[M]**:

| | Time | Plan |
|---|---|---|
| seq scan (index disabled) | 22.8ms | `Seq Scan`, 50001 removed |
| trgm GIN | **0.45ms** | `Bitmap Index Scan on memory_content_trgm` |

**51x**, and the `Index Cond` is genuinely on the trigram index. Build time 2.3s for
50k rows, index size 3.5MB against a 13MB heap **[M]**.

**A composite variant also works**, letting one index serve both predicates:

```sql
CREATE EXTENSION IF NOT EXISTS btree_gin;
CREATE INDEX memory_proj_content_trgm ON context0."Memory" USING GIN (
  (ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties,'"project_id"'::ag_catalog.agtype])::text),
  (lower(ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties,'"content"'::ag_catalog.agtype])::text)) gin_trgm_ops
);
```

Verified to produce a single `Bitmap Index Scan` with both conditions as `Index
Cond` **[M]**.

**Correctness risks.**
- pg_trgm ignores non-alphanumerics and pads words, so trigram semantics are not
  byte-exact substring semantics; the `LIKE` recheck restores exactness, and I
  confirmed `Recheck Cond` is present in the plan **[M]**. So results are correct;
  the index is only a candidate filter. **[V]**
  <https://www.postgresql.org/docs/17/pgtrgm.html>
- **Search terms shorter than 3 characters produce no trigrams and degrade to a full
  index scan.** The docs state this explicitly: "a pattern with no extractable
  trigrams will degenerate to a full-index scan" **[V]**. Kora must keep a
  minimum-keyword-length guard or short queries get *slower* than today.
- Moving off `cypher()` to the label table means you take responsibility for the
  label's storage contract. AGE label tables are ordinary tables, but this is a
  private-ish interface and could change across AGE versions. **[I]** Pin the AGE
  version and cover it with a test.

**Write amplification [M]:** inserting 5000 vertices took **153ms with the text
indexes present** vs **48.6ms without** - roughly **3.1x**. Non-trivial for a
write-heavy memory engine.

### 1.3 Option (b): tsvector generated column + GIN - **recommended**

```sql
ALTER TABLE context0."Memory"
  ADD COLUMN content_tsv tsvector
  GENERATED ALWAYS AS (
    to_tsvector('english',
      coalesce(ag_catalog.agtype_access_operator(
        VARIADIC ARRAY[properties,'"content"'::ag_catalog.agtype])::text, ''))
  ) STORED;

CREATE INDEX memory_content_tsv ON context0."Memory" USING GIN (content_tsv);
```

Generated columns are maintained by PostgreSQL automatically, so there is no trigger
to write and no way for the projection to drift **[V]**
<https://www.postgresql.org/docs/17/ddl-generated-columns.html>. This requires the
expression to be IMMUTABLE, which section 1.1 confirms it is, and requires an
explicit text-search configuration (`'english'`, not the GUC-dependent default) or
the expression is only STABLE and the DDL is rejected.

**This is the fastest option on the query shape the service actually issues [M]:**

| Approach | Time | Notes |
|---|---|---|
| current Cypher CONTAINS | **10.5ms** | project index used, then 2500-row filter + sort |
| trgm composite | **4.6ms** | 2.3x |
| **tsvector + ts_rank** | **2.8ms** | **3.8x** |

Index size 3.0MB, the smallest of the three **[M]**.

The reason the win is smaller than the headline 51x is important and worth stating
plainly: **the service sorts by `created_at DESC`, so it cannot stop early.** It must
retrieve every match before sorting. The 51x figure applies to selective lookups; the
3.8x figure is what production would actually see. I would not report the 51x number
to stakeholders.

**Additional advantages over (a):**
- `ts_rank` gives a real graded lexical score. This directly replaces
  `ranking.LexicalRelevance`, which today re-derives a score in Go by calling
  `strings.Contains` a second time over content the database already scanned. That is
  duplicated work and a weaker signal than BM25-style ranking.
- Stemming means "running" matches "run"; `CONTAINS` and trigrams do not do this.
- `websearch_to_tsquery` gives users quoted phrases and `-exclusion` for free **[V]**
  <https://www.postgresql.org/docs/17/textsearch-controls.html>.

**Correctness risks.** Stemming changes recall semantics - it is a product decision,
not a drop-in. Substring matching inside a word is lost (searching `kube` no longer
matches `kubernetes`). If that matters, **keep both**: tsvector for ranked retrieval,
trgm for substring/fuzzy. They compose, at the cost of the write amplification above.

### 1.4 Option (c): separate relational projection table

A `memory_search(memory_id uuid PK, project_id text, content text, tsv tsvector)`
table alongside the graph.

**Advantages [I]:** total freedom over indexing, no dependence on AGE label-table
internals, and the write path can be made asynchronous so the graph write is never
slowed. It also decouples from AGE version changes.

**Costs.** You now own a dual-write consistency problem. In-transaction dual write
keeps it consistent but adds the write cost back plus a second table's WAL. Async
means the search index is eventually consistent, and a memory engine that just stored
something and cannot find it a moment later is a bad user experience. **[I]**

**Verdict: not worth it now.** Option (b) achieves the same query performance with
zero consistency risk because PostgreSQL maintains the column. Revisit only if you
outgrow a single node, at which point this table is also the natural shard/replica
target.

### 1.5 Option (d): push keyword search onto pgvector (hybrid dense+sparse)

pgvector 0.8 supports `sparsevec` with up to 1000 non-zero elements **[V]** (README,
"Supported types ... `sparsevec` - up to 1,000 non-zero elements"). You could encode
SPLADE-style learned sparse vectors and do both retrievals through one index type.

**Verdict: not appropriate here [I].** Three reasons:
1. It requires a learned sparse model. Kora's embedder is pluggable and the
   default is bag-of-words; there is no SPLADE in the stack, so this is a large new
   dependency, not a tuning change.
2. The 1000-non-zero cap is a real constraint for long memories.
3. It would *replace* an exact-match capability with an approximate one. Users of a
   memory engine expect that searching for a literal identifier finds it. Postgres
   FTS keeps that guarantee; a learned sparse retriever does not.

Reconsider only if you adopt a hosted embedding model that emits sparse vectors.

### 1.6 Recommendation for section 1

Adopt **(b)**, keep **(a)** available behind a flag for substring/fuzzy queries, skip
(c) and (d). Critically, **query the label table with plain SQL rather than through
`cypher()`** - that is what makes any of this reachable by the planner.

---

## 2. RRF vs weighted score merging

### 2.1 What the service does today

`internal/ranking/relevance.go`:

```go
return clamp01(strong + agreementBoost*weak*(1-strong))
```

This merges a **cosine similarity** with a **fraction-of-keywords-matched** score as
if they were commensurable. They are not. Cosine similarity from a dense embedder is
typically compressed into a narrow high band (0.6-0.9) and its absolute value is
model-dependent, while `LexicalRelevance` is quantized to a few discrete values
(0, 0.75, 1.0 for a single keyword). Adding them with a fixed weight means **the
weight is implicitly calibrated to whichever embedding model happens to be
configured**, and Kora's embedder is swappable (384/768/1536-dim). Change the
model and the ranking silently changes character. **[I]**, but it follows directly
from the code.

### 2.2 RRF

$$\text{RRF}(d) = \sum_{r \in R} \frac{1}{k + \text{rank}_r(d)}$$

From Cormack, Clarke and Buettcher, SIGIR 2009 **[V]**
<https://plg.uwaterloo.ca/~gvcormac/cormacksigir09-rrf.pdf>. The paper's central
result is that this simple rank-based fusion outperformed both the individual systems
and the considerably more complex Condorcet fusion and score-standardization methods
they compared against, using $k=60$.

**Why it fits Kora specifically:** RRF consumes only *ranks*, so it never
compares a cosine similarity to a keyword score. It is therefore immune to the
embedder-swap problem above. That is a structural robustness gain, not a tuning gain.

Azure AI Search uses RRF as its production hybrid ranker and confirms $k=60$ **[V]**
<https://learn.microsoft.com/en-us/azure/search/hybrid-search-ranking>: "Experiments
show the algorithm performs best when you set `k` to a small value, such as 60."

Note the Azure doc also documents **weighted RRF** - per-retriever multipliers applied
before fusion. That is the natural way to keep the "tags are stronger evidence" and
"agreement is evidence" intuitions that Kora's current code encodes, without
reintroducing raw-score comparison.

### 2.3 State of the art, 2026

Honest characterization: **RRF is the default, not the frontier.** The current
consensus pipeline is *retrieve (dense + lexical) -> fuse with RRF -> rerank the top
~50-100 with a cross-encoder*, where the reranker contributes most of the remaining
quality **[V]**, e.g.
<https://www.digitalapplied.com/blog/hybrid-search-bm25-vector-reranking-reference-2026>,
<https://denser.ai/blog/hybrid-search-for-rag/>,
<https://redis.io/blog/reciprocal-rank-fusion/>.

I want to flag a caveat about these sources: they are engineering blog posts, and
several are content-marketing for vendors. They agree with each other and with the
Azure primary source on the shape of the pipeline and on $k=60$, which is why I cite
them, but I would not treat their specific quality percentages as reliable. The
Cormack paper and the Azure documentation are the two sources here I would actually
stand behind.

**Recommendation for Kora:** switch fusion to weighted RRF. Do *not* add a
cross-encoder reranker - it implies a model server on the query path and Kora's
value proposition is a self-contained Go binary plus Postgres. **[I]**

One caution: RRF discards score magnitude, which means it cannot express "nothing
matched well." Keep an absolute similarity floor before fusion so a query with no
good match returns few results rather than confidently returning the least-bad ones.

### 2.4 Interaction with `ranking.RankResults`

RRF outputs are small numbers around $1/(60+1) \approx 0.016$, on a scale unrelated to
the `[0,1]` that `scorer.go` expects to blend with recency/frequency/type. Feeding RRF
in raw would effectively zero out relevance. Normalize the fused score across the
candidate set (min-max over the returned page) before it reaches the composite scorer.
**[I]** This is the most likely way to get RRF subtly wrong.

---

## 3. pgvector tuning

All measured on 20k x 768-dim vectors, `maintenance_work_mem = 1GB` **[M]**.

### 3.1 HNSW build parameters

| Config | Build time | Index size |
|---|---|---|
| `m=16, ef_construction=64` (defaults) | 1.84s | 59 MB |
| `m=16, ef_construction=200` | 1.36s | 76 MB |
| `m=32, ef_construction=200` | 3.50s | 75 MB |
| `halfvec, m=16, ef_construction=64` | 1.07s | **38 MB** |

`m` is max connections per layer (default 16) and `ef_construction` is the build-time
candidate list (default 64); "a higher value of `ef_construction` provides better
recall at the cost of index build time / insert speed" **[V]** (pgvector README).
Doubling `m` roughly doubled build time **[M]**, consistent with the documented
tradeoff.

The README's own advice is to **"use the defaults unless seeing low recall"** **[V]**.
Given Kora has no recall measurement in place, that is the correct posture: do not
tune blind.

### 3.2 `hnsw.ef_search`

Default 40; "a higher value provides better recall at the cost of speed" **[V]**.
`SET LOCAL` inside a transaction scopes it to one query **[V]** - exactly the pattern
`nearestNeighbours` already uses for `iterative_scan`, so adding `ef_search` there is
a one-line change with no new machinery.

**Recommended:** raise to 100 for the project-scoped path. **[I]** Rationale: the
filter is applied after the index scan, so a scoped query starts from a candidate pool
already thinned by roughly the project's share of the corpus; a larger initial pool
directly counteracts that. This is the same reasoning that made `iterative_scan`
necessary.

### 3.3 An honest failure: I could not measure recall

I built a recall harness and **it produced recall = 1.000 for every configuration,
which is not a believable result.** Two bugs, both of which I want to state rather
than quietly drop:

1. My first harness passed the query vector via a correlated subquery, so the planner
   used a **seq scan, not the HNSW index** - I confirmed this in `EXPLAIN` **[M]**.
   It was measuring exact search against exact search.
2. After fixing that, recall was still 1.000 because **my query vectors were drawn
   from the indexed table itself.** A vector that is in the index is trivially found.

So: **the recall-vs-`ef_search` tradeoff is unmeasured here [U].** The build-time and
size numbers above are sound; any recall claim would need held-out query vectors and
real embeddings. I have written this up as a measurement task in section 7 rather than
guessing. The general direction (higher `ef_search` -> higher recall, slower) is
documented by pgvector **[V]**; the magnitude *for Kora's data* is unknown.

### 3.4 When to prefer IVFFlat

IVFFlat has "faster build times and uses less memory than HNSW, but has lower query
performance (in terms of speed-recall tradeoff)" **[V]**. It also requires the table
to have data before building, and a `lists` parameter of `rows/1000` up to 1M rows
**[V]**.

**Verdict for Kora: stay on HNSW [I].** IVFFlat's requirement that the index be
built after data exists conflicts with `InitSchema` running on every startup against a
possibly-empty database, and a memory engine's corpus grows continuously, so the
k-means centroids would drift and need periodic rebuilds. HNSW's "index can be created
without any data" property **[V]** is precisely what `InitSchema` needs.

### 3.5 Quantization: halfvec and binary

**halfvec: production-ready, and the best value here.** It is a first-class type,
indexable up to 4000 dimensions **[V]**. Measured **59MB -> 38MB, a 36% reduction,
with a faster build** **[M]**. It can be applied as an expression index without
changing the stored column:

```sql
CREATE INDEX ON public.memory_embeddings
  USING hnsw ((embedding::halfvec(768)) halfvec_cosine_ops);
```

The query must then cast identically (`embedding::halfvec(768) <=> $1::halfvec(768)`)
or the index will not be used - the same exact-match rule as section 1.1. Because the
underlying `vector` column is retained at full precision, **this is reversible**: drop
the index and you have lost nothing. That makes it a low-risk change.

Accuracy impact is **[U]** for Kora's data - unmeasured for the same reason as
3.3. fp16 retains ~3 decimal digits, and for cosine similarity over normalized
embeddings the effect on ranking is generally small **[I]**, but "generally small"
should be verified before it ships.

**Binary quantization: real, but not for this workload.** pgvector supports `bit`
vectors with Hamming/Jaccard distance **[V]**, and the README recommends it for
"faster build times at scale" **[V]**. Binary quantization typically costs substantial
recall and is used as a *first-stage* filter with full-precision reranking. At
Kora's scale that machinery is unjustified: halfvec gets a solid memory win with
far less risk. **[I]**

---

## 4. Go-side wins

### 4.1 pgx prepared statement caching - confirmed on by default

Verified two ways. From pgx source **[V]**
<https://github.com/jackc/pgx/blob/master/conn.go>:

```go
defaultQueryExecMode := QueryExecModeCacheStatement
statementCacheCapacity := 512
```

And observed at runtime in my Go harness **[M]**:

```
pgx DefaultQueryExecMode = cache statement
```

So **there is no win available here - it is already optimal.** The docstring for
`QueryExecModeCacheStatement` reads: "Automatically prepare and cache statements...
Queries are executed in a single round trip after the statement is cached. This is the
default." **[V]**

**But there is a real problem hiding behind this, specific to Kora's design.**
`cypherSQL` interpolates the Cypher body into the SQL string:

```go
fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$, $1) AS (result ag_catalog.agtype)`, ...)
```

The statement cache is keyed on the SQL text, so **every distinct Cypher shape
occupies its own cache slot.** `QueryMemories` builds its text from the number of
keywords and types, so the shape count grows combinatorially with filter variety.
With a 512-entry LRU and enough filter shapes, the cache thrashes: each miss costs a
`Prepare` round trip, and the server accumulates prepared statements. The existing
comment in `age.go` ("The generated Cypher text therefore depends solely on how many
keywords and types were supplied") shows the shape count is bounded, but bounded by a
product of two variables. **[I] - the mechanism is certain from the pgx source; that
Kora actually exceeds 512 shapes is not, and would need `pg_prepared_statements`
monitoring to confirm.**

Moving keyword search to a fixed-text SQL statement (section 1.3) removes this class
of problem for the hottest path.

### 4.2 pgx `SendBatch`

**There is currently no `SendBatch` anywhere in the codebase [M]** (grep: 0 matches).

The clear candidate is `Query` in `internal/service/memory.go`, which after ranking
issues three sequential round trips:

```go
contextEdges, _ := s.repo.GetContextEdges(ctx, ids)
...
_ = s.repo.IncrementAccessCounts(ctx, ids)
```

plus the earlier graph and vector queries. `GetContextEdges` and
`IncrementAccessCounts` are independent and both operate on the same `ids`. Batching
them collapses two round trips into one **[I]**.

Expected benefit is proportional to RTT, so it is near-worthless on a loopback socket
and material when Postgres is a network hop away - which is the Kubernetes deployment
this repo targets. At ~1ms RTT this saves ~1ms per query; against a measured 10.5ms
keyword search it is ~10%, so it is real but clearly second-order to section 1.

One caution: `IncrementAccessCounts` is deliberately fire-and-forget (`_ =`) so a
failure cannot fail the read. In a batch, pgx surfaces errors per-queued-statement,
so that error-isolation property must be preserved explicitly.

### 4.3 `properties(m)` vs individual fields - measured

Same filter, 2500 rows, warm cache **[M]**:

| Return shape | First run | Warm |
|---|---|---|
| `RETURN properties(m)` | 3.27ms | 0.66ms |
| `RETURN m.id, m.content, m.project_id` | 0.55ms | 0.53ms |
| plain SQL, no `cypher()` | 0.54ms | 0.48ms |

`properties(m)` is **~5x slower on first execution** and ~25% slower warm. The
first-execution gap is the interesting one for a service with many distinct query
shapes (see 4.1), because a cache-missing shape pays the cold cost every time.

Note also that `properties(m)` forces AGE to materialize the full property map,
serialize it to agtype text, ship it, and then Go re-parses it with
`json.Unmarshal` in `scanAgtype`. Returning only needed fields shrinks all four
stages. `hydrate` is the best candidate: it currently pulls full property maps for
every vector hit.

### 4.4 agtype unmarshalling overhead

`scanAgtype` scans each row into a `string` then `json.Unmarshal`s it. That is two
allocations and a full parse per row.

The honest answer: **I did not profile the Go side, so the size of this win is
unmeasured [U].** What I can say from the measurements above is that the database-side
gap between `properties(m)` and named fields (3.27ms -> 0.55ms) is larger than any
plausible JSON-parsing saving on 2500 small rows, so **narrowing the returned data is
the higher-leverage change, and it also reduces the parsing cost as a side effect.**
I would not invest in a hand-rolled agtype parser before doing section 1.

`scanAgtype` also silently swallows per-row scan and unmarshal errors (`continue`).
That is defensible for robustness, but it means a systematic schema mismatch would
present as "queries return fewer results than expected" with no error anywhere. Worth
a metric counter. **[I]**

---

## 5. Connection pooling - the most important finding

### 5.1 `SET LOCAL` survives transaction pooling. The `search_path` hook does not.

The brief asks specifically whether `SET LOCAL hnsw.iterative_scan` survives
transaction-mode pooling. **It does** - verified through a real PgBouncer 1.25.2 in
`pool_mode = transaction` **[M]**:

```
SET LOCAL inside txn -> strict_order
```

This is expected: transaction mode keeps a server connection assigned for the whole
transaction, and `SET LOCAL` is scoped to that transaction.

**But the service would break anyway, for a different reason, and I reproduced it.**

`NewPool` sets `search_path` via `cfg.AfterConnect`. That is *session* state, applied
once when pgx opens a connection. PgBouncer's own compatibility matrix marks
`SET/RESET` as **"Never"** supported in transaction pooling **[V]**
<https://www.pgbouncer.org/features.html>.

Reproduction, pgx v5 with Kora's exact `AfterConnect` hook, through PgBouncer in
transaction mode **[M]**:

```
before poison: search_path="ag_catalog, \"$user\", public"  cypherErr=<nil>   (AfterConnect ran 1x)
>>> another client runs: SET search_path = pg_catalog
after poison:  search_path="pg_catalog"
               cypherErr=ERROR: type "agtype" does not exist (SQLSTATE 42704)
               (AfterConnect ran 1x)
```

That is **verbatim the failure mode the code comment in `age.go` was written to
prevent**:

> connections where `agtype` does not resolve and every Cypher query fails with
> `type "agtype" does not exist`. The AfterConnect hook makes that impossible.

The hook makes it impossible *for a direct pgxpool connection*. It does not, and
cannot, make it impossible behind a transaction-mode pooler, because pgx's notion of
"a connection" stops corresponding to a server backend. `AfterConnect` ran exactly
once while the underlying server session changed underneath it.

I also demonstrated plain **state leakage between clients** **[M]**: with
`default_pool_size = 1`, client A ran `SET search_path = ag_catalog, public` and client
B, on a separate connection, observed A's value. The reason is
`server_reset_query_always = 0` by default **[M]**, which means the `DISCARD ALL`
reset query is **not** applied in transaction mode. So the state neither reliably
persists nor reliably resets - the worst of both.

**Mitigations, in order of preference [I]:**
1. **Session-mode pooling.** Preserves all session state; you lose most of the
   multiplexing benefit.
2. **Set `search_path` as a server-side default** so it is not session state at all:
   `ALTER ROLE kora SET search_path = ag_catalog, "$user", public`. This survives
   transaction pooling because every new backend picks it up, and it removes the need
   for the `AfterConnect` hook entirely. **This is the clean fix** and is worth doing
   regardless of whether PgBouncer is ever deployed, because it makes the service
   robust to a class of deployment it currently silently fails under.
3. **Schema-qualify everything.** The code already qualifies `ag_catalog.cypher(...)`,
   but the result column type `ag_catalog.agtype` is qualified in some paths and the
   error above shows at least one unqualified dependency remains.

Also note pgx's default `QueryExecModeCacheStatement` uses protocol-level prepared
statements, which transaction mode supports **only** with `max_prepared_statements`
non-zero **[V]** (PgBouncer features table, footnote 2). The instance I tested
defaulted to `200` **[M]**, so this is fine on PgBouncer 1.21+, but it is a version
dependency worth pinning.

### 5.2 At what concurrency does PgBouncer pay for itself?

The service currently defaults to `MaxConns = 10`, `MinConns = 2`, with the explicit
reasoning that pgxpool's default (node core count) is wrong under Kubernetes. That
reasoning is sound.

Total backend demand is `replicas x MaxConns`. Against PostgreSQL's default
`max_connections = 100` **[V]**
<https://www.postgresql.org/docs/17/runtime-config-connection.html>, 10 replicas
saturates the server.

**Guidance [I]:** PgBouncer starts paying for itself when `replicas x MaxConns`
approaches ~50-70% of `max_connections`, i.e. **around 5-7 replicas at the current
settings**. Below that it adds a hop, a process to operate, and the failure mode in
5.1, for no benefit.

I want to be clear that the "5-7 replicas" figure is reasoning from the configured
numbers, **not a measured throughput crossover** - I did not run a concurrency sweep,
so treat it as a trigger for measurement rather than a threshold to act on blindly.
**[U]**

Since `pgxpool` is already a pool, the benefit of adding PgBouncer is specifically
*cross-replica* connection sharing, which only matters once replica count is the thing
exhausting the server. Before reaching for PgBouncer, simply lowering `MaxConns` is
often enough and has none of the downsides.

---

## 6. Caching

### 6.1 Embedding cache - **yes, clearly worth it [I]**

`Query` calls `s.embedder.Embed(req.Query)` on **every** request. For any
network-backed embedder (OpenAI, Ollama) that is tens to hundreds of milliseconds and
dominates everything discussed in this document; the graph query it feeds is 10.5ms.

**Invalidation is trivial, which is what makes this the best caching candidate:** the
cache key is the query string and the value depends only on `(model, text)`. Embeddings
of a fixed string under a fixed model are **immutable**. There is no invalidation
problem at all - only eviction. An LRU of a few thousand entries keyed on
`hash(model_id + query_text)` is correct by construction. Include the model identity in
the key so a model swap cannot serve stale vectors.

This sidesteps the entire "writes are frequent" concern in the brief, because it caches
a pure function, not database state.

### 6.2 Hot project metadata - marginal [I]

Project metadata is small and already fronted by an index. The saving is one indexed
lookup, well under a millisecond. Not worth an invalidation story.

### 6.3 Query-result caching - **do not [I]**

This is where the brief's concern is well founded. Kora is a memory engine whose
entire purpose is that something stored is immediately retrievable. Caching result sets
means a `Store` must invalidate every cached query whose results *might* have changed -
which, for semantic search, is undecidable without running the search. The available
strategies are all bad: project-wide invalidation on any write (useless under frequent
writes) or short TTLs (a window in which the engine denies knowing what it was just
told).

There is a further correctness trap specific to this service: `Query` calls
`IncrementAccessCounts`, and access counts feed ranking and consolidation. Serving from
cache would skip that increment, so caching would **silently corrupt the decay and
consolidation signal** over time. That is a strong argument against it independent of
staleness.

**Recommendation: cache embeddings, do not cache results.**

---

## 7. How to measure each recommendation

Measurement matters more than usual here, because two of my own measurements were
invalid on the first attempt (section 3.3) and one headline number was misleading
(51x vs the realistic 3.8x, section 1.3).

| # | Change | How to measure | Success criterion |
|---|---|---|---|
| 1 | Avoid/repair PgBouncer transaction mode | Reproduce the harness from 5.1: pgxpool with `AfterConnect`, PgBouncer `pool_mode=transaction`, `default_pool_size=1`, second client sets `search_path`, then issue a Cypher query | Zero `type "agtype" does not exist`. Add as a CI integration test - it is a correctness regression test, not a benchmark |
| 2 | tsvector keyword search | `EXPLAIN (ANALYZE, BUFFERS)` on the **real** query shape *including* `ORDER BY created_at DESC LIMIT k`, at 10k/100k/1M rows, comparing against today's Cypher `CONTAINS` | `Bitmap Index Scan` present; p95 latency flat as corpus grows. Watch the **slope**, not the single-point speedup |
| 2b | Write amplification | Time bulk insert of 5k vertices with and without the new indexes (as in 1.2) | Regression stays within budget; measured 3.1x for trgm, so measure tsvector separately |
| 3 | RRF fusion | Offline: a labelled query set with nDCG@10 / recall@10, RRF vs current `CombineRelevance`. Then swap the embedder to a different dimension and re-run **both** | RRF wins or ties on nDCG, and **is materially more stable across the embedder swap** - that stability is the actual argument |
| 4 | `ef_search` / halfvec | **First build a valid recall harness**: query vectors held out of the index, ground truth from exact scan with the ANN index dropped. Sweep `ef_search` and fp32 vs halfvec | Recall@10 within ~1% of fp32 at 36% smaller index. Sanity check: if recall is exactly 1.000, the harness is broken |
| 5 | Named fields / `SendBatch` | `EXPLAIN ANALYZE` per query plus Go `pprof`; measure `SendBatch` with an artificial ~1ms RTT, since loopback hides the whole effect | Round trips per `Query` drop from 4 to 2; p95 improves under injected latency |
| 6 | Embedding cache | Cache hit-rate metric plus p50/p95 of `metrics.QueryDuration`, with a network-backed embedder | Hit rate >60% on realistic traffic; p95 drops by the embedder's latency times hit rate |

Two cross-cutting notes:

- **`metrics.QueryDuration` already exists** in `Query`. Sub-timers around retrieval,
  embedding, fusion, and hydration would make every row above measurable in production
  rather than only on a bench. That instrumentation is the cheapest high-value change
  in this document.
- Benchmark at **several corpus sizes**. Every optimization here changes an asymptote,
  and a single-size benchmark cannot distinguish a constant-factor win from an
  algorithmic one - which is exactly the trap the 51x-vs-3.8x discrepancy illustrates.

---

## 8. Summary of what is verified, inferred, and unknown

**Verified by measurement [M]:** no operator exists for AGE `CONTAINS`; all relevant
agtype casts are IMMUTABLE; `agtype::text` is `agtype_to_text` and returns unquoted
text; pg_trgm GIN and tsvector GIN expression indexes over agtype both build and are
genuinely used; 25ms baseline seq scan; 10.5ms -> 4.6ms (trgm) -> 2.8ms (tsvector) on
the real query shape; ~3.1x insert write amplification; HNSW build/size across
`m`/`ef_construction`; halfvec 59MB -> 38MB; `properties(m)` 3.27ms vs named fields
0.55ms; pgx defaults to `QueryExecModeCacheStatement`; no `SendBatch` in the codebase;
`SET LOCAL` survives transaction pooling; **`AfterConnect` `search_path` does not, and
the service fails with `type "agtype" does not exist`**; PgBouncer
`server_reset_query_always = 0` by default.

**Verified against primary sources [V]:** pgvector HNSW/IVFFlat parameters, defaults,
iterative scan, halfvec, binary vectors, filtering behaviour (pgvector README);
pg_trgm index support and the sub-3-character degradation (PostgreSQL docs); PgBouncer
pooling-mode compatibility matrix (pgbouncer.org); pgx exec-mode defaults (pgx source);
RRF formulation and result (Cormack et al., SIGIR 2009); RRF $k=60$ and weighted RRF
(Microsoft Learn).

**Inference, flagged inline [I]:** the ~5-7 replica PgBouncer threshold; RRF's
robustness advantage under embedder swaps; the statement-cache-thrash argument in 4.1;
halfvec accuracy being acceptable; the recommendation against sparse vectors and
against result caching.

**Not verified [U]:** recall vs `ef_search` for Kora's data, and halfvec's accuracy
cost - my harness was invalid twice and I did not obtain a trustworthy number; Go-side
JSON unmarshalling overhead, which I did not profile; whether Kora actually exceeds
pgx's 512-statement cache in practice; the PgBouncer concurrency crossover, which is
reasoning from configuration rather than a measured sweep.

The most important caveat: **the headline "51x" for indexed keyword search is not what
production would see.** Because the service sorts by `created_at DESC`, it cannot
terminate early, and the realistic figure is **3.8x**. I would report 3.8x.
