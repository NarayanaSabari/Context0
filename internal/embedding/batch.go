package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// BatchEmbedder is implemented by embedders whose provider can embed several
// texts in one request.
//
// It is deliberately a separate, optional interface rather than a method on
// Embedder. Bag-of-words is local and gains nothing from batching, and
// Ollama's /api/embeddings takes a single prompt, so forcing every
// implementation to grow a batch method would mean three of four providers
// implementing it as a loop and pretending. Callers should use the EmbedBatch
// helper, which uses the batch path when it exists and falls back otherwise.
type BatchEmbedder interface {
	Embedder

	// EmbedBatch embeds texts in as few requests as the provider allows,
	// returning one vector per input in the same order.
	//
	// It returns an error rather than a short slice if the provider returns
	// the wrong number of vectors: callers map results onto inputs by
	// position, so a misaligned result attaches embeddings to the wrong
	// memories, and nothing downstream can detect that.
	EmbedBatch(texts []string) ([][]float32, error)
}

// maxBatchSize caps how many texts go into one provider request.
//
// Google documents a limit of 100 inputs per batchEmbedContents call, and
// OpenAI limits by token count rather than item count. 100 respects the
// stricter of the two, and an oversized input is chunked rather than rejected:
// a single conversation can extract more memories than the limit, and failing
// the whole batch would be worse than the per-text loop this replaces.
const maxBatchSize = 100

// EmbedBatch embeds texts using e's batch API when it has one, and falls back
// to embedding them one at a time when it does not.
//
// This is the function call sites should use. It keeps them from having to
// know which provider is configured, which is what would otherwise leak the
// provider matrix into the service layer.
func EmbedBatch(e Embedder, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if b, ok := e.(BatchEmbedder); ok {
		return b.EmbedBatch(texts)
	}

	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("embed text %d: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}

// batchHTTPEmbedder adds a batch request path to httpEmbedder. The single-text
// Embed method is inherited unchanged, so a provider that batches still
// behaves identically for one-off calls such as embedding a query.
type batchHTTPEmbedder struct {
	httpEmbedder

	// batchURL is the endpoint for batch requests. Empty means the provider
	// batches through its normal endpoint (OpenAI takes an array as "input").
	batchURL string

	// batchBody builds the request body for a chunk of texts.
	batchBody func(texts []string) any

	// decodeBatch parses a batch response into one vector per input, in order.
	decodeBatch func(io.Reader) ([][]float64, error)
}

// EmbedBatch embeds texts in chunks of at most maxBatchSize.
func (b batchHTTPEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatchSize {
		end := start + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk := texts[start:end]

		vectors, err := b.embedChunk(chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
	}
	return out, nil
}

func (b batchHTTPEmbedder) embedChunk(texts []string) ([][]float32, error) {
	url := b.batchURL
	if url == "" {
		url = b.url
	}

	data, err := json.Marshal(b.batchBody(texts))
	if err != nil {
		return nil, fmt.Errorf("marshal batch request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create batch request: %w", redactErr(err, url))
	}
	req.Header.Set("Content-Type", "application/json")
	if b.auth != "" {
		req.Header.Set("Authorization", b.auth)
	}

	client := b.client
	if client == nil {
		client = defaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch embedding request: %w", redactErr(err, url))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("batch embedding error (%d): %s", resp.StatusCode, body)
	}

	raw, err := b.decodeBatch(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	// Callers map vectors onto inputs by position. A provider that returns a
	// different count has already broken that mapping, and continuing would
	// attach embeddings to the wrong memories with nothing downstream able to
	// notice.
	if len(raw) != len(texts) {
		return nil, fmt.Errorf("batch embedding returned %d vectors for %d texts", len(raw), len(texts))
	}

	out := make([][]float32, len(raw))
	for i, v := range raw {
		vec := make([]float32, len(v))
		for j, f := range v {
			vec[j] = float32(f)
		}
		out[i] = vec
	}
	return out, nil
}

// asHTTPEmbedder unwraps the shared httpEmbedder from either a plain provider
// or a batching one.
//
// Providers that gained a batch path are batchHTTPEmbedder values, which
// embed httpEmbedder rather than being one, so a `v.(httpEmbedder)` assertion
// that used to succeed now panics. Tests reach for the concrete type to check
// URLs, timeouts and defaults, and this keeps that possible without every one
// of them needing to know which providers batch.
func asHTTPEmbedder(e Embedder) (httpEmbedder, bool) {
	switch v := e.(type) {
	case httpEmbedder:
		return v, true
	case batchHTTPEmbedder:
		return v.httpEmbedder, true
	default:
		return httpEmbedder{}, false
	}
}
