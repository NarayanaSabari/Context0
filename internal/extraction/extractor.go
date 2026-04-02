// Package extraction converts raw conversation text into structured memory objects.
//
// Two extraction strategies are supported:
//
//   - LLM-based extraction: sends the conversation to a language model with a
//     structured prompt, then parses the JSON array response into typed memories.
//     This produces higher quality results but requires an LLM provider.
//
//   - Rule-based extraction: a zero-dependency fallback that scans each line of
//     the conversation for keyword patterns indicating preferences, procedures,
//     events, or facts. It strips speaker prefixes, filters noise (greetings,
//     filler), classifies each line by type, extracts technical topic tags, and
//     deduplicates results by content.
//
// The LLM strategy is used when a provider is configured; otherwise the rule-based
// strategy is applied automatically. If the LLM call fails at runtime, the
// extractor falls back to rule-based extraction transparently.
package extraction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/context0/context0/internal/llm"
	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

// ExtractedMemory represents a single memory extracted from a conversation. It
// carries the raw content, the classified memory type (semantic, episodic, or
// procedural), keyword tags, and an optional relation hint for linking to existing
// memories.
type ExtractedMemory struct {
	Content  string           `json:"content"`
	Type     model.MemoryType `json:"type"`
	Tags     []string         `json:"tags"`
	Relation *ExtractedRelation `json:"relation,omitempty"`
}

// ExtractedRelation indicates that an extracted memory relates to an existing
// memory in the graph. TargetHint is a content fragment used to fuzzy-match
// against existing memories when creating the relationship edge.
type ExtractedRelation struct {
	Type      model.RelationshipType `json:"type"`
	TargetHint string                `json:"target_hint"` // content fragment to match against existing memories
}

// Extractor extracts structured memories from raw conversations. It delegates to
// either the LLM-based or rule-based strategy depending on whether an LLM provider
// was supplied at construction time.
type Extractor struct {
	llmProvider llm.Provider // nil = use rule-based extraction
}

// NewExtractor creates a memory extractor.
// If llmProvider is nil, falls back to rule-based extraction.
func NewExtractor(llmProvider llm.Provider) *Extractor {
	return &Extractor{llmProvider: llmProvider}
}

// Extract processes a raw conversation and returns structured memories. It
// dispatches to the LLM strategy if a provider is available, otherwise falls back
// to rule-based extraction.
func (e *Extractor) Extract(ctx context.Context, conversation string, projectID string) ([]ExtractedMemory, error) {
	if e.llmProvider != nil {
		return e.llmExtract(ctx, conversation, projectID)
	}
	return e.ruleExtract(conversation), nil
}

// --- LLM-based extraction ---

// llmExtract sends the conversation to the configured LLM with a structured prompt
// requesting a JSON array of memories. Each memory includes content, type, and tags.
// If the LLM call fails, it transparently falls back to rule-based extraction.
func (e *Extractor) llmExtract(ctx context.Context, conversation string, _ string) ([]ExtractedMemory, error) {
	prompt := fmt.Sprintf(`You are a memory extraction engine. Analyze this conversation and extract structured memories.

For each memory, output a JSON object with:
- "content": the fact, preference, or event (concise, standalone statement)
- "type": one of "semantic" (fact), "episodic" (event that happened), "procedural" (how-to/pattern)
- "tags": 2-4 relevant keywords

Output a JSON array of memories. Extract ALL meaningful information — facts about people, decisions made, preferences stated, events that occurred, and patterns or workflows mentioned.

Conversation:
%s

Output ONLY the JSON array, no other text:`, conversation)

	response, err := e.llmProvider.Complete(ctx, prompt)
	if err != nil {
		// Fall back to rule-based if LLM fails.
		return e.ruleExtract(conversation), nil
	}

	return parseLLMResponse(response)
}

// parseLLMResponse extracts a JSON array from an LLM response string. It handles
// common formatting issues: strips markdown code fences, locates the outermost
// array brackets, and unmarshals each element into an ExtractedMemory. Empty
// content entries are silently skipped.
func parseLLMResponse(response string) ([]ExtractedMemory, error) {
	response = strings.TrimSpace(response)

	// Strip markdown code fences if present.
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	// Find the array boundaries.
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON array found in response")
	}
	response = response[start : end+1]

	var raw []struct {
		Content string   `json:"content"`
		Type    string   `json:"type"`
		Tags    []string `json:"tags"`
	}

	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse LLM JSON: %w", err)
	}

	var memories []ExtractedMemory
	for _, r := range raw {
		mt := model.MemoryTypeSemantic
		switch strings.ToLower(r.Type) {
		case "episodic", "episode", "event":
			mt = model.MemoryTypeEpisodic
		case "procedural", "procedure", "how-to", "pattern":
			mt = model.MemoryTypeProcedural
		}

		if r.Content == "" {
			continue
		}

		memories = append(memories, ExtractedMemory{
			Content: r.Content,
			Type:    mt,
			Tags:    r.Tags,
		})
	}

	return memories, nil
}

// --- Rule-based extraction (no LLM needed) ---

// ruleExtract processes each line of a conversation independently. For each line it:
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
func (e *Extractor) ruleExtract(conversation string) []ExtractedMemory {
	var memories []ExtractedMemory
	lines := strings.Split(conversation, "\n")

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
				Content: content,
				Type:    model.MemoryTypeSemantic,
				Tags:    append(extractTopics(content), "preference"),
			}
		}
	}

	// Detect procedural/how-to patterns.
	procPatterns := []string{"always run", "make sure to", "before deploying", "workflow", "step 1", "first,", "process for"}
	for _, p := range procPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content: content,
				Type:    model.MemoryTypeProcedural,
				Tags:    append(extractTopics(content), "workflow"),
			}
		}
	}

	// Detect decisions/events.
	eventPatterns := []string{"decided to", "switched to", "migrated", "deployed", "fixed", "changed", "meeting about", "discussed"}
	for _, p := range eventPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content: content,
				Type:    model.MemoryTypeEpisodic,
				Tags:    append(extractTopics(content), "event"),
			}
		}
	}

	// Detect facts (statements with "is", "uses", "has", "runs on").
	factPatterns := []string{" is ", " uses ", " has ", " runs on ", " built with ", " written in ", " deployed on ", " stored in "}
	for _, p := range factPatterns {
		if strings.Contains(lower, p) {
			return &ExtractedMemory{
				Content: content,
				Type:    model.MemoryTypeSemantic,
				Tags:    extractTopics(content),
			}
		}
	}

	// If nothing matches but it's substantial content, store as episodic.
	if len(content) > 30 {
		return &ExtractedMemory{
			Content: content,
			Type:    model.MemoryTypeEpisodic,
			Tags:    extractTopics(content),
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
