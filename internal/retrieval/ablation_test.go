package retrieval

import (
	"context"
	"testing"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// The ablation contract, issue #86: DisableGraphSignals leaves an engine that
// retrieves by full-text and vector search alone, indistinguishable from a
// stack in which the graph signals do not exist. Both directions are pinned --
// the switch must remove the signal, and the default must keep it -- because a
// mutant that inverts or ignores the flag turns every ablation measurement
// into a comparison of the engine with itself.

// entityOnlyFixture returns a repo where one memory is reachable only through
// the entity retriever: keyword and vector search know nothing of it.
func entityOnlyFixture() (*fakeRepo, model.MemoryWithContext, model.Memory) {
	keywordHit := memory("the deploy target is production")
	entityHit := model.Memory{
		ID:      uuid.New(),
		Content: "Biscuit trembles during thunderstorms",
		Type:    model.MemoryTypeSemantic,
	}
	return &fakeRepo{
		textResults:   []model.MemoryWithContext{keywordHit},
		searchable:    true,
		entityResults: []model.Memory{entityHit},
	}, keywordHit, entityHit
}

func TestRetrieve_GraphSignalsOffSkipsEntityRetrieval(t *testing.T) {
	repo, keywordHit, entityHit := entityOnlyFixture()

	eng := New(repo, fixedEmbedder{})
	eng.DisableGraphSignals()

	// "Biscuit" is a rule-extractable entity, so with signals on this query
	// would reach the entity retriever.
	results, err := eng.Retrieve(context.Background(), "What scares Biscuit?", "proj", nil, 5)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	if repo.entityCalls != 0 {
		t.Errorf("entity retriever consulted %d times with graph signals off: "+
			"the ablated engine is not the RAG baseline it claims to be", repo.entityCalls)
	}
	for _, r := range results {
		if r.Memory.ID == entityHit.ID {
			t.Errorf("entity-only memory %q surfaced with graph signals off", r.Memory.Content)
		}
	}
	if len(results) == 0 || results[0].Memory.ID != keywordHit.Memory.ID {
		t.Errorf("keyword retrieval should be untouched by the ablation; got %d results", len(results))
	}
}

// The control: by default the same fixture does surface the entity-only
// memory. Without this, a mutant that disables the graph signals
// unconditionally passes the test above and every ablation run measures
// nothing.
func TestRetrieve_GraphSignalsOnReachEntityOnlyMemories(t *testing.T) {
	repo, _, entityHit := entityOnlyFixture()

	results, err := New(repo, fixedEmbedder{}).Retrieve(context.Background(), "What scares Biscuit?", "proj", nil, 5)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	if repo.entityCalls == 0 {
		t.Fatal("entity retriever never consulted on a default engine")
	}
	for _, r := range results {
		if r.Memory.ID == entityHit.ID {
			return
		}
	}
	t.Error("entity-only memory did not surface on a default engine: the graph signal is dead")
}
