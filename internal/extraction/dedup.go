// dedup.go decides when two memories say the same thing, which is what lets
// the write path store one row instead of forty.
//
// The problem it solves, measured on a 40-question LoCoMo corpus: 6,010 stored
// memories expressed 573 distinct facts. `Caroline is transgender.` was stored
// 25 times, and `Caroline is a transgender person.` another 16 times as
// separate rows. Every one of those rows carries an embedding, an index entry,
// and a share of the graph's edges.
//
// This is a cost fix, not an accuracy fix. Measured on retrieved results, only
// 1% of retrieval slots were exact duplicates and 2% near duplicates; ranking
// already suppresses them. What redundancy costs is ingest spend, index size,
// and the consolidation job's workload, all at roughly 10x.
//
// # Why not embedding similarity
//
// The obvious implementation is a cosine threshold, and it is the wrong tool
// here for the same reason documented on relatedStdDevs in internal/service:
// similarity scales belong to the embedder, not the data. Measured on this
// engine's own providers, bag-of-words scores a related pair at 0.55 while
// gemini-embedding-2 scores the *same* pair at 0.88 and unrelated pairs at
// 0.64. No fixed threshold serves both, and Kora supports four providers plus
// any OpenAI-compatible endpoint.
//
// Worse, the failure is asymmetric and destructive. A missed duplicate costs
// one redundant row. A false merge silently destroys a fact, and dense
// embeddings put `moved to Sweden` and `moved from Sweden` within a hair of
// each other. So consolidation here is decided lexically, on evidence that
// does not move when the embedding provider changes: one memory's content
// words must appear in the other, in order, with matching polarity.
package extraction

import (
	"strings"

	"github.com/NarayanaSabari/Kora/pkg/model"
)

// Subsumption is the relationship between two memories' contents.
type Subsumption int

const (
	// Distinct means the two memories carry different information and both
	// must be kept.
	Distinct Subsumption = iota
	// Equivalent means they carry the same information in the same words, up
	// to case, punctuation and spacing.
	Equivalent
	// NewSubsumesOld means the newer memory says everything the older one
	// says, and possibly more, so the older one is redundant.
	NewSubsumesOld
	// OldSubsumesNew means the older memory already says everything the newer
	// one says, so the newer one adds nothing.
	OldSubsumesNew
)

// String renders a Subsumption for test failures and logs.
func (s Subsumption) String() string {
	switch s {
	case Equivalent:
		return "Equivalent"
	case NewSubsumesOld:
		return "NewSubsumesOld"
	case OldSubsumesNew:
		return "OldSubsumesNew"
	default:
		return "Distinct"
	}
}

// Subsumes reports how a newly extracted memory relates to an existing one.
//
// The test is an ordered subsequence over content words: `Caroline is
// transgender` appears, in order, inside `Caroline is a transgender person`,
// so the second says everything the first does. Order is what distinguishes
// this from set containment, which would call `Melanie gave Caroline a book`
// and `Caroline gave Melanie a book` the same fact.
//
// Two guards sit in front of it, because a subsequence match alone is not
// enough evidence to discard a row:
//
//   - Polarity. A statement can be negated, denied or hedged by a single
//     token, which inverts or qualifies the fact while leaving the subsequence
//     intact: "Caroline denies that she owns a car" contains every word of
//     "Caroline owns a car", in order.
//   - Content mass. A memory of one or two content words is a subsequence of
//     almost anything mentioning the same subject.
//
// Numbers and dates need no special case: they are content words, so a memory
// differing only in a quantity fails the subsequence test on that token.
func Subsumes(newer, old string) Subsumption {
	newTokens := contentTokens(newer)
	oldTokens := contentTokens(old)

	// Nothing to compare, or too little to be evidence of anything. The
	// minimum is what stops a bare `Biscuit.` from being folded into every
	// memory that mentions the dog.
	if len(newTokens) < minSubsumptionTokens || len(oldTokens) < minSubsumptionTokens {
		return Distinct
	}

	// Polarity first: a negated, denied or hedged statement and its plain
	// assertion are each other's near-subsequence, and merging them stores
	// something other than what was said.
	if hasPolarityMarker(newTokens) != hasPolarityMarker(oldTokens) {
		return Distinct
	}

	newSubsumesOld := isOrderedSubsequence(oldTokens, newTokens)
	oldSubsumesNew := isOrderedSubsequence(newTokens, oldTokens)

	switch {
	case newSubsumesOld && oldSubsumesNew:
		return Equivalent
	case newSubsumesOld:
		return NewSubsumesOld
	case oldSubsumesNew:
		return OldSubsumesNew
	default:
		return Distinct
	}
}

// minSubsumptionTokens is the fewest content words a memory must hold before
// containment says anything about it.
//
// Below this, a memory is a subsequence of anything sharing its subject:
// `Biscuit.` would be folded into every memory mentioning the dog, and the
// fact that Biscuit exists would be lost the moment a longer memory arrived.
// Three is the shortest span that can carry subject, relation and value, which
// is the smallest thing this engine calls a fact.
const minSubsumptionTokens = 3

// FoldRedundant collapses memories from a single extraction that say the same
// thing, keeping the most informative wording of each fact.
//
// This runs before anything is written, so the duplicates it removes are never
// embedded, never stored, and never linked. One conversation restating a fact
// three times is the common case: it produced the 25 copies of `Caroline is
// transgender.` in the measured corpus.
//
// Input order is preserved for the memories that survive, so the API response
// still reads in the order the conversation stated the facts.
func FoldRedundant(memories []ExtractedMemory) []ExtractedMemory {
	kept := make([]ExtractedMemory, 0, len(memories))

	for _, m := range memories {
		merged := false
		for i := range kept {
			// A different type is a different claim about the fact's nature,
			// and GetProfile splits static from dynamic on exactly that field.
			if kept[i].Type != m.Type {
				continue
			}

			switch Subsumes(m.Content, kept[i].Content) {
			case NewSubsumesOld, Equivalent:
				// The later wording says at least as much, so it replaces the
				// earlier one in place, holding its position in the output.
				kept[i].Content = m.Content
				kept[i].Tags = unionTags(kept[i].Tags, m.Tags)
				merged = true
			case OldSubsumesNew:
				// Already covered. Keep the tags, drop the row.
				kept[i].Tags = unionTags(kept[i].Tags, m.Tags)
				merged = true
			case Distinct:
			}
			if merged {
				break
			}
		}
		if !merged {
			kept = append(kept, m)
		}
	}

	return kept
}

// contentTokens is model.ContentTokens, aliased so the comparison below and
// the hash stored on the vertex can never drift apart: a hash that reports
// "duplicate" against a comparison that reports "distinct" would make the
// write path's behaviour depend on which check ran first.
func contentTokens(text string) []string { return model.ContentTokens(text) }

// polarityTokens are words that flip or qualify what a statement asserts,
// while leaving its other words untouched -- which is exactly the case a
// subsequence test cannot see.
//
// Narrower than internal/extraction's negationWords, which also strips
// auxiliaries so that two statements can be compared on topic. Here the
// question is polarity itself, so only the markers count: including "does"
// would make `Caroline does yoga` read as negated.
//
// Three groups, and the second and third are the ones a plain negation list
// misses:
//
//   - Grammatical negation: "not", "never", "isn't".
//   - Reporting and denial verbs. "Caroline denies that she owns a car" is a
//     token-for-token superset of "Caroline owns a car" with no negation word
//     in it, so a subsequence test folds the second into the first and stores
//     the opposite of what was said.
//   - Hedges. "Caroline might move to Lisbon" subsumes "Caroline moved to
//     Lisbon" the same way: a possibility is not the event.
//
// Over-inclusion here is cheap and under-inclusion is not. A word wrongly
// listed costs one redundant row; a word missing costs a destroyed fact.
var polarityTokens = map[string]bool{
	// Grammatical negation.
	"not": true, "never": true, "no": true, "none": true,
	"don't": true, "doesn't": true, "didn't": true,
	"isn't": true, "aren't": true, "wasn't": true, "weren't": true,
	"cannot": true, "can't": true, "won't": true, "wouldn't": true,
	"hasn't": true, "haven't": true, "hadn't": true,
	"couldn't": true, "shouldn't": true,
	"nor": true, "neither": true, "nothing": true, "nobody": true,
	"without": true, "unless": true,

	// Denial, doubt and reported speech: the statement is about the claim
	// rather than asserting it.
	"denies": true, "denied": true, "deny": true, "denying": true,
	"disputes": true, "disputed": true, "dispute": true,
	"refuses": true, "refused": true, "refuse": true,
	"doubts": true, "doubted": true, "doubt": true,
	"rejects": true, "rejected": true, "reject": true,
	"stopped": true, "quit": true, "former": true, "formerly": true,
	"ex": true, "no-longer": true,

	// Hedges: a possibility is not the fact.
	"might": true, "maybe": true, "perhaps": true, "possibly": true,
	"probably": true, "allegedly": true, "supposedly": true,
	"unlikely": true, "hopes": true, "hoped": true, "plans": true,
	"planned": true, "wants": true, "wanted": true, "considering": true,
	"if": true, "whether": true, "would": true, "could": true,
}

// hasPolarityMarker reports whether a token sequence carries a word that flips
// or qualifies what it asserts.
func hasPolarityMarker(tokens []string) bool {
	for _, t := range tokens {
		if polarityTokens[t] {
			return true
		}
	}
	return false
}

// isOrderedSubsequence reports whether every token of inner appears in outer,
// in the same relative order.
//
// Order is the whole point. Set containment would merge `Melanie gave Caroline
// a book` into `Caroline gave Melanie a book`, which have identical word sets
// and opposite meanings.
func isOrderedSubsequence(inner, outer []string) bool {
	if len(inner) > len(outer) {
		return false
	}
	i := 0
	for _, o := range outer {
		if i < len(inner) && inner[i] == o {
			i++
		}
	}
	return i == len(inner)
}

// unionTags merges two tag sets, preserving first-seen order and comparing
// case-insensitively, matching hasOverlappingTags in internal/service.
func unionTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, t := range append(append([]string{}, a...), b...) {
		key := strings.ToLower(strings.TrimSpace(t))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
