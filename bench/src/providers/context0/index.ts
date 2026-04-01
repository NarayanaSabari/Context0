import type {
  Provider,
  ProviderConfig,
  IngestOptions,
  IngestResult,
  SearchOptions,
  IndexingProgressCallback,
} from "../../types/provider"
import type { UnifiedSession } from "../../types/unified"
import { logger } from "../../utils/logger"
import { CONTEXT0_PROMPTS } from "./prompts"

/**
 * Context0 Memory Engine Provider for MemoryBench
 *
 * Connects to a running Context0 instance via REST API.
 * Stores conversation sessions as memory nodes in the graph,
 * and retrieves them via the query endpoint.
 */
export class Context0Provider implements Provider {
  name = "context0"
  prompts = CONTEXT0_PROMPTS
  concurrency = {
    default: 20,
    ingest: 5,
  }

  private baseUrl = ""
  private apiKey = ""

  async initialize(config: ProviderConfig): Promise<void> {
    this.apiKey = config.apiKey
    this.baseUrl = (config.baseUrl as string) || "http://localhost:8080"

    // Verify connectivity
    const resp = await fetch(`${this.baseUrl}/v1/health`)
    if (!resp.ok) {
      throw new Error(`Context0 health check failed: ${resp.status}`)
    }
    const health = (await resp.json()) as Record<string, unknown>
    logger.info(`Connected to Context0 v${health.version} (nodes: ${health.nodeCount}, edges: ${health.edgeCount})`)
  }

  async ingest(sessions: UnifiedSession[], options: IngestOptions): Promise<IngestResult> {
    const documentIds: string[] = []
    const containerTag = options.containerTag

    // Start a session in Context0 for the benchmark
    const sessResp = await this.post("/v1/sessions", {
      project_id: containerTag,
      agent_id: "memorybench",
    })
    const sessionId = sessResp.session?.id as string | undefined

    for (const session of sessions) {
      // Combine all messages into a single memory per session
      const content = session.messages
        .map((m) => `${m.speaker || m.role}: ${m.content}`)
        .join("\n")

      const date = (session.metadata?.formattedDate as string) ||
        (session.metadata?.date as string) || ""

      // Store as episodic memory (a conversation that happened)
      const resp = await this.post("/v1/memories", {
        content: `[Session: ${session.sessionId}${date ? ` on ${date}` : ""}]\n${content}`,
        type: 1, // EPISODIC
        project_id: containerTag,
        tags: ["session", session.sessionId],
        session_id: sessionId || "",
      })

      const memId = resp.memory?.id as string | undefined
      if (memId) {
        documentIds.push(memId)
      }
    }

    // End the session
    if (sessionId) {
      await this.post(`/v1/sessions/${sessionId}/end`, {})
    }

    return { documentIds }
  }

  async awaitIndexing(
    result: IngestResult,
    _containerTag: string,
    onProgress?: IndexingProgressCallback,
  ): Promise<void> {
    // Context0 with Apache AGE indexes synchronously on write — no waiting needed
    onProgress?.({
      completedIds: result.documentIds,
      failedIds: [],
      total: result.documentIds.length,
    })
  }

  async search(query: string, options: SearchOptions): Promise<unknown[]> {
    const limit = options.limit || 10

    const params = new URLSearchParams({
      query,
      project_id: options.containerTag,
      top_k: String(limit),
    })

    const resp = await this.get(`/v1/memories/query?${params.toString()}`)
    const results = (resp.results || []) as Array<Record<string, unknown>>

    return results.map((r) => {
      const memory = r.memory as Record<string, unknown> | undefined
      return {
        content: memory?.content ?? "",
        score: r.score ?? 0,
        id: memory?.id ?? "",
        type: memory?.type ?? "",
        tags: memory?.tags ?? [],
      }
    })
  }

  async clear(containerTag: string): Promise<void> {
    // Query all memories for this container and delete them
    try {
      const params = new URLSearchParams({
        query: "",
        project_id: containerTag,
        top_k: "500",
      })
      const resp = await this.get(`/v1/memories/query?${params.toString()}`)
      const results = (resp.results || []) as Array<Record<string, unknown>>

      for (const r of results) {
        const memory = r.memory as Record<string, unknown> | undefined
        const id = memory?.id as string | undefined
        if (id) {
          await this.del(`/v1/memories/${id}`)
        }
      }
      logger.info(`Cleared ${results.length} memories for container: ${containerTag}`)
    } catch (e) {
      logger.warn(`Failed to clear Context0 data: ${e}`)
    }
  }

  // --- HTTP helpers ---

  private async post(path: string, body: Record<string, unknown>): Promise<Record<string, unknown>> {
    const resp = await fetch(`${this.baseUrl}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(this.apiKey ? { "X-API-Key": this.apiKey } : {}),
      },
      body: JSON.stringify(body),
    })
    if (!resp.ok) {
      const text = await resp.text()
      throw new Error(`Context0 POST ${path} failed (${resp.status}): ${text}`)
    }
    const text = await resp.text()
    return text ? JSON.parse(text) : {}
  }

  private async get(path: string): Promise<Record<string, unknown>> {
    const resp = await fetch(`${this.baseUrl}${path}`, {
      headers: {
        ...(this.apiKey ? { "X-API-Key": this.apiKey } : {}),
      },
    })
    if (!resp.ok) {
      const text = await resp.text()
      throw new Error(`Context0 GET ${path} failed (${resp.status}): ${text}`)
    }
    return resp.json() as Promise<Record<string, unknown>>
  }

  private async del(path: string): Promise<void> {
    const resp = await fetch(`${this.baseUrl}${path}`, {
      method: "DELETE",
      headers: {
        ...(this.apiKey ? { "X-API-Key": this.apiKey } : {}),
      },
    })
    if (!resp.ok) {
      const text = await resp.text()
      logger.warn(`Context0 DELETE ${path} failed (${resp.status}): ${text}`)
    }
  }
}

export default Context0Provider
