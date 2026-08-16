package embedding

import (
	"encoding/json"
	"fmt"
	"io"
)

// NewGoogleEmbedder creates a Google AI embedding provider using Google's
// Generative AI (generativelanguage.googleapis.com) embedContent endpoint.
// It supports models like embedding-001 and text-embedding-004.
//
// Use this provider when you are already in the Google Cloud ecosystem or
// prefer Google's embedding models. The API key is passed as a query
// parameter rather than a Bearer token.
//
// Defaults:
//   - model: text-embedding-004
//   - dim: 768
func NewGoogleEmbedder(apiKey, model string, dim int) Embedder {
	if model == "" {
		model = "text-embedding-004"
	}
	if dim <= 0 {
		dim = 768
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:embedContent?key=%s",
		model, apiKey,
	)

	return httpEmbedder{
		url: url,
		body: func(text string) any {
			return map[string]any{
				"content": map[string]any{
					"parts": []map[string]string{
						{"text": text},
					},
				},
			}
		},
		decode: decodeGoogleEmbedding,
		dim:    dim,
	}
}

func decodeGoogleEmbedding(r io.Reader) ([]float64, error) {
	var result struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return nil, err
	}
	return result.Embedding.Values, nil
}
