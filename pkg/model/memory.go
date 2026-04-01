package model

import (
	"time"

	"github.com/google/uuid"
)

// MemoryType represents the category of a memory node.
type MemoryType string

const (
	MemoryTypeEpisodic   MemoryType = "episodic"
	MemoryTypeSemantic   MemoryType = "semantic"
	MemoryTypeProcedural MemoryType = "procedural"
)

// Memory represents a single memory node in the graph.
type Memory struct {
	ID          uuid.UUID  `json:"id"`
	Content     string     `json:"content"`
	Type        MemoryType `json:"type"`
	ProjectID   string     `json:"project_id"`
	Tags        []string   `json:"tags"`
	CreatedAt   time.Time  `json:"created_at"`
	AccessCount int64      `json:"access_count"`
	DecayScore  float64    `json:"decay_score"`
}

// MemoryWithContext is a memory returned from a query, including its connected edges.
type MemoryWithContext struct {
	Memory  Memory        `json:"memory"`
	Context []ContextEdge `json:"context"`
	Score   float64       `json:"score"`
}

// ContextEdge is a simplified edge returned alongside a memory to explain relevance.
type ContextEdge struct {
	Relationship RelationshipType `json:"relationship"`
	TargetID     uuid.UUID        `json:"target_id"`
	TargetContent string          `json:"target_content"`
	Weight       float64          `json:"weight"`
}
