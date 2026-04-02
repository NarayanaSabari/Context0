// Package llm defines the Provider interface for text generation and supplies
// concrete implementations for Ollama and OpenAI-compatible APIs. Providers
// are used by the extraction layer to turn raw text into structured entities
// and relationships.
package llm

import "context"

// Provider is the common interface for all LLM backends. Implementations
// must be safe for concurrent use.
type Provider interface {
	// Complete sends a single prompt to the language model and returns
	// the generated text. The context controls cancellation and timeouts.
	Complete(ctx context.Context, prompt string) (string, error)

	// Name returns a human-readable identifier for the provider and model,
	// used in log messages and metrics labels (e.g. "ollama/llama3.2").
	Name() string
}
