package graph

import (
	"context"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
)

// QueryFilter defines filters for graph traversal queries.
type QueryFilter struct {
	ProjectID string
	Keywords  []string
	Tags      []string
	Types     []model.MemoryType
	MaxDepth  int32
	TopK      int32
}

// Repository defines the interface for graph database operations.
// This abstraction allows swapping Apache AGE for another graph DB later.
type Repository interface {
	// InitSchema creates the graph and sets up initial labels/indexes.
	InitSchema(ctx context.Context) error

	// Close releases database connections.
	Close()

	// --- Project ---

	CreateProject(ctx context.Context, project model.Project) error
	GetProject(ctx context.Context, id string) (model.Project, error)

	// --- Memory ---

	CreateMemory(ctx context.Context, memory model.Memory) error
	GetMemory(ctx context.Context, id uuid.UUID) (model.Memory, error)
	DeleteMemory(ctx context.Context, id uuid.UUID) error
	IncrementAccessCount(ctx context.Context, id uuid.UUID) error

	// --- Edge ---

	CreateEdge(ctx context.Context, edge model.Edge) error
	GetEdgesFrom(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error)
	GetEdgesTo(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error)
	DeleteEdgesForNode(ctx context.Context, nodeID uuid.UUID) error

	// --- Session ---

	CreateSession(ctx context.Context, session model.Session) error
	EndSession(ctx context.Context, id uuid.UUID) (model.Session, error)
	LinkMemoryToSession(ctx context.Context, sessionID, memoryID uuid.UUID) error

	// --- Embeddings ---

	// StoreEmbedding stores a vector embedding for a memory node.
	StoreEmbedding(ctx context.Context, memoryID uuid.UUID, embedding []float32) error

	// SearchByVector returns memories similar to the given embedding vector.
	SearchByVector(ctx context.Context, embedding []float32, projectID string, topK int) ([]model.MemoryWithContext, error)

	// --- Query ---

	// QueryMemories performs a graph traversal based on the filter and returns ranked results.
	QueryMemories(ctx context.Context, filter QueryFilter) ([]model.MemoryWithContext, error)

	// GetSubgraph returns nodes and edges within a given depth from a center node.
	GetSubgraph(ctx context.Context, centerID uuid.UUID, depth int32) ([]model.Memory, []model.Edge, error)

	// --- Stats ---

	NodeCount(ctx context.Context) (int64, error)
	EdgeCount(ctx context.Context) (int64, error)
}
