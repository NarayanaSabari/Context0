/**
 * @file Sidebar.tsx
 * Left sidebar component that provides API key entry, text search, memory type
 * filtering, and a scrollable list of memory cards. Clicking a memory card
 * triggers the parent to load and display its subgraph.
 */

import { useState } from 'react'
import { Search, Key } from 'lucide-react'
import type { MemoryResult, MemoryType } from '../lib/types'
import { parseType } from '../lib/types'
import { setApiKey, getApiKey } from '../lib/api'
import { MEMORY_TYPES } from '../lib/memory-types'

/** Available filter options for the memory type pill buttons. */
const FILTERS: { label: string; value: MemoryType | 'all' }[] = [
  { label: 'All', value: 'all' },
  { label: 'Semantic', value: 'semantic' },
  { label: 'Episodic', value: 'episodic' },
  { label: 'Procedural', value: 'procedural' },
]

/** Props for the {@link Sidebar} component. */
interface Props {
  /** All memory query results to display in the list. */
  results: MemoryResult[]
  /** ID of the currently selected memory (highlighted in the list), or null. */
  selectedId: string | null
  /** Callback invoked when the user clicks a memory card. */
  onSelect: (id: string) => void
  /** Callback to trigger a data refresh (e.g. after changing the API key). */
  onRefresh: () => void
}

/**
 * Sidebar panel with API key input, search/filter controls, and a scrollable
 * memory list. Filters memories by type and text search across content and tags.
 *
 * @param props - See {@link Props}.
 */
export default function Sidebar({ results, selectedId, onSelect, onRefresh }: Props) {
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<MemoryType | 'all'>('all')
  const [key, setKey] = useState(getApiKey())

  // Apply type filter and case-insensitive text search across content and tags
  const filtered = results.filter((r) => {
    const t = parseType(r.memory.type)
    if (filter !== 'all' && t !== filter) return false
    if (search) {
      const q = search.toLowerCase()
      return (
        r.memory.content.toLowerCase().includes(q) ||
        (r.memory.tags ?? []).some((tag) => tag.toLowerCase().includes(q))
      )
    }
    return true
  })

  return (
    <aside className="w-[380px] shrink-0 bg-[#12131a] border-r border-[#262836] flex flex-col h-full">
      {/* API Key */}
      <div className="p-4 border-b border-[#262836]">
        <label className="text-[10px] uppercase tracking-widest text-[#6b6e82] font-semibold flex items-center gap-1 mb-2">
          <Key size={10} /> API Key
        </label>
        <input
          type="password"
          value={key}
          placeholder="Paste your API key..."
          className="w-full px-3 py-2 bg-[#0a0a0f] border border-[#262836] rounded-lg text-sm text-[#e1e2e8] font-mono placeholder:text-[#3b3e52] focus:border-[#7aa2f7] focus:outline-none"
          onChange={(e) => setKey(e.target.value)}
          onBlur={() => { setApiKey(key); onRefresh() }}
          onKeyDown={(e) => { if (e.key === 'Enter') { setApiKey(key); onRefresh() } }}
        />
      </div>

      {/* Search + Filters */}
      <div className="p-4 border-b border-[#262836]">
        <div className="relative">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-[#6b6e82]" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search memories..."
            className="w-full pl-9 pr-3 py-2 bg-[#0a0a0f] border border-[#262836] rounded-lg text-sm text-[#e1e2e8] placeholder:text-[#3b3e52] focus:border-[#7aa2f7] focus:outline-none"
          />
        </div>
        <div className="flex gap-1.5 mt-3">
          {FILTERS.map((f) => (
            <button
              key={f.value}
              onClick={() => setFilter(f.value)}
              className={`px-3 py-1 rounded-full text-[11px] font-semibold border transition-all ${
                filter === f.value
                  ? f.value === 'all'
                    ? 'bg-[#7aa2f7] border-[#7aa2f7] text-white'
                    : f.value === 'semantic'
                    ? 'bg-green-500/20 border-green-500/30 text-green-400'
                    : f.value === 'episodic'
                    ? 'bg-amber-500/20 border-amber-500/30 text-amber-400'
                    : 'bg-purple-500/20 border-purple-500/30 text-purple-400'
                  : 'border-[#262836] text-[#6b6e82] hover:border-[#4b4f64]'
              }`}
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>

      {/* Memory List */}
      <div className="flex-1 overflow-y-auto p-2">
        {filtered.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-center px-6">
            <div className="text-4xl opacity-20 mb-3">&#128065;</div>
            <div className="text-sm font-semibold text-[#6b6e82]">No memories found</div>
            <div className="text-xs text-[#4b4f64] mt-1">
              {results.length === 0 ? 'Enter API key and refresh' : 'Try a different search'}
            </div>
          </div>
        ) : (
          filtered.map((r) => {
            const t = parseType(r.memory.type)
            const badge = MEMORY_TYPES[t] ?? MEMORY_TYPES.semantic
            const Icon = badge.icon
            const date = new Date(r.memory.createdAt).toLocaleDateString()
            const isSelected = r.memory.id === selectedId

            return (
              <div
                key={r.memory.id}
                onClick={() => onSelect(r.memory.id)}
                className={`p-3 rounded-xl cursor-pointer mb-1 border transition-all ${
                  isSelected
                    ? 'bg-[#7aa2f7]/8 border-[#7aa2f7]/30'
                    : 'border-transparent hover:bg-[#1a1b26] hover:border-[#262836]'
                }`}
              >
                <div className="flex items-center gap-2">
                  <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[10px] font-bold uppercase tracking-wide border ${badge.badgeClassName}`}>
                    <Icon size={10} /> {t}
                  </span>
                  <span className="text-[10px] text-[#4b4f64]">{date}</span>
                </div>
                <div className="text-[13px] leading-[1.45] mt-2 line-clamp-2">
                  {r.memory.content}
                </div>
                {(r.memory.tags?.length ?? 0) > 0 && (
                  <div className="flex gap-1 mt-2 flex-wrap">
                    {r.memory.tags.slice(0, 4).map((tag) => (
                      <span key={tag} className="text-[10px] px-1.5 py-0.5 rounded-md bg-[#7aa2f7]/8 text-[#7aa2f7]/70">
                        {tag}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            )
          })
        )}
      </div>

      {/* Footer */}
      <div className="p-3 border-t border-[#262836] text-[10px] text-[#4b4f64] text-center">
        {filtered.length} of {results.length} memories
      </div>
    </aside>
  )
}
