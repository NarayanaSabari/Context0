package extraction

import (
	"testing"
	"time"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

func mem(content string, memType model.MemoryType, tags ...string) model.Memory {
	return model.Memory{
		ID:        uuid.New(),
		Content:   content,
		Type:      memType,
		ProjectID: "test-project",
		Tags:      tags,
		CreatedAt: time.Now().UTC(),
	}
}

func TestContradiction_SameSubjectDifferentValue(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new_ string
		want bool
	}{
		{"database switch", "Project uses MySQL", "Project uses PostgreSQL", true},
		{"language switch", "Backend uses Python", "Backend uses Go", true},
		{"person location", "Alice lives in London", "Alice lives in Tokyo", true},
		{"same fact", "Project uses PostgreSQL", "Project uses PostgreSQL 15", false},
		{"unrelated facts", "Project uses PostgreSQL", "Team has 5 members", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMem := mem(tt.old, model.MemoryTypeSemantic)
			newMem := mem(tt.new_, model.MemoryTypeSemantic)

			contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

			if tt.want && len(contradictions) == 0 {
				t.Error("expected contradiction, got none")
			}
			if !tt.want && len(contradictions) > 0 {
				for _, c := range contradictions {
					t.Logf("  unexpected: reason=%q confidence=%.2f", c.Reason, c.Confidence)
				}
			}
			for _, c := range contradictions {
				t.Logf("  found: reason=%q confidence=%.2f", c.Reason, c.Confidence)
			}
		})
	}
}

func TestContradiction_ReplacementSignals(t *testing.T) {
	tests := []struct {
		name     string
		old      string
		new_     string
		wantConf float64
	}{
		{"switched to", "Project uses MySQL for data", "We switched to PostgreSQL for data", 0.9},
		{"migrated to", "Backend runs on AWS for hosting", "We migrated to GCP for hosting", 0.9},
		{"no longer", "Team uses Slack", "Team no longer uses Slack, moved to Discord", 0.8},
		{"replaced with", "Auth uses JWT tokens", "We replaced JWT with session-based auth", 0.9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMem := mem(tt.old, model.MemoryTypeSemantic)
			newMem := mem(tt.new_, model.MemoryTypeSemantic)

			contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

			if len(contradictions) == 0 {
				t.Fatal("expected contradiction from replacement signal")
			}

			c := contradictions[0]
			t.Logf("reason=%q confidence=%.2f", c.Reason, c.Confidence)

			if c.Confidence < tt.wantConf {
				t.Errorf("confidence = %.2f, want >= %.2f", c.Confidence, tt.wantConf)
			}
		})
	}
}

func TestContradiction_Negation(t *testing.T) {
	oldMem := mem("The system supports multi-tenancy", model.MemoryTypeSemantic)
	newMem := mem("The system doesn't support multi-tenancy", model.MemoryTypeSemantic)

	contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

	if len(contradictions) == 0 {
		t.Fatal("expected contradiction from negation")
	}

	c := contradictions[0]
	t.Logf("reason=%q confidence=%.2f", c.Reason, c.Confidence)
}

func TestContradiction_SkipsNonSemantic(t *testing.T) {
	oldMem := mem("Discussed using MySQL", model.MemoryTypeEpisodic)
	newMem := mem("Project uses PostgreSQL", model.MemoryTypeSemantic)

	contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

	for _, c := range contradictions {
		if c.OldMemory.Type != model.MemoryTypeSemantic {
			t.Error("should not flag contradiction with episodic memory")
		}
	}
}

func TestContradiction_SkipsWhenNewIsEpisodic(t *testing.T) {
	oldMem := mem("Project uses MySQL", model.MemoryTypeSemantic)
	newMem := mem("Someone mentioned PostgreSQL today", model.MemoryTypeEpisodic)

	contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

	if len(contradictions) != 0 {
		t.Error("episodic new memory should not trigger contradiction detection")
	}
}

func TestContradiction_TagBasedOverlap(t *testing.T) {
	oldMem := mem("Primary database is MySQL 8.0", model.MemoryTypeSemantic, "database", "mysql")
	newMem := mem("Primary database is PostgreSQL 15", model.MemoryTypeSemantic, "database", "postgresql")

	contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

	if len(contradictions) == 0 {
		t.Fatal("expected contradiction with shared tags and different content")
	}

	t.Logf("found %d contradictions", len(contradictions))
	for _, c := range contradictions {
		t.Logf("  reason=%q confidence=%.2f", c.Reason, c.Confidence)
	}
}

func TestContradiction_NonTechDomain(t *testing.T) {
	// Medical domain
	oldMem := mem("Patient prefers morning appointments", model.MemoryTypeSemantic, "scheduling")
	newMem := mem("Patient prefers evening appointments", model.MemoryTypeSemantic, "scheduling")

	contradictions := DetectContradictions(newMem, []model.Memory{oldMem})

	if len(contradictions) == 0 {
		t.Fatal("expected contradiction in non-tech domain")
	}

	c := contradictions[0]
	t.Logf("non-tech contradiction: reason=%q confidence=%.2f", c.Reason, c.Confidence)
}

func TestKeywordOverlap(t *testing.T) {
	tests := []struct {
		a, b    string
		wantMin float64
		wantMax float64
	}{
		{"project uses postgresql", "project uses mysql", 0.3, 1.0},
		{"hello world", "completely different sentence", 0.0, 0.1},
		{"the cat sat on the mat", "the cat sat on the hat", 0.5, 1.0},
	}

	for _, tt := range tests {
		name := tt.a
		if len(name) > 20 {
			name = name[:20]
		}
		t.Run(name, func(t *testing.T) {
			got := keywordOverlap(tt.a, tt.b)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("overlap = %.3f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestExtractTriples(t *testing.T) {
	tests := []struct {
		text    string
		wantMin int
	}{
		{"project uses postgresql", 1},
		{"backend is fast and reliable", 1},
		{"alice lives in london", 1},
		{"hello world", 0},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			triples := extractTriples(tt.text)
			if len(triples) < tt.wantMin {
				t.Errorf("expected >= %d triples, got %d", tt.wantMin, len(triples))
			}
			for _, tr := range triples {
				t.Logf("  triple: %q %q %q", tr.subject, tr.verb, tr.object)
			}
		})
	}
}
