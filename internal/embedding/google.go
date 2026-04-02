package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GoogleEmbedder generates embeddings via Google's Generative AI
// (generativelanguage.googleapis.com) embedContent endpoint. It supports
// models like embedding-001 and text-embedding-004.
//
// Use this provider when you are already in the Google Cloud ecosystem or
// prefer Google's embedding models. The API key is passed as a query
// parameter rather than a Bearer token.
//
// Default model: text-embedding-004 (768 dimensions).
type GoogleEmbedder struct {
	apiKey string // Google API key, passed as ?key= query parameter
	model  string // model name (e.g. "text-embedding-004")
	dim    int    // expected vector dimension; auto-corrected after first call
}

// NewGoogleEmbedder creates a Google AI embedding provider. Defaults:
//   - model: text-embedding-004
//   - dim: 768
func NewGoogleEmbedder(apiKey, model string, dim int) *GoogleEmbedder {
	if model == "" {
		model = "text-embedding-004"
	}
	if dim <= 0 {
		dim = 768
	}
	return &GoogleEmbedder{apiKey: apiKey, model: model, dim: dim}
}

// Dimension returns the expected vector dimension. This value may be updated
// after the first successful Embed call if the model returns a different size.
func (g *GoogleEmbedder) Dimension() int { return g.dim }

// Embed sends text to the Google embedContent endpoint and returns the
// resulting vector. The request body wraps the text in Google's Content/Parts
// structure. The API returns float64 values which are down-cast to float32.
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

	// Auto-correct dimension on the first successful call.
	if len(vec) > 0 && g.dim != len(vec) {
		g.dim = len(vec)
	}

	return vec, nil
}
