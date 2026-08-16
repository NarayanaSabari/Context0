package service

import (
	"testing"
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
