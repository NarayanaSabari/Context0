package model

import (
	"time"

	"github.com/google/uuid"
)

// RelationshipType represents the type of edge between two nodes.
type RelationshipType string

const (
	RelBelongsTo  RelationshipType = "belongs_to"
	RelContains   RelationshipType = "contains"
	RelRelatesTo  RelationshipType = "relates_to"
	RelSupersedes RelationshipType = "supersedes"
	RelCausedBy   RelationshipType = "caused_by"
)

// Edge represents a directed relationship between two nodes in the graph.
type Edge struct {
	ID           uuid.UUID        `json:"id"`
	FromID       uuid.UUID        `json:"from_id"`
	ToID         uuid.UUID        `json:"to_id"`
	Relationship RelationshipType `json:"relationship"`
	Weight       float64          `json:"weight"`
	CreatedAt    time.Time        `json:"created_at"`
}
