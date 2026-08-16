package embedding

import (
	"encoding/json"
	"fmt"
	"io"
)

// NewOpenAIEmbedder creates an OpenAI-compatible embedding provider. It is
// compatible with any service that implements the same request/response
// schema, including Azure OpenAI, vLLM, LiteLLM, Together AI, and Anyscale.
//
// Use this provider when you need the highest embedding quality and can
// accept the latency/cost of a cloud API call per embedding. For cost-sensitive
// workloads, consider using Ollama with a local model instead.
//
// Defaults:
//   - baseURL: https://api.openai.com/v1
//   - model: text-embedding-3-small
//   - dim: 1536
func NewOpenAIEmbedder(baseURL, apiKey, model string, dim int) Embedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if dim <= 0 {
		dim = 1536
	}

	var auth string
	if apiKey != "" {
		auth = "Bearer " + apiKey
	}

	return httpEmbedder{
		url:  baseURL + "/embeddings",
		auth: auth,
		body: func(text string) any {
			return map[string]any{"model": model, "input": text}
		},
		decode: decodeOpenAIEmbedding,
		dim:    dim,
	}
}

func decodeOpenAIEmbedding(r io.Reader) ([]float64, error) {
	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}
	return result.Data[0].Embedding, nil
}
