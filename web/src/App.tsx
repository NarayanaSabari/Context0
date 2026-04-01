import { useState, useCallback, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ReactFlowProvider } from '@xyflow/react'
import { Network, RefreshCw, LayoutGrid } from 'lucide-react'
import Sidebar from './components/Sidebar'
import GraphView from './components/GraphView'
import { fetchHealth, fetchMemories, fetchSubgraph } from './lib/api'
import { memoriesToNodes, edgesToFlowEdges, resultsToFullGraph } from './lib/graph-utils'
import type { Memory, SubgraphResponse } from './lib/types'
import type { Node, Edge } from '@xyflow/react'

type ViewMode = 'focus' | 'all'

export default function App() {
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [selectedMemory, setSelectedMemory] = useState<Memory | null>(null)
  const [graphNodes, setGraphNodes] = useState<Node[]>([])
  const [graphEdges, setGraphEdges] = useState<Edge[]>([])
  const [viewMode, setViewMode] = useState<ViewMode>('focus')
  const [refreshKey, setRefreshKey] = useState(0)

  const health = useQuery({
    queryKey: ['health', refreshKey],
    queryFn: fetchHealth,
  })

  const memories = useQuery({
    queryKey: ['memories', refreshKey],
    queryFn: () => fetchMemories(200),
  })

  const results = useMemo(() => memories.data ?? [], [memories.data])

  const handleSelect = useCallback(
    async (id: string) => {
      setSelectedId(id)
      setViewMode('focus')

      const mem = results.find((r) => r.memory.id === id)?.memory ?? null
      setSelectedMemory(mem)

      try {
        const sub: SubgraphResponse = await fetchSubgraph(id)
        const allMems = [
          ...(mem ? [mem] : []),
          ...(sub.nodes ?? []).filter((n) => n.id !== id),
        ]
        const nodes = memoriesToNodes(allMems, id)
        const edges = edgesToFlowEdges(sub.edges ?? [])

        // If no edges from API, create implicit edges from neighbors to center
        if (edges.length === 0 && sub.nodes?.length) {
          for (const n of sub.nodes) {
            if (n.id === id) continue
            edges.push({
              id: `imp-${id.slice(0, 8)}-${n.id.slice(0, 8)}`,
              source: id,
              target: n.id,
              type: 'smoothstep',
              animated: false,
              style: { stroke: '#7aa2f7', strokeWidth: 1.5, opacity: 0.4 },
              label: 'neighbor',
              labelStyle: { fill: '#7aa2f7', fontSize: 9 },
              labelBgStyle: { fill: '#0a0a0f', fillOpacity: 0.9 },
            })
          }
        }

        setGraphNodes(nodes)
        setGraphEdges(edges)
      } catch (e) {
        console.error('Failed to load subgraph:', e)
      }
    },
    [results],
  )

  const handleShowAll = useCallback(() => {
    setViewMode('all')
    setSelectedId(null)
    setSelectedMemory(null)
    const { nodes, edges } = resultsToFullGraph(results)
    setGraphNodes(nodes)
    setGraphEdges(edges)
  }, [results])

  const handleClear = useCallback(() => {
    setSelectedMemory(null)
  }, [])

  const handleRefresh = useCallback(() => {
    setRefreshKey((k) => k + 1)
  }, [])

  const handleNodeClick = useCallback(
    (id: string) => {
      const mem = results.find((r) => r.memory.id === id)?.memory ?? null
      setSelectedMemory(mem)
      setSelectedId(id)
    },
    [results],
  )

  return (
    <div className="h-screen flex flex-col overflow-hidden">
      {/* Header */}
      <header className="bg-[#12131a] border-b border-[#262836] px-6 py-3 flex items-center justify-between shrink-0">
        <div className="flex items-center gap-3">
          <Network size={20} className="text-[#7aa2f7]" />
          <h1 className="text-[16px] font-bold tracking-tight">
            <span className="text-[#7aa2f7]">Context0</span> Memory Graph
          </h1>
        </div>

        <div className="flex items-center gap-5 text-[12px] text-[#6b6e82]">
          <div>Nodes <span className="text-[#e1e2e8] font-semibold ml-1">{health.data?.nodeCount ?? '-'}</span></div>
          <div>Edges <span className="text-[#e1e2e8] font-semibold ml-1">{health.data?.edgeCount ?? '-'}</span></div>
          <div>v{health.data?.version ?? '-'}</div>

          <div className="flex gap-1.5 ml-2">
            <button
              onClick={handleShowAll}
              className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[11px] font-semibold border transition-all ${
                viewMode === 'all'
                  ? 'bg-[#7aa2f7]/15 border-[#7aa2f7]/30 text-[#7aa2f7]'
                  : 'border-[#262836] text-[#6b6e82] hover:border-[#4b4f64] hover:text-[#9b9eb5]'
              }`}
            >
              <LayoutGrid size={12} /> Overview
            </button>
            <button
              onClick={handleRefresh}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-[11px] font-semibold border border-[#262836] text-[#6b6e82] hover:border-[#4b4f64] hover:text-[#9b9eb5] transition-all"
            >
              <RefreshCw size={12} className={memories.isFetching ? 'animate-spin' : ''} /> Refresh
            </button>
          </div>
        </div>
      </header>

      {/* Main */}
      <div className="flex flex-1 overflow-hidden">
        <Sidebar
          results={results}
          selectedId={selectedId}
          onSelect={handleSelect}
          onRefresh={handleRefresh}
        />

        <ReactFlowProvider>
          {graphNodes.length > 0 ? (
            <GraphView
              nodes={graphNodes}
              edges={graphEdges}
              selectedMemory={selectedMemory}
              onNodeClick={handleNodeClick}
              onClearSelection={handleClear}
            />
          ) : (
            <div className="flex-1 flex flex-col items-center justify-center text-center bg-[#0a0a0f]">
              <Network size={56} className="text-[#262836] mb-4" />
              <div className="text-lg font-semibold text-[#4b4f64]">Select a memory to explore</div>
              <div className="text-sm text-[#3b3e52] mt-1.5 max-w-md">
                Click any memory in the sidebar to see its neighborhood graph, or click
                <span className="text-[#7aa2f7] font-medium"> Overview </span>
                to see all memories at once.
              </div>
            </div>
          )}
        </ReactFlowProvider>
      </div>
    </div>
  )
}
