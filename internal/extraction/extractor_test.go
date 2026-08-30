package extraction

import (
	"strings"
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

// TestExtract_SingleLineConversation covers the shape an HTTP client actually
// sends.
//
// Extraction split on newlines only, so a conversation delivered as one JSON
// string -- which is what any API caller does unless they deliberately embed
// "\n" -- was treated as a single utterance. The result was one memory whose
// content was the whole conversation, "Assistant: Noted." included, where the
// newline-separated form produced two clean ones.
//
// Nothing failed. The API returned 200 and a memory, so the only symptom was
// worse recall later, from a record that mixed two facts with filler.
func TestExtract_SingleLineConversation(t *testing.T) {
	const oneLine = "User: I prefer PostgreSQL over MySQL for this project. " +
		"Assistant: Noted. User: We deploy on Kubernetes every Friday."

	got := Extract(oneLine)

	if len(got) < 2 {
		t.Fatalf("extracted %d memories from a single-line conversation, want at least 2; "+
			"the turns were not separated", len(got))
	}

	// No memory may carry another speaker's turn inside it.
	for _, m := range got {
		if strings.Contains(m.Content, "Assistant:") || strings.Contains(m.Content, "User:") {
			t.Errorf("memory content still contains a speaker label: %q", m.Content)
		}
	}

	// Both distinct facts must survive as separate memories.
	var sawPostgres, sawDeploy bool
	for _, m := range got {
		if strings.Contains(m.Content, "PostgreSQL") {
			sawPostgres = true
			if strings.Contains(m.Content, "Kubernetes") {
				t.Errorf("two separate facts were merged into one memory: %q", m.Content)
			}
		}
		if strings.Contains(m.Content, "Kubernetes") {
			sawDeploy = true
		}
	}
	if !sawPostgres || !sawDeploy {
		t.Errorf("lost a fact: postgres=%v deploy=%v; got %d memories",
			sawPostgres, sawDeploy, len(got))
	}
}

// TestExtract_DoesNotSplitOrdinaryProse is the counterpart: the speaker pattern
// must not fire on a colon that is merely punctuation, or every "the rule is:"
// sentence gets torn in half.
func TestExtract_DoesNotSplitOrdinaryProse(t *testing.T) {
	for _, s := range []string{
		"We always run the tests first: it catches the obvious mistakes early.",
		"The deployment window is 09:00 to 17:00 on weekdays only.",
		"Remember this: the database prefers batched writes over single inserts.",
	} {
		parts := splitOnSpeakerLabels(s)
		if len(parts) != 1 {
			t.Errorf("split ordinary prose into %d parts: %q -> %v", len(parts), s, parts)
		}
	}
}

// TestExtractIgnoresSpeakerOnlyLines: a line that is nothing but a speaker
// label carries no content, and storing it would put an empty memory in the
// graph that matches queries by recency while saying nothing.
//
// stripSpeaker returns "" for these. The explicit guard in Extract is
// belt-and-braces: isNoise("") is also true, so classifyLine rejects them a
// second time. Mutation testing flagged the guard as unprotected, and it is --
// removing it changes nothing, which makes it an equivalent mutant rather than
// a test gap. The invariant is worth pinning either way, because it is the
// second layer that currently holds it up.
func TestExtractIgnoresSpeakerOnlyLines(t *testing.T) {
	// Padded past the 10-character minimum so the length filter is not what
	// rejects them; the empty-content guard has to be what does.
	transcript := strings.Join([]string{
		"user:                    ",
		"assistant:               ",
		"Alice:                   ",
		"user: I prefer dark mode in the editor",
		"moderator:               ",
	}, "\n")

	got := Extract(transcript)

	for _, m := range got {
		if strings.TrimSpace(m.Content) == "" {
			t.Errorf("extracted an empty memory %+v; it would match queries by "+
				"recency while carrying no information", m)
		}
	}

	// The one real utterance must still be extracted, so the guard cannot be
	// satisfied by dropping everything.
	var found bool
	for _, m := range got {
		if strings.Contains(strings.ToLower(m.Content), "dark mode") {
			found = true
		}
	}
	if !found {
		t.Errorf("the real utterance was not extracted; got %d memories: %+v", len(got), got)
	}
}

// A line that is nothing but a speaker label produces no memory.
//
// The rule-based extractor strips a "Name:" prefix, and a line consisting only
// of that prefix strips to nothing. Storing it would put an empty memory in the
// graph: retrievable, meaningless, and counted in every total the engine
// reports.
//
// The length filter above does not catch it -- "Caroline Wentworth:" is well
// over the minimum -- so this is the only thing standing between a transcript's
// stray label lines and the store.
func TestRuleExtractor_SkipsLinesThatAreOnlyASpeakerLabel(t *testing.T) {
	memories, err := RuleExtractor{}.Extract("Caroline Wentworth:\nCaroline said that she adopted a rescue dog named Biscuit.")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	for _, m := range memories {
		if strings.TrimSpace(m.Content) == "" {
			t.Error("an empty memory was extracted from a line that was only a speaker label")
		}
	}
	if len(memories) != 1 {
		t.Errorf("got %d memories from one real line and one bare label: %v", len(memories), memories)
	}
}
