package embedding

// Embedder generates vector embeddings from text.
// Implementations can use local models, external APIs, or simple heuristics.
type Embedder interface {
	// Embed returns a vector embedding for the given text.
	Embed(text string) ([]float32, error)

	// Dimension returns the dimensionality of the output vectors.
	Dimension() int
}
