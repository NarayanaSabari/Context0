// contradiction.go detects factual contradictions between semantic memories using
// four complementary strategies applied in order of confidence:
//
//  1. Replacement signals (confidence 0.9) -- the new memory contains explicit
//     replacement language ("switched to", "migrated to", "no longer", etc.) and
//     shares keywords or tags with the old memory.
//
//  2. Subject-verb-object divergence (confidence 0.85) -- both memories contain
//     the same subject+verb pattern but with different objects, indicating a value
//     change (e.g. "backend uses Python" vs "backend uses Go").
//
//  3. Negation conflict (confidence 0.8) -- the memories have high keyword overlap
//     (Jaccard >= 0.4) but exactly one of them contains a negation word ("not",
//     "don't", "never", etc.).
//
//  4. Tag-based divergence (confidence 0.5) -- the memories share 50%+ of their
//     tags but have moderate content similarity (0.3-0.8 Jaccard), suggesting they
//     discuss the same topic with different conclusions.
//
// Only semantic (fact) memories are checked; episodic and procedural memories are
// inherently non-contradictory.

package extraction

import (
	"strings"

	"github.com/context0/context0/pkg/model"
)

// Contradiction represents a detected conflict between a newly stored memory and
// an existing memory. It captures both memories, a human-readable reason explaining
// the detected conflict, and a confidence score in [0, 1] indicating how certain
// the detection is.
type Contradiction struct {
	NewMemory  model.Memory
	OldMemory  model.Memory
	Reason     string
	Confidence float64
}

// DetectContradictions compares a new memory against existing memories
// and returns any contradictions found.
// Only semantic memories (facts) are checked — episodic and procedural
// memories don't contradict each other.
func DetectContradictions(newMem model.Memory, existing []model.Memory) []Contradiction {
	if newMem.Type != model.MemoryTypeSemantic {
		return nil
	}

	var contradictions []Contradiction

	for _, old := range existing {
		if old.ID == newMem.ID || old.Type != model.MemoryTypeSemantic || old.ProjectID != newMem.ProjectID {
			continue
		}

		if c := detectPair(newMem, old); c != nil {
			contradictions = append(contradictions, *c)
		}
	}

	return contradictions
}

// detectPair runs all four contradiction detection strategies against a single
// pair of memories, returning the first match (strategies are ordered by
// descending confidence so the strongest signal wins).
func detectPair(newMem, oldMem model.Memory) *Contradiction {
	newLower := strings.ToLower(newMem.Content)
	oldLower := strings.ToLower(oldMem.Content)

	// Strategy 1: Explicit replacement signals.
	// "switched to X", "migrated to X", "changed X to Y", "no longer X", "replaced X with Y"
	replacementSignals := []string{
		"switched to", "migrated to", "changed to", "moved to",
		"replaced", "no longer", "instead of", "not anymore",
		"updated to", "upgraded to", "downgraded to",
	}
	for _, signal := range replacementSignals {
		if strings.Contains(newLower, signal) {
			// Check if the old memory talks about a related topic.
			// Low threshold because replacement statements often use different words.
			overlap := keywordOverlap(newLower, oldLower)
			tagOvl := tagOverlapRatio(newMem.Tags, oldMem.Tags)
			if overlap >= 0.15 || tagOvl >= 0.3 {
				return &Contradiction{
					NewMemory:  newMem,
					OldMemory:  oldMem,
					Reason:     "replacement signal: " + signal,
					Confidence: 0.9,
				}
			}
		}
	}

	// Strategy 2: Same structure, different value.
	// Extract "X <verb> Y" patterns from both and compare.
	newTriples := extractTriples(newLower)
	oldTriples := extractTriples(oldLower)

	for _, nt := range newTriples {
		for _, ot := range oldTriples {
			if nt.subject == ot.subject && nt.verb == ot.verb && nt.object != ot.object {
				// Same subject + verb but different object → contradiction.
				return &Contradiction{
					NewMemory:  newMem,
					OldMemory:  oldMem,
					Reason:     nt.subject + " " + nt.verb + ": " + nt.object + " vs " + ot.object,
					Confidence: 0.85,
				}
			}
		}
	}

	// Strategy 3: High keyword overlap but negation present.
	overlap := keywordOverlap(newLower, oldLower)
	if overlap >= 0.4 {
		negations := []string{"not ", "don't ", "doesn't ", "no longer ", "never ", "isn't ", "aren't ", "wasn't "}
		newNeg := hasAny(newLower, negations)
		oldNeg := hasAny(oldLower, negations)
		if newNeg != oldNeg {
			return &Contradiction{
				NewMemory:  newMem,
				OldMemory:  oldMem,
				Reason:     "negation conflict with high similarity",
				Confidence: 0.8,
			}
		}
	}

	// Strategy 4: Tag-based overlap check.
	// If two memories share 50%+ tags but have low content similarity, check for value differences.
	if len(newMem.Tags) > 0 && len(oldMem.Tags) > 0 {
		tagOverlap := tagOverlapRatio(newMem.Tags, oldMem.Tags)
		if tagOverlap >= 0.5 && overlap >= 0.3 && overlap < 0.8 {
			// Similar topic (shared tags) but different content — possible contradiction.
			// Only flag if content similarity is moderate (not too similar = duplicate, not too different = unrelated).
			return &Contradiction{
				NewMemory:  newMem,
				OldMemory:  oldMem,
				Reason:     "shared tags with divergent content",
				Confidence: 0.5,
			}
		}
	}

	return nil
}

// triple represents a subject-verb-object pattern extracted from natural language.
// It is used by Strategy 2 to detect when two memories assign different values
// (objects) to the same subject+verb combination.
type triple struct {
	subject string
	verb    string
	object  string
}

// extractTriples scans text for "subject verb object" patterns by searching for
// known verbs (" uses ", " is ", " prefers ", etc.) and extracting the surrounding
// words as subject (up to 3 trailing words before the verb) and object (up to 3
// leading words after the verb).
func extractTriples(text string) []triple {
	var triples []triple

	verbs := []string{
		" uses ", " is ", " runs ", " has ", " prefers ",
		" requires ", " supports ", " includes ", " contains ",
		" speaks ", " lives in ", " works at ", " works for ",
		" costs ", " weighs ", " measures ", " takes ",
	}

	for _, verb := range verbs {
		idx := strings.Index(text, verb)
		if idx < 0 {
			continue
		}

		subjectPart := strings.TrimSpace(text[:idx])
		objectPart := strings.TrimSpace(text[idx+len(verb):])

		subject := lastWords(subjectPart, 3)
		object := firstWords(objectPart, 3)

		if subject != "" && object != "" {
			triples = append(triples, triple{
				subject: subject,
				verb:    strings.TrimSpace(verb),
				object:  object,
			})
		}
	}

	return triples
}

// keywordOverlap calculates the Jaccard similarity coefficient between the
// significant (non-stop) words of two strings. Returns a value in [0, 1] where
// 1.0 means identical word sets. This is used by multiple strategies to gauge
// topical similarity between two memories.
func keywordOverlap(a, b string) float64 {
	aWords := significantWords(a)
	bWords := significantWords(b)

	if len(aWords) == 0 || len(bWords) == 0 {
		return 0
	}

	intersection := 0
	for w := range aWords {
		if bWords[w] {
			intersection++
		}
	}

	union := len(aWords) + len(bWords) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// significantWords tokenizes text and returns a set of words after removing
// stop words and short tokens (length <= 1). The result is used for Jaccard
// similarity calculations.
func significantWords(text string) map[string]bool {
	words := strings.Fields(text)
	result := make(map[string]bool)

	stop := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true,
		"it": true, "its": true, "this": true, "that": true,
		"of": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "with": true, "by": true, "from": true, "as": true,
		"and": true, "but": true, "or": true, "we": true, "our": true,
		"my": true, "i": true, "you": true, "your": true, "they": true,
	}

	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()-")
		if len(w) > 1 && !stop[w] {
			result[w] = true
		}
	}

	return result
}

// tagOverlapRatio computes the ratio of shared tags to the size of the smaller
// tag set. This asymmetric measure ensures that a memory with few specific tags
// can still show high overlap with a memory that has many tags.
func tagOverlapRatio(a, b []string) float64 {
	setA := make(map[string]bool)
	for _, t := range a {
		setA[strings.ToLower(t)] = true
	}

	overlap := 0
	for _, t := range b {
		if setA[strings.ToLower(t)] {
			overlap++
		}
	}

	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	if smaller == 0 {
		return 0
	}

	return float64(overlap) / float64(smaller)
}

// hasAny returns true if the text contains any of the given substrings.
func hasAny(text string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// lastWords returns the last n meaningful (non-stop, non-punctuation) words from
// a string. Used to extract the subject portion before a verb in triple extraction.
func lastWords(s string, n int) string {
	words := strings.Fields(s)
	stop := map[string]bool{"the": true, "a": true, "an": true, "our": true, "my": true, "this": true, "we": true, "i": true}

	var meaningful []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()-")
		if !stop[w] && len(w) > 1 {
			meaningful = append(meaningful, w)
		}
	}

	if len(meaningful) == 0 {
		return ""
	}
	if len(meaningful) <= n {
		return strings.Join(meaningful, " ")
	}
	return strings.Join(meaningful[len(meaningful)-n:], " ")
}

// firstWords returns the first n meaningful words from a string. Used to extract
// the object portion after a verb in triple extraction.
func firstWords(s string, n int) string {
	words := strings.Fields(s)
	var meaningful []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()-")
		if len(w) > 1 {
			meaningful = append(meaningful, w)
			if len(meaningful) >= n {
				break
			}
		}
	}
	return strings.Join(meaningful, " ")
}
