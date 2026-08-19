package embedding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpEmbedder implements the Embedder interface for any provider that
// accepts a JSON POST and returns a JSON response containing a vector of
// float64 values. Provider-specific request/response shapes are supplied
// via url, body, and decode.
type httpEmbedder struct {
	url    string                             // full request URL, including any query parameters
	auth   string                             // Authorization header value (e.g. "Bearer ..."); empty means none
	body   func(text string) any              // builds the JSON-encodable request body for the given text
	decode func(io.Reader) ([]float64, error) // parses the response body into a raw embedding vector
	dim    int                                // fixed vector dimension, set at construction

	// client is the HTTP client used for requests. Nil means defaultClient.
	// Only tests set this.
	client *http.Client
}

// defaultClient carries a timeout, unlike http.DefaultClient.
//
// An embedding provider that accepts a connection and then stalls would
// otherwise block the calling Store indefinitely: http.DefaultClient has no
// timeout at all, so the request has no upper bound. Store holds a database
// connection across this call, so a hung provider drains the pool and takes
// down writes that have nothing to do with embeddings.
var defaultClient = &http.Client{Timeout: 30 * time.Second}

// redactURL removes credentials from a URL before it reaches a log.
//
// Google's provider passes its API key as a query parameter, and Go's
// transport errors embed the full request URL. Those errors are logged at
// Error level on every embedding failure, so an unreachable or slow provider
// wrote the API key into the logs -- which are shipped and retained far more
// widely than any secret store.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		// Unparseable: return nothing rather than risk echoing a credential.
		return "[unparseable url]"
	}
	q := u.Query()
	for _, k := range []string{"key", "api_key", "apikey", "access_token", "token"} {
		if q.Has(k) {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	return u.String()
}

// redactErr rewrites any occurrence of the raw URL in an error with its
// redacted form. net/http wraps transport errors in *url.Error, which prints
// the full URL, so replacing the substring covers both that and any provider
// error that echoes the endpoint.
func redactErr(err error, rawURL string) error {
	if err == nil {
		return nil
	}
	msg := strings.ReplaceAll(err.Error(), rawURL, redactURL(rawURL))
	return errors.New(msg)
}

// Dimension returns the fixed vector dimension this embedder was constructed with.
func (h httpEmbedder) Dimension() int { return h.dim }

// Embed sends text to the configured endpoint and returns the resulting
// vector, down-cast from the provider's float64 values to float32 for
// pgvector compatibility.
func (h httpEmbedder) Embed(text string) ([]float32, error) {
	data, err := json.Marshal(h.body(text))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", redactErr(err, h.url))
	}
	req.Header.Set("Content-Type", "application/json")
	if h.auth != "" {
		req.Header.Set("Authorization", h.auth)
	}

	client := h.client
	if client == nil {
		client = defaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", redactErr(err, h.url))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Bounded: a provider erroring with a large body should not put all of
		// it in the log line.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("embedding error (%d): %s", resp.StatusCode, respBody)
	}

	raw, err := h.decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}
	return vec, nil
}
