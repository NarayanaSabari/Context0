package embedding

import (
	"testing"
)

func TestNewFromConfig_BagOfWords(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		dim      int
		wantDim  int
	}{
		{"default empty", "", 0, 384},
		{"explicit bow", "bag-of-words", 0, 384},
		{"short name", "bow", 0, 384},
		{"custom dim", "bag-of-words", 512, 512},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := NewFromConfig(ProviderConfig{Provider: tt.provider, Dim: tt.dim})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if e.Dimension() != tt.wantDim {
				t.Errorf("dim = %d, want %d", e.Dimension(), tt.wantDim)
			}
		})
	}
}

func TestNewFromConfig_Ollama(t *testing.T) {
	e, err := NewFromConfig(ProviderConfig{Provider: "ollama", Model: "test-model", BaseURL: "http://localhost:11434"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Dimension() != 768 {
		t.Errorf("ollama default dim = %d, want 768", e.Dimension())
	}
}

func TestNewFromConfig_OpenAI_RequiresKey(t *testing.T) {
	_, err := NewFromConfig(ProviderConfig{Provider: "openai"})
	if err == nil {
		t.Fatal("expected error for openai without API key")
	}
}

func TestNewFromConfig_Google_RequiresKey(t *testing.T) {
	_, err := NewFromConfig(ProviderConfig{Provider: "google"})
	if err == nil {
		t.Fatal("expected error for google without API key")
	}
}

func TestNewFromConfig_OpenAI_WithKey(t *testing.T) {
	e, err := NewFromConfig(ProviderConfig{Provider: "openai", APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Dimension() != 1536 {
		t.Errorf("openai default dim = %d, want 1536", e.Dimension())
	}
}

func TestNewFromConfig_Google_WithKey(t *testing.T) {
	e, err := NewFromConfig(ProviderConfig{Provider: "google", APIKey: "test-key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Dimension() != 768 {
		t.Errorf("google default dim = %d, want 768", e.Dimension())
	}
}

func TestNewFromConfig_Unknown(t *testing.T) {
	_, err := NewFromConfig(ProviderConfig{Provider: "unknown-provider"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
