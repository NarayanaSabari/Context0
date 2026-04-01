package service

import (
	"strings"
	"testing"
	"time"

	"github.com/context0/context0/pkg/model"
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

func TestFilterTimeKeywords(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantTime   bool
		wantClean  string
	}{
		{"no time", "what database", false, "what database"},
		{"today", "what changed today", true, "what changed"},
		{"last week", "show me last week updates", true, "show me  updates"},
		{"recent", "recent changes", true, "changes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var timeAfter *time.Time
			got := filterTimeKeywords(tt.query, &timeAfter)

			if tt.wantTime && timeAfter == nil {
				t.Error("expected timeAfter to be set")
			}
			if !tt.wantTime && timeAfter != nil {
				t.Error("expected timeAfter to be nil")
			}

			got = strings.TrimSpace(got)
			expected := strings.TrimSpace(tt.wantClean)
			if got != expected {
				t.Errorf("cleaned query = %q, want %q", got, expected)
			}
		})
	}
}

func TestParseQuery_Defaults(t *testing.T) {
	p := ParseQuery("test query", "proj1", nil, 0, 0)

	if p.MaxDepth != 2 {
		t.Errorf("default MaxDepth = %d, want 2", p.MaxDepth)
	}
	if p.TopK != 5 {
		t.Errorf("default TopK = %d, want 5", p.TopK)
	}
	if p.ProjectID != "proj1" {
		t.Errorf("ProjectID = %q, want 'proj1'", p.ProjectID)
	}
}

func TestParseQuery_Limits(t *testing.T) {
	p := ParseQuery("test", "proj1", nil, 10, 50)

	if p.MaxDepth != 5 {
		t.Errorf("MaxDepth should be capped at 5, got %d", p.MaxDepth)
	}
	if p.TopK != 20 {
		t.Errorf("TopK should be capped at 20, got %d", p.TopK)
	}
}

func TestBuildCypherQuery(t *testing.T) {
	p := ParsedQuery{
		ProjectID: "myproject",
		Keywords:  []string{"postgres"},
		Types:     []model.MemoryType{model.MemoryTypeSemantic},
		TopK:      5,
	}

	q := BuildCypherQuery(p)

	if !strings.Contains(q, "m.project_id = 'myproject'") {
		t.Error("query should filter by project_id")
	}
	if !strings.Contains(q, "'semantic'") {
		t.Error("query should filter by type")
	}
	if !strings.Contains(q, "postgres") {
		t.Error("query should contain keyword")
	}
	if !strings.Contains(q, "LIMIT") {
		t.Error("query should have LIMIT")
	}
}

func TestBuildNeighborhoodQuery(t *testing.T) {
	q := BuildNeighborhoodQuery("abc-123", nil)
	if !strings.Contains(q, "abc-123") {
		t.Error("query should contain memory ID")
	}

	q = BuildNeighborhoodQuery("abc-123", []model.RelationshipType{model.RelSupersedes, model.RelCausedBy})
	if !strings.Contains(q, "supersedes|caused_by") {
		t.Error("query should filter by edge types")
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
