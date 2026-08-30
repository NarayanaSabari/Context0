// entity.go names the people, places, organisations and works a memory is
// about, so the graph can link memories through what they share rather than
// through how similar their wording is.
//
// # Why this exists
//
// Multi-hop questions are the weakest category on LoCoMo, at 65%. The graph
// links memory to memory by embedding similarity, which clusters paraphrases
// of one fact rather than connecting `Caroline` to her dog `Biscuit` to
// `thunderstorms`. A question needing two hops has nothing to traverse.
//
// Both Mem0 and Zep model entities as first-class objects and link memories
// through them. Mem0's implementation (`mem0/utils/entity_extraction.py`) uses
// spaCy NER, keeping PERSON, ORG, GPE, LOC, FAC, PRODUCT, WORK_OF_ART, EVENT,
// NORP, LAW and LANGUAGE, and explicitly rejecting DATE, TIME, CARDINAL,
// ORDINAL, QUANTITY, MONEY and PERCENT. Crucially it makes no LLM call on the
// write path.
//
// # Why not spaCy
//
// Go has no spaCy, and a CGo binding is out: every dependency in this project
// is OSI-approved and pure Go. That leaves asking the extraction LLM, which is
// free because exactly one call per conversation is already made and its
// schema already returns tags, and a heuristic for when no LLM is configured.
//
// Both exist here. The LLM path is in llm.go and produces better entities; the
// heuristic below is what the zero-dependency default gets, and what a
// provider outage falls back to. Neither adds a network round trip.
//
// # The over-generation problem
//
// Entity quality bounds everything downstream. Too many entities make a dense
// graph carrying no information, which is exactly the failure a2f53ac hit with
// semantic linking, so this file is far more concerned with rejecting than
// with recall. Without part-of-speech tags the only signal available is
// capitalisation, and English capitalises the first word of every sentence, so
// the filters below carry most of the weight.
package extraction

import (
	"regexp"
	"strings"

	"github.com/NarayanaSabari/Kora/pkg/model"
)

// maxEntitiesPerMemory bounds how many entities one memory may contribute.
//
// Every entity becomes a node and an edge, so an over-generating extractor
// grows the graph with memories x entities and makes every later traversal
// costlier -- the same arithmetic that put caps on autoLinkByTags and
// detectAndSupersede. A memory that appears to be about more than a handful of
// entities is usually a memory the extractor failed to split.
const maxEntitiesPerMemory = 6

// minEntityLen is the shortest string treated as an entity. Initials and
// single letters carry no linking value and collide constantly.
const minEntityLen = 2

// quotedSpan matches a double-quoted string, which is how titles of works
// appear in conversation: `read "Charlotte's Web"`. Mem0 takes quoted strings
// as entities for the same reason.
//
// Double quotes only. English uses the apostrophe and the closing single quote
// interchangeably, so a single-quote pattern cuts `"Charlotte's Web"` at the
// possessive and yields `Charlotte` -- a different entity, and one that links
// the memory to every other Charlotte in the corpus.
//
// Bounded to one line and to 60 characters, so an unbalanced quote cannot
// swallow a whole conversation.
var quotedSpan = regexp.MustCompile(`["“]([^"”\n]{2,60})["”]`)

// capitalisedRun matches one or more consecutive capitalised words, optionally
// joined by a lowercase particle: "New York", "Bank of England",
// "Charlotte's Web".
//
// The particle list is closed and short. Allowing arbitrary lowercase words
// between capitals would join two unrelated names across a sentence boundary.
var capitalisedRun = regexp.MustCompile(`\b[A-Z][\p{L}\p{N}'’-]*(?:\s+(?:of|the|de|van|von|del|la|le|and|&)\s+[A-Z][\p{L}\p{N}'’-]*|\s+[A-Z][\p{L}\p{N}'’-]*)*`)

// sentenceStart matches the position after a sentence boundary, used to tell a
// name from a word capitalised only because it begins a sentence.
var sentenceStart = regexp.MustCompile(`(?:^|[.!?]["”']?\s+)$`)

// ExtractEntities names the entities a single memory is about.
//
// Two sources, matching Mem0's minus the part-of-speech-dependent one:
// capitalised runs, and quoted spans. Noun compounds are deliberately not
// attempted -- Mem0 filters those against a list of heads too generic to be
// useful, which only works because spaCy has already identified them as noun
// compounds rather than as any other capitalised text.
//
// The result is normalised, deduplicated, and capped. Order is the order of
// first appearance, so the cap keeps what the memory leads with rather than
// whatever the map happened to yield.
func ExtractEntities(content string) []string {
	var out []string
	seen := make(map[string]bool)

	add := func(name string) {
		name = strings.TrimSpace(strings.Trim(name, `.,;:!?"'“”()[]{}`))
		if !isPlausibleEntity(name) {
			return
		}
		key := NormalizeEntity(name)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, name)
	}

	// Quoted spans first: a title in quotes is unambiguous, and taking it
	// before the capitalisation pass means it survives the cap.
	//
	// Their ranges are remembered so the capitalisation pass does not also
	// emit the words inside them: `"Charlotte's Web"` is one entity, and
	// adding `Charlotte` alongside it links the memory to every other
	// Charlotte in the corpus.
	quoted := quotedSpan.FindAllStringIndex(content, -1)
	for _, m := range quotedSpan.FindAllStringSubmatch(content, -1) {
		add(m[1])
	}

	for _, loc := range capitalisedRun.FindAllStringIndex(content, -1) {
		span := content[loc[0]:loc[1]]

		if overlaps(quoted, loc) {
			continue
		}

		// A run that starts a sentence begins with a word English capitalises
		// for grammatical reasons, so its first word has to earn its place
		// against the stoplist. "The deadline is in April" contributes
		// nothing; "Caroline adopted a dog" contributes Caroline.
		//
		// Only the leading word is at issue -- a capital anywhere later in a
		// sentence is already evidence of a name -- so a rejected opener is
		// trimmed off rather than discarding the whole run: "The Bank of
		// England refused" keeps "Bank of England".
		if sentenceStart.MatchString(content[:loc[0]]) && isCommonSentenceOpener(firstWord(span)) {
			span = strings.TrimSpace(strings.TrimPrefix(span, firstWord(span)))
		}

		// A run that was nothing but its opener is now empty, and add rejects
		// it: isPlausibleEntity has a minimum length. There is deliberately no
		// guard here for that case -- it would be a second place deciding what
		// is not an entity, and the two would drift.

		add(span)
	}

	if len(out) > maxEntitiesPerMemory {
		out = out[:maxEntitiesPerMemory]
	}
	return out
}

// overlaps reports whether a span intersects any of the given ranges.
func overlaps(ranges [][]int, span []int) bool {
	for _, r := range ranges {
		if span[0] < r[1] && r[0] < span[1] {
			return true
		}
	}
	return false
}

// NormalizeEntity is model.NormalizeEntity, aliased so the names this package
// produces and the node identity the repository MERGEs on can never drift
// apart. A difference between the two would create a second node for an entity
// that already exists, which is the linking failure entities exist to fix.
func NormalizeEntity(name string) string { return model.NormalizeEntity(name) }

// firstWord returns the leading word of a span.
func firstWord(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// isPlausibleEntity applies the rejections that keep the graph sparse enough
// to carry information.
//
// The categories mirror the ones Mem0 explicitly discards from spaCy's output
// -- DATE, TIME, CARDINAL, ORDINAL, QUANTITY -- because they are shared by
// unrelated memories in enormous numbers. Linking every memory mentioning
// "Monday" to every other one produces a hub with no meaning, and hubs are
// what make a traversal expensive.
func isPlausibleEntity(name string) bool {
	if len([]rune(name)) < minEntityLen || len(name) > 80 {
		return false
	}

	lower := strings.ToLower(name)

	// A temporal or numeric expression is not an entity, however capitalised.
	for _, w := range strings.Fields(lower) {
		w = strings.Trim(w, `.,;:!?"'`)
		if temporalWords[w] {
			return false
		}
	}
	if isNumeric(lower) {
		return false
	}

	// Must contain a letter. "42" and "III" carry no linking value.
	hasLetter := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127 {
			hasLetter = true
			break
		}
	}
	return hasLetter
}

// isNumeric reports whether every token is a number or a number word.
func isNumeric(lower string) bool {
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		f = strings.Trim(f, `.,;:!?%$`)
		if numberWords[f] {
			continue
		}
		digits := true
		for _, r := range f {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if !digits {
			return false
		}
	}
	return true
}

// temporalWords are the DATE and TIME expressions Mem0 rejects from spaCy's
// output. They are capitalised in ordinary English and shared by unrelated
// memories in bulk, so admitting them builds hubs rather than links.
var temporalWords = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
	"january": true, "february": true, "march": true, "april": true,
	"may": true, "june": true, "july": true, "august": true,
	"september": true, "october": true, "november": true, "december": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "sept": true, "oct": true,
	"nov": true, "dec": true,
	"today": true, "tomorrow": true, "yesterday": true,
	"morning": true, "afternoon": true, "evening": true, "night": true,
	"week": true, "month": true, "year": true, "day": true,
	"spring": true, "summer": true, "autumn": true, "fall": true, "winter": true,
	"am": true, "pm": true, "christmas": true, "easter": true,
}

// numberWords are CARDINAL and ORDINAL expressions, rejected for the same
// reason as temporalWords.
var numberWords = map[string]bool{
	"one": true, "two": true, "three": true, "four": true, "five": true,
	"six": true, "seven": true, "eight": true, "nine": true, "ten": true,
	"eleven": true, "twelve": true, "hundred": true, "thousand": true,
	"million": true, "billion": true,
	"first": true, "second": true, "third": true, "fourth": true, "fifth": true,
	"sixth": true, "seventh": true, "eighth": true, "ninth": true, "tenth": true,
}

// sentenceOpeners are words that begin an English sentence and are capitalised
// for that reason alone.
//
// This list is the single most important filter here, because without
// part-of-speech tags every sentence-initial word looks exactly like a name.
// It is what stops "The", "When" and "Later" from becoming graph nodes that
// half the corpus mentions.
var sentenceOpeners = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true,
	"these": true, "those": true, "it": true, "its": true, "there": true,
	"here": true, "he": true, "she": true, "they": true, "we": true,
	"you": true, "i": true, "his": true, "her": true, "their": true,
	"our": true, "your": true, "my": true,
	"what": true, "when": true, "where": true, "who": true, "why": true,
	"how": true, "which": true, "whose": true,
	"and": true, "but": true, "or": true, "so": true, "if": true,
	"because": true, "while": true, "after": true, "before": true,
	"since": true, "although": true, "though": true, "unless": true,
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"do": true, "does": true, "did": true, "have": true, "has": true,
	"had": true, "will": true, "would": true, "can": true, "could": true,
	"should": true, "may": true, "might": true, "must": true,
	"yes": true, "no": true, "not": true, "now": true, "then": true,
	"later": true, "also": true, "just": true, "still": true, "very": true,
	"every": true, "some": true, "any": true, "all": true, "both": true,
	"one": true, "two": true, "for": true, "from": true, "with": true,
	"about": true, "as": true, "at": true, "by": true, "in": true,
	"of": true, "on": true, "to": true, "up": true, "out": true,
	"let": true, "let's": true, "please": true, "thanks": true,
	"hello": true, "hi": true, "hey": true, "okay": true, "ok": true,
	"sure": true, "right": true, "well": true, "actually": true,
}

// isCommonSentenceOpener reports whether a word is capitalised because it
// starts a sentence rather than because it names something.
func isCommonSentenceOpener(word string) bool {
	return sentenceOpeners[strings.ToLower(strings.Trim(word, `.,;:!?"'`))]
}
