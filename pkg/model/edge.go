// Package model defines the core domain types for the Kora memory engine,
// including memories, edges, sessions, and projects.
package model

import (
	"time"

	"github.com/google/uuid"
)

// RelationshipType represents the semantic type of a directed edge between
// two memory nodes in the Kora knowledge graph.
type RelationshipType string

const (
	// RelBelongsTo indicates that the source node is owned by or scoped to the target
	// (e.g., a memory belongs to a project or session).
	RelBelongsTo RelationshipType = "belongs_to"

	// RelContains indicates that the source node is a parent that contains the target
	// (inverse of belongs_to).
	RelContains RelationshipType = "contains"

	// RelRelatesTo indicates a general semantic association between two memories.
	RelRelatesTo RelationshipType = "relates_to"

	// RelSupersedes indicates that the source memory replaces or updates the target,
	// used when newer information overrides older knowledge.
	RelSupersedes RelationshipType = "supersedes"

	// RelCausedBy indicates that the source memory was produced as a consequence
	// of the target (e.g., a decision caused by a prior event).
	RelCausedBy RelationshipType = "caused_by"
)

// relationshipTypes is the closed set of edge labels Kora recognizes.
var relationshipTypes = map[RelationshipType]bool{
	RelBelongsTo:  true,
	RelContains:   true,
	RelRelatesTo:  true,
	RelSupersedes: true,
	RelCausedBy:   true,
}

// Valid reports whether the relationship is one of the defined edge types.
//
// This matters beyond input hygiene: a relationship becomes a Cypher edge
// label, and openCypher has no parameter slot for labels, so the value is
// necessarily interpolated into the query text. Checking membership in this
// closed set is what makes that interpolation safe -- an arbitrary string
// could otherwise close the pattern and append clauses.
func (r RelationshipType) Valid() bool {
	return relationshipTypes[r]
}

// Edge represents a directed, weighted relationship between two memory nodes
// in the Kora knowledge graph. Edges are created explicitly via the
// Connect RPC or automatically during extraction.
type Edge struct {
	// ID is the unique identifier for this edge.
	ID uuid.UUID `json:"id"`

	// FromID is the source memory node of this directed edge.
	FromID uuid.UUID `json:"from_id"`

	// ToID is the target memory node of this directed edge.
	ToID uuid.UUID `json:"to_id"`

	// Relationship is the semantic type of the connection (e.g., relates_to, supersedes).
	Relationship RelationshipType `json:"relationship"`

	// Weight is the strength of this relationship, between 0 and 1.
	// Higher values indicate stronger associations used in graph traversal ranking.
	Weight float64 `json:"weight"`

	// CreatedAt is the timestamp when this edge was first created.
	CreatedAt time.Time `json:"created_at"`
}
