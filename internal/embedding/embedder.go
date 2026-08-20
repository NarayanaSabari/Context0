// Package embedding provides text-to-vector embedding generation for Kora's
// semantic memory search. It defines the Embedder interface and ships multiple
// implementations:
//
//   - BagOfWordsEmbedder: zero-dependency, hashed bag-of-words (good for MVP/testing)
//   - OllamaEmbedder: local inference via Ollama (good for privacy, no API key needed)
//   - OpenAIEmbedder: cloud-based via OpenAI-compatible APIs (highest quality)
//   - GoogleEmbedder: cloud-based via Google Generative AI API
//
// Use NewFromConfig to create an Embedder from runtime configuration. The
// resulting vectors are stored in pgvector via the graph repository and used
// for approximate nearest neighbor search during memory retrieval.
package embedding

// Embedder generates vector embeddings from text. All implementations produce
// fixed-length float32 vectors suitable for cosine similarity comparison.
// Implementations must be safe for concurrent use.
type Embedder interface {
	// Embed converts the given text into a dense vector embedding.
	// The returned slice length always equals Dimension().
	Embed(text string) ([]float32, error)

	// Dimension returns the fixed dimensionality of vectors produced by this
	// embedder. This value is used to configure the pgvector column size.
	Dimension() int
}
