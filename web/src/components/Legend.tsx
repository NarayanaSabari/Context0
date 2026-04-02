/**
 * @file Legend.tsx
 * Graph legend component displayed in the bottom-left corner of the graph view.
 * Shows color-coded icons for each memory node type and line styles for each
 * relationship edge type, helping users interpret the visual graph encoding.
 */

import { Brain, Clock, Wrench } from 'lucide-react'

/** Legend entries for memory node types, each with an icon, label, and color. */
const NODES = [
  { icon: Brain, label: 'Semantic (facts)', color: '#9ece6a' },
  { icon: Clock, label: 'Episodic (events)', color: '#e0af68' },
  { icon: Wrench, label: 'Procedural (how-to)', color: '#bb9af7' },
]

/** Legend entries for edge relationship types, each with a label, color, and line style. */
const EDGES = [
  { label: 'relates to', color: '#7aa2f7', dashed: false },
  { label: 'supersedes', color: '#f7768e', dashed: true },
  { label: 'caused by', color: '#e0af68', dashed: false },
]

/**
 * Renders a compact legend overlay in the bottom-left of the graph canvas.
 * Displays node type icons with labels and edge relationship line samples,
 * helping users understand the graph's visual encoding.
 */
export default function Legend() {
  return (
    <div className="absolute bottom-4 left-4 bg-[#12131a] border border-[#262836] rounded-xl p-4 z-10 text-[12px]">
      <div className="text-[10px] uppercase tracking-widest text-[#4b4f64] font-semibold mb-2">Nodes</div>
      <div className="space-y-1.5">
        {NODES.map(({ icon: Icon, label, color }) => (
          <div key={label} className="flex items-center gap-2">
            <Icon size={12} style={{ color }} />
            <span className="text-[#9b9eb5]">{label}</span>
          </div>
        ))}
      </div>
      <div className="text-[10px] uppercase tracking-widest text-[#4b4f64] font-semibold mt-3 mb-2">Edges</div>
      <div className="space-y-1.5">
        {EDGES.map(({ label, color, dashed }) => (
          <div key={label} className="flex items-center gap-2">
            <svg width="18" height="4">
              <line
                x1="0" y1="2" x2="18" y2="2"
                stroke={color} strokeWidth="2"
                strokeDasharray={dashed ? '3 2' : 'none'}
              />
            </svg>
            <span className="text-[#9b9eb5]">{label}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
