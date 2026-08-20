/**
 * @file types.ts
 * Shared TypeScript interfaces and type definitions for the Kora Memory Graph.
 * Defines the core domain models (Memory, Edge), API response shapes, and
 * helper functions for parsing protobuf-style enum strings into display-friendly values.
 */

/** A single memory node stored in the knowledge graph. */
export interface Memory {
  /** Unique identifier for the memory. */
  id: string
  /** The textual content of the memory. */
  content: string
  /** Raw memory type string from the API (e.g. "MEMORY_TYPE_SEMANTIC"). */
  type: string
  /** The project this memory belongs to. */
  projectId: string
  /** User-defined tags for categorization and search. */
  tags: string[]
  /** ISO 8601 timestamp of when the memory was created. */
  createdAt: string
  /** Number of times this memory has been accessed/retrieved. */
  accessCount: number
  /** Decay score representing memory freshness (1.0 = fresh, lower = stale). */
  decayScore: number
}

/** An edge returned as part of a memory's context in query results. */
export interface ContextEdge {
  /** The type of relationship (e.g. "RELATIONSHIP_TYPE_RELATES_TO"). */
  relationship: string
  /** ID of the related memory node. */
  targetId: string
  /** Content preview of the related memory. */
  targetContent: string
  /** Strength of the relationship (0.0 to 1.0). */
  weight: number
}

/** A single result from a memory query, pairing a memory with its context and relevance score. */
export interface MemoryResult {
  /** The memory node. */
  memory: Memory
  /** Related edges providing context for this memory. */
  context: ContextEdge[]
  /** Relevance score from the query (higher = more relevant). */
  score: number
}

/** A directed edge in the knowledge graph connecting two memory nodes. */
export interface Edge {
  /** Unique identifier for the edge. */
  id: string
  /** ID of the source memory node. */
  fromId: string
  /** ID of the target memory node. */
  toId: string
  /** Raw relationship type string from the API. */
  relationship: string
  /** Strength of the relationship (0.0 to 1.0). */
  weight: number
  /** ISO 8601 timestamp of when the edge was created. */
  createdAt: string
}

/** Response shape for the subgraph API endpoint (`/v1/memories/:id/graph`). */
export interface SubgraphResponse {
  /** Neighboring memory nodes within the requested depth. */
  nodes: Memory[]
  /** Edges connecting the nodes in the subgraph. */
  edges: Edge[]
}

/** Response shape for the health check API endpoint (`/v1/health`). */
export interface HealthResponse {
  /** Service status (e.g. "ok"). */
  status: string
  /** API version string. */
  version: string
  /** Total number of memory nodes in the graph. */
  nodeCount: number
  /** Total number of edges in the graph. */
  edgeCount: number
}

/** Display-friendly memory type used in the UI. */
export type MemoryType = 'semantic' | 'episodic' | 'procedural'

/**
 * Converts a raw protobuf-style memory type string to a display-friendly {@link MemoryType}.
 * Falls back to `'semantic'` for unrecognized values.
 *
 * @param raw - The raw type string from the API (e.g. "MEMORY_TYPE_SEMANTIC").
 * @returns The parsed {@link MemoryType}.
 */
export function parseType(raw: string): MemoryType {
  const map: Record<string, MemoryType> = {
    MEMORY_TYPE_SEMANTIC: 'semantic',
    MEMORY_TYPE_EPISODIC: 'episodic',
    MEMORY_TYPE_PROCEDURAL: 'procedural',
  }
  return map[raw] ?? 'semantic'
}

/**
 * Converts a raw protobuf-style relationship type string to a human-readable label.
 * Returns the raw value unchanged if no mapping is found.
 *
 * @param raw - The raw relationship string from the API (e.g. "RELATIONSHIP_TYPE_RELATES_TO").
 * @returns A display-friendly relationship label (e.g. "relates_to").
 */
export function parseRel(raw: string): string {
  const map: Record<string, string> = {
    RELATIONSHIP_TYPE_RELATES_TO: 'relates_to',
    RELATIONSHIP_TYPE_SUPERSEDES: 'supersedes',
    RELATIONSHIP_TYPE_CAUSED_BY: 'caused_by',
  }
  return map[raw] ?? raw
}
