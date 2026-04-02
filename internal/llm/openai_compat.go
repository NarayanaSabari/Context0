package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OpenAICompatProvider implements [Provider] for any server that exposes the
// OpenAI chat completions API (POST /chat/completions). This covers the
// official OpenAI API as well as compatible alternatives such as vLLM,
// LiteLLM, Groq, Together, and Anthropic via proxy.
type OpenAICompatProvider struct {
	baseURL string       // e.g. "https://api.openai.com/v1"
	apiKey  string       // Bearer token; may be empty for unauthenticated endpoints
	model   string       // model identifier passed in the request body
	client  *http.Client // reusable HTTP client
}

// NewOpenAICompatProvider creates a provider for any OpenAI-compatible API.
// When baseURL is empty it defaults to "https://api.openai.com/v1". When
// model is empty it defaults to "gpt-4o-mini". The apiKey may be empty for
// local or unauthenticated endpoints.
func NewOpenAICompatProvider(baseURL, apiKey, model string) *OpenAICompatProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAICompatProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{},
	}
}

// Name returns "openai-compat/<model>" for use in log messages and metrics.
func (o *OpenAICompatProvider) Name() string {
	return fmt.Sprintf("openai-compat/%s", o.model)
}

// Complete sends the prompt as a single user message to the chat completions
// endpoint and returns the first choice's content. Temperature is fixed at
// 0.1 for deterministic extraction results.
func (o *OpenAICompatProvider) Complete(ctx context.Context, prompt string) (string, error) {
	// Build the chat completions request with a single user message.
	body := map[string]any{
		"model": o.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1, // low temperature for reproducible extraction
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Only set the Authorization header when a key is configured.
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()

	// Surface non-200 responses with the raw body for debugging.
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("api error (%d): %s", resp.StatusCode, respBody)
	}

	// Decode the standard OpenAI chat completions response structure.
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	// Return the content from the first (and typically only) choice.
	return result.Choices[0].Message.Content, nil
}
