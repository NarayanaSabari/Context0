import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import type { MemoryNodeData } from '../lib/graph-utils'
import { Brain, Clock, Wrench } from 'lucide-react'

const ICONS: Record<string, typeof Brain> = {
  semantic: Brain,
  episodic: Clock,
  procedural: Wrench,
}

function MemoryNode({ data }: NodeProps) {
  const d = data as unknown as MemoryNodeData
  const Icon = ICONS[d.memType] ?? Brain

  return (
    <>
      <Handle type="target" position={Position.Top} className="!bg-transparent !border-0" />
      <div className="flex items-start gap-2">
        <Icon size={14} className="mt-0.5 shrink-0 opacity-70" />
        <div className="text-[12px] leading-[1.4] break-words">{d.label}</div>
      </div>
      {d.memory.tags?.length > 0 && (
        <div className="flex gap-1 mt-1.5 flex-wrap">
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
      <Handle type="source" position={Position.Bottom} className="!bg-transparent !border-0" />
    </>
  )
}

export default memo(MemoryNode)
