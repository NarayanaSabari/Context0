/**
 * @file graph-utils.ts
 * Utility functions for transforming Kora memory data into React Flow
 * graph elements (nodes and edges). Handles layout positioning, color theming
 * by memory type, and edge styling by relationship type.
 */

import type { Node, Edge as FlowEdge } from '@xyflow/react'
import type { Memory, Edge, MemoryResult } from './types'
import { parseType, parseRel } from './types'
import { MEMORY_TYPES } from './memory-types'

/** Data payload attached to each React Flow memory node. */
export interface MemoryNodeData {
  /** The underlying memory object. */
  memory: Memory
  /** Display-friendly memory type (semantic, episodic, procedural). */
  memType: string
  /** Truncated content string used as the node label. */
  label: string
  /** Index signature required by React Flow's generic node data type. */
  [key: string]: unknown
}

/** Stroke colors for each relationship type on graph edges. */
const REL_COLORS: Record<string, string> = {
  relates_to: '#7aa2f7',
  supersedes: '#f7768e',
  caused_by:  '#e0af68',
  contains:   '#9ece6a',
}

/**
 * Converts an array of memories into React Flow node objects.
 * Positions nodes in a radial layout around the center node.
 *
 * @param memories - The memory objects to convert into nodes.
 * @param centerId - Optional ID of the central/focused memory (placed at origin with thicker border).
 * @returns An array of React Flow {@link Node} objects with {@link MemoryNodeData}.
 */
export function memoriesToNodes(
  memories: Memory[],
  centerId?: string,
): Node<MemoryNodeData>[] {
  const count = memories.length
  const radius = Math.max(250, count * 40)

  return memories.map((m, i) => {
    const t = parseType(m.type)
    const isCenter = m.id === centerId
    // Distribute non-center nodes evenly around a circle
    const angle = (2 * Math.PI * i) / count

    return {
      id: m.id,
      type: 'memory',
      position: isCenter
        ? { x: 0, y: 0 }
        : { x: Math.cos(angle) * radius, y: Math.sin(angle) * radius },
      data: {
        memory: m,
        memType: t,
        label: m.content.length > 50 ? m.content.slice(0, 50) + '...' : m.content,
      },
      style: {
        background: MEMORY_TYPES[t].graph.bg,
        borderColor: MEMORY_TYPES[t].graph.border,
        borderWidth: isCenter ? 3 : 2,
        borderStyle: 'solid' as const,
        color: MEMORY_TYPES[t].graph.border,
        borderRadius: 12,
        padding: 12,
        fontSize: 13,
        width: isCenter ? 220 : 200,
        maxWidth: 220,
      },
    }
  })
}

/**
 * Builds a styled React Flow edge for a relationship type, shared by the
 * focus-mode subgraph path and the overview path so styling stays consistent.
 */
function relEdge(id: string, source: string, target: string, rel: string, weight: number): FlowEdge {
  return {
    id,
    source,
    target,
    label: rel.replace('_', ' '),
    type: 'smoothstep',
    animated: rel === 'supersedes',
    style: {
      stroke: REL_COLORS[rel] ?? '#444',
      strokeWidth: Math.max(1.5, (weight ?? 1) * 2),
    },
    labelStyle: {
      fill: REL_COLORS[rel] ?? '#888',
      fontSize: 10,
      fontWeight: 600,
    },
    labelBgStyle: {
      fill: '#0a0a0f',
      fillOpacity: 0.9,
    },
  }
}

/**
 * Converts Kora graph edges into React Flow edge objects with
 * relationship-based styling (color, width, animation).
 *
 * @param edges - The raw edges from the API.
 * @returns An array of styled React Flow {@link FlowEdge} objects.
 */
export function edgesToFlowEdges(edges: Edge[]): FlowEdge[] {
  return edges.map((e) => relEdge(e.id, e.fromId, e.toId, parseRel(e.relationship), e.weight))
}

/**
 * Transforms all memory query results into a full graph for overview mode.
 * Lays out nodes in a grid and creates edges from each result's real context edges.
 *
 * @param results - All memory query results to display.
 * @returns An object containing the positioned nodes and their real relationship edges.
 */
export function resultsToFullGraph(
  results: MemoryResult[],
): { nodes: Node<MemoryNodeData>[]; edges: FlowEdge[] } {
  const memories = results.map((r) => r.memory)
  const nodes = memoriesToNodes(memories)

  // Layout in a grid instead of circle for "all" view
  const cols = Math.ceil(Math.sqrt(nodes.length))
  nodes.forEach((n, i) => {
    n.position = {
      x: (i % cols) * 280,
      y: Math.floor(i / cols) * 160,
    }
  })

  // Connect memories using their real context edges, deduplicating since the
  // backend reports an undirected A-B relationship from both A's and B's context.
  const displayedIds = new Set(memories.map((m) => m.id))
  const seen = new Set<string>()
  const edges: FlowEdge[] = []
  for (const r of results) {
    for (const ctx of r.context ?? []) {
      if (!displayedIds.has(ctx.targetId)) continue
      const rel = parseRel(ctx.relationship)
      const key = [r.memory.id, ctx.targetId].sort().join('|') + '|' + rel
      if (seen.has(key)) continue
      seen.add(key)
      edges.push(relEdge(key, r.memory.id, ctx.targetId, rel, ctx.weight))
    }
  }

  return { nodes, edges }
}
