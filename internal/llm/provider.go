package llm

import "context"

// Provider generates text completions from a language model.
type Provider interface {
	// Complete sends a prompt and returns the model's response.
	Complete(ctx context.Context, prompt string) (string, error)

	// Name returns the provider name for logging.
	Name() string
}
