// Package model defines the core domain types for the Kora memory engine,
// including memories, edges, sessions, and projects.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Session represents an agent interaction session in the Kora system.
// Sessions group related memories created during a single agent conversation,
// enabling temporal context and conversation-scoped retrieval.
type Session struct {
	// ID is the unique identifier for this session.
	ID uuid.UUID `json:"id"`

	// ProjectID is the identifier of the project this session belongs to.
	ProjectID string `json:"project_id"`

	// AgentID identifies the agent (e.g., Claude, a custom bot) that owns this session.
	AgentID string `json:"agent_id"`

	// StartedAt is the timestamp when the session was opened.
	StartedAt time.Time `json:"started_at"`

	// EndedAt is the timestamp when the session was closed. A nil value indicates
	// the session is still active.
	EndedAt *time.Time `json:"ended_at,omitempty"`
}
