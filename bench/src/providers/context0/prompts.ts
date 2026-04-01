import type { ProviderPrompts } from "../../types/prompts"

export const CONTEXT0_PROMPTS: ProviderPrompts = {
  answerPrompt: (question: string, searchResults: unknown[]) => {
    const context = searchResults
      .map((r: unknown) => {
        if (typeof r === "string") return r
        const mem = r as Record<string, unknown>
        return mem.content ?? JSON.stringify(r)
      })
      .join("\n---\n")

    return `You are answering questions based on memories stored in a graph-based memory engine.

Here are the relevant memories retrieved from the knowledge graph:

<memories>
${context}
</memories>

Based ONLY on the memories above, answer the following question. If the memories don't contain enough information, say "I don't have enough information to answer this."

Question: ${question}

Answer concisely and directly.`
  },

  judgePrompt: (question: string, hypothesis: string, groundTruth: string) => {
    return `You are evaluating whether an AI answer is correct based on the ground truth.

Question: ${question}

Ground Truth Answer: ${groundTruth}

AI's Answer: ${hypothesis}

Evaluate whether the AI's answer is semantically correct compared to the ground truth. The answer doesn't need to be word-for-word identical, but must convey the same key information.

Respond with ONLY "correct" or "incorrect" followed by a brief explanation.`
  },
}
