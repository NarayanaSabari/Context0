/**
 * @file graph-utils.ts
 * Utility functions for transforming Context0 memory data into React Flow
 * graph elements (nodes and edges). Handles layout positioning, color theming
 * by memory type, and edge styling by relationship type.
 */

import type { Node, Edge as FlowEdge } from '@xyflow/react'
import type { Memory, Edge, MemoryResult } from './types'
import { parseType, parseRel } from './types'

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

/** Background, border, and text colors for each memory type. */
const TYPE_COLORS: Record<string, { bg: string; border: string; text: string }> = {
  semantic:   { bg: '#162316', border: '#9ece6a', text: '#9ece6a' },
  episodic:   { bg: '#261e10', border: '#e0af68', text: '#e0af68' },
  procedural: { bg: '#1e1528', border: '#bb9af7', text: '#bb9af7' },
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
        background: TYPE_COLORS[t]?.bg ?? '#1a1b26',
        borderColor: TYPE_COLORS[t]?.border ?? '#555',
        borderWidth: isCenter ? 3 : 2,
        borderStyle: 'solid' as const,
        color: TYPE_COLORS[t]?.text ?? '#e1e2e8',
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
 * Converts Context0 graph edges into React Flow edge objects with
 * relationship-based styling (color, width, animation).
 *
 * @param edges - The raw edges from the API.
 * @returns An array of styled React Flow {@link FlowEdge} objects.
 */
export function edgesToFlowEdges(edges: Edge[]): FlowEdge[] {
  return edges.map((e) => {
    const rel = parseRel(e.relationship)
    return {
      id: e.id,
      source: e.fromId,
      target: e.toId,
      label: rel.replace('_', ' '),
      type: 'smoothstep',
      animated: rel === 'supersedes',
      style: {
        stroke: REL_COLORS[rel] ?? '#444',
        strokeWidth: Math.max(1.5, (e.weight ?? 1) * 2),
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
  })
}

/**
 * Transforms all memory query results into a full graph for overview mode.
 * Lays out nodes in a grid and creates edges between memories that share tags.
 *
 * @param results - All memory query results to display.
 * @returns An object containing the positioned nodes and tag-based edges.
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

  // Connect memories that share tags
  const edges: FlowEdge[] = []
  for (let i = 0; i < memories.length; i++) {
    for (let j = i + 1; j < memories.length; j++) {
      const shared = (memories[i].tags ?? []).filter((t) =>
        (memories[j].tags ?? []).includes(t),
      )
      if (shared.length > 0) {
        edges.push({
          id: `tag-${memories[i].id.slice(0, 8)}-${memories[j].id.slice(0, 8)}`,
          source: memories[i].id,
          target: memories[j].id,
          label: shared[0],
          type: 'smoothstep',
          style: { stroke: '#7aa2f7', strokeWidth: 1.5, opacity: 0.5 },
          labelStyle: { fill: '#7aa2f7', fontSize: 9 },
          labelBgStyle: { fill: '#0a0a0f', fillOpacity: 0.9 },
        })
      }
    }
  }

  return { nodes, edges }
}
