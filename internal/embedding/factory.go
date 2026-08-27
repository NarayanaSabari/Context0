package embedding

import "fmt"

// ProviderConfig holds the configuration for creating an Embedder via the
// factory function NewFromConfig. It maps directly to environment variables
// or config file fields, allowing runtime selection of the embedding backend.
type ProviderConfig struct {
	// Provider selects the embedding backend. Supported values:
	//   "bag-of-words" / "bow" / "" (default) - zero-dependency hashed BoW
	//   "ollama"  - local Ollama instance (no API key, privacy-friendly)
	//   "openai"  - OpenAI or any compatible API (highest quality)
	//   "google"  - Google Generative AI API
	Provider string
	// Model is the provider-specific model name (e.g. "nomic-embed-text",
	// "text-embedding-3-small", "gemini-embedding-001"). Empty means use default.
	Model string
	// APIKey is required for cloud providers (openai, google). Set via
	// KORA_EMBEDDING_API_KEY environment variable.
	APIKey string
	// BaseURL overrides the default API endpoint. Useful for self-hosted
	// Ollama instances or OpenAI-compatible proxies (vLLM, LiteLLM, etc.).
	BaseURL string
	// Dim sets the vector dimension. Zero or negative means auto-detect
	// (use the provider's default dimension).
	Dim int
}

// NewFromConfig creates an Embedder from the given configuration. When
// Provider is empty, it defaults to the bag-of-words embedder. Returns an
// error if a cloud provider is requested without an API key or if the
// provider name is unrecognized.
func NewFromConfig(cfg ProviderConfig) (Embedder, error) {
	switch cfg.Provider {
	case "bag-of-words", "bow", "":
		dim := cfg.Dim
		if dim <= 0 {
			dim = 384
		}
		return NewBagOfWordsEmbedder(dim), nil

	case "ollama":
		return NewOllamaEmbedder(cfg.BaseURL, cfg.Model, cfg.Dim), nil

	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("openai embedding provider requires KORA_EMBEDDING_API_KEY")
		}
		return NewOpenAIEmbedder(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Dim), nil

	case "google":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("google embedding provider requires KORA_EMBEDDING_API_KEY")
		}
		return NewGoogleEmbedder(cfg.APIKey, cfg.Model, cfg.Dim), nil

	default:
		return nil, fmt.Errorf("unknown embedding provider: %q (available: bag-of-words, ollama, openai, google)", cfg.Provider)
	}
}
