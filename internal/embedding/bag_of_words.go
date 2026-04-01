package embedding

import (
	"math"
	"strings"
)

// BagOfWordsEmbedder generates fixed-dimension vectors using hashed bag-of-words.
// This is a zero-dependency embedding approach suitable for MVP.
// It uses feature hashing (hashing trick) to map tokens into a fixed-size vector,
// with TF-IDF-like weighting for better similarity matching.
//
// Replace with a real model (Ollama, sentence-transformers) for production quality.
type BagOfWordsEmbedder struct {
	dim int
}

// NewBagOfWordsEmbedder creates an embedder with the given vector dimension.
// Recommended: 384 (matches common small models for future migration).
func NewBagOfWordsEmbedder(dim int) *BagOfWordsEmbedder {
	if dim <= 0 {
		dim = 384
	}
	return &BagOfWordsEmbedder{dim: dim}
}

func (e *BagOfWordsEmbedder) Dimension() int {
	return e.dim
}

func (e *BagOfWordsEmbedder) Embed(text string) ([]float32, error) {
	tokens := tokenize(text)
	vec := make([]float32, e.dim)

	if len(tokens) == 0 {
		return vec, nil
	}

	// Count term frequencies.
	tf := make(map[string]int)
	for _, t := range tokens {
		tf[t]++
	}

	// Hash each token into the vector with TF-weighted contribution.
	for token, count := range tf {
		// TF weight: 1 + log(count)
		weight := float32(1.0 + math.Log(float64(count)))

		// Use two hash positions per token (reduces collision impact).
		h1 := fnvHash(token) % uint32(e.dim)
		h2 := fnvHash(token+"_salt") % uint32(e.dim)

		// Sign from a third hash (allows subtraction to reduce bias).
		sign := float32(1.0)
		if fnvHash(token+"_sign")%2 == 0 {
			sign = -1.0
		}

		vec[h1] += weight * sign
		vec[h2] += weight * sign
	}

	// Also hash bigrams for phrase-level similarity.
	for i := 0; i < len(tokens)-1; i++ {
		bigram := tokens[i] + "_" + tokens[i+1]
		h := fnvHash(bigram) % uint32(e.dim)
		vec[h] += 0.5
	}

	// L2 normalize.
	normalize(vec)

	return vec, nil
}

// tokenize splits text into lowercase tokens, filtering stop words and short words.
func tokenize(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	var tokens []string
	for _, w := range words {
		// Strip non-alphanumeric edges.
		w = strings.Trim(w, ".,;:!?\"'()[]{}/-")
		if len(w) < 2 {
			continue
		}
		if stopWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

// fnvHash is FNV-1a hash for strings.
func fnvHash(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// normalize applies L2 normalization to a vector.
func normalize(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return
	}
	norm := float32(math.Sqrt(sum))
	for i := range vec {
		vec[i] /= norm
	}
}

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"do": true, "does": true, "did": true, "have": true, "has": true,
	"had": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "can": true, "shall": true,
	"it": true, "its": true, "this": true, "that": true, "these": true,
	"those": true, "he": true, "she": true, "they": true, "we": true,
	"you": true, "me": true, "him": true, "her": true, "us": true,
	"my": true, "your": true, "his": true, "our": true, "their": true,
	"of": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "with": true, "by": true, "from": true, "as": true,
	"into": true, "about": true, "between": true, "through": true,
	"and": true, "but": true, "or": true, "not": true, "no": true,
	"if": true, "then": true, "so": true, "what": true, "which": true,
	"who": true, "how": true, "when": true, "where": true, "why": true,
}
