// Package extraction converts raw conversation text into structured memory objects.
//
// Extraction is rule-based: a zero-dependency scanner that walks each line of
// the conversation for keyword patterns indicating preferences, procedures,
// events, or facts. It strips speaker prefixes, filters noise (greetings,
// filler), classifies each line by type, extracts technical topic tags, and
// deduplicates results by content.
package extraction

import (
	"regexp"
	"strings"
	"time"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// ExtractedMemory represents a single memory extracted from a conversation. It
// carries the raw content, the classified memory type (semantic, episodic, or
// procedural), and keyword tags.
type ExtractedMemory struct {
	Content string           `json:"content"`
	Type    model.MemoryType `json:"type"`
	Tags    []string         `json:"tags"`

	// Entities are the people, places, organisations and works this memory is
	// about. They become first-class nodes, so two memories mentioning the
	// same entity are one hop apart regardless of how differently they are
	// worded.
	//
	// Distinct from Tags, which are topic labels from a fixed vocabulary and
	// exist to group memories by subject area. An entity is a thing the world
	// contains; a tag is a category this engine recognises.
	Entities []string `json:"entities,omitempty"`
}

// Extract processes each line of a conversation independently. For each line it:
//  1. Strips speaker prefixes (e.g. "user: ").
//  2. Filters out noise (greetings, filler, very short lines).
//  3. Classifies the line into a memory type by matching keyword patterns:
//     - Preference patterns ("prefer", "like to", etc.) -> semantic
//     - Procedural patterns ("always run", "before deploying", etc.) -> procedural
//     - Event patterns ("decided to", "switched to", etc.) -> episodic
//     - Fact patterns (" is ", " uses ", etc.) -> semantic
//     - Substantial unmatched content (>30 chars) -> episodic (default)
//  4. Extracts technical topic tags from recognized terms.
//  5. Deduplicates by exact content (case-insensitive).
func Extract(conversation string) []ExtractedMemory {
	var memories []ExtractedMemory
	lines := splitUtterances(conversation)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 10 {
			continue
		}

		// Strip speaker prefix ("user:", "assistant:", "Alice:", etc.)
		content := stripSpeaker(line)
		if content == "" {
			continue
		}

		mem := classifyLine(content)
		if mem != nil {
			memories = append(memories, *mem)
		}
	}

	// Deduplicate by content similarity.
	return dedup(memories)
}

// splitUtterances breaks a conversation into individual turns.
//
// Newlines are the obvious separator and were the only one, which meant a
// conversation sent as a single JSON string -- the normal shape for an HTTP API
// client -- was treated as one utterance. Sending
//
//	"User: I prefer PostgreSQL. Assistant: Noted. User: We deploy on Fridays."
//
// produced exactly one memory whose content was the entire string including
// "Assistant: Noted.", while the same text with newlines produced two clean
// ones. Two distinct facts silently became one polluted record, and nothing
// reported a problem.
//
// So a speaker label also starts a new turn, wherever it appears. The pattern
// is deliberately narrow -- one or two capitalised words followed by ": ",
// anchored to a sentence boundary -- so it does not fire inside ordinary prose
// like "The rule is: never deploy on Friday", where the text before the colon
// is not a name.
func splitUtterances(conversation string) []string {
	var out []string
	for _, line := range strings.Split(conversation, "\n") {
		out = append(out, splitOnSpeakerLabels(line)...)
	}
	return out
}

// speakerLabel matches a speaker prefix appearing mid-line.
var speakerLabel = regexp.MustCompile(`(^|[.!?]\s+)([A-Z][A-Za-z0-9_-]{0,20}(?:\s+[A-Z][A-Za-z0-9_-]{0,20})?):\s`)

// splitOnSpeakerLabels cuts one line at each embedded speaker label.
func splitOnSpeakerLabels(line string) []string {
	locs := speakerLabel.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return []string{line}
	}

	var parts []string
	prev := 0
	for _, loc := range locs {
		// The turn starts where the speaker's name does, not at the preceding
		// sentence boundary the pattern anchored on.
		start := loc[4]
		if start > prev {
			if seg := strings.TrimSpace(line[prev:start]); seg != "" {
				parts = append(parts, seg)
			}
		}
		prev = start
	}
	if seg := strings.TrimSpace(line[prev:]); seg != "" {
		parts = append(parts, seg)
	}
	return parts
}

// stripSpeaker removes a leading "Speaker: " prefix from a conversation line.
// It only matches if the colon appears within the first 30 characters to avoid
// stripping content that happens to contain a colon further in.
func stripSpeaker(line string) string {
	if idx := strings.Index(line, ": "); idx > 0 && idx < 30 {
		return strings.TrimSpace(line[idx+2:])
	}
	return line
}

// classifyLine attempts to classify a single line of conversation content into
// a typed ExtractedMemory. It checks keyword pattern lists in priority order:
// preferences, procedural, events, then facts. Returns nil if the content is
// noise or too short to be meaningful.
func classifyLine(content string) *ExtractedMemory {
	lower := strings.ToLower(content)

	// Skip greetings, filler, and very short content.
	if isNoise(lower) {
		return nil
	}

	// Detect preferences.
	prefPatterns := []string{"prefer", "like to", "always use", "rather", "favorite", "usually"}
	for _, p := range prefPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content:  content,
				Type:     model.MemoryTypeSemantic,
				Tags:     append(extractTopics(content), "preference"),
				Entities: ExtractEntities(content),
			}
		}
	}

	// Detect procedural/how-to patterns.
	procPatterns := []string{"always run", "make sure to", "before deploying", "workflow", "step 1", "first,", "process for"}
	for _, p := range procPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content:  content,
				Type:     model.MemoryTypeProcedural,
				Tags:     append(extractTopics(content), "workflow"),
				Entities: ExtractEntities(content),
			}
		}
	}

	// Detect decisions/events.
	eventPatterns := []string{"decided to", "switched to", "migrated", "deployed", "fixed", "changed", "meeting about", "discussed"}
	for _, p := range eventPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content:  content,
				Type:     model.MemoryTypeEpisodic,
				Tags:     append(extractTopics(content), "event"),
				Entities: ExtractEntities(content),
			}
		}
	}

	// Detect facts (statements with "is", "uses", "has", "runs on").
	factPatterns := []string{" is ", " uses ", " has ", " runs on ", " built with ", " written in ", " deployed on ", " stored in "}
	for _, p := range factPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content:  content,
				Type:     model.MemoryTypeSemantic,
				Tags:     extractTopics(content),
				Entities: ExtractEntities(content),
			}
		}
	}

	// If nothing matches but it's substantial content, store as episodic.
	if len(content) > 30 {
		return &ExtractedMemory{
			Content:  content,
			Type:     model.MemoryTypeEpisodic,
			Tags:     extractTopics(content),
			Entities: ExtractEntities(content),
		}
	}

	return nil
}

// isNoise returns true if the lowercased line is a greeting, filler phrase, or
// too short (under 15 characters) to contain meaningful information.
func isNoise(lower string) bool {
	noise := []string{
		"hello", "hi there", "hey", "thanks", "thank you", "ok", "okay",
		"sure", "yes", "no", "got it", "sounds good", "right", "hmm",
		"let me", "i see", "understood",
	}
	for _, n := range noise {
		if strings.TrimSpace(lower) == n {
			return true
		}
	}
	return len(lower) < 15
}

// extractTopics pulls keyword-like topics from content by matching words against
// a curated dictionary of technical terms (databases, languages, frameworks, cloud
// providers, etc.). Returns at most 4 tags, deduplicated and lowercased.
func extractTopics(content string) []string {
	words := strings.Fields(content)
	var topics []string
	seen := make(map[string]bool)

	techTerms := map[string]bool{
		"postgresql": true, "postgres": true, "mysql": true, "redis": true,
		"kubernetes": true, "k8s": true, "docker": true, "helm": true,
		"go": true, "golang": true, "python": true, "typescript": true, "rust": true, "java": true,
		"grpc": true, "rest": true, "api": true, "graphql": true,
		"react": true, "vue": true, "angular": true, "nextjs": true,
		"aws": true, "gcp": true, "azure": true,
		"auth": true, "oauth": true, "jwt": true, "authentication": true,
		"database": true, "cache": true, "queue": true, "deployment": true,
		"testing": true, "ci": true, "cd": true, "cicd": true,
		"git": true, "github": true, "gitlab": true,
	}

	for _, w := range words {
		w = strings.Trim(strings.ToLower(w), ".,;:!?\"'()[]{}/-")
		if len(w) < 2 {
			continue
		}
		if techTerms[w] && !seen[w] {
			seen[w] = true
			topics = append(topics, w)
		}
	}

	if len(topics) > 4 {
		topics = topics[:4]
	}
	return topics
}

// dedup removes duplicate extracted memories by comparing lowercased content.
// The first occurrence of each unique content string is kept.
func dedup(memories []ExtractedMemory) []ExtractedMemory {
	var result []ExtractedMemory
	seen := make(map[string]bool)
	for _, m := range memories {
		key := strings.ToLower(m.Content)
		if !seen[key] {
			seen[key] = true
			result = append(result, m)
		}
	}
	return result
}

// ToMemories converts a slice of ExtractedMemory values into fully initialized
// model.Memory objects, assigning new UUIDs, setting the project ID, and
// initializing access count to 0 and decay score to 1.0 (maximum freshness).
func ToMemories(extracted []ExtractedMemory, projectID string) []model.Memory {
	var memories []model.Memory
	now := time.Now().UTC()

	for _, e := range extracted {
		memories = append(memories, model.Memory{
			ID:          uuid.New(),
			Content:     e.Content,
			Type:        e.Type,
			ProjectID:   projectID,
			Tags:        e.Tags,
			CreatedAt:   now,
			AccessCount: 0,
			DecayScore:  1.0,
		})
	}
	return memories
}
