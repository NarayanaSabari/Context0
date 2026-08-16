/**
 * @file memory-types.ts
 * Single source of truth for per-memory-type display metadata (icon, colors,
 * labels) used across the graph, sidebar, detail panel, and legend.
 */

import { Brain, Clock, Wrench, type LucideIcon } from 'lucide-react'
import type { MemoryType } from './types'

/** Display metadata for a single memory type, covering every UI surface that renders it. */
export interface MemoryTypeMeta {
  /** Lucide icon component representing this type. */
  icon: LucideIcon
  /** Human-readable label used in filter pills and the legend context. */
  label: string
  /** Longer description shown in the detail panel subtitle. */
  description: string
  /** Hex colors for graph node background/border. */
  graph: { bg: string; border: string }
  /** Tailwind classes for the sidebar list badge. */
  badgeClassName: string
  /** Tailwind text color class used in the detail panel header. */
  detailColorClassName: string
}

/** Per-memory-type display metadata, keyed by {@link MemoryType}. */
export const MEMORY_TYPES: Record<MemoryType, MemoryTypeMeta> = {
  semantic: {
    icon: Brain,
    label: 'Semantic (facts)',
    description: 'Semantic — a known fact',
    graph: { bg: '#162316', border: '#9ece6a' },
    badgeClassName: 'bg-green-500/10 text-green-400 border-green-500/20',
    detailColorClassName: 'text-green-400',
  },
  episodic: {
    icon: Clock,
    label: 'Episodic (events)',
    description: 'Episodic — something that happened',
    graph: { bg: '#261e10', border: '#e0af68' },
    badgeClassName: 'bg-amber-500/10 text-amber-400 border-amber-500/20',
    detailColorClassName: 'text-amber-400',
  },
  procedural: {
    icon: Wrench,
    label: 'Procedural (how-to)',
    description: 'Procedural — a learned pattern',
    graph: { bg: '#1e1528', border: '#bb9af7' },
    badgeClassName: 'bg-purple-500/10 text-purple-400 border-purple-500/20',
    detailColorClassName: 'text-purple-400',
  },
}
