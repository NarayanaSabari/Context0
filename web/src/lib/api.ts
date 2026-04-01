import type { HealthResponse, MemoryResult, SubgraphResponse } from './types'

let apiKey = new URLSearchParams(window.location.search).get('key') ?? ''

export function setApiKey(key: string) {
  apiKey = key
}

export function getApiKey() {
  return apiKey
}

function headers(): HeadersInit {
  const h: Record<string, string> = {}
  if (apiKey) h['X-API-Key'] = apiKey
  return h
}

export async function fetchHealth(): Promise<HealthResponse> {
  const r = await fetch('/v1/health', { headers: headers() })
  return r.json()
}

export async function fetchMemories(topK = 200): Promise<MemoryResult[]> {
  const r = await fetch(`/v1/memories/query?query=&top_k=${topK}`, { headers: headers() })
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`)
  const d = await r.json()
  return d.results ?? []
}

export async function fetchSubgraph(id: string, depth = 2): Promise<SubgraphResponse> {
  const r = await fetch(`/v1/memories/${id}/graph?depth=${depth}`, { headers: headers() })
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`)
  return r.json()
}
