package ranking

import (
	"testing"
	"time"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

func TestRecencyFactor(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name      string
		createdAt time.Time
		wantMin   float64
		wantMax   float64
	}{
		{"just created", now, 0.99, 1.01},
		{"1 day ago", now.Add(-24 * time.Hour), 0.85, 1.0},
		{"7 days ago (half-life)", now.Add(-7 * 24 * time.Hour), 0.45, 0.55},
		{"30 days ago", now.Add(-30 * 24 * time.Hour), 0.0, 0.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recencyFactor(tt.createdAt, now)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("recencyFactor() = %f, want between %f and %f", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestAverageEdgeWeight(t *testing.T) {
	tests := []struct {
		name  string
		edges []model.ContextEdge
		want  float64
	}{
		{"empty", nil, 0},
		{"single", []model.ContextEdge{{Weight: 0.8}}, 0.8},
		{"multiple", []model.ContextEdge{{Weight: 0.6}, {Weight: 1.0}}, 0.8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := averageEdgeWeight(tt.edges)
			if got != tt.want {
				t.Errorf("averageEdgeWeight() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	now := time.Now().UTC()
	w := DefaultWeights()

	recentSemantic := model.MemoryWithContext{
		Memory: model.Memory{
			ID:          uuid.New(),
			Type:        model.MemoryTypeSemantic,
			CreatedAt:   now,
			AccessCount: 10,
		},
	}

	oldEpisodic := model.MemoryWithContext{
		Memory: model.Memory{
			ID:          uuid.New(),
			Type:        model.MemoryTypeEpisodic,
			CreatedAt:   now.Add(-30 * 24 * time.Hour),
			AccessCount: 0,
		},
	}

	scoreRecent := Score(recentSemantic, w, now)
	scoreOld := Score(oldEpisodic, w, now)

	if scoreRecent <= scoreOld {
		t.Errorf("recent semantic (%f) should score higher than old episodic (%f)", scoreRecent, scoreOld)
	}
}

func TestRankResults(t *testing.T) {
	now := time.Now().UTC()
	w := DefaultWeights()

	results := []model.MemoryWithContext{
		{Memory: model.Memory{ID: uuid.New(), Type: model.MemoryTypeEpisodic, CreatedAt: now.Add(-30 * 24 * time.Hour), AccessCount: 0}},
		{Memory: model.Memory{ID: uuid.New(), Type: model.MemoryTypeSemantic, CreatedAt: now, AccessCount: 10}},
		{Memory: model.Memory{ID: uuid.New(), Type: model.MemoryTypeProcedural, CreatedAt: now.Add(-time.Hour), AccessCount: 5}},
	}

	ranked := RankResults(results, w, 2)

	if len(ranked) != 2 {
		t.Fatalf("expected 2 results, got %d", len(ranked))
	}

	// First result should have the highest score.
	if ranked[0].Score < ranked[1].Score {
		t.Errorf("results not properly ranked: first=%.3f, second=%.3f", ranked[0].Score, ranked[1].Score)
	}
}
