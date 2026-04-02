/**
 * @file GraphView.tsx
 * Main graph visualization component built on React Flow. Renders an interactive,
 * pannable/zoomable canvas of memory nodes and relationship edges. Includes
 * the graph legend and a detail panel overlay for the selected memory.
 */

import { useCallback, useEffect } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  useNodesState,
  useEdgesState,
  type Node,
  type Edge,
  type NodeTypes,
} from '@xyflow/react'
import MemoryNode from './MemoryNode'
import Legend from './Legend'
import DetailPanel from './DetailPanel'
import type { Memory } from '../lib/types'

/** Registers the custom MemoryNode component as the "memory" node type for React Flow. */
const nodeTypes: NodeTypes = { memory: MemoryNode }

/** Props for the {@link GraphView} component. */
interface Props {
  /** Initial set of graph nodes to render. */
  nodes: Node[]
  /** Initial set of graph edges to render. */
  edges: Edge[]
  /** The currently selected memory for the detail panel, or null. */
  selectedMemory: Memory | null
  /** Callback invoked when a graph node is clicked (receives the node ID). */
  onNodeClick: (id: string) => void
  /** Callback to clear the current memory selection (e.g. on pane click). */
  onClearSelection: () => void
}

/**
 * Interactive graph visualization of memory nodes and their relationships.
 * Wraps React Flow with custom node types, dark-themed controls, a legend,
 * and the {@link DetailPanel} overlay. Syncs internal React Flow state with
 * incoming prop changes via a `useEffect` hook.
 *
 * @param props - See {@link Props}.
 */
export default function GraphView({
  nodes: initNodes,
  edges: initEdges,
  selectedMemory,
  onNodeClick,
  onClearSelection,
}: Props) {
  const [nodes, setNodes, onNodesChange] = useNodesState(initNodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(initEdges)

  // Sync nodes/edges when props change
  useEffect(() => {
    setNodes(initNodes)
    setEdges(initEdges)
  }, [initNodes, initEdges, setNodes, setEdges])

  /** Forwards React Flow node click events to the parent via the node's ID. */
  const handleNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      onNodeClick(node.id)
    },
    [onNodeClick],
  )

  /** Clears the selection when the user clicks on empty canvas space. */
  const handlePaneClick = useCallback(() => {
    onClearSelection()
  }, [onClearSelection])

  return (
    <div className="flex-1 relative">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={handleNodeClick}
        onPaneClick={handlePaneClick}
        fitView
        fitViewOptions={{ padding: 0.3 }}
        minZoom={0.1}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
        className="bg-[#0a0a0f]"
      >
        <Background color="#1a1b26" gap={40} size={1} />
        <Controls
          position="top-right"
          className="!bg-[#12131a] !border-[#262836] !rounded-xl !shadow-xl [&>button]:!bg-[#12131a] [&>button]:!border-[#262836] [&>button]:!text-[#6b6e82] [&>button:hover]:!bg-[#1a1b26]"
        />
      </ReactFlow>
      <Legend />
      <DetailPanel memory={selectedMemory} onClose={onClearSelection} />
    </div>
  )
}
