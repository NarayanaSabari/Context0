package extraction

import (
	"testing"
)

func TestRuleExtract_Facts(t *testing.T) {
	conversation := `user: Our backend is written in Go using gRPC
assistant: Got it. What database are you using?
user: The project uses PostgreSQL 15 with Apache AGE for graph queries
assistant: Nice. How is it deployed?
user: Kubernetes deployment uses Helm charts`

	results := Extract(conversation)

	if len(results) == 0 {
		t.Fatal("expected at least 1 extracted memory")
	}

	// Should find "uses" patterns as semantic facts.
	foundPostgres := false
	foundGo := false
	for _, r := range results {
		if contains(r.Content, "PostgreSQL") || contains(r.Content, "postgres") {
			foundPostgres = true
		}
		if contains(r.Content, "Go") || contains(r.Content, "gRPC") {
			foundGo = true
		}
	}

	if !foundPostgres {
		t.Error("should extract PostgreSQL fact")
	}
	if !foundGo {
		t.Error("should extract Go/gRPC fact")
	}

	t.Logf("extracted %d memories:", len(results))
	for _, r := range results {
		t.Logf("  [%s] %s (tags: %v)", r.Type, r.Content, r.Tags)
	}
}

func TestRuleExtract_Preferences(t *testing.T) {
	conversation := `user: I prefer TypeScript over JavaScript
user: I always use vim for editing
user: My favorite framework is Next.js`

	results := Extract(conversation)

	prefCount := 0
	for _, r := range results {
		for _, tag := range r.Tags {
			if tag == "preference" {
				prefCount++
				break
			}
		}
	}

	if prefCount == 0 {
		t.Error("should extract at least 1 preference")
	}

	t.Logf("found %d preferences out of %d memories", prefCount, len(results))
}

func TestRuleExtract_Events(t *testing.T) {
	conversation := `user: We decided to switch from MySQL to PostgreSQL last week
user: The team deployed the new auth service yesterday
user: We discussed the migration strategy in today's meeting`

	results := Extract(conversation)

	episodicCount := 0
	for _, r := range results {
		if r.Type == "episodic" {
			episodicCount++
		}
	}

	if episodicCount == 0 {
		t.Error("should extract at least 1 episodic memory")
	}

	t.Logf("found %d episodic memories out of %d", episodicCount, len(results))
}

func TestRuleExtract_Noise(t *testing.T) {
	conversation := `user: hello
assistant: hi there
user: thanks
assistant: sure
user: ok`

	results := Extract(conversation)

	if len(results) != 0 {
		t.Errorf("noise should produce 0 memories, got %d", len(results))
	}
}

func TestToMemories(t *testing.T) {
	extracted := []ExtractedMemory{
		{Content: "test fact", Type: "semantic", Tags: []string{"test"}},
		{Content: "test event", Type: "episodic", Tags: []string{"test"}},
	}

	memories := ToMemories(extracted, "project-1")

	if len(memories) != 2 {
		t.Fatalf("expected 2, got %d", len(memories))
	}
	if memories[0].ProjectID != "project-1" {
		t.Error("project ID not set")
	}
	if memories[0].ID.String() == "" {
		t.Error("UUID not generated")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsCI(s, sub))
}

func containsCI(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(toLower(s), toLower(sub)) >= 0)
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
