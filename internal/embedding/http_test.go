package embedding

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The HTTP-backed providers had no tests at all. They are the ones that run in
// production -- the bag-of-words embedder is a local fallback -- and every one
// of their defaults, their auth handling, and their error paths went
// unexercised. Mutation testing reported 26 survivors in this package, almost
// all of them here.
//
// These tests drive each provider against an httptest server, so they cover
// the request that is actually sent and the response handling as a whole,
// rather than asserting on struct fields.

// captureRequest runs fn against a server that records the request it receives
// and replies with the given body.
func captureRequest(t *testing.T, status int, respBody string, fn func(baseURL string) Embedder) (*http.Request, string, []float32, error) {
	t.Helper()

	var gotReq *http.Request
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotReq = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)

	vec, err := fn(srv.URL).Embed("hello world")
	return gotReq, gotBody, vec, err
}

func TestOpenAIEmbedderRequestAndResponse(t *testing.T) {
	req, body, vec, err := captureRequest(t, http.StatusOK,
		`{"data":[{"embedding":[0.25,-0.5,1]}]}`,
		func(u string) Embedder { return NewOpenAIEmbedder(u, "secret-key", "custom-model", 3) })
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if req.URL.Path != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", req.URL.Path)
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	// Without the Bearer prefix every request is rejected as unauthenticated.
	if got := req.Header.Get("Authorization"); got != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer secret-key")
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent["model"] != "custom-model" {
		t.Errorf("model = %v, want custom-model", sent["model"])
	}
	if sent["input"] != "hello world" {
		t.Errorf("input = %v, want the text passed to Embed", sent["input"])
	}

	// float64 -> float32 conversion must preserve exactly representable values.
	want := []float32{0.25, -0.5, 1}
	if len(vec) != len(want) {
		t.Fatalf("vector length = %d, want %d", len(vec), len(want))
	}
	for i := range want {
		if vec[i] != want[i] {
			t.Errorf("vec[%d] = %v, want %v", i, vec[i], want[i])
		}
	}
}

// TestOpenAIEmbedderOmitsAuthorizationWithoutKey: a local OpenAI-compatible
// server (vLLM, LiteLLM, Ollama's compat endpoint) usually needs no key, and
// sending "Bearer " with nothing after it is worse than sending nothing --
// some servers reject it outright.
func TestOpenAIEmbedderOmitsAuthorizationWithoutKey(t *testing.T) {
	req, _, _, err := captureRequest(t, http.StatusOK,
		`{"data":[{"embedding":[1,2,3]}]}`,
		func(u string) Embedder { return NewOpenAIEmbedder(u, "", "m", 3) })
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want no header when no API key is configured", got)
	}
}

func TestOllamaEmbedderRequestAndResponse(t *testing.T) {
	req, body, vec, err := captureRequest(t, http.StatusOK,
		`{"embedding":[1,2,3,4]}`,
		func(u string) Embedder { return NewOllamaEmbedder(u, "nomic-embed-text", 4) })
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if req.URL.Path != "/api/embeddings" {
		t.Errorf("path = %q, want /api/embeddings", req.URL.Path)
	}
	// Ollama uses "prompt", not "input". Sending the wrong key returns an
	// empty embedding rather than an error, so the memory would be stored
	// with a zero vector and match nothing.
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent["prompt"] != "hello world" {
		t.Errorf("request body = %s, want the text under the \"prompt\" key", body)
	}
	if _, ok := sent["input"]; ok {
		t.Errorf("request sent an \"input\" key; Ollama expects \"prompt\": %s", body)
	}
	if len(vec) != 4 {
		t.Errorf("vector length = %d, want 4", len(vec))
	}
	// Ollama needs no credential, so no Authorization header should be sent.
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none for Ollama", got)
	}
}

// TestGoogleEmbedderDoesNotLeakAPIKeyIntoErrors covers a credential leak.
//
// Google passes its API key as a query parameter rather than a header, and
// Go's transport errors embed the full request URL. Those errors are logged at
// Error level by StoreMemory on every embedding failure, so an unreachable or
// slow provider wrote the API key into the application log -- which is shipped
// and retained far more widely than any secret store, and cannot be
// retroactively scrubbed.
func TestGoogleEmbedderDoesNotLeakAPIKeyIntoErrors(t *testing.T) {
	const secret = "AIzaSy-SUPER-SECRET-KEY-VALUE"

	// Point the embedder at a port with nothing listening, which is what a
	// misconfigured or down provider looks like.
	e := NewGoogleEmbedder(secret, "text-embedding-004", 768).(httpEmbedder)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := srv.URL
	srv.Close()
	e.url = closedURL + "/v1beta/models/text-embedding-004:embedContent?key=" + secret

	_, err := e.Embed("some text")
	if err == nil {
		t.Fatal("Embed succeeded against a closed server")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the API key appears in the error that gets logged:\n  %v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("the error does not show the key was redacted, so it may not "+
			"have been recognised as a credential:\n  %v", err)
	}
}

// TestRedactURL covers the credential parameters directly.
func TestRedactURL(t *testing.T) {
	cases := []struct {
		in     string
		secret string
	}{
		{"https://api.example.com/v1:embed?key=SECRET123", "SECRET123"},
		{"https://api.example.com/v1?api_key=SECRET123", "SECRET123"},
		{"https://api.example.com/v1?apikey=SECRET123", "SECRET123"},
		{"https://api.example.com/v1?access_token=SECRET123", "SECRET123"},
		{"https://api.example.com/v1?token=SECRET123", "SECRET123"},
		{"https://user:SECRET123@api.example.com/v1", "SECRET123"},
		{"https://api.example.com/v1?key=SECRET123&model=m", "SECRET123"},
	}
	for _, tc := range cases {
		got := redactURL(tc.in)
		if strings.Contains(got, tc.secret) {
			t.Errorf("redactURL(%q) = %q, still contains the credential", tc.in, got)
		}
	}

	// A URL with no credential must survive intact, or error messages stop
	// being useful for diagnosing the endpoint.
	plain := "https://api.openai.com/v1/embeddings"
	if got := redactURL(plain); got != plain {
		t.Errorf("redactURL(%q) = %q, want it unchanged", plain, got)
	}
}

// TestHTTPEmbedderHasARequestTimeout: http.DefaultClient has no timeout, so a
// provider that accepts the connection and then stalls blocks Embed forever.
// Store holds a database connection across this call, so a hung provider
// drains the pool and takes down writes unrelated to embeddings.
func TestHTTPEmbedderHasARequestTimeout(t *testing.T) {
	if defaultClient.Timeout <= 0 {
		t.Fatal("the embedding HTTP client has no timeout; a stalled provider " +
			"blocks Store indefinitely while holding a pool connection")
	}

	// Prove it actually cuts off a stalled provider, rather than merely being
	// set on a client that is not used.
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked // never respond
	}))
	defer srv.Close()
	defer close(blocked)

	e := NewOpenAIEmbedder(srv.URL, "k", "m", 3).(httpEmbedder)
	e.client = &http.Client{Timeout: 150 * time.Millisecond}

	done := make(chan error, 1)
	go func() { _, err := e.Embed("text"); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Embed returned no error against a provider that never responded")
		}
	case <-time.After(5 * time.Second):
		t.Error("Embed did not return after the client timeout; the timeout is not applied")
	}
}

// TestHTTPEmbedderReportsProviderErrors: an embedding failure is logged and the
// memory is stored without a vector, so a failure that is not reported as an
// error becomes a memory silently missing from vector search.
func TestHTTPEmbedderReportsProviderErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"bad key"}`, "401"},
		{"rate limited", http.StatusTooManyRequests, `{"error":"slow down"}`, "429"},
		{"server error", http.StatusInternalServerError, `{"error":"boom"}`, "500"},
		{"malformed json", http.StatusOK, `{"data":[{"embedding":`, "decode"},
		{"empty data array", http.StatusOK, `{"data":[]}`, "no embedding data"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, vec, err := captureRequest(t, tc.status, tc.body,
				func(u string) Embedder { return NewOpenAIEmbedder(u, "k", "m", 3) })
			if err == nil {
				t.Fatalf("Embed returned no error for a %s response; the caller "+
					"would store a memory with vector %v and never know vector "+
					"search was broken", tc.name, vec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q, so the cause is not "+
					"diagnosable from the log", err, tc.want)
			}
			if vec != nil {
				t.Errorf("a failed Embed returned a non-nil vector %v", vec)
			}
		})
	}
}

// TestHTTPEmbedderReportsUnreachableProvider covers the transport failure,
// which is what a down or misconfigured provider actually looks like.
func TestHTTPEmbedderReportsUnreachableProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	if _, err := NewOpenAIEmbedder(url, "k", "m", 3).Embed("text"); err == nil {
		t.Error("Embed succeeded against a closed server")
	}
}

// TestProviderDefaults pins the documented fallbacks. These are what a
// deployment gets when the corresponding env var is unset, and a wrong
// dimension default does not fail loudly: it fails when the vector column
// width no longer matches.
func TestProviderDefaults(t *testing.T) {
	cases := []struct {
		name string
		got  Embedder
		dim  int
	}{
		{"openai", NewOpenAIEmbedder("", "", "", 0), 1536},
		{"ollama", NewOllamaEmbedder("", "", 0), 768},
		{"google", NewGoogleEmbedder("", "", 0), 1536},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.got.Dimension(); got != tc.dim {
				t.Errorf("default dimension = %d, want %d", got, tc.dim)
			}
			h, ok := tc.got.(httpEmbedder)
			if !ok {
				t.Fatalf("provider is not an httpEmbedder")
			}
			// A default URL that is empty or relative produces a confusing
			// transport error rather than a clear connection failure.
			if !strings.HasPrefix(h.url, "http://") && !strings.HasPrefix(h.url, "https://") {
				t.Errorf("default URL %q is not absolute", h.url)
			}
		})
	}
}

// TestProviderExplicitValuesOverrideDefaults: the defaults must not win over
// configuration, or a deployment pointing at a local provider silently calls
// the cloud one.
func TestProviderExplicitValuesOverrideDefaults(t *testing.T) {
	e := NewOpenAIEmbedder("https://example.invalid/v1", "k", "m", 42)
	if got := e.Dimension(); got != 42 {
		t.Errorf("dimension = %d, want the configured 42", got)
	}
	h := e.(httpEmbedder)
	if !strings.HasPrefix(h.url, "https://example.invalid/v1") {
		t.Errorf("url = %q, want the configured base URL", h.url)
	}
}

// TestHTTPEmbedderDimensionMismatchIsVisible documents a real gap rather than
// asserting current behaviour is correct.
//
// A provider can return a vector of a different length than the configured
// dimension -- a changed model, a truncating proxy, a misconfigured `dim`.
// Embed does not check, so the mismatch reaches StoreEmbedding and fails at
// the pgvector column width, where the error is logged and discarded. The
// memory is then stored and permanently absent from vector search.
//
// This test pins what Embed does today so the behaviour is at least recorded
// and cannot change unnoticed.
func TestHTTPEmbedderDimensionMismatchIsVisible(t *testing.T) {
	// Configured for 3 dimensions, provider returns 5.
	_, _, vec, err := captureRequest(t, http.StatusOK,
		`{"data":[{"embedding":[1,2,3,4,5]}]}`,
		func(u string) Embedder { return NewOpenAIEmbedder(u, "k", "m", 3) })
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(vec) == 3 {
		t.Fatal("Embed now truncates to the configured dimension; " +
			"update this test to pin the new contract")
	}
	if len(vec) != 5 {
		t.Fatalf("vector length = %d, want the provider's 5", len(vec))
	}
	t.Logf("known gap: Embed returned %d values for a %d-dimension embedder; "+
		"the mismatch is only caught later, at the pgvector column width",
		len(vec), 3)
}

// TestBagOfWordsRejectsInvalidDimension: the local fallback must not be
// constructible in a state that produces unusable vectors.
func TestBagOfWordsRejectsInvalidDimension(t *testing.T) {
	for _, dim := range []int{0, -1, -1000} {
		e := NewBagOfWordsEmbedder(dim)
		if e.Dimension() <= 0 {
			t.Errorf("NewBagOfWordsEmbedder(%d) has dimension %d; a non-positive "+
				"dimension yields empty vectors that match everything equally",
				dim, e.Dimension())
		}
		vec, err := e.Embed("some text")
		if err != nil {
			t.Errorf("Embed: %v", err)
			continue
		}
		if len(vec) != e.Dimension() {
			t.Errorf("Embed returned %d values, want Dimension() = %d",
				len(vec), e.Dimension())
		}
	}
}

// TestBagOfWordsHandlesEmptyAndUnnormalisableInput: text with no usable tokens
// produces a zero-magnitude vector, and normalising that divides by zero.
func TestBagOfWordsHandlesEmptyAndUnnormalisableInput(t *testing.T) {
	e := NewBagOfWordsEmbedder(64)
	for _, text := range []string{"", "   ", "\n\t", "!!!", "???"} {
		vec, err := e.Embed(text)
		if err != nil {
			t.Errorf("Embed(%q): %v", text, err)
			continue
		}
		if len(vec) != 64 {
			t.Errorf("Embed(%q) returned %d values, want 64", text, len(vec))
		}
		for i, v := range vec {
			if v != v { // NaN
				t.Errorf("Embed(%q)[%d] is NaN; a NaN in the vector makes every "+
					"distance comparison against it false", text, i)
				break
			}
			if v > 1e30 || v < -1e30 {
				t.Errorf("Embed(%q)[%d] = %v, non-finite", text, i, v)
				break
			}
		}
	}
}

// TestProviderDefaultModelAndURL pins each provider's default model and
// endpoint, not just its dimension.
//
// A wrong default model is silent: the request succeeds against a real
// provider, returns a vector of a different length or distribution, and the
// only symptom is worse search results. The model name is part of the URL for
// Google and part of the body for the others, so both are checked.
func TestProviderDefaultModelAndURL(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		h := NewOpenAIEmbedder("", "", "", 0).(httpEmbedder)
		if !strings.HasPrefix(h.url, "https://api.openai.com/v1") {
			t.Errorf("default URL = %q, want the OpenAI API base", h.url)
		}
		if !strings.HasSuffix(h.url, "/embeddings") {
			t.Errorf("URL = %q, want the /embeddings path", h.url)
		}
		body, _ := json.Marshal(h.body("x"))
		if !strings.Contains(string(body), "text-embedding-3-small") {
			t.Errorf("default request body = %s, want model text-embedding-3-small", body)
		}
	})

	t.Run("ollama", func(t *testing.T) {
		h := NewOllamaEmbedder("", "", 0).(httpEmbedder)
		if !strings.HasPrefix(h.url, "http://localhost:11434") {
			t.Errorf("default URL = %q, want the local Ollama endpoint", h.url)
		}
		if !strings.HasSuffix(h.url, "/api/embeddings") {
			t.Errorf("URL = %q, want the /api/embeddings path", h.url)
		}
		body, _ := json.Marshal(h.body("x"))
		if !strings.Contains(string(body), "nomic-embed-text") {
			t.Errorf("default request body = %s, want model nomic-embed-text", body)
		}
	})

	t.Run("google", func(t *testing.T) {
		h := NewGoogleEmbedder("", "", 0).(httpEmbedder)
		if !strings.Contains(h.url, "gemini-embedding-001") {
			t.Errorf("default URL = %q, want the gemini-embedding-001 model", h.url)
		}
		if !strings.Contains(h.url, "generativelanguage.googleapis.com") {
			t.Errorf("default URL = %q, want the Google AI endpoint", h.url)
		}
	})
}

// TestProviderConfiguredModelReachesTheProvider: an explicitly configured
// model must actually be sent, or a deployment silently embeds with the
// default while its config says otherwise.
func TestProviderConfiguredModelReachesTheProvider(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		h := NewOpenAIEmbedder("", "", "my-model", 0).(httpEmbedder)
		body, _ := json.Marshal(h.body("x"))
		if !strings.Contains(string(body), "my-model") {
			t.Errorf("request body = %s, want the configured model", body)
		}
	})
	t.Run("ollama", func(t *testing.T) {
		h := NewOllamaEmbedder("", "my-model", 0).(httpEmbedder)
		body, _ := json.Marshal(h.body("x"))
		if !strings.Contains(string(body), "my-model") {
			t.Errorf("request body = %s, want the configured model", body)
		}
	})
	t.Run("google", func(t *testing.T) {
		h := NewGoogleEmbedder("", "my-model", 0).(httpEmbedder)
		if !strings.Contains(h.url, "my-model") {
			t.Errorf("URL = %q, want the configured model", h.url)
		}
	})
}

// TestGoogleDecoderReadsTheDocumentedShape: Google nests its vector under
// embedding.values, unlike Ollama's flat "embedding". Reading the wrong field
// yields an empty vector rather than an error, so the memory is stored with a
// zero embedding and matches nothing.
func TestGoogleDecoderReadsTheDocumentedShape(t *testing.T) {
	h := NewGoogleEmbedder("k", "m", 2).(httpEmbedder)

	got, err := h.decode(strings.NewReader(`{"embedding":{"values":[0.5,0.25]}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0] != 0.5 || got[1] != 0.25 {
		t.Errorf("decoded %v, want [0.5 0.25]", got)
	}

	// The flat Ollama shape must not accidentally parse as a Google response.
	flat, err := h.decode(strings.NewReader(`{"embedding":[0.5,0.25]}`))
	if err == nil && len(flat) > 0 {
		t.Errorf("the Google decoder accepted an Ollama-shaped response and "+
			"returned %v; a shape mismatch must not silently yield a vector", flat)
	}
}

// TestFactoryDefaultDimension: the factory's own dimension fallback is
// separate from each provider's, and it decides the pgvector column width for
// the default bag-of-words setup.
func TestFactoryDefaultDimension(t *testing.T) {
	for _, provider := range []string{"", "bag-of-words", "bow"} {
		e, err := NewFromConfig(ProviderConfig{Provider: provider})
		if err != nil {
			t.Fatalf("NewFromConfig(%q): %v", provider, err)
		}
		if got := e.Dimension(); got != 384 {
			t.Errorf("NewFromConfig(%q) dimension = %d, want 384: this value "+
				"sets the vector column width and cannot change silently",
				provider, got)
		}
	}

	// A negative or zero Dim must fall back rather than produce an unusable
	// embedder.
	for _, dim := range []int{0, -1} {
		e, err := NewFromConfig(ProviderConfig{Provider: "bow", Dim: dim})
		if err != nil {
			t.Fatalf("NewFromConfig(dim=%d): %v", dim, err)
		}
		if e.Dimension() <= 0 {
			t.Errorf("NewFromConfig(dim=%d) produced dimension %d", dim, e.Dimension())
		}
	}
}

// TestBagOfWordsSignHashProducesDiscriminatingVectors covers the sign hash,
// whose purpose is vector geometry rather than any single output value.
//
// The property that matters is that unrelated texts are not forced toward
// mutual similarity by systematic positive bias. Asserting on specific vector
// components would pin the hash function instead of the behaviour.
func TestBagOfWordsSignHashProducesDiscriminatingVectors(t *testing.T) {
	e := NewBagOfWordsEmbedder(384)

	cosine := func(a, b []float32) float64 {
		var dot float64
		for i := range a {
			dot += float64(a[i]) * float64(b[i])
		}
		return dot
	}

	related1, _ := e.Embed("the database connection pool was exhausted")
	related2, _ := e.Embed("the database connection pool is saturated")
	unrelated, _ := e.Embed("breakfast pancakes with maple syrup")

	simRelated := cosine(related1, related2)
	simUnrelated := cosine(related1, unrelated)

	if simRelated <= simUnrelated {
		t.Errorf("related texts scored %.4f but unrelated scored %.4f; "+
			"the embedding does not separate them", simRelated, simUnrelated)
	}
	// Without sign variation every vector is non-negative and unrelated texts
	// still score well above zero purely from hash collisions.
	if simUnrelated > 0.5 {
		t.Errorf("unrelated texts scored %.4f, too high to discriminate", simUnrelated)
	}
}

// TestConfiguredDimensionIsHonoured: a configured dimension must reach the
// embedder for every provider.
//
// The dimension decides the pgvector column width. If a provider ignores the
// configured value and uses its own default, InitSchema builds the column at
// one width while the embedder produces vectors of another, and every
// StoreEmbedding fails at write time -- where the error is logged and
// discarded, so the memories are simply absent from vector search.
func TestConfiguredDimensionIsHonoured(t *testing.T) {
	const want = 111

	providers := map[string]Embedder{
		"openai": NewOpenAIEmbedder("", "k", "m", want),
		"ollama": NewOllamaEmbedder("", "m", want),
		"google": NewGoogleEmbedder("k", "m", want),
		"bow":    NewBagOfWordsEmbedder(want),
	}
	for name, e := range providers {
		if got := e.Dimension(); got != want {
			t.Errorf("%s: Dimension() = %d, want the configured %d", name, got, want)
		}
	}

	// Through the factory, which is how a deployment actually builds these.
	for _, cfg := range []ProviderConfig{
		{Provider: "bow", Dim: want},
		{Provider: "ollama", Dim: want},
		{Provider: "openai", APIKey: "k", Dim: want},
		{Provider: "google", APIKey: "k", Dim: want},
	} {
		e, err := NewFromConfig(cfg)
		if err != nil {
			t.Fatalf("NewFromConfig(%+v): %v", cfg, err)
		}
		if got := e.Dimension(); got != want {
			t.Errorf("NewFromConfig(provider=%q, dim=%d) produced dimension %d; "+
				"a provider that ignores the configured dimension makes every "+
				"embedding write fail against the pgvector column width",
				cfg.Provider, want, got)
		}
	}

	// The bag-of-words vector must actually be that long, not merely report it.
	vec, err := NewBagOfWordsEmbedder(want).Embed("some text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != want {
		t.Errorf("Embed produced %d values but Dimension() reports %d", len(vec), want)
	}
}

// TestAuthorizationHeaderOnlyWhenConfigured: sending "Bearer" with an empty
// value is rejected outright by some OpenAI-compatible servers, so the header
// must be absent rather than empty when no key is set.
func TestAuthorizationHeaderOnlyWhenConfigured(t *testing.T) {
	cases := []struct {
		name    string
		apiKey  string
		wantHdr string
	}{
		{"with key", "sk-test", "Bearer sk-test"},
		{"without key", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var present bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Authorization")
				_, present = r.Header["Authorization"]
				_, _ = io.WriteString(w, `{"data":[{"embedding":[1,2,3]}]}`)
			}))
			defer srv.Close()

			if _, err := NewOpenAIEmbedder(srv.URL, tc.apiKey, "m", 3).Embed("x"); err != nil {
				t.Fatalf("Embed: %v", err)
			}
			if got != tc.wantHdr {
				t.Errorf("Authorization = %q, want %q", got, tc.wantHdr)
			}
			if tc.apiKey == "" && present {
				t.Error("an empty Authorization header was sent; some providers " +
					"reject that outright, unlike an absent header")
			}
		})
	}
}

// TestGoogleEmbedderRequestsIndexableDimension guards the pgvector constraint
// that made the Google provider unusable: gemini-embedding-001 returns 3072
// dimensions by default, and pgvector refuses to build an HNSW index above
// 2000 ("column cannot have more than 2000 dimensions", SQLSTATE 54000), so
// the server died on schema init before serving a single request.
//
// Two things have to hold. The default dimension must stay indexable, and the
// request must actually carry outputDimensionality: without it the API ignores
// the configured dim and returns 3072 regardless, which is the exact shape of
// the original failure.
func TestGoogleEmbedderRequestsIndexableDimension(t *testing.T) {
	const pgvectorHNSWLimit = 2000

	h := NewGoogleEmbedder("", "", 0).(httpEmbedder)
	if h.Dimension() > pgvectorHNSWLimit {
		t.Errorf("default dimension = %d, want <= %d so pgvector can index it",
			h.Dimension(), pgvectorHNSWLimit)
	}

	body, err := json.Marshal(h.body("x"))
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if !strings.Contains(string(body), "outputDimensionality") {
		t.Errorf("request body = %s, want outputDimensionality so the API "+
			"projects down instead of returning its 3072-dim default", body)
	}

	// An explicit dimension must reach the wire, not just the struct field.
	custom := NewGoogleEmbedder("", "", 768).(httpEmbedder)
	cb, err := json.Marshal(custom.body("x"))
	if err != nil {
		t.Fatalf("marshal custom body: %v", err)
	}
	if !strings.Contains(string(cb), "768") {
		t.Errorf("request body = %s, want the configured dimension 768", cb)
	}
}
