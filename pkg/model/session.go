package model

import (
	"time"

	"github.com/google/uuid"
)

// Session represents an agent interaction session.
type Session struct {
	ID        uuid.UUID  `json:"id"`
	ProjectID string     `json:"project_id"`
	AgentID   string     `json:"agent_id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}
