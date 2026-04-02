// Package model defines the core domain types for the Context0 memory engine,
// including memories, edges, sessions, and projects.
package model

import "time"

// Project represents a top-level scoping container in the Context0 system.
// All memories, edges, and sessions are associated with exactly one project,
// providing tenant-level isolation for multi-project deployments.
type Project struct {
	// ID is the unique identifier for this project (typically a slug or UUID string).
	ID string `json:"id"`

	// Name is the human-readable display name of the project.
	Name string `json:"name"`

	// CreatedAt is the timestamp when the project was created.
	CreatedAt time.Time `json:"created_at"`
}
