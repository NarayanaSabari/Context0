package service

import (
	"testing"

	"github.com/NarayanaSabari/Kora/internal/ranking"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// TestExactKeywordMatchOutranksVectorOnlyResult reproduces a correctness
// violation observed in a soak run: a memory was not returned by a query for a
// keyword that appears verbatim in its own content, while unrelated memories
// were.
//
// The cause is a units mismatch at the merge. The graph retriever filters by
// keyword, so every hit it returns genuinely contains the term, and it grades
// them with LexicalRelevance -- where a content hit is worth 0.75. The vector
// retriever runs unfiltered and passes raw cosine similarity through as
// relevance, and a bag-of-words embedding routinely puts near-duplicate
// sentences above 0.85.
//
// So a memory that does not contain the keyword at all can enter the merged
// set at 0.87 and outrank one that does at 0.75. With top_k truncation, the
// exact match is discarded and the write appears unreadable by its own text.
func TestExactKeywordMatchOutranksVectorOnlyResult(t *testing.T) {
	exact := model.MemoryWithContext{
		Memory: model.Memory{
			ID:      uuid.New(),
			Content: "soak kjgzoaii about prometheus metrics collection",
		},
	}
	// Graded the way Query grades graph hits: one keyword, matched in content.
	exact.Relevance = ranking.LexicalRelevance(exact.Memory.Content, nil, []string{"kjgzoaii"})

	// A vector-only hit: semantically near, but it does not contain the term.
	// 0.8715 is a real score observed from this deployment.
	vectorOnly := model.MemoryWithContext{
		Memory: model.Memory{ID: uuid.New(), Content: "I prefer prometheus for this"},
		Score:  0.8715425782901834,
	}

	merged := mergeResults(
		[]model.MemoryWithContext{exact},
		[]model.MemoryWithContext{vectorOnly},
		nil, nil,
	)

	var exactRel, vectorRel float64
	for _, r := range merged {
		switch r.Memory.ID {
		case exact.Memory.ID:
			exactRel = r.Relevance
		case vectorOnly.Memory.ID:
			vectorRel = r.Relevance
		}
	}

	if exactRel <= vectorRel {
		t.Errorf("a memory containing the query term verbatim scored %.4f, "+
			"below a memory that does not contain it at all (%.4f): "+
			"lexical and cosine relevance are not on the same scale",
			exactRel, vectorRel)
	}
}
