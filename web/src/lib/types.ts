export interface Memory {
  id: string
  content: string
  type: string
  projectId: string
  tags: string[]
  createdAt: string
  accessCount: number
  decayScore: number
}

export interface ContextEdge {
  relationship: string
  targetId: string
  targetContent: string
  weight: number
}

export interface MemoryResult {
  memory: Memory
  context: ContextEdge[]
  score: number
}

export interface Edge {
  id: string
  fromId: string
  toId: string
  relationship: string
  weight: number
  createdAt: string
}

export interface SubgraphResponse {
  nodes: Memory[]
  edges: Edge[]
}

export interface HealthResponse {
  status: string
  version: string
  nodeCount: number
  edgeCount: number
}

export type MemoryType = 'semantic' | 'episodic' | 'procedural'

export function parseType(raw: string): MemoryType {
  const map: Record<string, MemoryType> = {
    MEMORY_TYPE_SEMANTIC: 'semantic',
    MEMORY_TYPE_EPISODIC: 'episodic',
    MEMORY_TYPE_PROCEDURAL: 'procedural',
  }
  return map[raw] ?? 'semantic'
}

export function parseRel(raw: string): string {
  const map: Record<string, string> = {
    RELATIONSHIP_TYPE_RELATES_TO: 'relates_to',
    RELATIONSHIP_TYPE_SUPERSEDES: 'supersedes',
    RELATIONSHIP_TYPE_CAUSED_BY: 'caused_by',
  }
  return map[raw] ?? raw
}
