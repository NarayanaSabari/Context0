package graph

// fts.go is keyword retrieval done in SQL rather than in Cypher.
//
// # Why this is not a Cypher query
//
// The graph retriever matched keywords with Cypher `CONTAINS`, which is
// substring matching with no term weighting: `the` counted exactly as much as
// `zqxjklmw`, and it matched inside words, so `go` matched `going`, `mango`
// and `algorithm`.
//
// It also could not be indexed, which docs/research/keyword-search-indexing.md
// established by measurement: a GIN trigram index over the same expression is
// sound and helps when queried through SQL, and AGE refuses it even under
// `enable_seqscan=off`, because `agtype_string_match_contains` is an ordinary
// function to the planner with no operator class binding it to any index.
// Refusing an index under `enable_seqscan=off` is not a costing decision; it
// means no index can serve the predicate at all. That left keyword search as a
// sequential scan whose cost grew with the whole graph: 44,809 rows scanned and
// 38,876 discarded for one term.
//
// That document's own recommendation was to move keyword retrieval to
// PostgreSQL full-text search executed as SQL against the Memory table and
// joined back to the graph by id. This is that.
//
// # What changes
//
// `to_tsvector` lexes and stems, so `go` matches `going` and no longer matches
// `mango` or `algorithm`. `ts_rank_cd` weights by term frequency and proximity,
// so a rare term outweighs a common one -- measured on the same text,
// `zqxjklmw` ranks 0.1 against 0.0 for `the`, which is not a difference
// `CONTAINS` can express at all. And the whole thing plans as a Bitmap Index
// Scan against a GIN index, which is what `CONTAINS` could never do.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// textSearchConfig is the dictionary used for both indexing and querying.
//
// It must be identical in both places or the index cannot serve the query:
// to_tsvector('english', x) and to_tsvector('simple', x) are different
// expressions, and a GIN index is built over one exact expression.
//
// 'english' rather than 'simple' because stemming is most of the value here.
// It is what makes "adopted" match a query for "adopt" and what removes stop
// words from the ranking entirely, which is the substance of "rare terms
// outweigh common ones".
//
// The cost is that a non-English corpus gets English stemming, which is
// wrong-but-harmless: unrecognised words are left as their own lexemes, so
// matching degrades to exact-token rather than breaking.
const textSearchConfig = "english"

// contentExpr is the SQL expression extracting a Memory vertex's searchable
// text: its content, plus its tags.
//
// AGE keeps every property inside one agtype column, so property access is the
// only way to reach either from SQL, and the FTS index has to be built over
// exactly this expression for the planner to use it. Any difference between
// the index's spelling and the query's produces an index the planner silently
// ignores, which is indistinguishable from it working.
//
// Tags are included because the retriever this replaces matched
// `content OR tags`, and dropping them would make a memory findable by every
// word of its prose but not by the deliberate label someone attached to it --
// a regression with no symptom except worse results. They are stored as a JSON
// array string, so concatenating them is enough for to_tsvector to lex the
// individual tags out; `coalesce` covers the untagged case, where the property
// is absent and the access returns SQL NULL rather than an empty string.
const contentExpr = `(coalesce((ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties, '"content"'::ag_catalog.agtype]))::text, '') || ' ' || ` +
	`coalesce((ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties, '"tags"'::ag_catalog.agtype]))::text, ''))`

// contentExprFor is contentExpr qualified by a table alias, for the subquery
// that counts documents per term.
func contentExprFor(alias string) string {
	return strings.ReplaceAll(contentExpr, "properties", alias+".properties")
}

// projectExprFor is projectExpr qualified by a table alias.
func projectExprFor(alias string) string {
	return strings.ReplaceAll(projectExpr, "properties", alias+".properties")
}

// idExpr is the same, for the id property.
const idExpr = `(ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties, '"id"'::ag_catalog.agtype]))::text`

// projectExpr is the same, for project_id.
const projectExpr = `(ag_catalog.agtype_access_operator(VARIADIC ARRAY[properties, '"project_id"'::ag_catalog.agtype]))::text`

// initFullTextSchema builds the GIN index backing keyword retrieval.
// Idempotent, and called from InitSchema on every startup.
func (r *AGERepository) initFullTextSchema(ctx context.Context) error {
	// The index expression is spelled exactly as the query spells it,
	// including the configuration name. Any difference -- a different
	// dictionary, a missing cast -- produces an index the planner silently
	// cannot use, which looks identical to it working.
	stmt := fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS memory_content_fts_idx ON %s."Memory" `+
			`USING gin (to_tsvector('%s', %s))`,
		GraphName, textSearchConfig, contentExpr,
	)
	if _, err := r.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("create full-text index: %w", err)
	}

	// A GIN index over an expression has no statistics until ANALYZE runs, and
	// without them the planner may still choose a sequential scan -- the exact
	// state this index exists to avoid.
	if _, err := r.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Memory"`, GraphName)); err != nil {
		slog.Warn("could not analyze the Memory label after creating the full-text index; "+
			"keyword search may sequential-scan until autoanalyze runs",
			slog.Any("error", err))
	}

	return nil
}

// KeywordHit is a memory matched by full-text search, with its lexical rank.
type KeywordHit struct {
	// ID is the matched memory.
	ID uuid.UUID
	// Rank is the raw ts_rank_cd score. Unbounded above and not comparable
	// across queries of different lengths, which is why the caller squashes it
	// through a query-length-adaptive sigmoid rather than using it directly.
	// See ranking.NormalizeBM25.
	Rank float64
}

// termsCTE lexes each query term on its own and computes its inverse document
// frequency within the project.
//
// # Why per-term, and why IDF
//
// ts_rank_cd measures term frequency and cover density -- how often the terms
// occur in a document and how close together -- and has no inverse document
// frequency at all. Verified against Postgres 18: a term appearing in one
// document of the corpus and one appearing in 1,775 both rank 0.1 on an
// equivalent document.
//
// So ts_rank_cd alone does not deliver "rare terms outweigh common ones". It
// looks like it does, because a query for `the` scores 0 -- but that is the
// stop-word dictionary, not weighting, and it says nothing about the ordinary
// words that make up most of a question. In this engine's own corpus `said`
// appears in 1,775 of 4,638 memories and carries almost no information, and
// no dictionary will remove it.
//
// IDF is therefore computed explicitly, with the standard BM25 form
// `ln(1 + (N - df + 0.5) / (df + 0.5))`. Measured on the same corpus: `biscuit`
// (386 documents) scores 2.49 and `said` (1,775) scores 0.96, so a memory
// matching the distinctive term outranks one matching the common one by more
// than a factor of two.
//
// Each df is one Bitmap Index Scan against the same GIN index the search uses,
// so the cost is a handful of index probes rather than a scan.
//
// # Why terms are lexed individually
//
// The obvious alternative is to join them into `"a OR b OR c"` and hand that
// to websearch_to_tsquery. That is wrong, and silently so: it parses its whole
// input as one search expression, so a term containing its syntax rewrites the
// query around it. Verified against Postgres 18:
//
//	'cats OR say"hello OR dogs'  ->  'cat' | 'say' & 'hello' <2> 'dog'
//	'cats OR -known'             ->  'cat' | !'known'
//
// The first turns the final OR into an AND and a phrase distance; the second
// turns a search term into a negation, so a query for `-known` excludes
// exactly what it asked for. Both are reachable, because extractKeywords
// strips punctuation only at a token's edges -- deliberately, so `node.js` and
// `well-known` survive intact, and so does an embedded quote.
//
// Lexing each term alone means a term can only ever produce its own lexemes.
const termsCTE = `terms AS (
	SELECT plainto_tsquery('` + textSearchConfig + `', t) AS q
	FROM unnest($1::text[]) AS t
), lexed AS (
	SELECT q FROM terms WHERE q::text <> ''
)`

// idfExpr weights a term by how rare it is in the scoped corpus, using the
// standard BM25 IDF. See termsCTE.
const idfExpr = `ln(1 + (corpus.n - df.d + 0.5) / (df.d + 0.5))`

// SearchByKeywords ranks memories by full-text relevance to the query terms.
//
// The score is the IDF-weighted sum of each term's ts_rank_cd against the
// memory: sum over terms of idf(term) * ts_rank_cd(memory, term). That is the
// shape of BM25 -- a per-term relevance weighted by the term's rarity -- built
// from the two pieces Postgres provides separately.
//
// Terms are OR-ed, so a memory matching any of them is a candidate and the
// weighted sum decides how good a one. AND would be wrong here: this is the
// recall-oriented retriever in a hybrid, and requiring every term turns a
// five-word question into a near-empty result set.
//
// Returns raw scores. Normalising them is the ranking layer's job, because the
// right normalisation depends on the query length and the ranking layer is
// where that decision is documented and tested.
//
// An empty result is not an error: a query whose terms are all stop words has
// nothing to match.
func (r *AGERepository) SearchByKeywords(ctx context.Context, projectID string, keywords []string, limit int) ([]KeywordHit, error) {
	if len(keywords) == 0 || limit <= 0 {
		return nil, nil
	}

	// Every value is a parameter; only GraphName and the text search
	// configuration are interpolated, and both are compile-time constants of
	// this package.
	//
	// project_id compares against the extracted text directly. AGE stores
	// string properties as JSON, but `agtype_access_operator(...)::text`
	// unwraps the quotes on the way out, so the extracted value is the plain
	// string -- verified against the live table rather than assumed, because
	// the failure mode is silent: a mismatched comparison returns no rows,
	// which is indistinguishable from a project having no memories.
	//
	// The scoped and unscoped forms are written out rather than assembled from
	// fragments. They differ by one predicate in three places, and building
	// that by string surgery is how a WHERE ends up in the wrong clause of a
	// query nothing will fail loudly about.
	var (
		sql  string
		args []any
	)

	if projectID == "" {
		sql = fmt.Sprintf(`
WITH %s,
corpus AS (SELECT GREATEST(count(*), 1)::float8 AS n FROM %s."Memory"),
df AS (
	SELECT lexed.q, GREATEST((
		SELECT count(*) FROM %s."Memory" m
		WHERE to_tsvector('%s', %s) @@ lexed.q
	), 1)::float8 AS d
	FROM lexed
)
SELECT id, sum(weighted) AS rank FROM (
	SELECT %s AS id, %s * ts_rank_cd(to_tsvector('%s', %s), df.q) AS weighted
	FROM %s."Memory", df, corpus
	WHERE to_tsvector('%s', %s) @@ df.q
) AS scored
GROUP BY id ORDER BY rank DESC LIMIT $2`,
			termsCTE,
			GraphName,
			GraphName, textSearchConfig, contentExprFor("m"),
			idExpr, idfExpr, textSearchConfig, contentExpr,
			GraphName,
			textSearchConfig, contentExpr,
		)
		args = []any{keywords, limit}
	} else {
		sql = fmt.Sprintf(`
WITH %s,
corpus AS (SELECT GREATEST(count(*), 1)::float8 AS n FROM %s."Memory" WHERE %s = $2),
df AS (
	SELECT lexed.q, GREATEST((
		SELECT count(*) FROM %s."Memory" m
		WHERE %s = $2 AND to_tsvector('%s', %s) @@ lexed.q
	), 1)::float8 AS d
	FROM lexed
)
SELECT id, sum(weighted) AS rank FROM (
	SELECT %s AS id, %s * ts_rank_cd(to_tsvector('%s', %s), df.q) AS weighted
	FROM %s."Memory", df, corpus
	WHERE %s = $2 AND to_tsvector('%s', %s) @@ df.q
) AS scored
GROUP BY id ORDER BY rank DESC LIMIT $3`,
			termsCTE,
			GraphName, projectExpr,
			GraphName, projectExprFor("m"), textSearchConfig, contentExprFor("m"),
			idExpr, idfExpr, textSearchConfig, contentExpr,
			GraphName,
			projectExpr, textSearchConfig, contentExpr,
		)
		args = []any{keywords, projectID, limit}
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var hits []KeywordHit
	for rows.Next() {
		var rawID string
		var rank float64
		if err := rows.Scan(&rawID, &rank); err != nil {
			return nil, fmt.Errorf("scan keyword hit: %w", err)
		}
		id, perr := uuid.Parse(strings.Trim(rawID, `"`))
		if perr != nil {
			continue
		}
		hits = append(hits, KeywordHit{ID: id, Rank: rank})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}

	return hits, nil
}

// hydrateKeywordHits loads the full memories behind a set of keyword hits,
// preserving the ranking order.
//
// One round trip for the whole set, for the same reason GetContextEdges and
// IncrementAccessCounts batch: this runs on the read path, and a lookup per hit
// would put a round trip per candidate on every query.
func (r *AGERepository) hydrateKeywordHits(ctx context.Context, hits []KeywordHit) ([]model.MemoryWithContext, error) {
	if len(hits) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	list, err := uuidLiteralList(ids)
	if err != nil {
		return nil, err
	}

	// Literal id list, not a parameter: see uuidLiteralList for why the
	// parameterized forms fall off the index when statistics are stale.
	q := `MATCH (m:Memory) WHERE m.id IN ` + list + ` RETURN properties(m)`
	rows, err := r.cypher(ctx, q, nil)
	if err != nil {
		return nil, fmt.Errorf("hydrate keyword hits: %w", err)
	}
	props, err := scanAgtype[memoryProps](rows)
	if err != nil {
		return nil, fmt.Errorf("scan keyword hits: %w", err)
	}

	byID := make(map[uuid.UUID]model.Memory, len(props))
	for _, p := range props {
		mem := p.toModel()
		byID[mem.ID] = mem
	}

	// Rebuilt in hit order rather than in the order the graph returned them,
	// so the ranking the SQL query computed survives hydration.
	out := make([]model.MemoryWithContext, 0, len(hits))
	for _, h := range hits {
		mem, ok := byID[h.ID]
		if !ok {
			// The memory was deleted between the two queries. Skipping is
			// correct: returning a zero-valued memory would surface an empty
			// result to the caller.
			continue
		}
		out = append(out, model.MemoryWithContext{Memory: mem, Score: h.Rank})
	}
	return out, nil
}

// SearchByText is the full keyword retrieval path: rank in SQL, hydrate from
// the graph.
//
// Hydration runs after the search has released its connection, matching
// SearchByVector: holding two pool connections per call deadlocks the pool as
// soon as concurrency reaches MaxConns, which is a bug this repository has
// already had once.
func (r *AGERepository) SearchByText(ctx context.Context, projectID string, keywords []string, limit int) ([]model.MemoryWithContext, error) {
	hits, err := r.SearchByKeywords(ctx, projectID, keywords, limit)
	if err != nil {
		return nil, err
	}
	return r.hydrateKeywordHits(ctx, hits)
}
