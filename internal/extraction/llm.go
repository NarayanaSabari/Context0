package extraction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NarayanaSabari/Kora/internal/metrics"
	"github.com/NarayanaSabari/Kora/pkg/model"
)

// Extractor turns raw conversation text into structured memories.
//
// Two implementations exist, and they trade quality against dependencies:
//
//   - RuleExtractor (the default) needs nothing but CPU. It scans line by
//     line, so it is fast, free and private, but it transcribes rather than
//     distils: one memory per utterance, pronouns unresolved, questions and
//     acknowledgements stored as memories.
//   - LLMExtractor sends the conversation to a chat-completions endpoint and
//     asks for standalone facts. It costs a network round trip and API spend,
//     and produces memories that are meaningful when retrieved alone.
//
// The distinction matters because retrieval returns a single memory without
// the conversation around it. "He hates thunderstorms" is a useful sentence in
// context and useless as a stored memory.
type Extractor interface {
	// Extract converts conversation text into memories. It returns an error
	// only for programming errors, never for provider failures: Extract sits
	// on a write path where the caller holds no other copy of the
	// conversation, so a failing provider must degrade rather than discard.
	Extract(conversation string) ([]ExtractedMemory, error)
}

// RuleExtractor is the zero-dependency default, wrapping the package-level
// Extract function in the Extractor interface.
type RuleExtractor struct{}

// Extract runs the rule-based scanner. The error is always nil; it exists to
// satisfy the interface.
func (RuleExtractor) Extract(conversation string) ([]ExtractedMemory, error) {
	return Extract(conversation), nil
}

// extractionPrompt instructs the model to distil rather than transcribe.
//
// Every rule here corresponds to a failure mode observed in real extractor
// output on LoCoMo data: fragments that cannot be understood alone, pronouns
// with no referent, questions stored as facts, speaker attribution lost
// because the rule-based extractor stripped the "Name:" prefix, and -- from
// the LLM pass itself -- specifics generalised away, where "moved from Sweden"
// was stored as "moved from her home country" and the question asking which
// country became unanswerable from a memory that had just been retrieved
// correctly.
const extractionPrompt = `Extract durable memories from this conversation.

Each memory must be a standalone statement that is still meaningful when read
on its own, months later, with none of this conversation available:

- Name the person. Write "Caroline adopted a dog", never "she adopted a dog".
- Resolve every pronoun and reference against the conversation.
- Keep every specific exactly as stated: names of people, places, books,
  organisations, dates, and quantities. Never replace a specific with a
  category. "moved from Sweden" must not become "moved from her home country",
  and "read Charlotte's Web" must not become "read a book as a child".
- Resolve every relative time reference against the date the line was spoken.
  "yesterday" said on 8 May 2023 becomes "on 7 May 2023"; "last week" becomes
  "the week before 9 June 2023". Never keep the relative phrase: a memory
  saying "last week" is unreadable on its own, because whoever reads it later
  has no idea which week.
- Keep the qualifiers that make a fact answerable. "counseling for transgender
  people" is a different fact from "counseling".
- Merge statements that describe one fact into a single memory.
- Skip greetings, acknowledgements, questions, and anything with no lasting value.
- Record what was said as fact, not that a sentence was uttered.

Classify each memory:
- "semantic": stable facts, preferences, relationships, attributes
- "episodic": events that happened at a particular time
- "procedural": habits, routines, how someone does something

Also name the entities each memory is about, so memories about the same person
or place can be connected:

- People, animals, places, organisations, products, events, and titles of
  works. Write each one as it appears: "Caroline", "Biscuit", "Sweden",
  "Charlotte's Web".
- Never dates, times, quantities, money, or percentages. "Monday" and "April"
  are not entities: nearly every memory mentions one, so they connect
  everything to everything and carry no information.
- Only entities the memory is actually about. At most six.
- Omit the field entirely when the memory names nothing.

Return ONLY a JSON array, no prose and no code fence:
[{"content": "...", "type": "semantic", "tags": ["topic"], "entities": ["Caroline"]}]

Return [] if the conversation contains nothing worth remembering.

Conversation:
`

// llmDefaultClient carries a timeout, unlike http.DefaultClient.
//
// Extract holds no database connection while this runs, but it does occupy a
// request goroutine and the caller's own deadline, and a provider that accepts
// a connection then stalls would otherwise hang until the client gives up.
// The budget is larger than the embedding client's 30s because extraction
// generates many more output tokens than an embedding call.
var llmDefaultClient = &http.Client{Timeout: 120 * time.Second}

// LLMExtractor extracts memories using any OpenAI-compatible
// chat-completions endpoint: OpenAI itself, Ollama, vLLM, LiteLLM, OpenRouter,
// or Gemini through its compatibility layer.
//
// One wire format rather than one implementation per vendor, for the same
// reason internal/embedding shares httpEmbedder: the differences are a URL and
// a model name, and a self-hosted engine should not need a code change to
// point at a different provider.
type LLMExtractor struct {
	url    string
	apiKey string
	model  string

	// fallback produces memories when the provider fails. Never nil.
	fallback Extractor
}

// NewLLMExtractor creates an extractor backed by an OpenAI-compatible
// chat-completions endpoint.
//
// baseURL may be either a bare host ("https://api.openai.com") or a full
// endpoint; the standard path is appended when missing, so an operator can
// paste whichever form their provider documents.
func NewLLMExtractor(baseURL, apiKey, modelName string) *LLMExtractor {
	endpoint := strings.TrimSuffix(baseURL, "/")
	if !strings.Contains(endpoint, "/chat/completions") {
		endpoint += "/v1/chat/completions"
	}
	// Guard against the "/v1/v1/..." that results from a base URL that already
	// carries the version segment, which is how most providers document it.
	endpoint = strings.Replace(endpoint, "/v1/v1/", "/v1/", 1)

	return &LLMExtractor{
		url:      endpoint,
		apiKey:   apiKey,
		model:    modelName,
		fallback: RuleExtractor{},
	}
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// llmMemory is the per-memory shape requested from the model. Type is a plain
// string rather than model.MemoryType because it is untrusted input: it is
// validated in toExtracted before it becomes a domain value.
type llmMemory struct {
	Content  string   `json:"content"`
	Type     string   `json:"type"`
	Tags     []string `json:"tags"`
	Entities []string `json:"entities"`
}

// Extract asks the model for standalone memories, falling back to rule-based
// extraction if anything goes wrong.
//
// The fallback is not defensive padding. Extract is a write path: the caller
// posts a conversation and keeps no copy of the memories it would have
// produced, so an error return means the content is lost. Rule-based
// extraction is worse but never fails and needs no network, which makes it the
// right floor. Every fallback is recorded here -- a warning and the
// kora_extraction_fallbacks_total counter -- because the caller cannot see
// it: Extract returns success either way.
func (e *LLMExtractor) Extract(conversation string) ([]ExtractedMemory, error) {
	memories, err := e.extractViaLLM(conversation)
	if err != nil {
		// The error is not returned, because a failing provider must not cost
		// the caller a conversation. It is recorded here rather than left to
		// the caller, which is a change: the comment used to claim the service
		// layer surfaced it, and the service layer cannot -- Extract returns
		// success, so there is nothing there to surface.
		//
		// Found by running the engine against a small local model, seeing
		// transcript lines come back as memories, and having to infer from
		// their shape that the LLM pass had failed. A deployment whose
		// provider has been down for a week looked exactly like a healthy one.
		metrics.ExtractionFallbacks.WithLabelValues("error").Inc()
		slog.Warn("LLM extraction failed; the rule-based extractor answered instead",
			slog.String("model", e.model),
			slog.Any("error", err))
		return e.fallback.Extract(conversation)
	}
	if len(memories) == 0 {
		// An empty result is ambiguous -- either the conversation genuinely
		// held nothing, or the model misunderstood the task. The rule-based
		// pass is cheap and settles it: if it also finds nothing, there was
		// nothing.
		fallback, ferr := e.fallback.Extract(conversation)

		// The disagreement is the signal, not the empty result on its own. A
		// conversation of greetings genuinely holds nothing and both passes
		// say so, which is normal and must not warn -- an operator trained to
		// ignore this warning will ignore the one that matters. A model that
		// returns nothing from a conversation the rule-based scanner finds
		// plenty in is not reading the task, and that is worth waking up for.
		//
		// Measured while validating this: a 3B model returned an empty array
		// for a transcript the rule-based pass turned into 26 memories, and
		// the only visible symptom was memories that read like transcript
		// lines.
		if len(fallback) > 0 {
			metrics.ExtractionFallbacks.WithLabelValues("empty").Inc()
			slog.Warn("LLM extraction found nothing in a conversation the rule-based extractor did; the model may not be following the task",
				slog.String("model", e.model),
				slog.Int("rule_based_memories", len(fallback)))
		} else {
			slog.Debug("neither extractor found anything in this conversation",
				slog.String("model", e.model))
		}

		return fallback, ferr
	}
	return memories, nil
}

func (e *LLMExtractor) extractViaLLM(conversation string) ([]ExtractedMemory, error) {
	payload, err := json.Marshal(chatRequest{
		Model: e.model,
		Messages: []chatMessage{
			{Role: "user", Content: extractionPrompt + conversation},
		},
		// Extraction is a reading task, not a creative one: the same
		// conversation should produce the same memories on every call.
		Temperature: 0,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, e.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", redactExtractionErr(err, e.url))
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := llmDefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extraction request: %w", redactExtractionErr(err, e.url))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Bounded: a provider erroring with a large body should not put all of
		// it into a log line.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("extraction error (%d): %s", resp.StatusCode, body)
	}

	var decoded chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, errors.New("provider returned no choices")
	}

	return parseExtracted(decoded.Choices[0].Message.Content)
}

// parseExtracted turns the model's message content into validated memories.
func parseExtracted(content string) ([]ExtractedMemory, error) {
	var raw []llmMemory
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &raw); err != nil {
		return nil, fmt.Errorf("model did not return a JSON array: %w", err)
	}

	memories := make([]ExtractedMemory, 0, len(raw))
	for _, m := range raw {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			// A memory with no content can never match a query.
			continue
		}
		memories = append(memories, ExtractedMemory{
			Content: content,
			Type:    parseMemoryType(m.Type),
			Tags:    m.Tags,
			// The model's entities are cleaned through the same filters the
			// heuristic extractor uses, and fall back to it when the model
			// returns none.
			//
			// Not trusted verbatim, because a model that has been told six
			// times to omit dates will still return "April" -- and an entity
			// nearly every memory mentions connects everything to everything,
			// which is worse than no graph at all. The filters are cheap and
			// the failure they prevent is not.
			Entities: reconcileEntities(m.Entities, content),
		})
	}
	return memories, nil
}

// reconcileEntities validates the model's entities and falls back to the
// heuristic extractor when it returned nothing usable.
//
// The fallback matters because entities are what make multi-hop traversal
// work: a model that quietly stops returning the field would disable the
// feature with no other symptom, and the heuristic is free.
func reconcileEntities(fromModel []string, content string) []string {
	out := make([]string, 0, len(fromModel))
	seen := make(map[string]bool, len(fromModel))

	for _, e := range fromModel {
		e = strings.TrimSpace(e)
		if !isPlausibleEntity(e) {
			continue
		}
		key := NormalizeEntity(e)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}

	if len(out) == 0 {
		return ExtractEntities(content)
	}
	if len(out) > maxEntitiesPerMemory {
		out = out[:maxEntitiesPerMemory]
	}
	return out
}

// parseMemoryType maps the model's type string onto the engine's vocabulary.
//
// Unrecognised values fall back to semantic rather than being rejected: a
// misclassified fact is still a fact worth keeping, whereas an unrecognised
// type would reach ranking.TypePriority as a zero value and score the memory
// at zero. Semantic is the safe default because it carries the highest type
// priority, so a misclassification cannot bury a memory.
func parseMemoryType(s string) model.MemoryType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "episodic":
		return model.MemoryTypeEpisodic
	case "procedural":
		return model.MemoryTypeProcedural
	default:
		return model.MemoryTypeSemantic
	}
}

// stripCodeFence removes a Markdown code fence around a JSON payload.
//
// Models wrap JSON in ```json fences regardless of instructions not to.
// Without this the response fails to parse and every extraction silently falls
// back to the rule-based path while appearing to work.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence and its optional language tag.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// redactExtractionErr strips credentials from a URL echoed in a transport
// error, mirroring internal/embedding: net/http wraps errors in *url.Error,
// which prints the full request URL, and these errors are logged.
func redactExtractionErr(err error, rawURL string) error {
	if err == nil {
		return nil
	}
	u, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return errors.New(strings.ReplaceAll(err.Error(), rawURL, "[unparseable url]"))
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
	return errors.New(strings.ReplaceAll(err.Error(), rawURL, u.String()))
}

// ProviderConfig selects and configures an Extractor. It mirrors
// embedding.ProviderConfig so the two backends are configured the same way.
type ProviderConfig struct {
	// Provider is "rule" (or empty) for the zero-dependency scanner, or "llm"
	// for an OpenAI-compatible chat endpoint.
	Provider string
	// Model is the chat model name, e.g. "gpt-4o-mini". Ignored by "rule".
	Model string
	// APIKey authenticates against the provider. Not required for a local
	// endpoint such as Ollama.
	APIKey string
	// BaseURL is the chat-completions endpoint.
	BaseURL string
}

// NewFromConfig builds an Extractor from configuration.
//
// An unknown provider is an error rather than a silent fallback: an operator
// who sets KORA_EXTRACTION_PROVIDER to a typo has asked for higher-quality
// extraction, and quietly serving the rule-based scanner instead would look
// identical to it working while every stored memory is worse than requested.
func NewFromConfig(cfg ProviderConfig) (Extractor, error) {
	switch cfg.Provider {
	case "rule", "":
		return RuleExtractor{}, nil

	case "llm":
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("llm extraction requires KORA_EXTRACTION_BASE_URL")
		}
		if cfg.Model == "" {
			return nil, fmt.Errorf("llm extraction requires KORA_EXTRACTION_MODEL")
		}
		return NewLLMExtractor(cfg.BaseURL, cfg.APIKey, cfg.Model), nil

	default:
		return nil, fmt.Errorf("unknown extraction provider: %q (available: rule, llm)", cfg.Provider)
	}
}
