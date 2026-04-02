package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OllamaEmbedder generates embeddings via a local or remote Ollama instance.
// Ollama is a good choice when:
//   - You want higher quality than bag-of-words without sending data to a cloud API.
//   - The deployment environment has a GPU or sufficient CPU for inference.
//   - No API key management is desired.
//
// Default model: nomic-embed-text (768 dimensions, good quality, runs on CPU).
// Other popular models: all-minilm (384 dims), mxbai-embed-large (1024 dims).
type OllamaEmbedder struct {
	baseURL string // Ollama server URL (default: http://localhost:11434)
	model   string // model name to request from Ollama
	dim     int    // expected vector dimension; updated on first successful call
}

// NewOllamaEmbedder creates an Ollama embedding provider. Defaults:
//   - baseURL: http://localhost:11434
//   - model: nomic-embed-text
//   - dim: 768
func NewOllamaEmbedder(baseURL, model string, dim int) *OllamaEmbedder {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	if dim <= 0 {
		dim = 768 // nomic-embed-text default
	}
	return &OllamaEmbedder{baseURL: baseURL, model: model, dim: dim}
}

// Dimension returns the expected vector dimension. This value may be updated
// after the first successful Embed call if the model returns a different size.
func (o *OllamaEmbedder) Dimension() int { return o.dim }

// Embed sends text to the Ollama /api/embeddings endpoint and returns the
// resulting vector. The Ollama API returns float64 values which are
// down-cast to float32 for pgvector compatibility.
func (o *OllamaEmbedder) Embed(text string) ([]float32, error) {
	body := map[string]any{
		"model":  o.model,
		"prompt": text,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := http.Post(o.baseURL+"/api/embeddings", "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error (%d): %s", resp.StatusCode, respBody)
	}

	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Convert float64 to float32 (pgvector and cosine math use float32).
	vec := make([]float32, len(result.Embedding))
	for i, v := range result.Embedding {
		vec[i] = float32(v)
	}

	// Auto-correct dimension on the first successful call so that
	// Dimension() returns the true model output size going forward.
	if len(vec) > 0 && o.dim != len(vec) {
		o.dim = len(vec)
	}

	return vec, nil
}
