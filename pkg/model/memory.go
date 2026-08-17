// Package model defines the core domain types for the Context0 memory engine,
// including memories, edges, sessions, and projects.
package model

import (
	"time"

	"github.com/google/uuid"
)

// MemoryType represents the category of a memory node in the Context0 graph.
// Each type maps to a distinct cognitive category used for retrieval and profiling.
type MemoryType string

const (
	// MemoryTypeEpisodic represents event-based memories tied to specific interactions
	// or moments in time (e.g., "user asked about deployment on March 5th").
	MemoryTypeEpisodic MemoryType = "episodic"

	// MemoryTypeSemantic represents factual, long-lived knowledge extracted from
	// conversations (e.g., "user prefers Go over Python").
	MemoryTypeSemantic MemoryType = "semantic"

	// MemoryTypeProcedural represents learned processes, workflows, or how-to
	// knowledge (e.g., "deploy by running make release").
	MemoryTypeProcedural MemoryType = "procedural"
)

// Memory represents a single memory node in the Context0 knowledge graph.
// Memories are the fundamental unit of stored knowledge, created either
// explicitly via the Store RPC or automatically via the Extract RPC.
type Memory struct {
	// ID is the unique identifier for this memory node.
	ID uuid.UUID `json:"id"`

	// Content is the natural-language text of the memory.
	Content string `json:"content"`

	// Type is the cognitive category of this memory (episodic, semantic, or procedural).
	Type MemoryType `json:"type"`

	// ProjectID is the identifier of the project this memory is scoped to.
	ProjectID string `json:"project_id"`

	// Tags is a set of free-form labels for filtering and categorization.
	Tags []string `json:"tags"`

	// CreatedAt is the timestamp when the memory was first stored.
	CreatedAt time.Time `json:"created_at"`

	// AccessCount tracks how many times this memory has been retrieved by queries.
	// Used as an input to the decay scoring algorithm.
	AccessCount int64 `json:"access_count"`

	// DecayScore is a computed value between 0 and 1 representing the memory's
	// current relevance. Lower scores indicate the memory is fading and may be
	// pruned or deprioritized in query results.
	DecayScore float64 `json:"decay_score"`
}

// MemoryWithContext is a memory returned from a query, bundled with its
// neighboring edges and a relevance score. It provides the caller with
// enough context to understand why the memory was retrieved.
type MemoryWithContext struct {
	// Memory is the retrieved memory node.
	Memory Memory `json:"memory"`

	// Context lists the edges connecting this memory to related nodes,
	// explaining its relevance. Populated by the caller after ranking, not
	// by the repository query itself.
	Context []ContextEdge `json:"context,omitempty"`

	// Relevance is the retrieval-stage match quality in [0, 1]: how well this
	// memory answers the query text itself, independent of when it was created
	// or how often it has been read. Vector retrieval sets it from cosine
	// similarity; graph retrieval sets it from lexical keyword overlap. A query
	// with no search terms leaves it at 1.0 for every candidate, making it a
	// constant that cannot distort the remaining signals.
	//
	// Ranking consumes this field; it is the only channel through which
	// retrieval can influence the final order.
	Relevance float64 `json:"relevance"`

	// Score is the composite relevance score combining query relevance,
	// recency, access frequency, and memory type. Higher is more relevant.
	// Assigned by the ranking layer, which overwrites whatever was here.
	Score float64 `json:"score"`
}

// ContextEdge is a simplified edge returned alongside a memory in query
// results to explain how the memory relates to a neighboring node.
type ContextEdge struct {
	// Relationship is the semantic type of the connection to the target node.
	Relationship RelationshipType `json:"relationship"`

	// TargetID is the identifier of the connected memory node.
	TargetID uuid.UUID `json:"target_id"`

	// TargetContent is the text content of the connected memory, included to
	// avoid an extra lookup by the caller.
	TargetContent string `json:"target_content"`

	// Weight is the strength of this edge, between 0 and 1.
	Weight float64 `json:"weight"`
}
