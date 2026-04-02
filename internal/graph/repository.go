// Package graph defines the graph database abstraction layer for Context0's
// memory storage. It provides a Repository interface that encapsulates all
// graph operations (nodes, edges, sessions, embeddings, queries) and allows
// swapping the underlying graph engine without affecting consumers.
//
// The primary implementation is AGERepository (see age.go), which uses
// Apache AGE on PostgreSQL with pgvector for vector similarity search.
package graph

import (
	"context"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

// QueryFilter defines filters for graph traversal queries. Callers combine
// multiple filter fields to narrow down which Memory nodes are returned.
// All non-zero fields are AND-ed together; Keywords are OR-ed within the group.
type QueryFilter struct {
	// ProjectID restricts results to memories belonging to this project.
	ProjectID string
	// Keywords are matched case-insensitively against memory content and tags.
	Keywords []string
	// Tags filters memories that contain any of the specified tags.
	Tags []string
	// Types restricts results to specific memory types (e.g. fact, episode).
	Types []model.MemoryType
	// MaxDepth limits how many hops a graph traversal may follow.
	MaxDepth int32
	// TopK caps the number of results returned. Defaults to 5 if zero.
	TopK int32
}

// Repository defines the interface for graph database operations.
// This abstraction allows swapping Apache AGE for another graph DB
// (e.g. Neo4j, Memgraph) without changing business logic. All methods
// accept a context.Context for cancellation and timeout control.
type Repository interface {
	// InitSchema creates the graph, extensions, tables, and indexes
	// required by the implementation. Safe to call multiple times.
	InitSchema(ctx context.Context) error

	// Close releases database connections and any held resources.
	Close()

	// --- Project operations ---

	// CreateProject persists a new project node in the graph.
	CreateProject(ctx context.Context, project model.Project) error
	// GetProject retrieves a project by its string identifier.
	GetProject(ctx context.Context, id string) (model.Project, error)

	// --- Memory operations ---

	// CreateMemory persists a new memory node with tags and metadata.
	CreateMemory(ctx context.Context, memory model.Memory) error
	// GetMemory retrieves a single memory node by UUID.
	GetMemory(ctx context.Context, id uuid.UUID) (model.Memory, error)
	// DeleteMemory removes a memory node and all its connected edges.
	DeleteMemory(ctx context.Context, id uuid.UUID) error
	// IncrementAccessCount atomically increments the access counter on a
	// memory node, used for recency/frequency-based ranking.
	IncrementAccessCount(ctx context.Context, id uuid.UUID) error

	// --- Edge operations ---

	// CreateEdge creates a directed, weighted relationship between two nodes.
	CreateEdge(ctx context.Context, edge model.Edge) error
	// GetEdgesFrom returns all outgoing edges from the given node.
	GetEdgesFrom(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error)
	// GetEdgesTo returns all incoming edges to the given node.
	GetEdgesTo(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error)
	// DeleteEdgesForNode removes all edges (both directions) connected to a node.
	DeleteEdgesForNode(ctx context.Context, nodeID uuid.UUID) error

	// --- Session operations ---

	// CreateSession persists a new session node and links it to its project
	// via a belongs_to edge.
	CreateSession(ctx context.Context, session model.Session) error
	// EndSession stamps the ended_at timestamp on a session and returns
	// the updated session. Returns an error if the session does not exist.
	EndSession(ctx context.Context, id uuid.UUID) (model.Session, error)
	// LinkMemoryToSession creates a contains edge from a session to a memory,
	// recording that the memory was produced or referenced during the session.
	LinkMemoryToSession(ctx context.Context, sessionID, memoryID uuid.UUID) error

	// --- Embedding operations (pgvector) ---

	// StoreEmbedding upserts a vector embedding for a memory node into the
	// pgvector-backed embeddings table. If an embedding already exists for
	// the given memoryID, it is replaced.
	StoreEmbedding(ctx context.Context, memoryID uuid.UUID, embedding []float32) error

	// SearchByVector performs approximate nearest neighbor search using cosine
	// similarity against stored embeddings. Results are returned ranked by
	// descending similarity score. If projectID is non-empty, results are
	// scoped to that project.
	SearchByVector(ctx context.Context, embedding []float32, projectID string, topK int) ([]model.MemoryWithContext, error)

	// --- Query operations ---

	// QueryMemories performs a filtered graph traversal, matching Memory nodes
	// by project, type, and keywords. Results include a relevance score and
	// are ordered by creation time (newest first).
	QueryMemories(ctx context.Context, filter QueryFilter) ([]model.MemoryWithContext, error)

	// GetSubgraph returns all Memory nodes and edges reachable within the
	// given depth from a center node. Depth is clamped to [1, 5].
	GetSubgraph(ctx context.Context, centerID uuid.UUID, depth int32) ([]model.Memory, []model.Edge, error)

	// --- Statistics ---

	// NodeCount returns the total number of nodes in the graph.
	NodeCount(ctx context.Context) (int64, error)
	// EdgeCount returns the total number of directed edges in the graph.
	EdgeCount(ctx context.Context) (int64, error)
}
