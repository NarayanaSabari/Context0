import { X, Brain, Clock, Wrench, Hash, Activity, TrendingDown } from 'lucide-react'
import type { Memory } from '../lib/types'
import { parseType } from '../lib/types'

const TYPE_META: Record<string, { icon: typeof Brain; color: string; label: string }> = {
  semantic:   { icon: Brain, color: 'text-green-400', label: 'Semantic — a known fact' },
  episodic:   { icon: Clock, color: 'text-amber-400', label: 'Episodic — something that happened' },
  procedural: { icon: Wrench, color: 'text-purple-400', label: 'Procedural — a learned pattern' },
}

interface Props {
  memory: Memory | null
  onClose: () => void
}

export default function DetailPanel({ memory, onClose }: Props) {
  if (!memory) return null

  const t = parseType(memory.type)
  const meta = TYPE_META[t] ?? TYPE_META.semantic
  const Icon = meta.icon

  return (
    <div className="absolute top-4 right-4 w-[350px] max-h-[calc(100%-32px)] overflow-y-auto bg-[#12131a] border border-[#262836] rounded-2xl shadow-2xl z-10">
      {/* Header */}
      <div className="flex items-start justify-between p-5 pb-0">
        <div className="flex items-center gap-2">
          <Icon size={16} className={meta.color} />
          <span className={`text-xs font-bold uppercase tracking-wide ${meta.color}`}>{t}</span>
        </div>
        <button onClick={onClose} className="text-[#6b6e82] hover:text-white transition-colors">
          <X size={16} />
        </button>
      </div>
      <div className="px-5 mt-1 text-[11px] text-[#4b4f64]">{meta.label}</div>

      {/* Content */}
      <div className="p-5 space-y-4">
        <div>
          <div className="text-[15px] leading-[1.5] font-medium">{memory.content}</div>
        </div>

        {/* Tags */}
        {(memory.tags?.length ?? 0) > 0 && (
          <div className="flex gap-1.5 flex-wrap">
            {memory.tags.map((tag) => (
              <span key={tag} className="inline-flex items-center gap-1 px-2 py-1 rounded-lg bg-[#7aa2f7]/10 text-[#7aa2f7] text-[11px]">
                <Hash size={9} /> {tag}
              </span>
            ))}
          </div>
        )}

        {/* Metadata */}
        <div className="grid grid-cols-2 gap-3">
          <div className="bg-[#0a0a0f] rounded-xl p-3">
            <div className="flex items-center gap-1.5 text-[10px] text-[#6b6e82] uppercase tracking-wide mb-1">
              <Activity size={10} /> Accessed
            </div>
            <div className="text-lg font-semibold">{memory.accessCount ?? 0}<span className="text-xs text-[#4b4f64] ml-1">times</span></div>
          </div>
          <div className="bg-[#0a0a0f] rounded-xl p-3">
            <div className="flex items-center gap-1.5 text-[10px] text-[#6b6e82] uppercase tracking-wide mb-1">
              <TrendingDown size={10} /> Decay
            </div>
            <div className="text-lg font-semibold">{parseFloat(String(memory.decayScore ?? 1)).toFixed(2)}</div>
          </div>
        </div>

        {/* ID + Date */}
        <div className="space-y-2 pt-2 border-t border-[#262836]">
          <div>
            <div className="text-[10px] text-[#4b4f64] uppercase tracking-wide">ID</div>
            <div className="text-[11px] font-mono text-[#6b6e82] mt-0.5 break-all">{memory.id}</div>
          </div>
          <div>
            <div className="text-[10px] text-[#4b4f64] uppercase tracking-wide">Created</div>
            <div className="text-[12px] text-[#6b6e82] mt-0.5">
              {new Date(memory.createdAt).toLocaleString()}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
