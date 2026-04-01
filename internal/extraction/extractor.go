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

// ExtractedMemory is a memory extracted from a conversation.
type ExtractedMemory struct {
	Content  string           `json:"content"`
	Type     model.MemoryType `json:"type"`
	Tags     []string         `json:"tags"`
	Relation *ExtractedRelation `json:"relation,omitempty"`
}

// ExtractedRelation indicates this memory relates to an existing memory.
type ExtractedRelation struct {
	Type      model.RelationshipType `json:"type"`
	TargetHint string                `json:"target_hint"` // content fragment to match against existing memories
}

// Extractor extracts structured memories from raw conversations.
type Extractor struct {
	llmProvider llm.Provider // nil = use rule-based extraction
}

// NewExtractor creates a memory extractor.
// If llmProvider is nil, falls back to rule-based extraction.
func NewExtractor(llmProvider llm.Provider) *Extractor {
	return &Extractor{llmProvider: llmProvider}
}

// Extract processes a raw conversation and returns structured memories.
func (e *Extractor) Extract(ctx context.Context, conversation string, projectID string) ([]ExtractedMemory, error) {
	if e.llmProvider != nil {
		return e.llmExtract(ctx, conversation, projectID)
	}
	return e.ruleExtract(conversation), nil
}

// --- LLM-based extraction ---

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

func parseLLMResponse(response string) ([]ExtractedMemory, error) {
	// Find JSON array in the response.
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

func stripSpeaker(line string) string {
	// Match "Speaker: content" pattern.
	if idx := strings.Index(line, ": "); idx > 0 && idx < 30 {
		return strings.TrimSpace(line[idx+2:])
	}
	return line
}

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

// extractTopics pulls keyword-like topics from content.
func extractTopics(content string) []string {
	// Technical terms are usually capitalized or contain special chars.
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

// ToMemories converts extracted memories to model.Memory objects.
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
