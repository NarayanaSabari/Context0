package service

import (
	"testing"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"empty", "", nil},
		{"stop words only", "what is the", nil},
		{"with keywords", "what database does this project use", []string{"database"}},
		{"multiple keywords", "postgres database migration", []string{"postgres", "database", "migration"}},
		{"short words filtered", "a b cd ef", []string{"cd", "ef"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKeywords(tt.query)
			if len(got) != len(tt.want) {
				t.Errorf("extractKeywords(%q) = %v, want %v", tt.query, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("keyword[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseQuery_Defaults(t *testing.T) {
	f := ParseQuery("test query", "proj1", nil, 0)

	if f.TopK != 5 {
		t.Errorf("default TopK = %d, want 5", f.TopK)
	}
	if f.ProjectID != "proj1" {
		t.Errorf("ProjectID = %q, want 'proj1'", f.ProjectID)
	}
}

func TestParseQuery_Limits(t *testing.T) {
	f := ParseQuery("test", "proj1", nil, 50)

	if f.TopK != 20 {
		t.Errorf("TopK should be capped at 20, got %d", f.TopK)
	}
}

func TestHasOverlappingTags(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"no overlap", []string{"a", "b"}, []string{"c", "d"}, false},
		{"overlap", []string{"a", "b"}, []string{"b", "c"}, true},
		{"case insensitive", []string{"Foo"}, []string{"foo"}, true},
		{"empty", nil, []string{"a"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasOverlappingTags(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("hasOverlappingTags(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestMergeResults_CarriesRelevanceForward guards the contract between
// retrieval and ranking: whatever the two retrievers determined about query
// match quality must survive the merge in the Relevance field, because that is
// the only channel ranking reads.
func TestMergeResults_CarriesRelevanceForward(t *testing.T) {
	graphOnly := uuid.New()
	vectorOnly := uuid.New()
	both := uuid.New()

	graphResults := []model.MemoryWithContext{
		{Memory: model.Memory{ID: graphOnly}, Relevance: 0.8},
		{Memory: model.Memory{ID: both}, Relevance: 0.6},
	}
	// The repository reports cosine similarity in Score, not Relevance.
	vectorResults := []model.MemoryWithContext{
		{Memory: model.Memory{ID: vectorOnly}, Score: 0.7},
		{Memory: model.Memory{ID: both}, Score: 0.5},
	}

	merged := mergeResults(graphResults, vectorResults)

	if len(merged) != 3 {
		t.Fatalf("expected 3 deduplicated results, got %d", len(merged))
	}

	byID := make(map[uuid.UUID]model.MemoryWithContext, len(merged))
	for _, m := range merged {
		byID[m.Memory.ID] = m
	}

	if got := byID[graphOnly].Relevance; got != 0.8 {
		t.Errorf("graph-only relevance = %f, want 0.8", got)
	}
	if got := byID[vectorOnly].Relevance; got != 0.7 {
		t.Errorf("vector-only relevance = %f, want 0.7 (cosine similarity promoted)", got)
	}

	// Agreement between retrievers must lift the memory above either input.
	if got := byID[both].Relevance; got <= 0.6 {
		t.Errorf("cross-retriever agreement should boost relevance above 0.6, got %f", got)
	} else if got > 1.0 {
		t.Errorf("merged relevance %f exceeds 1.0", got)
	}
}

// TestMergeResults_IsDeterministic pins the ordering guarantee. Merging happens
// through a map, and Go randomizes map iteration, so without an explicit sort
// the candidate order would vary between identical queries.
func TestMergeResults_IsDeterministic(t *testing.T) {
	var graphResults []model.MemoryWithContext
	for i := 0; i < 10; i++ {
		graphResults = append(graphResults, model.MemoryWithContext{
			Memory:    model.Memory{ID: uuid.New()},
			Relevance: 0.5,
		})
	}

	first := mergeResults(graphResults, nil)
	for i := 0; i < 20; i++ {
		got := mergeResults(graphResults, nil)
		for j := range got {
			if got[j].Memory.ID != first[j].Memory.ID {
				t.Fatalf("mergeResults order varies between identical calls at %d", j)
			}
		}
	}
}
