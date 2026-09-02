// Package retrieval answers a query against the memory graph.
//
// It exists because the read path is the part of this engine whose behaviour
// is hardest to reason about and easiest to break: three retrievers whose
// results have to be put on one scale, a fallback for queries with nothing to
// search for, and candidate pools that decide what ranking is even allowed to
// consider. Living inside the gRPC service, none of that could be exercised
// without a request around it.
//
// The boundary is deliberate. This package decides what comes back and in what
// order. It does not translate protobuf, count metrics, load context edges, or
// record access counts: those are about serving a request rather than about
// retrieval.
//
// Per docs/adr/0001-apache-age-is-load-bearing.md, the Repo interface below is
// a test seam and not a portability claim.
package retrieval

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/extraction"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/logging"
	"github.com/NarayanaSabari/Kora/internal/ranking"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// Repo is the slice of the graph repository the read path needs.
//
// Narrow on purpose: it names six operations out of the repository's several
// dozen, so a test can supply a fake without implementing a graph store, and a
// reader can see the whole surface the read path touches.
type Repo interface {
	// SearchByText also reports the sum of the query terms' IDF weights, the
	// scale a complete lexical match would score on. See ranking.FullMatchScore.
	SearchByText(ctx context.Context, projectID string, keywords []string, limit int) ([]model.MemoryWithContext, float64, error)
	KeywordsAreSearchable(ctx context.Context, keywords []string) (bool, error)
	QueryMemories(ctx context.Context, filter graph.QueryFilter) ([]model.MemoryWithContext, error)
	SearchByVector(ctx context.Context, embedding []float32, projectID string, topK int) ([]model.MemoryWithContext, error)
	FindMemoriesByEntities(ctx context.Context, projectID string, names []string, limit int) ([]model.Memory, error)
	CountEntityMatches(ctx context.Context, ids []uuid.UUID, names []string) (map[uuid.UUID]int, error)
}

// Engine runs the read path against a repository and an optional embedder.
//
// A nil embedder is a supported configuration rather than a degraded one to
// guard against: keyword and entity retrieval answer on their own, and the
// vector retriever is skipped rather than failing the query.
type Engine struct {
	repo     Repo
	embedder embedding.Embedder

	// graphSignalsOff removes every graph-derived signal from retrieval.
	// Set once at startup via DisableGraphSignals; see that method.
	graphSignalsOff bool

	// now is the clock ranking scores recency against. time.Now in
	// production; fixed by the evaluation harness so that a benchmark's
	// numbers do not drift with the calendar. Set once via SetClock, before
	// the engine serves queries.
	now func() time.Time

	// fusion is how the three retrievers' signals become one relevance.
	// Startup-only, like the other switches.
	fusion Fusion
}

// Fusion selects how keyword, vector and entity evidence are combined.
//
// Three modes, because the evaluation harness showed the choice is not
// cosmetic. See docs/WORKLOG.md, Track A.
//
//   - FusionMinMax, the default: a weighted sum over signals rescaled per
//     query, keyword by its best raw score in the pool and cosine by the
//     pool's range, so each signal spans [0, 1] on every query rather than
//     on whatever range the corpus happens to produce.
//   - FusionLinear: the same weighted sum over the sigmoid-normalised keyword
//     score and raw cosine similarity. The original design, kept for
//     ablation. Measured: the sigmoid saturates at two matched terms and
//     nomic-embed-text cosines occupy a 0.3-wide band, so this mode is a
//     keyword ranker with a semantic tie-break, and 43 of the 44 answerable
//     misses on the turns corpus were evidence that a retriever had found and
//     this fusion had buried.
//   - FusionRRF: reciprocal rank fusion of the keyword and vector rankings,
//     with entity overlap added as a fraction of a rank-1 contribution.
//     Scale-free, and measured worse than min-max on both corpora (MRR
//     0.453 against 0.499 on turns): rank fusion discards how far apart two
//     candidates were, which is exactly the information that separates a
//     strong semantic match from a weak one.
type Fusion struct {
	Mode FusionMode
	// Keyword, Semantic and Entity weight the three signals; they are
	// normalised to sum to one.
	Keyword, Semantic, Entity float64
	// RRFK is reciprocal rank fusion's smoothing constant.
	RRFK float64
	// Coverage, in [0, 1], is how much the min-max keyword scale leans on
	// what a complete match would score rather than on the pool's best. At
	// 0 the pool's best keyword match always grades 1.0, however little of
	// the query it covers; at 1 it grades by the share of the query's IDF
	// mass it matches. See mergeResults.
	Coverage float64
}

// FusionMode names a fusion strategy.
type FusionMode string

const (
	FusionLinear FusionMode = "linear"
	FusionMinMax FusionMode = "minmax"
	FusionRRF    FusionMode = "rrf"
)

// DefaultFusion is what the engine ships with.
func DefaultFusion() Fusion {
	wk, ws, we := ranking.DefaultFusionWeights()
	return Fusion{Mode: FusionMinMax, Keyword: wk, Semantic: ws, Entity: we, RRFK: 60, Coverage: defaultCoverage}
}

// New returns an Engine. The embedder may be nil.
func New(repo Repo, embedder embedding.Embedder) *Engine {
	return &Engine{
		repo:     repo,
		embedder: embedder,
		now:      func() time.Time { return time.Now().UTC() },
		fusion:   DefaultFusion(),
	}
}

// SetFusion replaces the fusion strategy. Startup-only.
func (e *Engine) SetFusion(f Fusion) error {
	switch f.Mode {
	case FusionLinear, FusionMinMax, FusionRRF:
	default:
		return fmt.Errorf("unknown fusion mode %q", f.Mode)
	}
	if f.Keyword < 0 || f.Semantic < 0 || f.Entity < 0 || f.Keyword+f.Semantic+f.Entity <= 0 {
		return fmt.Errorf("fusion weights must be non-negative and not all zero, got %v/%v/%v", f.Keyword, f.Semantic, f.Entity)
	}
	if f.Mode == FusionRRF && f.RRFK <= 0 {
		return fmt.Errorf("rrf k must be positive, got %v", f.RRFK)
	}
	if f.Coverage < 0 || f.Coverage > 1 {
		return fmt.Errorf("coverage must be in [0, 1], got %v", f.Coverage)
	}
	e.fusion = f
	return nil
}

// defaultCoverage is the exponent in the keyword scale
// poolBest * (fullMatch / poolBest)^coverage.
//
// Swept over 0, 0.25, 0.5, 0.75 and 1 on both harness corpora
// (docs/WORKLOG.md, Track A change 3): every value is inside the noise on
// LoCoMo, so the choice is made by the two instruments that can see it. At
// 0 the pool's best lexical match grades 1.0 even when it covers one common
// word of a four-word question, which breaks
// TestQuery_OneCommonWordDoesNotOutrankAStrongSemanticMatch. At 0.5 and
// above the golden suite's paraphrase MRR falls from 0.679 to 0.622 with the
// bag-of-words embedder, because genuine partial matches are discounted too
// hard for a weak semantic signal to make up. 0.25 satisfies both: the
// golden suite scores exactly as at 0, and the one-word decoys grade 0.32.
const defaultCoverage = 0.25

// SetClock replaces the clock the recency signal is measured against.
//
// Startup-only, like DisableGraphSignals: it writes an unsynchronised field
// that Retrieve reads. The only caller outside tests is the offline
// evaluation harness, which needs the same corpus ranked identically on
// every run.
func (e *Engine) SetClock(now func() time.Time) {
	e.now = now
}

// DisableGraphSignals removes every graph-derived signal from retrieval,
// leaving full-text and vector search alone. One switch for all of them, so an
// ablation run cannot accidentally disable some signals and not others.
//
// This is the ablation mode (issue #86): the engine behaves as plain hybrid
// RAG, and the measured gap between that and normal operation is what the
// graph contributes. Not a tuning knob -- a deployment that wants less of one
// signal wants a ranking change, not this.
//
// Startup-only: call it before the engine serves queries. It writes an
// unsynchronised field that Retrieve reads.
func (e *Engine) DisableGraphSignals() {
	e.graphSignalsOff = true
}

// Retrieve answers a query: three retrievers, merged onto one scale, ranked,
// and truncated to the caller's top-K.
//
// An empty projectID searches across every project, which is the documented
// behaviour of the API this serves. See
// docs/adr/0002-one-deployment-is-one-trust-domain.md.
func (e *Engine) Retrieve(
	ctx context.Context,
	query, projectID string,
	types []model.MemoryType,
	topK int32,
) ([]model.MemoryWithContext, error) {
	return e.retrieve(ctx, query, projectID, types, topK, nil)
}

// Trace is what each retriever contributed to one query.
//
// It exists for the evaluation harness, which has to say *why* a piece of
// evidence was not returned: never a candidate in any retriever, a candidate
// that fusion ranked below the cut, or a candidate the entity signal pushed
// down. The ranked list alone cannot distinguish those, and each calls for a
// different fix.
type Trace struct {
	// Keywords are the terms the keyword retriever searched; Unsearchable
	// reports that it fell back to the recency query instead.
	Keywords     []string
	Unsearchable bool
	// QueryEntities are the normalised entities read from the query text.
	QueryEntities []string
	// Keyword, Vector and Entity are each retriever's candidate pool in the
	// order it returned them. Keyword scores are the normalised relevance
	// (Raw holds ts_rank_cd), Vector scores are cosine similarity, Entity
	// scores are the share of the query's entities the memory names.
	Keyword []Candidate
	Vector  []Candidate
	Entity  []Candidate
	// Ranked is the final list with the components each score was fused
	// from.
	Ranked []Ranked
	// Stages is how long each step of the read path took, keyed by step
	// name, so the harness can say where a query's latency went.
	Stages map[string]time.Duration
}

// stage records the duration of one step when tracing is on.
func (t *Trace) stage(name string, start time.Time) {
	if t == nil {
		return
	}
	if t.Stages == nil {
		t.Stages = make(map[string]time.Duration, 8)
	}
	t.Stages[name] += time.Since(start)
}

// Candidate is one retriever's view of one memory.
type Candidate struct {
	ID    uuid.UUID
	Score float64
	Raw   float64
}

// Ranked is one result with its fused relevance decomposed.
type Ranked struct {
	ID        uuid.UUID
	Keyword   float64
	Cosine    float64
	Entity    float64
	Relevance float64
	Score     float64
}

// RetrieveTraced is Retrieve that also reports how the answer was assembled.
func (e *Engine) RetrieveTraced(
	ctx context.Context,
	query, projectID string,
	types []model.MemoryType,
	topK int32,
) ([]model.MemoryWithContext, *Trace, error) {
	trace := &Trace{}
	results, err := e.retrieve(ctx, query, projectID, types, topK, trace)
	return results, trace, err
}

func (e *Engine) retrieve(
	ctx context.Context,
	query, projectID string,
	types []model.MemoryType,
	topK int32,
	trace *Trace,
) ([]model.MemoryWithContext, error) {
	// Parse query into structured form with time filtering.
	filter := ParseQuery(query, projectID, types, topK)

	// --- Keyword retrieval: PostgreSQL full-text search ---
	//
	// This was Cypher `CONTAINS`, which is substring matching with no term
	// weighting: `the` counted exactly as much as `zqxjklmw`, and it matched
	// inside words, so `go` matched `mango` and `algorithm`. It also could not
	// be indexed at all -- AGE refuses an index for it even under
	// `enable_seqscan=off`, which docs/research/keyword-search-indexing.md
	// establishes is not a costing decision but the absence of any operator
	// class that could serve the predicate.
	//
	// ts_rank_cd grades the match instead of asserting it, which is what lets
	// the fusion below be additive rather than tiered.
	stageStart := time.Now()
	graphResults, idfSum, err := e.repo.SearchByText(ctx, projectID, filter.Keywords, keywordCandidatePool(filter.TopK))
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	fullMatch := ranking.FullMatchScore(idfSum)
	trace.stage("keyword", stageStart)

	// Raw ts_rank_cd values arrive in Score. Normalising them is a ranking
	// decision -- the right curve depends on the query's length -- so it
	// happens here rather than in the repository.
	//
	// A query with no searchable terms retrieves nothing by keyword, which is
	// correct: there is nothing to match. The other two retrievers cover it.
	for i := range graphResults {
		graphResults[i].Relevance = ranking.NormalizeBM25(graphResults[i].Score, len(filter.Keywords))
	}
	if trace != nil {
		trace.Keywords = filter.Keywords
		for _, r := range graphResults {
			trace.Keyword = append(trace.Keyword, Candidate{ID: r.Memory.ID, Score: r.Relevance, Raw: r.Score})
		}
	}

	// A query with nothing to search for still has to return something, so it
	// falls back to the plain graph query: a bare "list everything" request is
	// answered by recency and the other retrievers rather than by an empty
	// result.
	//
	// The condition is precisely "there was nothing to search for", which is
	// neither of the two obvious approximations:
	//
	//   - "the caller supplied no keywords" misses queries whose every term is
	//     a stop word to PostgreSQL but not to extractKeywords. "have", "being"
	//     and "and" survive the first list and are removed by the second, so
	//     such a query has keywords, lexes to an empty tsquery, and would
	//     return nothing at all.
	//   - "keyword retrieval returned nothing" is worse: it cannot tell an
	//     unsearchable query from a searchable one with no matches, and the
	//     second is an answer. Falling back there hands the caller a page of
	//     unrelated memories -- and restores exactly what full-text search was
	//     adopted to remove, since a query for `go` would again return the
	//     memory about mangoes.
	//
	// So the question is asked of PostgreSQL, whose dictionary owns it.
	unsearchable := len(filter.Keywords) == 0
	if !unsearchable && len(graphResults) == 0 {
		searchable, serr := e.repo.KeywordsAreSearchable(ctx, filter.Keywords)
		if serr != nil {
			// Treat it as searchable: a failed check must not turn a precise
			// empty answer into a page of unrelated memories.
			logging.FromContext(ctx).Warn("could not determine whether the query has searchable terms; treating it as a real search",
				slog.Any("error", serr))
		} else {
			unsearchable = !searchable
		}
	}

	if unsearchable {
		// Without its keywords: they matched nothing, and QueryMemories filters
		// on them with CONTAINS, so passing them through makes the fallback
		// return nothing for exactly the queries that need it. What is wanted
		// here is the project's memories by recency, which is what the filter
		// reduces to once the keywords are gone.
		fallback := filter
		fallback.Keywords = nil

		graphResults, err = e.repo.QueryMemories(ctx, fallback)
		if err != nil {
			return nil, fmt.Errorf("graph query: %w", err)
		}
		fullMatch = 0
		if trace != nil {
			trace.Unsearchable = true
			trace.Keyword = nil
		}
		for i := range graphResults {
			// Nothing matched, so no candidate is more relevant than another by
			// keyword. Relevance is left at zero rather than set to a constant:
			// these are fallback candidates, and the vector and entity
			// retrievers' signals should still order them if either has an
			// opinion. Recency, frequency and type order the rest.
			//
			// Score too. QueryMemories fills it with a placeholder 1.0, and
			// the min-max fusion reads the raw keyword score from Score; a
			// placeholder there would hand every fallback candidate the full
			// keyword signal for matching nothing.
			graphResults[i].Relevance = 0
			graphResults[i].Score = 0
		}
	}

	var vectorResults []model.MemoryWithContext
	if e.embedder != nil && query != "" {
		stageStart = time.Now()
		if queryVec, err := e.embedder.Embed(query); err == nil {
			var verr error
			// The vector retriever gets the same candidate pool size as the
			// graph one, for the same reason: this LIMIT runs before ranking,
			// so it decides what ranking is allowed to consider.
			//
			// It was topK*2, which is far too tight to act as the safety net
			// for the keyword retriever. The two retrievers exist to cover each
			// other -- a paraphrased query that shares no keyword with the
			// answer is exactly what vector search is for -- and a pool of 20
			// cannot do that in a project holding thousands of memories.
			vectorResults, verr = e.repo.SearchByVector(ctx, queryVec, projectID, vectorCandidatePool(filter.TopK))
			if verr != nil {
				// The query still returns keyword results, so the caller sees a
				// quietly worse answer rather than an error. Record it.
				logging.FromContext(ctx).Warn("vector search failed; falling back to keyword results only",
					slog.String("project_id", projectID),
					slog.Any("error", verr))
			}
		}
		trace.stage("vector", stageStart)
	}

	// --- Entity retrieval: the second hop ---
	//
	// A third source, and the only one that can reach a memory sharing neither
	// words nor phrasing with the query. "What is Biscuit afraid of?" and "the
	// dog hates thunderstorms" have no keyword in common and need not be close
	// in embedding space, but both are about Biscuit, and the graph knows it.
	//
	// This is what multi-hop questions were missing. They were the weakest
	// LoCoMo category at 65% because the graph clustered paraphrases of one
	// fact rather than connecting things to each other.
	//
	// Run before the merge, not after, because the merge is where the three
	// signals are put on one scale. Adding entity hits afterwards left them at
	// zero relevance in RelevanceTier's unmatched tier, permanently below any
	// memory containing any query word however common -- which made the recall
	// half of this feature unreachable in every project with more than a
	// handful of keyword matches.
	if trace != nil {
		for _, r := range vectorResults {
			trace.Vector = append(trace.Vector, Candidate{ID: r.Memory.ID, Score: r.Score, Raw: r.Score})
		}
	}

	var entityResults []model.Memory
	var entityOverlap map[uuid.UUID]float64
	if !e.graphSignalsOff {
		var queryEntities []string
		stageStart = time.Now()
		entityResults, entityOverlap, queryEntities = e.entityMatches(ctx, projectID, query, filter.TopK,
			graphResults, vectorResults)
		trace.stage("entity", stageStart)
		if trace != nil {
			trace.QueryEntities = queryEntities
			for _, m := range entityResults {
				trace.Entity = append(trace.Entity, Candidate{ID: m.ID, Score: entityOverlap[m.ID], Raw: entityOverlap[m.ID]})
			}
		}
	}

	// Merge: deduplicate by ID, and put the three retrievers' signals on one
	// scale. Keywords are passed so the merge can tell a candidate that
	// lexically matched from one the vector retriever surfaced on similarity
	// alone; the two scores are otherwise not comparable. See
	// ranking.RelevanceTier.
	stageStart = time.Now()
	results := mergeResults(graphResults, vectorResults, entityResults, entityOverlap, fullMatch, e.fusion)

	// Rank results using scoring function. This consumes the Relevance set
	// above, so retrieval quality drives the final order.
	results = ranking.RankResultsAt(results, int(filter.TopK), e.now())
	trace.stage("merge+rank", stageStart)

	if trace != nil {
		keyword := make(map[uuid.UUID]float64, len(graphResults))
		for _, r := range graphResults {
			keyword[r.Memory.ID] = r.Relevance
		}
		cosine := make(map[uuid.UUID]float64, len(vectorResults))
		for _, r := range vectorResults {
			cosine[r.Memory.ID] = r.Score
		}
		for _, r := range results {
			trace.Ranked = append(trace.Ranked, Ranked{
				ID:        r.Memory.ID,
				Keyword:   keyword[r.Memory.ID],
				Cosine:    cosine[r.Memory.ID],
				Entity:    entityOverlap[r.Memory.ID],
				Relevance: r.Relevance,
				Score:     r.Score,
			})
		}
	}
	return results, nil
}

// entityCandidatePoolFactor and maxEntityCandidatePool size the entity
// retriever's candidate pool. See entityCandidatePool.
const (
	entityCandidatePoolFactor = 5
	maxEntityCandidatePool    = 100
)

// Keyword candidate pool sizing, matching the graph retriever's previous
// over-fetch. This LIMIT runs before ranking, so it decides what ranking is
// allowed to consider, and the three retrievers exist to cover each other --
// a pool that is generous on one side but tight on another leaves the hybrid
// with fewer working retrievers than it appears to have.
//
// The cap is the graph side's, because a keyword hit costs one row and no
// embedding to hydrate.
const (
	graphCandidatePoolFactor = 10
	maxGraphCandidatePool    = 500
)

// Vector candidate pool sizing, mirroring the graph retriever's over-fetch in
// internal/graph. Both LIMITs run before ranking, so both decide what the
// ranking layer is allowed to see, and a pool that is generous on one side but
// tight on the other leaves the hybrid with only one working retriever.
//
// The cap is lower than the graph side's 500 because each vector candidate
// carries an embedding to hydrate, so the memory cost per row is far higher.
// 200 still comfortably exceeds any plausible topK.
const (
	vectorCandidatePoolFactor = 10
	maxVectorCandidatePool    = 200
)

// defaultTopK is how many results a query returns when the caller does not
// say. Small on purpose: a caller that has not thought about it is better
// served by a short, precise answer than a long one.
const defaultTopK = 5

// maxTopK bounds how many results one query may return.
//
// A bound is necessary. top_k sizes both candidate pools and the hydrated
// result set, and it arrives on an unauthenticated request field, so an
// unbounded value is a memory-exhaustion vector. The candidate pools cap
// themselves well below this (500 graph, 200 vector), so the cost that scales
// with top_k is hydration and the context-edge lookup, both of which are
// bounded per row.
//
// It was 20, and nothing said so. memory.proto documents top_k as "Maximum
// number of results to return", so a caller asking for 50 received 20 with no
// error, no warning, and no way to discover the limit short of reading the
// source. That is the part that was wrong: a cap is defensible, a silent one
// is not. Comparable engines default to retrieving 20-30, which made the
// undocumented clamp a quality ceiling as well as a surprise.
//
// 200 is chosen to sit above any plausible request while staying an order of
// magnitude below the point where hydration cost matters. The bound is now
// documented in memory.proto.
const maxTopK = 200

// keywordCandidatePool returns how many keyword hits to fetch for a query
// asking for topK results.
func keywordCandidatePool(topK int32) int {
	pool := int(topK) * graphCandidatePoolFactor
	if pool > maxGraphCandidatePool {
		pool = maxGraphCandidatePool
	}
	if pool < 1 {
		pool = 1
	}
	return pool
}

// entityCandidatePool returns how many entity matches to fetch for a query
// asking for topK results.
//
// Sized against topK rather than fixed, and capped, for the same reason as
// vectorCandidatePool: this limit runs before ranking, so it decides what
// ranking is allowed to consider. The cap matters more here than for the other
// retrievers, because an entity naming the corpus's own subject is mentioned
// by most of it -- "Caroline" appears in nearly every memory of a corpus about
// Caroline, so an unbounded entity match is a full table scan wearing a
// traversal's clothes.
func entityCandidatePool(topK int32) int {
	pool := int(topK) * entityCandidatePoolFactor
	if pool > maxEntityCandidatePool {
		pool = maxEntityCandidatePool
	}
	if pool < 1 {
		pool = 1
	}
	return pool
}

// vectorCandidatePool returns how many nearest neighbours to fetch for a query
// asking for topK results.
func vectorCandidatePool(topK int32) int {
	pool := int(topK) * vectorCandidatePoolFactor
	if pool > maxVectorCandidatePool {
		pool = maxVectorCandidatePool
	}
	if pool < 1 {
		pool = 1
	}
	return pool
}

// ParseQuery converts a raw query string and request parameters into a
// graph.QueryFilter. It extracts keywords (filtering stop words) and clamps
// topK to a safe bound.
func ParseQuery(query string, projectID string, types []model.MemoryType, topK int32) graph.QueryFilter {
	keywords := extractKeywords(query)

	if topK <= 0 {
		topK = defaultTopK
	}
	if topK > maxTopK {
		topK = maxTopK
	}

	return graph.QueryFilter{
		ProjectID: projectID,
		Keywords:  keywords,
		Types:     types,
		TopK:      topK,
		// Query ranks its results, so it must see more than TopK candidates:
		// the Cypher LIMIT runs before ranking, and created_at ties make the
		// database's choice among equals arbitrary.
		OverFetch: true,
	}
}

// extractKeywords splits a query string into keywords for tag/content matching.
func extractKeywords(query string) []string {
	if query == "" {
		return nil
	}
	// Simple tokenization: split on spaces, filter short words.
	words := strings.Fields(strings.ToLower(query))
	var keywords []string
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "do": true, "does": true, "did": true,
		"what": true, "which": true, "who": true, "how": true, "when": true,
		"where": true, "this": true, "that": true, "it": true, "of": true,
		"in": true, "on": true, "for": true, "to": true, "with": true,
		"my": true, "our": true, "we": true, "i": true, "use": true,
		"project": true,
	}
	for _, w := range words {
		// Strip surrounding punctuation before anything else. Keywords are
		// matched with CONTAINS against stored content, so a token carrying
		// its sentence punctuation can never match: "group?" is not a
		// substring of "...support group yesterday". Questions are the normal
		// way a memory engine is queried, and the "?" attaches to the last
		// word, which is often the most specific one in the query.
		//
		// Trimmed only at the edges, so identifiers keep the punctuation that
		// is part of them: "api-key", "node.js" and "user's" survive intact.
		// A token that was nothing but punctuation trims to empty and is then
		// dropped by the length check below.
		w = strings.Trim(w, `.,;:!?"'()[]{}<>-`)
		if len(w) < 2 {
			continue
		}
		if stopWords[w] {
			continue
		}
		keywords = append(keywords, w)
	}
	return keywords
}

// mergeResults combines graph-, vector- and entity-retrieved results into a
// single deduplicated slice, carrying each candidate's retrieval relevance
// forward for the ranking layer.
//
// Each retriever contributes a signal on its own scale, and every candidate is
// scored on all three whether or not that retriever found it:
//
//   - Keyword hits arrive with a normalised ts_rank_cd in Relevance, set by
//     the caller because the normalisation depends on the query length.
//   - Vector hits carry cosine similarity in Score.
//   - Entity overlap arrives as a map, since a memory the other two retrievers
//     found is just as much about the query's subject as one this retriever
//     surfaced.
//
// ranking.FuseRelevance combines them additively. That a memory can enter on
// entity evidence alone is the point: "the dog bolts under the bed" answers
// "what is Biscuit afraid of?" without sharing one query term.
//
// The returned slice is sorted by memory ID. Merging happens through a map, and
// Go randomizes map iteration, so without this the candidate order (and
// therefore the resolution of any score tie downstream) would vary between
// identical queries.
func mergeResults(
	graph, vector []model.MemoryWithContext,
	entity []model.Memory,
	entityOverlap map[uuid.UUID]float64,
	fullMatch float64,
	fusion Fusion,
) []model.MemoryWithContext {
	seen := make(map[uuid.UUID]*model.MemoryWithContext, len(graph)+len(vector)+len(entity))

	// Per-query scales and ranks for the fusion modes that need them. The
	// keyword retriever returns its pool best-first and the vector retriever
	// nearest-first, so a position is a rank.
	//
	// The keyword scale is what a complete match would score when the
	// repository could say (ranking.FullMatchScore), and the pool's best
	// otherwise. The difference is whether a pool whose best candidate
	// matches one common word grades that candidate as complete or as weak.
	var maxKeyword float64
	keywordRank := make(map[uuid.UUID]int, len(graph))
	for i, r := range graph {
		keywordRank[r.Memory.ID] = i + 1
		if r.Score > maxKeyword {
			maxKeyword = r.Score
		}
	}
	// The scale is a blend of the two: poolBest^(1-c) * fullMatch^c, so
	// that c = 0 is plain per-query min-max (Bruch et al.'s TM2C2, where the
	// lexical maximum is the observed one) and c = 1 grades every candidate
	// by the share of the query it covers.
	keywordScale := maxKeyword
	if fullMatch > 0 && maxKeyword > 0 && fusion.Coverage > 0 {
		keywordScale = math.Pow(maxKeyword, 1-fusion.Coverage) * math.Pow(fullMatch, fusion.Coverage)
	}
	vectorRank := make(map[uuid.UUID]int, len(vector))
	cosMin, cosMax := math.Inf(1), math.Inf(-1)
	for i, r := range vector {
		vectorRank[r.Memory.ID] = i + 1
		cosMin = math.Min(cosMin, r.Score)
		cosMax = math.Max(cosMax, r.Score)
	}

	// Which candidates a retriever other than vector search found. The
	// semantic gate below does not apply to those: they were not rescued by a
	// weak signal, they were independently retrieved on evidence the gate says
	// nothing about. See ranking.PassesSemanticGate.
	otherEvidence := make(map[uuid.UUID]bool, len(graph)+len(entity))

	// Track the cosine signal separately from the keyword one. They are
	// different kinds of evidence and the fusion below weights them
	// differently, so overwriting one with the other would silently reweight
	// the query.
	cosine := make(map[uuid.UUID]float64, len(vector))
	for _, r := range vector {
		cosine[r.Memory.ID] = r.Score
	}

	// Add all keyword results, whose normalised ts_rank_cd the caller has
	// already placed in Relevance.
	for i := range graph {
		r := graph[i]
		seen[r.Memory.ID] = &r
		otherEvidence[r.Memory.ID] = true
	}

	// Add vector-only results. A memory full-text search did not return
	// matched none of the query's terms, so it carries no keyword evidence.
	for i := range vector {
		r := vector[i]
		if _, ok := seen[r.Memory.ID]; ok {
			continue
		}
		r.Relevance = 0
		seen[r.Memory.ID] = &r
	}

	// Add entity-only results: memories about the right thing that neither
	// other retriever reached. Their evidence is applied below.
	for i := range entity {
		mem := entity[i]
		otherEvidence[mem.ID] = true
		if _, ok := seen[mem.ID]; ok {
			continue
		}
		r := model.MemoryWithContext{Memory: mem}
		seen[mem.ID] = &r
	}

	// Resolve every candidate on one scale, additively.
	//
	// The semantic gate runs first, and that ordering is the substance of it:
	// gating after the combine lets keyword overlap alone rescue a candidate
	// the embedder says is unrelated. It applies only where there is a cosine
	// score to judge -- a candidate vector search never scored is unmeasured,
	// not weak, and discarding those would disable keyword and entity
	// retrieval whenever no embedder is configured.
	results := make([]model.MemoryWithContext, 0, len(seen))
	for id, r := range seen {
		cos, scored := cosine[id]
		if !ranking.PassesSemanticGate(cos, scored, otherEvidence[id]) {
			continue
		}
		// Additive, not tiered. See ranking.FuseRelevance: the tier ranked any
		// lexical match above any non-match, which with a graded keyword
		// signal is a stronger claim than the evidence supports.
		switch fusion.Mode {
		case FusionMinMax:
			// r.Score still holds the raw ts_rank_cd for keyword hits; a
			// candidate the keyword retriever did not return has no lexical
			// evidence and scores zero, as in the linear mode.
			var kw float64
			if _, hit := keywordRank[id]; hit && keywordScale > 0 {
				// Clamped by FuseWeighted: a term repeated in one memory can
				// score above what one occurrence per term would.
				kw = r.Score / keywordScale
			}
			var sem float64
			if scored {
				sem = ranking.MinMax(cos, cosMin, cosMax)
			}
			r.Relevance = ranking.FuseWeighted(kw, sem, entityOverlap[id], fusion.Keyword, fusion.Semantic, fusion.Entity)
		case FusionRRF:
			// Entity overlap has no rank of its own -- the entity retriever
			// orders by recency -- so it enters as a share of what a rank-1
			// keyword or vector hit would contribute.
			sum := ranking.RRF(keywordRank[id], fusion.RRFK, fusion.Keyword) +
				ranking.RRF(vectorRank[id], fusion.RRFK, fusion.Semantic) +
				ranking.RRF(1, fusion.RRFK, fusion.Entity)*entityOverlap[id]
			best := ranking.RRF(1, fusion.RRFK, fusion.Keyword+fusion.Semantic+fusion.Entity)
			r.Relevance = sum / best
		default:
			r.Relevance = ranking.FuseWeighted(r.Relevance, cos, entityOverlap[id], fusion.Keyword, fusion.Semantic, fusion.Entity)
		}
		results = append(results, *r)
	}
	// Byte order equals the canonical string order, since each byte maps to
	// two hex digits monotonically and the dashes sit at fixed positions.
	// Comparing bytes avoids allocating two strings per comparison, which
	// was a fifth of the read path's allocations at 500 candidates.
	sort.Slice(results, func(i, j int) bool {
		return bytes.Compare(results[i].Memory.ID[:], results[j].Memory.ID[:]) < 0
	})
	return results
}

// entityMatches finds memories naming the same entities as the query, and
// reports how much of the query's entity set every candidate names.
//
// Two distinct jobs, and both matter:
//
//   - Recall. A memory sharing no keyword and no close embedding with the
//     query is unreachable by the other two retrievers, and a memory about the
//     same thing is exactly the case they miss.
//   - Ordering. Among memories the other retrievers found, the ones about the
//     query's subject are more likely to answer it.
//
// The overlap map covers every candidate, not only the ones this found: a
// memory the keyword retriever already had is just as much about Biscuit, and
// rewarding only the new ones would rank a weak entity-only hit above a strong
// hit naming the same entity.
//
// A failure degrades to the results the other retrievers produced. The graph
// is supplementary here, so losing it makes the answer quietly worse rather
// than wrong.
func (e *Engine) entityMatches(
	ctx context.Context,
	projectID, query string,
	topK int32,
	existing ...[]model.MemoryWithContext,
) ([]model.Memory, map[uuid.UUID]float64, []string) {
	// Entities are scoped per project, and an unscoped query has no project to
	// traverse. Cross-project entity matching would breach the tenant boundary
	// every other retriever respects.
	if projectID == "" || query == "" {
		return nil, nil, nil
	}

	// The query is a sentence, so it is read for entities the same way a
	// memory is. That symmetry is the point: the query names Biscuit, the
	// memory names Biscuit, and they meet at one node.
	queryEntities := make([]string, 0, 4)
	for _, e := range extraction.ExtractEntities(query) {
		if n := model.NormalizeEntity(e); n != "" {
			queryEntities = append(queryEntities, n)
		}
	}
	if len(queryEntities) == 0 {
		return nil, nil, nil
	}

	log := logging.FromContext(ctx)

	matches, err := e.repo.FindMemoriesByEntities(ctx, projectID, queryEntities, entityCandidatePool(topK))
	if err != nil {
		log.Warn("entity retrieval failed; results come from keyword and vector search only",
			slog.String("project_id", projectID),
			slog.Any("error", err))
		return nil, nil, queryEntities
	}

	// Every candidate is scored, including the ones the other retrievers
	// found, so the signal orders the whole set rather than only its own hits.
	ids := make([]uuid.UUID, 0, len(matches))
	seen := make(map[uuid.UUID]bool, len(matches))
	for _, group := range existing {
		for _, r := range group {
			if !seen[r.Memory.ID] {
				seen[r.Memory.ID] = true
				ids = append(ids, r.Memory.ID)
			}
		}
	}
	for _, m := range matches {
		if !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}

	// Asked of the database as a count of the query's own entities per
	// candidate, rather than as every entity of every candidate. The overlap
	// only ever counted query entities, so the answer is the same and the
	// rows that carried the other names never leave PostgreSQL.
	counts, err := e.repo.CountEntityMatches(ctx, ids, queryEntities)
	if err != nil {
		log.Warn("loading entities for ranking failed; results are ordered without the entity signal",
			slog.Int("result_count", len(ids)),
			slog.Any("error", err))
		return matches, nil, queryEntities
	}

	overlap := make(map[uuid.UUID]float64, len(counts))
	for id, n := range counts {
		if o := ranking.EntityOverlapCount(n, queryEntities); o > 0 {
			overlap[id] = o
		}
	}
	return matches, overlap, queryEntities
}
