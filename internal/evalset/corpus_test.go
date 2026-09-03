package evalset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestTurnsCorpus_DeterministicIDs pins that a doc's id is a pure function of
// its turn key (conversation + dia id), not of its position or of what else
// is in the dataset. Ranking's id tie-breaks, and reproducing a run against a
// second database, both depend on this.
func TestTurnsCorpus_DeterministicIDs(t *testing.T) {
	dsA := &Dataset{Turns: []Turn{
		{DiaID: "D1:1", Conversation: "conv-x", Content: "foo"},
		{DiaID: "D1:2", Conversation: "conv-x", Content: "bar"},
	}}
	// Same two turns as dsA, reordered, plus an unrelated third turn from a
	// different conversation.
	dsB := &Dataset{Turns: []Turn{
		{DiaID: "D1:2", Conversation: "conv-x", Content: "bar"},
		{DiaID: "D2:1", Conversation: "conv-y", Content: "baz"},
		{DiaID: "D1:1", Conversation: "conv-x", Content: "foo"},
	}}

	corpusA := TurnsCorpus(dsA)
	corpusB := TurnsCorpus(dsB)

	byKey := func(c *Corpus) map[string]uuid.UUID {
		out := make(map[string]uuid.UUID, len(c.Docs))
		for _, d := range c.Docs {
			out[d.Sources[0]] = d.ID
		}
		return out
	}
	idsA, idsB := byKey(corpusA), byKey(corpusB)

	for _, key := range []string{TurnKey("conv-x", "D1:1"), TurnKey("conv-x", "D1:2")} {
		if idsA[key] != idsB[key] {
			t.Errorf("id for %s = %s in corpus A, %s in corpus B; ids must depend only on the turn", key, idsA[key], idsB[key])
		}
	}

	// Same dataset, run twice: identical ids in identical order.
	corpusA2 := TurnsCorpus(dsA)
	for i := range corpusA.Docs {
		if corpusA.Docs[i].ID != corpusA2.Docs[i].ID {
			t.Errorf("doc %d id changed between two runs on the same dataset: %s vs %s", i, corpusA.Docs[i].ID, corpusA2.Docs[i].ID)
		}
	}
}

func TestTurnsCorpus_SourcesAndProjectID(t *testing.T) {
	ds := &Dataset{Turns: []Turn{
		{DiaID: "D1:1", Conversation: "conv-x", Content: "foo said", Session: 1},
		{DiaID: "D1:2", Conversation: "conv-x", Content: "bar said", Session: 1},
	}}
	c := TurnsCorpus(ds)

	if len(c.Docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(c.Docs))
	}
	wantSources := []string{TurnKey("conv-x", "D1:1")}
	if !reflect.DeepEqual(c.Docs[0].Sources, wantSources) {
		t.Errorf("Docs[0].Sources = %v, want %v", c.Docs[0].Sources, wantSources)
	}
	wantSources = []string{TurnKey("conv-x", "D1:2")}
	if !reflect.DeepEqual(c.Docs[1].Sources, wantSources) {
		t.Errorf("Docs[1].Sources = %v, want %v", c.Docs[1].Sources, wantSources)
	}

	present := c.Present()
	if !present[TurnKey("conv-x", "D1:1")] || !present[TurnKey("conv-x", "D1:2")] {
		t.Errorf("Present() = %v, want both turn keys present", present)
	}
	if present[TurnKey("conv-x", "D9:9")] {
		t.Error("Present() reports a turn key that was never added")
	}

	sources := c.Sources()
	if len(sources) != 2 {
		t.Fatalf("Sources() has %d entries, want 2", len(sources))
	}
	if !reflect.DeepEqual(sources[c.Docs[0].ID], c.Docs[0].Sources) {
		t.Errorf("Sources()[%s] = %v, want %v", c.Docs[0].ID, sources[c.Docs[0].ID], c.Docs[0].Sources)
	}

	if got := ProjectID("conv-x"); got != "locomo-conv-x" {
		t.Errorf("ProjectID(conv-x) = %q, want locomo-conv-x", got)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestExtractedCorpus(t *testing.T) {
	dir := t.TempDir()
	id1 := "11111111-1111-1111-1111-111111111111"
	id2 := "22222222-2222-2222-2222-222222222222"

	// Written out of id order, to check ExtractedCorpus sorts.
	docs := []SnapshotDoc{
		{
			ID: id2, Conversation: "conv-1", Content: "second doc", Type: "episodic",
			CreatedAt: "2023-05-08T14:00:00Z",
		},
		{
			ID: id1, Conversation: "conv-1", Content: "first doc", Type: "semantic",
			Tags: []string{"x"}, CreatedAt: "2023-05-08T13:56:00Z", Entities: []string{"Paris"},
		},
	}
	snapshotPath := filepath.Join(dir, "snapshot.json")
	writeJSON(t, snapshotPath, docs)

	labels := map[string][]string{
		id1: {"D1:1", "D1:2"},
		// id2 intentionally has no labels entry: an extracted doc that was
		// not aligned to any turn.
	}
	labelsPath := filepath.Join(dir, "labels.json")
	writeJSON(t, labelsPath, labels)

	c, err := ExtractedCorpus(snapshotPath, labelsPath)
	if err != nil {
		t.Fatalf("ExtractedCorpus: %v", err)
	}
	if c.Name != "extracted" {
		t.Errorf("Name = %q, want extracted", c.Name)
	}
	wantClock := time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)
	if !c.Clock.Equal(wantClock) {
		t.Errorf("Clock = %v, want %v", c.Clock, wantClock)
	}

	if len(c.Docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(c.Docs))
	}
	// Sorted by id ascending: "1111..." before "2222...".
	if c.Docs[0].ID.String() != id1 || c.Docs[1].ID.String() != id2 {
		t.Errorf("docs not sorted by id: got [%s %s], want [%s %s]", c.Docs[0].ID, c.Docs[1].ID, id1, id2)
	}

	first := c.Docs[0]
	wantSources := []string{TurnKey("conv-1", "D1:1"), TurnKey("conv-1", "D1:2")}
	if !reflect.DeepEqual(first.Sources, wantSources) {
		t.Errorf("first doc Sources = %v, want %v", first.Sources, wantSources)
	}
	if !reflect.DeepEqual(first.Entities, []string{"Paris"}) {
		t.Errorf("first doc Entities = %v, want [Paris]", first.Entities)
	}

	second := c.Docs[1]
	if len(second.Sources) != 0 {
		t.Errorf("second doc Sources = %v, want none: it has no labels entry", second.Sources)
	}
}

func TestExtractedCorpus_BadCreatedAtErrors(t *testing.T) {
	dir := t.TempDir()
	id := "11111111-1111-1111-1111-111111111111"

	docs := []SnapshotDoc{
		{ID: id, Conversation: "conv-1", Content: "bad doc", Type: "episodic", CreatedAt: "not-a-timestamp"},
	}
	snapshotPath := filepath.Join(dir, "snapshot.json")
	writeJSON(t, snapshotPath, docs)

	labelsPath := filepath.Join(dir, "labels.json")
	writeJSON(t, labelsPath, map[string][]string{})

	if _, err := ExtractedCorpus(snapshotPath, labelsPath); err == nil {
		t.Fatal("ExtractedCorpus with an unparseable created_at returned nil error")
	}
}
