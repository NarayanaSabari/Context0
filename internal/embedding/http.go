package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// httpEmbedder implements the Embedder interface for any provider that
// accepts a JSON POST and returns a JSON response containing a vector of
// float64 values. Provider-specific request/response shapes are supplied
// via url, body, and decode.
type httpEmbedder struct {
	url    string                             // full request URL, including any query parameters
	auth   string                             // Authorization header value (e.g. "Bearer ..."); empty means none
	body   func(text string) any              // builds the JSON-encodable request body for the given text
	decode func(io.Reader) ([]float64, error) // parses the response body into a raw embedding vector
	dim    int                                // fixed vector dimension, set at construction
}

// Dimension returns the fixed vector dimension this embedder was constructed with.
func (h httpEmbedder) Dimension() int { return h.dim }

// Embed sends text to the configured endpoint and returns the resulting
// vector, down-cast from the provider's float64 values to float32 for
// pgvector compatibility.
func (h httpEmbedder) Embed(text string) ([]float32, error) {
	data, err := json.Marshal(h.body(text))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.auth != "" {
		req.Header.Set("Authorization", h.auth)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding error (%d): %s", resp.StatusCode, respBody)
	}

	raw, err := h.decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}
	return vec, nil
}
