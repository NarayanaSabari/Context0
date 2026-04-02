package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OpenAIEmbedder generates embeddings via the OpenAI /v1/embeddings API.
// It is compatible with any service that implements the same request/response
// schema, including Azure OpenAI, vLLM, LiteLLM, Together AI, and Anyscale.
//
// Use this provider when you need the highest embedding quality and can
// accept the latency/cost of a cloud API call per embedding. For cost-sensitive
// workloads, consider using Ollama with a local model instead.
//
// Default model: text-embedding-3-small (1536 dimensions).
type OpenAIEmbedder struct {
	baseURL string // API base URL (default: https://api.openai.com/v1)
	apiKey  string // Bearer token for Authorization header
	model   string // model identifier sent in the request body
	dim     int    // expected vector dimension; auto-corrected after first call
}

// NewOpenAIEmbedder creates an OpenAI-compatible embedding provider. Defaults:
//   - baseURL: https://api.openai.com/v1
//   - model: text-embedding-3-small
//   - dim: 1536
func NewOpenAIEmbedder(baseURL, apiKey, model string, dim int) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if dim <= 0 {
		dim = 1536
	}
	return &OpenAIEmbedder{baseURL: baseURL, apiKey: apiKey, model: model, dim: dim}
}

// Dimension returns the expected vector dimension. This value may be updated
// after the first successful Embed call if the model returns a different size.
func (o *OpenAIEmbedder) Dimension() int { return o.dim }

// Embed sends text to the OpenAI-compatible /embeddings endpoint and returns
// the first embedding vector from the response. The API returns float64
// values which are down-cast to float32 for pgvector compatibility.
func (o *OpenAIEmbedder) Embed(text string) ([]float32, error) {
	body := map[string]any{
		"model": o.model,
		"input": text,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", o.baseURL+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai error (%d): %s", resp.StatusCode, respBody)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}

	raw := result.Data[0].Embedding
	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}

	// Auto-correct dimension on the first successful call.
	if len(vec) > 0 && o.dim != len(vec) {
		o.dim = len(vec)
	}

	return vec, nil
}
