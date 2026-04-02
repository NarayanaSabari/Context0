/**
 * @file api.ts
 * HTTP client functions for communicating with the Context0 Memory Graph API.
 * Handles API key management (from URL params or manual entry) and provides
 * typed fetch wrappers for health, memory query, and subgraph endpoints.
 */

import type { HealthResponse, MemoryResult, SubgraphResponse } from './types'

/** API key initialized from the `?key=` URL search parameter, if present. */
let apiKey = new URLSearchParams(window.location.search).get('key') ?? ''

/**
 * Updates the in-memory API key used for subsequent requests.
 * @param key - The new API key value.
 */
export function setApiKey(key: string) {
  apiKey = key
}

/**
 * Returns the current API key.
 * @returns The API key string, or an empty string if not set.
 */
export function getApiKey() {
  return apiKey
}

/**
 * Builds the request headers, attaching the API key as `X-API-Key` if one is set.
 * @returns A headers object suitable for passing to `fetch`.
 */
function headers(): HeadersInit {
  const h: Record<string, string> = {}
  if (apiKey) h['X-API-Key'] = apiKey
  return h
}

/**
 * Fetches the health/status of the Context0 API.
 * @returns A promise resolving to the health response with node/edge counts and version.
 */
export async function fetchHealth(): Promise<HealthResponse> {
  const r = await fetch('/v1/health', { headers: headers() })
  return r.json()
}

/**
 * Queries the API for all memories, returning up to `topK` results.
 * Uses an empty query string to match all memories.
 *
 * @param topK - Maximum number of memory results to return (default: 200).
 * @returns A promise resolving to an array of {@link MemoryResult} objects.
 * @throws {Error} If the API responds with a non-OK status.
 */
export async function fetchMemories(topK = 200): Promise<MemoryResult[]> {
  const r = await fetch(`/v1/memories/query?query=&top_k=${topK}`, { headers: headers() })
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`)
  const d = await r.json()
  return d.results ?? []
}

/**
 * Fetches the subgraph (neighboring nodes and edges) around a specific memory.
 *
 * @param id - The ID of the central memory node.
 * @param depth - How many hops from the center to include (default: 2).
 * @returns A promise resolving to the subgraph with nodes and edges.
 * @throws {Error} If the API responds with a non-OK status.
 */
export async function fetchSubgraph(id: string, depth = 2): Promise<SubgraphResponse> {
  const r = await fetch(`/v1/memories/${id}/graph?depth=${depth}`, { headers: headers() })
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`)
  return r.json()
}
