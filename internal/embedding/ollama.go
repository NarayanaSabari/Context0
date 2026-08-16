package embedding

import (
	"encoding/json"
	"io"
)

// NewOllamaEmbedder creates an Ollama embedding provider. Ollama is a good
// choice when:
//   - You want higher quality than bag-of-words without sending data to a cloud API.
//   - The deployment environment has a GPU or sufficient CPU for inference.
//   - No API key management is desired.
//
// Defaults:
//   - baseURL: http://localhost:11434
//   - model: nomic-embed-text (768 dimensions, good quality, runs on CPU)
//   - dim: 768
//
// Other popular models: all-minilm (384 dims), mxbai-embed-large (1024 dims).
func NewOllamaEmbedder(baseURL, model string, dim int) Embedder {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	if dim <= 0 {
		dim = 768 // nomic-embed-text default
	}
	return httpEmbedder{
		url: baseURL + "/api/embeddings",
		body: func(text string) any {
			return map[string]any{"model": model, "prompt": text}
		},
		decode: decodeOllamaEmbedding,
		dim:    dim,
	}
}

func decodeOllamaEmbedding(r io.Reader) ([]float64, error) {
	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return nil, err
	}
	return result.Embedding, nil
}
