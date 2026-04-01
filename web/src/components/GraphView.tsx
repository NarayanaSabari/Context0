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

const nodeTypes: NodeTypes = { memory: MemoryNode }

interface Props {
  nodes: Node[]
  edges: Edge[]
  selectedMemory: Memory | null
  onNodeClick: (id: string) => void
  onClearSelection: () => void
}

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

  const handleNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      onNodeClick(node.id)
    },
    [onNodeClick],
  )

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
