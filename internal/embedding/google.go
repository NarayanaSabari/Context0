package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GoogleEmbedder generates embeddings via Google's Generative AI API.
// Uses the embedding-001 or text-embedding-004 model.
type GoogleEmbedder struct {
	apiKey string
	model  string
	dim    int
}

// NewGoogleEmbedder creates a Google AI embedding provider.
// Default model: text-embedding-004 (768 dims).
func NewGoogleEmbedder(apiKey, model string, dim int) *GoogleEmbedder {
	if model == "" {
		model = "text-embedding-004"
	}
	if dim <= 0 {
		dim = 768
	}
	return &GoogleEmbedder{apiKey: apiKey, model: model, dim: dim}
}

func (g *GoogleEmbedder) Dimension() int { return g.dim }

func (g *GoogleEmbedder) Embed(text string) ([]float32, error) {
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:embedContent?key=%s",
		g.model, g.apiKey,
	)

	body := map[string]any{
		"content": map[string]any{
			"parts": []map[string]string{
				{"text": text},
			},
		},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("google request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("google error (%d): %s", resp.StatusCode, respBody)
	}

	var result struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	raw := result.Embedding.Values
	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}

	if len(vec) > 0 && g.dim != len(vec) {
		g.dim = len(vec)
	}

	return vec, nil
}
