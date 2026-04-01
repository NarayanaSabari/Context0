import type { Node, Edge as FlowEdge } from '@xyflow/react'
import type { Memory, Edge, MemoryResult } from './types'
import { parseType, parseRel } from './types'

export interface MemoryNodeData {
  memory: Memory
  memType: string
  label: string
  [key: string]: unknown
}

const TYPE_COLORS: Record<string, { bg: string; border: string; text: string }> = {
  semantic:   { bg: '#162316', border: '#9ece6a', text: '#9ece6a' },
  episodic:   { bg: '#261e10', border: '#e0af68', text: '#e0af68' },
  procedural: { bg: '#1e1528', border: '#bb9af7', text: '#bb9af7' },
}

const REL_COLORS: Record<string, string> = {
  relates_to: '#7aa2f7',
  supersedes: '#f7768e',
  caused_by:  '#e0af68',
  contains:   '#9ece6a',
}

export function memoriesToNodes(
  memories: Memory[],
  centerId?: string,
): Node<MemoryNodeData>[] {
  const count = memories.length
  const radius = Math.max(250, count * 40)

  return memories.map((m, i) => {
    const t = parseType(m.type)
    const isCenter = m.id === centerId
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
