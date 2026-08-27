package embedding

import (
	"encoding/json"
	"fmt"
	"io"
)

// NewGoogleEmbedder creates a Google AI embedding provider using Google's
// Generative AI (generativelanguage.googleapis.com) embedContent endpoint.
//
// Use this provider when you are already in the Google Cloud ecosystem or
// prefer Google's embedding models. The API key is passed as a query
// parameter rather than a Bearer token.
//
// The request always sets outputDimensionality. Current Gemini embedding
// models return 3072 dimensions by default, and pgvector refuses to build an
// HNSW index above 2000 ("column cannot have more than 2000 dimensions"), so
// an unconstrained request makes the server fail on schema init. Asking the
// API to project down keeps the vector indexable.
//
// Defaults:
//   - model: gemini-embedding-001
//   - dim: 1536 (under pgvector's 2000-dimension HNSW limit)
func NewGoogleEmbedder(apiKey, model string, dim int) Embedder {
	if model == "" {
		model = "gemini-embedding-001"
	}
	if dim <= 0 {
		dim = 1536
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
				"outputDimensionality": dim,
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
