package evalset

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/NarayanaSabari/Kora/internal/extraction"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// Doc is one memory the evaluation stores, with the ground truth it carries.
type Doc struct {
	ID           uuid.UUID
	Conversation string
	Content      string
	Type         model.MemoryType
	Tags         []string
	CreatedAt    time.Time
	// Entities are linked through the graph exactly as the write path would
	// link them, so the entity retriever sees the same graph it would in
	// production.
	Entities []string
	// Sources are the TurnKeys this doc is evidence for. A verbatim turn is
	// evidence for itself; an extracted memory is evidence for the turn(s) it
	// was aligned to.
	Sources []string
}

// Corpus is a set of docs and the instant they are ranked at.
type Corpus struct {
	Name string
	Docs []Doc
	// Clock is the fixed "now" ranking measures recency against. Fixed per
	// corpus so that a run today and a run next month score identically.
	Clock time.Time
}

// Present returns the set of turn keys at least one doc is evidence for.
// Evidence the corpus does not hold cannot be retrieved, so questions whose
// evidence is entirely absent are reported as unscorable rather than as
// retrieval failures.
func (c *Corpus) Present() map[string]bool {
	present := make(map[string]bool)
	for _, d := range c.Docs {
		for _, s := range d.Sources {
			present[s] = true
		}
	}
	return present
}

// Sources returns each doc's evidence keys by id.
func (c *Corpus) Sources() map[uuid.UUID][]string {
	out := make(map[uuid.UUID][]string, len(c.Docs))
	for _, d := range c.Docs {
		out[d.ID] = d.Sources
	}
	return out
}

// ProjectID is the Kora project a conversation's memories live in. One
// project per conversation, as the benchmark adapter does, because each
// question may only see its own conversation.
func ProjectID(conversation string) string {
	return "locomo-" + conversation
}

// turnsNamespace makes turn ids a pure function of the turn, so the same
// corpus gets the same ids on every run and ranking's id tie-breaks are
// reproducible across databases.
var turnsNamespace = uuid.MustParse("5b0a4a6e-2f0e-4d3c-9e8a-7c1f3d2b1a00")

// TurnsCorpus stores every rendered turn verbatim: the adapter's "turns"
// ingest mode, which isolates retrieval from extraction.
//
// Timestamps mimic a benchmark ingest rather than the conversation's own
// dates: sessions are written minutes apart on one day, and the clock sits a
// day later, so recency is near-uniform and slightly favours later sessions,
// as it did in every published run. Using the 2023 conversation dates would
// instead put every memory three years old at a 90-day half-life, where
// recency contributes nothing and the corpus stops resembling a live store.
func TurnsCorpus(ds *Dataset) *Corpus {
	base := time.Date(2026, time.August, 30, 10, 0, 0, 0, time.UTC)
	c := &Corpus{Name: "turns", Clock: base.Add(24 * time.Hour)}
	for _, t := range ds.Turns {
		key := TurnKey(t.Conversation, t.DiaID)
		c.Docs = append(c.Docs, Doc{
			ID:           uuid.NewSHA1(turnsNamespace, []byte(key)),
			Conversation: t.Conversation,
			Content:      t.Content,
			Type:         model.MemoryTypeEpisodic,
			CreatedAt:    base.Add(time.Duration(t.Session) * time.Minute),
			Entities:     extraction.ExtractEntities(t.Content),
			Sources:      []string{key},
		})
	}
	return c
}

// SnapshotDoc is one memory as dumped from a live database by
// scripts/eval_fixtures.py.
type SnapshotDoc struct {
	ID           string   `json:"id"`
	Conversation string   `json:"conversation"`
	Content      string   `json:"content"`
	Type         string   `json:"type"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	Entities     []string `json:"entities"`
}

// LoadSnapshot reads a dumped corpus.
func LoadSnapshot(path string) ([]SnapshotDoc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var docs []SnapshotDoc
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return docs, nil
}

// ExtractedCorpus builds a corpus from a snapshot of LLM-extracted memories
// and their alignment labels (memory id to the dia ids it was extracted
// from). The memories keep their original ids, timestamps and entity links,
// so the store the eval builds is the store the benchmark queried, minus
// access counts.
//
// The clock is the morning after the snapshot's ingest, matching when the
// benchmark queried it.
func ExtractedCorpus(snapshotPath, labelsPath string) (*Corpus, error) {
	docs, err := LoadSnapshot(snapshotPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(labelsPath)
	if err != nil {
		return nil, err
	}
	labels := make(map[string][]string)
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, fmt.Errorf("parse %s: %w", labelsPath, err)
	}

	c := &Corpus{Name: "extracted", Clock: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)}
	for _, d := range docs {
		id, err := uuid.Parse(d.ID)
		if err != nil {
			return nil, fmt.Errorf("snapshot memory id %q: %w", d.ID, err)
		}
		created, err := time.Parse(time.RFC3339, d.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("snapshot memory %s created_at %q: %w", d.ID, d.CreatedAt, err)
		}
		var sources []string
		for _, dia := range labels[d.ID] {
			sources = append(sources, TurnKey(d.Conversation, dia))
		}
		c.Docs = append(c.Docs, Doc{
			ID:           id,
			Conversation: d.Conversation,
			Content:      d.Content,
			Type:         model.MemoryType(d.Type),
			Tags:         d.Tags,
			CreatedAt:    created,
			Entities:     d.Entities,
			Sources:      sources,
		})
	}
	sort.Slice(c.Docs, func(i, j int) bool { return c.Docs[i].ID.String() < c.Docs[j].ID.String() })
	return c, nil
}
