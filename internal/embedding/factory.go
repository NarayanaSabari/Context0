package embedding

import "fmt"

// ProviderConfig holds the configuration for creating an embedder.
type ProviderConfig struct {
	Provider string // "bag-of-words" | "ollama" | "openai" | "google"
	Model    string // model name (provider-specific)
	APIKey   string // API key (for cloud providers)
	BaseURL  string // base URL override
	Dim      int    // vector dimension (0 = auto-detect)
}

// NewFromConfig creates an Embedder from configuration.
// Falls back to BagOfWords if the requested provider is unavailable.
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
			return nil, fmt.Errorf("openai embedding provider requires CONTEXT0_EMBEDDING_API_KEY")
		}
		return NewOpenAIEmbedder(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Dim), nil

	case "google":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("google embedding provider requires CONTEXT0_EMBEDDING_API_KEY")
		}
		return NewGoogleEmbedder(cfg.APIKey, cfg.Model, cfg.Dim), nil

	default:
		return nil, fmt.Errorf("unknown embedding provider: %q (available: bag-of-words, ollama, openai, google)", cfg.Provider)
	}
}
