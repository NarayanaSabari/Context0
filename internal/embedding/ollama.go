package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OllamaEmbedder generates embeddings via a local or remote Ollama instance.
// Default model: nomic-embed-text (768 dims, good quality, runs on CPU).
type OllamaEmbedder struct {
	baseURL string
	model   string
	dim     int
}

// NewOllamaEmbedder creates an Ollama embedding provider.
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

func (o *OllamaEmbedder) Dimension() int { return o.dim }

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

	// Convert float64 to float32.
	vec := make([]float32, len(result.Embedding))
	for i, v := range result.Embedding {
		vec[i] = float32(v)
	}

	// Update dimension on first successful call.
	if len(vec) > 0 && o.dim != len(vec) {
		o.dim = len(vec)
	}

	return vec, nil
}
