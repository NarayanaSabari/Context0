package evalset

import (
	"context"
	"fmt"
	"time"

	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// Store is the slice of the repository the loader needs.
type Store interface {
	CreateMemory(ctx context.Context, mem model.Memory) error
	StoreEmbedding(ctx context.Context, memoryID uuid.UUID, projectID string, embedding []float32) error
	LinkEntities(ctx context.Context, mem model.Memory, names []string) (int, error)
}

// LoadStats is what a load wrote.
type LoadStats struct {
	Memories int
	Entities int
	Duration time.Duration
}

// LoadCorpus writes a corpus through the repository's own write primitives:
// the memory vertex, its embedding, and its entity links.
//
// It deliberately bypasses ingest.Engine. The write path folds near
// duplicates and links neighbours, which would change which docs exist and
// break the one-to-one mapping between a doc and its evidence. Those
// behaviours are exercised by the golden suite; this loader's job is to put
// a known corpus in front of the read path unchanged.
//
// Docs are written in corpus order, which is fixed, so two loads of one
// corpus produce identical tables.
func LoadCorpus(ctx context.Context, store Store, c *Corpus, embedder embedding.Embedder, progress func(done, total int)) (LoadStats, error) {
	start := time.Now()
	var stats LoadStats
	for i, d := range c.Docs {
		mem := model.Memory{
			ID:          d.ID,
			Content:     d.Content,
			Type:        d.Type,
			ProjectID:   ProjectID(d.Conversation),
			Tags:        d.Tags,
			CreatedAt:   d.CreatedAt,
			AccessCount: 0,
			DecayScore:  1,
		}
		if err := store.CreateMemory(ctx, mem); err != nil {
			return stats, fmt.Errorf("create memory %s: %w", d.ID, err)
		}
		stats.Memories++

		vec, err := embedder.Embed(d.Content)
		if err != nil {
			return stats, fmt.Errorf("embed memory %s: %w", d.ID, err)
		}
		if err := store.StoreEmbedding(ctx, mem.ID, mem.ProjectID, vec); err != nil {
			return stats, fmt.Errorf("store embedding %s: %w", d.ID, err)
		}

		if len(d.Entities) > 0 {
			linked, err := store.LinkEntities(ctx, mem, d.Entities)
			if err != nil {
				return stats, fmt.Errorf("link entities %s: %w", d.ID, err)
			}
			stats.Entities += linked
		}

		if progress != nil && (i+1)%500 == 0 {
			progress(i+1, len(c.Docs))
		}
	}
	stats.Duration = time.Since(start)
	return stats, nil
}
