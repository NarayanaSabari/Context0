package extraction

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	} {
		if !strings.Contains(extractionPrompt, want) {
			t.Errorf("the extraction prompt no longer instructs %q; "+
				"models generalise proper nouns away without it", want)
		}
	}
}
