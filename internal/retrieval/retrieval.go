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
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

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
// Narrow on purpose: it names seven operations out of the repository's several
// dozen, so a test can supply a fake without implementing a graph store, and a
// reader can see the whole surface the read path touches.
type Repo interface {
	SearchByText(ctx context.Context, projectID string, keywords []string, limit int) ([]model.MemoryWithContext, error)
	KeywordsAreSearchable(ctx context.Context, keywords []string) (bool, error)
	QueryMemories(ctx context.Context, filter graph.QueryFilter) ([]model.MemoryWithContext, error)
	SearchByVector(ctx context.Context, embedding []float32, projectID string, topK int) ([]model.MemoryWithContext, error)
	FindMemoriesByEntities(ctx context.Context, projectID string, names []string, limit int) ([]model.Memory, error)
	GetMemoryEntities(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]string, error)
	EntityMentionStats(ctx context.Context, projectID string, names []string) (map[string]int64, int64, error)
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
}

// New returns an Engine. The embedder may be nil.
func New(repo Repo, embedder embedding.Embedder) *Engine {
	return &Engine{repo: repo, embedder: embedder}
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
	graphResults, err := e.repo.SearchByText(ctx, projectID, filter.Keywords, keywordCandidatePool(filter.TopK))
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}

	// Raw ts_rank_cd values arrive in Score. Normalising them is a ranking
	// decision -- the right curve depends on the query's length -- so it
	// happens here rather than in the repository.
	//
	// A query with no searchable terms retrieves nothing by keyword, which is
	// correct: there is nothing to match. The other two retrievers cover it.
	for i := range graphResults {
		graphResults[i].Relevance = ranking.NormalizeBM25(graphResults[i].Score, len(filter.Keywords))
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
		for i := range graphResults {
			// Nothing matched, so no candidate is more relevant than another by
			// keyword. Relevance is left at zero rather than set to a constant:
			// these are fallback candidates, and the vector and entity
			// retrievers' signals should still order them if either has an
			// opinion. Recency, frequency and type order the rest.
			graphResults[i].Relevance = 0
		}
	}

	var vectorResults []model.MemoryWithContext
	if e.embedder != nil && query != "" {
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
	var entityResults []model.Memory
	var entityOverlap map[uuid.UUID]float64
	if !e.graphSignalsOff {
		entityResults, entityOverlap = e.entityMatches(ctx, projectID, query, filter.TopK,
			graphResults, vectorResults)
	}

	// Merge: deduplicate by ID, and put the three retrievers' signals on one
	// scale. Keywords are passed so the merge can tell a candidate that
	// lexically matched from one the vector retriever surfaced on similarity
	// alone; the two scores are otherwise not comparable. See
	// ranking.RelevanceTier.
	results := mergeResults(graphResults, vectorResults, entityResults, entityOverlap)

	// Rank results using scoring function. This consumes the Relevance set
	// above, so retrieval quality drives the final order.
	results = ranking.RankResults(results, int(filter.TopK))
	return results, nil
}

// entityWeights turns per-entity mention counts into discrimination weights in
// [0, 1], on a bounded log curve:
//
//	w(e) = ln(1 + N/df(e)) / ln(1 + N)
//
// where N is the project's memory count and df(e) how many of its memories
// mention the entity. An entity in every memory scores ln(2)/ln(1+N) -- near
// zero in any real project -- and one mentioned once scores 1. Entities the
// stats do not cover are left out of the map, which EntityOverlap reads as
// full weight: they cannot be matched anyway, so their only role is padding
// the denominator, exactly as before weighting.
//
// On the ablation baseline corpus (2,537 memories) this weighs "caroline",
// named by most memories of her conversations, at roughly a tenth of an
// entity mentioned three times.
//
// Returns nil -- uniform weights, the unweighted behaviour -- when the stats
// are unavailable or the project is too small for frequency to mean anything.
func (e *Engine) entityWeights(ctx context.Context, projectID string, queryEntities []string) map[string]float64 {
	counts, total, err := e.repo.EntityMentionStats(ctx, projectID, queryEntities)
	if err != nil {
		logging.FromContext(ctx).Warn("entity mention stats failed; entity matches are weighted uniformly",
			slog.String("project_id", projectID),
			slog.Any("error", err))
		return nil
	}
	if total <= 1 || len(counts) == 0 {
		return nil
	}

	denom := math.Log(1 + float64(total))
	weights := make(map[string]float64, len(counts))
	for name, df := range counts {
		if df < 1 {
			continue
		}
		weights[name] = math.Log(1+float64(total)/float64(df)) / denom
	}
	return weights
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
) []model.MemoryWithContext {
	seen := make(map[uuid.UUID]*model.MemoryWithContext, len(graph)+len(vector)+len(entity))

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
		r.Relevance = ranking.FuseRelevance(r.Relevance, cos, entityOverlap[id])
		results = append(results, *r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Memory.ID.String() < results[j].Memory.ID.String()
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
) ([]model.Memory, map[uuid.UUID]float64) {
	// Entities are scoped per project, and an unscoped query has no project to
	// traverse. Cross-project entity matching would breach the tenant boundary
	// every other retriever respects.
	if projectID == "" || query == "" {
		return nil, nil
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
		return nil, nil
	}

	log := logging.FromContext(ctx)

	matches, err := e.repo.FindMemoriesByEntities(ctx, projectID, queryEntities, entityCandidatePool(topK))
	if err != nil {
		log.Warn("entity retrieval failed; results come from keyword and vector search only",
			slog.String("project_id", projectID),
			slog.Any("error", err))
		return nil, nil
	}

	// How much each query entity is worth. An entity mentioned by most of the
	// project's memories discriminates nothing -- the ablation baseline
	// measured this signal, unweighted, hurting adversarial questions 6-to-1
	// by pulling generic entity matches into questions whose right answer was
	// no answer -- while a rarely mentioned entity nearly answers the query
	// by itself.
	//
	// A failure degrades to uniform weights, which is the unweighted
	// behaviour: worse ordering, never a lost result.
	weights := e.entityWeights(ctx, projectID, queryEntities)

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

	byMemory, err := e.repo.GetMemoryEntities(ctx, ids)
	if err != nil {
		log.Warn("loading entities for ranking failed; results are ordered without the entity signal",
			slog.Int("result_count", len(ids)),
			slog.Any("error", err))
		return matches, nil
	}

	overlap := make(map[uuid.UUID]float64, len(ids))
	for _, id := range ids {
		if o := ranking.EntityOverlap(byMemory[id], queryEntities, weights); o > 0 {
			overlap[id] = o
		}
	}
	return matches, overlap
}
