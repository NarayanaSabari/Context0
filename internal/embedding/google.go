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
//   - model: gemini-embedding-2
//   - dim: 1536 (under pgvector's 2000-dimension HNSW limit)
//
// gemini-embedding-001 remains a reasonable alternative: measured here on ten
// paraphrased retrieval probes the two are within noise on quality (10/10 vs
// 9/10, average margin +0.1188 vs +0.1177), but -001 answered in ~550ms
// against ~2.4s for -2. Prefer -001 when embedding latency is on the critical
// path, such as bulk ingest or a latency-scored benchmark.
func NewGoogleEmbedder(apiKey, model string, dim int) Embedder {
	return newGoogleEmbedderWithBase("https://generativelanguage.googleapis.com", apiKey, model, dim)
}

// newGoogleEmbedderWithBase is NewGoogleEmbedder with the API host injected,
// so tests can point it at a stub without reaching the network.
func newGoogleEmbedderWithBase(base, apiKey, model string, dim int) Embedder {
	if model == "" {
		model = "gemini-embedding-2"
	}
	if dim <= 0 {
		dim = 1536
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:embedContent?key=%s", base, model, apiKey)
	batchURL := fmt.Sprintf("%s/v1beta/models/%s:batchEmbedContents?key=%s", base, model, apiKey)

	// Google's batch endpoint requires each sub-request to name the model
	// again, fully qualified, even though the URL already identifies it.
	qualified := "models/" + model

	return batchHTTPEmbedder{
		httpEmbedder: httpEmbedder{
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
		},
		batchURL: batchURL,
		batchBody: func(texts []string) any {
			requests := make([]map[string]any, len(texts))
			for i, text := range texts {
				requests[i] = map[string]any{
					"model": qualified,
					"content": map[string]any{
						"parts": []map[string]string{
							{"text": text},
						},
					},
					"outputDimensionality": dim,
				}
			}
			return map[string]any{"requests": requests}
		},
		decodeBatch: decodeGoogleBatch,
	}
}

func decodeGoogleBatch(r io.Reader) ([][]float64, error) {
	var result struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return nil, err
	}

	// Google returns embeddings positionally, with no index field, so arrival
	// order is the only mapping back to the inputs.
	out := make([][]float64, len(result.Embeddings))
	for i, e := range result.Embeddings {
		out[i] = e.Values
	}
	return out, nil
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
