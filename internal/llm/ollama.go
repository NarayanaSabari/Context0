package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OllamaProvider implements [Provider] by communicating with a local or remote
// Ollama instance over its HTTP API (/api/generate). It uses non-streaming
// mode so the entire response is returned in a single JSON payload.
type OllamaProvider struct {
	baseURL string      // Ollama server URL (e.g. "http://localhost:11434")
	model   string      // model tag to use (e.g. "llama3.2")
	client  *http.Client // reusable HTTP client
}

// NewOllamaProvider creates an Ollama LLM provider. When baseURL is empty it
// defaults to "http://localhost:11434". When model is empty it defaults to
// "llama3.2".
func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3.2"
	}
	return &OllamaProvider{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{},
	}
}

// Name returns "ollama/<model>" for use in log messages and metrics.
func (o *OllamaProvider) Name() string {
	return fmt.Sprintf("ollama/%s", o.model)
}

// Complete sends the prompt to the Ollama /api/generate endpoint in non-
// streaming mode and returns the generated text.
func (o *OllamaProvider) Complete(ctx context.Context, prompt string) (string, error) {
	// Build the Ollama generate request payload.
	body := map[string]any{
		"model":  o.model,
		"prompt": prompt,
		"stream": false, // request full response in a single JSON object
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/api/generate", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	// Surface non-200 responses with the raw body for debugging.
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama error (%d): %s", resp.StatusCode, respBody)
	}

	// Decode the non-streaming response which carries the full text in
	// the "response" field.
	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Response, nil
}

// IsOllamaAvailable performs a lightweight health check against the Ollama
// /api/tags endpoint. It returns true when the server responds with 200 OK.
// When baseURL is empty, it defaults to "http://localhost:11434".
func IsOllamaAvailable(baseURL string) bool {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	resp, err := http.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
