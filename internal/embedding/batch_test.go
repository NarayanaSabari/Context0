package embedding

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// countingServer returns a stub embedding endpoint plus a counter of how many
// HTTP requests it received, which is the thing batching is meant to reduce.
func countingServer(t *testing.T, dim int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		var req struct {
			Input    any `json:"input"`
			Requests []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"requests"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		// Google batch shape.
		if len(req.Requests) > 0 {
			embeddings := make([]map[string]any, 0, len(req.Requests))
			for i := range req.Requests {
				embeddings = append(embeddings, map[string]any{
					"values": ramp(dim, float64(i+1)),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
			return
		}

		// OpenAI shape: input is either a string or an array of strings.
		texts, _ := req.Input.([]any)
		if texts == nil {
			texts = []any{req.Input}
		}
		data := make([]map[string]any, 0, len(texts))
		for i := range texts {
			// index is what the real API returns and what the decoder sorts
			// by; omitting it would collapse every result onto index 0.
			data = append(data, map[string]any{
				"index":     i,
				"embedding": ramp(dim, float64(i+1)),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))

	return srv, &calls
}

// ramp builds a distinguishable non-zero vector, so a test can tell which
// input a returned embedding corresponds to.
func ramp(dim int, seed float64) []float64 {
	v := make([]float64, dim)
	for i := range v {
		v[i] = seed
	}
	return v
}

// TestEmbedBatch_SendsOneRequest is the whole point: N texts must cost one
// round trip, not N.
//
// Ingest was one HTTP request per memory at a fan-out of 8. Measured against
// gemini-embedding-2, a single embed takes ~2.06s while a batch of 32 takes
// 4.17s, so a 2,894-memory corpus spent roughly 745s embedding where batching
// costs ~94s. The request count matters as much as the latency: cloud
// providers rate-limit per key, and 2,894 requests is where throttling starts.
func TestEmbedBatch_SendsOneRequest(t *testing.T) {
	const dim = 8
	srv, calls := countingServer(t, dim)
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "k", "m", dim)
	batcher, ok := e.(BatchEmbedder)
	if !ok {
		t.Fatal("the OpenAI embedder must implement BatchEmbedder; it is the provider " +
			"whose API supports batching natively")
	}

	texts := []string{"first", "second", "third", "fourth"}
	got, err := batcher.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("embed batch: %v", err)
	}

	if len(got) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(got), len(texts))
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("batching %d texts made %d HTTP requests, want 1", len(texts), n)
	}
}

// TestEmbedBatch_PreservesOrder pins that result i belongs to text i.
//
// Callers map vectors back onto memories positionally, so a provider that
// reorders or drops one would silently attach every embedding to the wrong
// memory. That corruption is invisible: retrieval still returns results, they
// are just wrong, and nothing in the system would flag it.
func TestEmbedBatch_PreservesOrder(t *testing.T) {
	const dim = 4
	srv, _ := countingServer(t, dim)
	defer srv.Close()

	batcher := NewOpenAIEmbedder(srv.URL, "k", "m", dim).(BatchEmbedder)

	got, err := batcher.EmbedBatch([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("embed batch: %v", err)
	}

	// The stub encodes position as the vector's value.
	for i, vec := range got {
		want := float32(i + 1)
		if vec[0] != want {
			t.Errorf("vector %d has value %v, want %v: results are not in input order",
				i, vec[0], want)
		}
	}
}

// TestEmbedBatch_GoogleUsesBatchEndpoint pins that the Google provider posts
// to batchEmbedContents rather than looping over embedContent.
func TestEmbedBatch_GoogleUsesBatchEndpoint(t *testing.T) {
	const dim = 4
	var path string
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path = r.URL.Path

		var req struct {
			Requests []json.RawMessage `json:"requests"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		embeddings := make([]map[string]any, 0, len(req.Requests))
		for i := range req.Requests {
			embeddings = append(embeddings, map[string]any{"values": ramp(dim, float64(i+1))})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
	}))
	defer srv.Close()

	e := newGoogleEmbedderWithBase(srv.URL, "k", "m", dim)
	batcher, ok := e.(BatchEmbedder)
	if !ok {
		t.Fatal("the Google embedder must implement BatchEmbedder")
	}

	got, err := batcher.EmbedBatch([]string{"one", "two", "three"})
	if err != nil {
		t.Fatalf("embed batch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d vectors, want 3", len(got))
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1", calls)
	}
	if path != "/v1beta/models/m:batchEmbedContents" {
		t.Errorf("posted to %q, want the batchEmbedContents endpoint", path)
	}
}

// TestEmbedBatch_RejectsShortResponse pins that a provider returning fewer
// vectors than texts is an error, not a silent truncation.
//
// Returning a short slice would leave the caller mapping vector i onto memory
// i for a prefix and then losing the rest, which is exactly the positional
// corruption that is impossible to detect downstream.
func TestEmbedBatch_RejectsShortResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Two vectors regardless of how many were asked for.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 0, "embedding": ramp(4, 1)},
				{"index": 1, "embedding": ramp(4, 2)},
			},
		})
	}))
	defer srv.Close()

	batcher := NewOpenAIEmbedder(srv.URL, "k", "m", 4).(BatchEmbedder)

	if _, err := batcher.EmbedBatch([]string{"a", "b", "c", "d"}); err == nil {
		t.Error("a provider returning 2 vectors for 4 texts must be an error: " +
			"silently returning a short slice misaligns every embedding after it")
	}
}

// TestEmbedBatch_EmptyInput pins that embedding nothing is not an error and
// costs no request.
func TestEmbedBatch_EmptyInput(t *testing.T) {
	srv, calls := countingServer(t, 4)
	defer srv.Close()

	batcher := NewOpenAIEmbedder(srv.URL, "k", "m", 4).(BatchEmbedder)

	got, err := batcher.EmbedBatch(nil)
	if err != nil {
		t.Fatalf("embedding an empty batch returned an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d vectors for no input", len(got))
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("embedding nothing made %d HTTP requests", n)
	}
}

// TestEmbedBatchHelper_FallsBackForNonBatchingProviders pins that a provider
// without a batch API still works through the same call site.
//
// Bag-of-words is local and Ollama's embeddings endpoint takes one prompt at a
// time, so callers must not have to ask which provider they hold.
func TestEmbedBatchHelper_FallsBackForNonBatchingProviders(t *testing.T) {
	e := NewBagOfWordsEmbedder(16)
	if _, ok := any(e).(BatchEmbedder); ok {
		t.Skip("bag-of-words now batches natively; this test no longer covers the fallback")
	}

	texts := []string{"the deploy target is production", "postgres runs the write model"}
	got, err := EmbedBatch(e, texts)
	if err != nil {
		t.Fatalf("EmbedBatch fallback: %v", err)
	}
	if len(got) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(got), len(texts))
	}

	// The fallback must produce exactly what Embed would have.
	for i, text := range texts {
		want, err := e.Embed(text)
		if err != nil {
			t.Fatalf("embed: %v", err)
		}
		for j := range want {
			if got[i][j] != want[j] {
				t.Fatalf("fallback vector %d differs from Embed at index %d", i, j)
			}
		}
	}
}

// TestEmbedBatchHelper_UsesBatchWhenAvailable is the other half: the helper
// must not quietly loop when the provider can batch.
func TestEmbedBatchHelper_UsesBatchWhenAvailable(t *testing.T) {
	const dim = 4
	srv, calls := countingServer(t, dim)
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "k", "m", dim)
	if _, err := EmbedBatch(e, []string{"a", "b", "c"}); err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("helper made %d requests for a batching provider, want 1", n)
	}
}

// TestEmbedBatch_ChunksOversizedInput pins that a batch larger than the
// provider's limit is split rather than rejected.
//
// Providers cap batch size (Google at 100 inputs per call), and a conversation
// can extract more memories than that. Failing the whole batch would make
// large conversations unembeddable, which is worse than the per-text loop this
// replaces.
func TestEmbedBatch_ChunksOversizedInput(t *testing.T) {
	const dim = 4
	srv, calls := countingServer(t, dim)
	defer srv.Close()

	batcher := NewOpenAIEmbedder(srv.URL, "k", "m", dim).(BatchEmbedder)

	texts := make([]string, maxBatchSize+5)
	for i := range texts {
		texts[i] = fmt.Sprintf("memory %d", i)
	}

	got, err := batcher.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("embed batch: %v", err)
	}
	if len(got) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(got), len(texts))
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("a batch of %d made %d requests, want 2 chunks", len(texts), n)
	}
}

// mustHTTPEmbedder unwraps the shared httpEmbedder from a provider, failing
// the test if it is not HTTP-backed.
//
// Tests inspect the concrete type to check URLs, defaults and timeouts.
// Providers that batch are batchHTTPEmbedder values, which embed httpEmbedder
// rather than being one, so a direct type assertion panics on exactly the
// providers this package most needs covered.
func mustHTTPEmbedder(t *testing.T, e Embedder) httpEmbedder {
	t.Helper()
	h, ok := asHTTPEmbedder(e)
	if !ok {
		t.Fatalf("provider %T is not HTTP-backed", e)
	}
	return h
}
