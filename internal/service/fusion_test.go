package service

// End-to-end tests for additive fusion.
//
// The change these pin: relevance was strictly tiered, mapping any lexical
// match into [0.5, 1.0] and any non-match into [0, 0.5). That is a stronger
// claim than the evidence supports -- a memory containing one common query
// word outranked a near-perfect semantic match sharing no token.
//
// The tier was right while the lexical signal was boolean CONTAINS, because a
// boolean cannot say how good a match is and the tier stood in for that
// missing information. With ts_rank_cd the signal is graded, so the tier
// compensates for nothing and only its cost remains.

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/graph"
)

// scriptedEmbedder places chosen texts at chosen points on a circle, so a test
// can state the semantic relationship it wants to exercise instead of hoping
// the bag-of-words embedder happens to produce it.
//
// It exists because this behaviour cannot be reached with the real embedder.
// The case is "a strong semantic match sharing no query token", and
// bag-of-words scores exactly zero for text sharing no token -- by
// construction, since that is what it measures. Only a model that understands
// paraphrase produces the input, and this engine supports four providers
// precisely so an operator can supply one.
type scriptedEmbedder struct {
	// angles maps a substring to a position in radians. The first substring
	// found in the text decides the vector.
	angles map[string]float64
}

// Dimension matches the test database's column width rather than the two
// dimensions the angles actually use; the remaining components stay zero.
// The schema is created once per test binary, so an embedder of a different
// width would be rejected by verifyEmbeddingDim.
func (e scriptedEmbedder) Dimension() int { return 384 }

func (e scriptedEmbedder) Embed(text string) ([]float32, error) {
	v := make([]float32, e.Dimension())
	for probe, angle := range e.angles {
		if strings.Contains(text, probe) {
			v[0], v[1] = float32(math.Cos(angle)), float32(math.Sin(angle))
			return v, nil
		}
	}
	// Unmatched text sits far from everything the test placed deliberately.
	// Never the zero vector, which StoreEmbedding rightly refuses.
	v[0], v[1] = 0, -1
	return v, nil
}

// scriptedEmbedderService is consolidationTestService with a chosen embedder.
func scriptedEmbedderService(t *testing.T, e embedding.Embedder) (*MemoryService, *graph.AGERepository, context.Context) {
	t.Helper()

	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, e.Dimension())
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	return NewMemoryService(repo, e), repo, ctx
}

// TestQuery_OneCommonWordDoesNotOutrankAStrongSemanticMatch is the acceptance
// criterion, stated as a query rather than as unit arithmetic.
//
// The decoys contain one query word and nothing else relevant. The target
// contains no query word at all and is a paraphrase, which is exactly what
// vector search exists to find and what the tier made unreachable: it mapped
// every decoy into [0.5, 1.0] for containing "paperwork" and the target into
// [0, 0.5) for containing nothing, whatever the embedder said.
func TestQuery_OneCommonWordDoesNotOutrankAStrongSemanticMatch(t *testing.T) {
	// The query and the answer are near-identical in meaning and share no
	// stemmed word; the decoys are unrelated in meaning and share one.
	svc, _, ctx := scriptedEmbedderService(t, scriptedEmbedder{angles: map[string]float64{
		"lodged during the fourth month": 0.05,
		"corporate tax paperwork due":    0.0,
		"office plant":                   1.4,
	}})
	projectID := fmt.Sprintf("fusion-tier-%d", time.Now().UnixNano())

	const target = "Caroline said that company returns are always lodged during the fourth month."
	lines := []string{target}
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf(
			"Melanie said that the office plant near paperwork desk %d needs watering.", i+100))
	}

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: strings.Join(lines, "\n"),
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "corporate tax paperwork due",
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("the query returned nothing")
	}

	if !strings.Contains(resp.Results[0].Memory.Content, "company returns") {
		t.Errorf("the single top result is %q; a memory sharing one common word "+
			"must not outrank a near-perfect semantic match that shares none",
			resp.Results[0].Memory.Content)
	}
}

// The opposite direction, and the reason the tier existed: a verbatim match
// must still beat a memory the embedder merely places nearby.
//
// This is the case that produced RelevanceTier in the first place -- an
// unfiltered vector hit at 0.87 cosine displaced a memory that literally
// contained the query's unique term. Additive fusion has to keep that working
// without the tier's collateral damage.
func TestQuery_AVerbatimMatchStillBeatsSemanticSimilarityAlone(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("fusion-verbatim-%d", time.Now().UnixNano())

	lines := []string{"Caroline said that the zqxjklmw marker was recorded in the ledger."}
	// Memories about the same subject in similar words, which a bag-of-words
	// embedder scores highly against the query, and none of which contain the
	// marker.
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf(
			"Caroline said that a marker was recorded in the ledger on page %d.", i+100))
	}

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: strings.Join(lines, "\n"),
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "zqxjklmw",
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("a query for a term that exists in exactly one memory returned nothing")
	}
	if !strings.Contains(resp.Results[0].Memory.Content, "zqxjklmw") {
		t.Errorf("the top result is %q; the only memory containing the query's "+
			"unique term must come first", resp.Results[0].Memory.Content)
	}
}

// A query with no searchable terms must still return something. Most of the
// engine's non-search uses -- profiles, "what do you know about me" -- arrive
// this way, and full-text search has nothing to match on them.
func TestQuery_WithNoSearchableTermsStillReturnsMemories(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("fusion-noterms-%d", time.Now().UnixNano())

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that she adopted a rescue dog last spring.\n" +
			"Caroline said that she walks along the river each morning.",
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	// The third case is the one that needed a real fix. extractKeywords has
	// its own stop-word list and PostgreSQL's english dictionary has another,
	// so "have" and "being" survive the first and are removed by the second:
	// the query produces keywords, they lex to an empty tsquery, and a
	// fallback keyed on the caller's input would skip it and return nothing.
	for _, query := range []string{
		"",                    // no query at all
		"what is it",          // every word in extractKeywords' stop list
		"have being had were", // survives extractKeywords, empty to Postgres
	} {
		resp, err := svc.Query(ctx, &pb.QueryRequest{
			ProjectId: projectID,
			Query:     query,
			TopK:      5,
		})
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if len(resp.Results) == 0 {
			t.Errorf("a query of %q returned nothing; a request with no searchable "+
				"terms is a request for what is there, not an error", query)
		}
	}
}

// Stemming reaches a memory whose wording differs from the query's, which
// substring matching could not: `adopt` and `adopted` share no substring
// relationship in the direction that matters.
func TestQuery_StemmingReachesInflectedForms(t *testing.T) {
	svc, _, ctx := consolidationTestService(t)
	projectID := fmt.Sprintf("fusion-stem-%d", time.Now().UnixNano())

	if _, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId:    projectID,
		Conversation: "Caroline said that she adopted a nervous rescue dog in the spring.",
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	resp, err := svc.Query(ctx, &pb.QueryRequest{
		ProjectId: projectID,
		Query:     "adopting a rescue",
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("a query for `adopting` did not reach a memory about `adopted`")
	}
	if !strings.Contains(resp.Results[0].Memory.Content, "adopted") {
		t.Errorf("the top result is %q, want the memory about adopting a dog",
			resp.Results[0].Memory.Content)
	}
}
