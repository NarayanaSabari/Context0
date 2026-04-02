/**
 * @file MemoryNode.tsx
 * Custom React Flow node component for rendering a memory in the graph.
 * Displays an icon (by memory type), a truncated content label, and up to
 * three tags. Memoized to avoid unnecessary re-renders during graph interactions.
 */

import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import type { MemoryNodeData } from '../lib/graph-utils'
import { Brain, Clock, Wrench } from 'lucide-react'

/** Maps each memory type to its corresponding icon component. */
const ICONS: Record<string, typeof Brain> = {
  semantic: Brain,
  episodic: Clock,
  procedural: Wrench,
}

/**
 * Renders a single memory as a React Flow graph node.
 *
 * Displays a type-specific icon alongside the memory's truncated content.
 * Shows up to 3 tags below the content. Includes invisible connection
 * handles at the top (target) and bottom (source) for edge routing.
 *
 * @param props - Standard React Flow {@link NodeProps}; `data` is cast to {@link MemoryNodeData}.
 */
function MemoryNode({ data }: NodeProps) {
  const d = data as unknown as MemoryNodeData
  const Icon = ICONS[d.memType] ?? Brain

  return (
    <>
      {/* Invisible target handle for incoming edges */}
      <Handle type="target" position={Position.Top} className="!bg-transparent !border-0" />
      <div className="flex items-start gap-2">
        <Icon size={14} className="mt-0.5 shrink-0 opacity-70" />
        <div className="text-[12px] leading-[1.4] break-words">{d.label}</div>
      </div>
      {d.memory.tags?.length > 0 && (
        <div className="flex gap-1 mt-1.5 flex-wrap">
          {/* Show at most 3 tags to keep the node compact */}
          {d.memory.tags.slice(0, 3).map((tag) => (
            <span
              key={tag}
              className="text-[9px] px-1.5 py-0.5 rounded bg-white/5 text-white/40"
            >
              {tag}
            </span>
          ))}
        </div>
      )}
      {/* Invisible source handle for outgoing edges */}
      <Handle type="source" position={Position.Bottom} className="!bg-transparent !border-0" />
    </>
  )
}

export default memo(MemoryNode)
