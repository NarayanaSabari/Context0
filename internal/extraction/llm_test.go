package extraction

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/NarayanaSabari/Kora/internal/metrics"
	"github.com/NarayanaSabari/Kora/pkg/model"
)

// The conversation used across these tests. It is deliberately ordinary
// dialogue rather than a list of facts, because that is what the rule-based
// extractor handles badly: it stores one memory per utterance, so questions,
// acknowledgements and pronoun-bearing fragments all become "memories".
const sampleConversation = `Caroline: Hey Mel! How are you doing?
Melanie: Pretty good thanks! What have you been up to?
Caroline: I adopted a rescue dog last month. His name is Biscuit.
Melanie: Aww that sounds lovely.
Caroline: He hates thunderstorms, so I walk him early in the morning.`

// stubLLM returns a server that responds with the given assistant message
// content, in the OpenAI chat-completions shape every supported provider
// speaks.
func stubLLM(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"content": reply}},
			},
		})
	}))
}

// TestLLMExtractor_DistilsRatherThanTranscribes is the reason this extractor
// exists.
//
// The rule-based extractor is one-memory-per-line: it classifies and filters,
// but never merges or rewrites, so "He hates thunderstorms" is stored with no
// record of who "he" is, and "Aww that sounds lovely" is stored as a memory at
// all. Measured on a LoCoMo corpus, 2,360 of 6,760 stored memories contained a
// question mark. Retrieval later returns those fragments alone, with no
// surrounding conversation to resolve them against.
//
// An LLM pass is asked for standalone facts instead. This test pins the
// contract with the rest of the engine -- what a returned memory must look
// like -- rather than the model's wording, which is not ours to assert on.
func TestLLMExtractor_DistilsRatherThanTranscribes(t *testing.T) {
	srv := stubLLM(t, `[
	  {"content": "Caroline adopted a rescue dog named Biscuit last month.", "type": "episodic", "tags": ["pets", "biscuit"]},
	  {"content": "Caroline's dog Biscuit is afraid of thunderstorms.", "type": "semantic", "tags": ["pets", "biscuit"]},
	  {"content": "Caroline walks Biscuit early each morning.", "type": "procedural", "tags": ["pets", "routine"]}
	]`)
	defer srv.Close()

	e := NewLLMExtractor(srv.URL, "test-key", "test-model")
	got, err := e.Extract(sampleConversation)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected the 3 distilled memories the model returned, got %d", len(got))
	}

	// Greetings and acknowledgements must not survive as memories.
	for _, m := range got {
		if strings.Contains(m.Content, "?") {
			t.Errorf("a question was stored as a memory: %q", m.Content)
		}
	}

	// Types must round-trip into the engine's own vocabulary, since ranking
	// and profile-building both switch on them.
	wantTypes := map[model.MemoryType]bool{
		model.MemoryTypeEpisodic:   false,
		model.MemoryTypeSemantic:   false,
		model.MemoryTypeProcedural: false,
	}
	for _, m := range got {
		if _, ok := wantTypes[m.Type]; !ok {
			t.Errorf("memory %q has type %q, which is not one of the engine's three types",
				m.Content, m.Type)
		}
		wantTypes[m.Type] = true
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("type %q was in the model's response but did not survive parsing", typ)
		}
	}

	// Tags drive graph linking, so they have to survive too.
	if len(got[0].Tags) == 0 {
		t.Error("tags were dropped; they are one of the two inputs to auto-linking")
	}
}

// TestLLMExtractor_FallsBackWhenProviderFails pins that an unavailable or
// broken LLM degrades extraction quality rather than losing the conversation.
//
// Extract is a write path. A caller sending a conversation has no copy of the
// memories it would have produced, so returning an error here means the
// content is simply gone. The rule-based extractor needs no network and always
// produces something, which makes it the right floor to fall back to.
func TestLLMExtractor_FallsBackWhenProviderFails(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"5xx": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"malformed json": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "not json at all"}}]}`))
		},
		"empty array": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "[]"}}]}`))
		},
	}

	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			e := NewLLMExtractor(srv.URL, "test-key", "test-model")
			got, err := e.Extract(sampleConversation)
			if err != nil {
				t.Fatalf("extraction returned an error instead of falling back: %v -- "+
					"the caller has no other copy of this conversation", err)
			}
			if len(got) == 0 {
				t.Error("extraction produced nothing: a failing LLM must degrade to " +
					"rule-based extraction, not silently discard the conversation")
			}
		})
	}
}

// TestLLMExtractor_ToleratesFencedJSON pins that a response wrapped in a
// Markdown code fence still parses.
//
// Models return fenced JSON constantly, whatever the prompt says, and treating
// that as a parse failure would silently drop every extraction back to the
// rule-based path while looking like it was working.
func TestLLMExtractor_ToleratesFencedJSON(t *testing.T) {
	srv := stubLLM(t, "```json\n[{\"content\": \"Caroline owns a dog named Biscuit.\", \"type\": \"semantic\"}]\n```")
	defer srv.Close()

	got, err := NewLLMExtractor(srv.URL, "k", "m").Extract(sampleConversation)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("fenced JSON did not parse: got %d memories, want 1", len(got))
	}
	if got[0].Content != "Caroline owns a dog named Biscuit." {
		t.Errorf("content mangled by fence stripping: %q", got[0].Content)
	}
}

// TestLLMExtractor_RejectsUnusableMemories pins the validation applied to
// whatever the model returns.
//
// The model is an untrusted input here. An empty content string stores a
// memory that can never match a query, and an unrecognised type would reach
// ranking's TypePriority map as a zero value and score every such memory at 0.
func TestLLMExtractor_RejectsUnusableMemories(t *testing.T) {
	srv := stubLLM(t, `[
	  {"content": "", "type": "semantic"},
	  {"content": "   ", "type": "semantic"},
	  {"content": "Caroline owns a dog named Biscuit.", "type": "nonsense-type"},
	  {"content": "Caroline lives in Lisbon.", "type": "semantic"}
	]`)
	defer srv.Close()

	got, err := NewLLMExtractor(srv.URL, "k", "m").Extract(sampleConversation)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	for _, m := range got {
		if strings.TrimSpace(m.Content) == "" {
			t.Error("an empty memory was kept; it can never match a query")
		}
		switch m.Type {
		case model.MemoryTypeEpisodic, model.MemoryTypeSemantic, model.MemoryTypeProcedural:
		default:
			t.Errorf("memory %q kept unrecognised type %q, which ranking scores as zero",
				m.Content, m.Type)
		}
	}

	// The unrecognised type is a classification error, not a content error:
	// the fact itself is still worth keeping, defaulted to a safe type.
	if len(got) != 2 {
		t.Errorf("expected the 2 memories with usable content, got %d", len(got))
	}
}

// TestLLMExtractor_SendsTheConversation guards against the prompt being built
// without the conversation in it, which would make the extractor confidently
// return memories about nothing.
func TestLLMExtractor_SendsTheConversation(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
	}))
	defer srv.Close()

	_, _ = NewLLMExtractor(srv.URL, "k", "m").Extract(sampleConversation)

	if !strings.Contains(body, "Biscuit") {
		t.Error("the conversation was not included in the request to the model")
	}
	if !strings.Contains(body, "test-model") && !strings.Contains(body, `"model"`) {
		t.Error("the request carries no model field")
	}
}

// TestExtractionPrompt_DemandsSpecifics pins the instruction that keeps named
// entities out of the generaliser.
//
// The prompt is the whole implementation of this extractor, so it is the thing
// worth testing. Without an explicit rule, models paraphrase specifics into
// categories: on LoCoMo, "Caroline moved from Sweden" was stored as "Caroline
// moved from her home country", and the question asking which country became
// unanswerable from a memory that retrieval had surfaced correctly. Losing a
// proper noun at write time is unrecoverable, unlike a ranking mistake.
func TestExtractionPrompt_DemandsSpecifics(t *testing.T) {
	for _, want := range []string{
		"Never replace a specific with a",
		"names of people, places",
		"Resolve every pronoun",
		// A memory saying "last week" cannot be read on its own: retrieval
		// returns it without the conversation that dated it. Observed on
		// LoCoMo as "gave a talk last week (from 9 June 2023)", against a
		// ground truth of "The week before 9 June 2023".
		"Resolve every relative time reference",
	} {
		if !strings.Contains(extractionPrompt, want) {
			t.Errorf("the extraction prompt no longer instructs %q; "+
				"models generalise proper nouns away without it", want)
		}
	}
}

// A provider that needs no key must not be sent an empty credential.
//
// This is the configuration charts/kora/values-quality.yaml recommends: a local
// Ollama, serving an OpenAI-compatible API, with no key. Sending
// "Authorization: Bearer " to it is at best noise and at worst a 401 from a
// gateway that parses the header before deciding it is empty -- and the failure
// would look like a broken extractor rather than a spurious header.
func TestLLMExtractor_SendsNoAuthorizationHeaderWithoutAKey(t *testing.T) {
	var authSeen string
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		_, sawHeader = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
	}))
	defer srv.Close()

	if _, err := NewLLMExtractor(srv.URL, "", "test-model").Extract(sampleConversation); err != nil {
		t.Fatalf("Extract against a keyless provider: %v", err)
	}

	if sawHeader {
		t.Errorf("sent Authorization: %q to a provider configured without a key", authSeen)
	}
}

// And a provider that does need one gets it.
func TestLLMExtractor_SendsTheKeyWhenThereIsOne(t *testing.T) {
	var authSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
	}))
	defer srv.Close()

	if _, err := NewLLMExtractor(srv.URL, "sk-secret", "test-model").Extract(sampleConversation); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if authSeen != "Bearer sk-secret" {
		t.Errorf("Authorization = %q, want the configured key: a provider that requires "+
			"authentication would reject every extraction", authSeen)
	}
}

// A provider that fails must be visible, not just survivable.
//
// The fallback to the rule-based extractor is deliberate: losing a
// conversation because a provider is down is worse than storing a cruder
// version of it. But Extract returns success either way, so a deployment whose
// provider has been failing for a week looked exactly like a healthy one, and
// the only symptom was memories that read like transcript lines.
//
// Found by running the engine against a small local model and having to infer
// from the shape of the output that the LLM pass had failed.
func TestLLMExtractor_FallbackIsCounted(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
		reason  string
	}{
		{
			name: "a provider that errors",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			reason: "error",
		},
		{
			name: "a provider that answers with nothing",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
			},
			reason: "empty",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := counterValue(t, tt.reason)

			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			got, err := NewLLMExtractor(srv.URL, "k", "m").Extract(sampleConversation)
			if err != nil {
				t.Fatalf("a failing provider cost the caller the conversation: %v", err)
			}
			if len(got) == 0 {
				t.Error("the rule-based fallback produced nothing; the conversation was lost silently")
			}

			if after := counterValue(t, tt.reason); after <= before {
				t.Errorf("kora_extraction_fallbacks_total{reason=%q} did not move (%v to %v): "+
					"a degraded deployment is indistinguishable from a healthy one",
					tt.reason, before, after)
			}
		})
	}
}

// A conversation that is genuinely empty must not count as a fallback.
//
// The "empty" reason means disagreement: the model found nothing where the
// rule-based pass found memories. When both extractors agree there is nothing,
// that is healthy traffic -- a conversation of greetings -- and a counter that
// fires on healthy traffic cannot be alerted on.
func TestLLMExtractor_AgreedEmptyIsNotCounted(t *testing.T) {
	before := counterValue(t, "empty")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
	}))
	defer srv.Close()

	// "hi" carries no extractable facts: the rule-based pass also finds
	// nothing, so the two extractors agree.
	got, err := NewLLMExtractor(srv.URL, "k", "m").Extract("user: hi")
	if err != nil {
		t.Fatalf("an empty conversation must not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no memories from an empty conversation, got %d", len(got))
	}

	if after := counterValue(t, "empty"); after != before {
		t.Errorf("kora_extraction_fallbacks_total{reason=%q} moved (%v to %v) on healthy traffic: "+
			"an operator alerted on this counter would be paged by every small talk",
			"empty", before, after)
	}
}

// counterValue reads one label of the fallback counter.
func counterValue(t *testing.T, reason string) float64 {
	t.Helper()

	c, err := metrics.ExtractionFallbacks.GetMetricWithLabelValues(reason)
	if err != nil {
		t.Fatalf("counter %q: %v", reason, err)
	}
	return testutil.ToFloat64(c)
}
