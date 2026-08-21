package embedding

import (
	"math"
	"strings"
)

// BagOfWordsEmbedder generates fixed-dimension vectors using feature hashing
// (the "hashing trick"). It is a zero-dependency, CPU-only embedding approach
// suitable for development, testing, and MVP deployments where installing a
// model server is not practical.
//
// Algorithm overview:
//  1. Tokenize input text (lowercase, strip punctuation, remove stop words).
//  2. Compute term frequencies and apply TF weighting: 1 + log(count).
//  3. Hash each token into two positions using FNV-1a, with a sign hash to
//     reduce directional bias from collisions.
//  4. Hash consecutive token bigrams at half weight for phrase-level signal.
//  5. L2-normalize the final vector so cosine similarity works correctly.
//
// Limitations: no semantic understanding -- relies purely on lexical overlap.
// For production quality, switch to OllamaEmbedder or OpenAIEmbedder.
type BagOfWordsEmbedder struct {
	// dim is uint32 rather than int because that is the type the hashing
	// actually needs: positions are `fnvHash(token) % dim`, and fnvHash
	// returns uint32. Holding it as an int meant a conversion inside the hot
	// loop, and that conversion is where the wrap happened - a dim that is an
	// exact multiple of 2^32 became uint32(0) and the modulo panicked with a
	// divide-by-zero on the first text embedded.
	//
	// Storing the narrow type moves the single conversion to the constructor,
	// where the range is checked once, and leaves the loop conversion-free.
	dim uint32
}

// maxDim caps the vector dimension this embedder will accept.
//
// KORA_EMBEDDING_DIM is operator-supplied and was only checked for being
// negative, so `KORA_EMBEDDING_DIM=4294967296` passed validation, started the
// server cleanly, and took it down on the first memory stored.
//
// 65536 is far above any real embedding model - the largest in common use is
// 3072 - so the cap refuses nothing legitimate, and it keeps the dimension
// comfortably inside uint32.
const maxDim = 65536

// NewBagOfWordsEmbedder creates an embedder with the given vector dimension.
// Recommended value is 384 because it matches common small transformer models
// (e.g. all-MiniLM-L6-v2), making migration to a real model seamless -- the
// pgvector column dimension stays the same.
//
// Out-of-range dimensions fall back to the default rather than erroring: this
// constructor has no error return, and the configuration layer rejects bad
// values with a message naming the variable before it ever gets here.
func NewBagOfWordsEmbedder(dim int) *BagOfWordsEmbedder {
	if dim <= 0 || dim > maxDim {
		dim = 384
	}
	// Safe by the bounds above: dim is now within [1, maxDim].
	return &BagOfWordsEmbedder{dim: uint32(dim)}
}

// Dimension returns the fixed vector size used by this embedder.
func (e *BagOfWordsEmbedder) Dimension() int {
	return int(e.dim)
}

// Embed converts text into a fixed-dimension vector using hashed bag-of-words
// with TF weighting and bigram features. The output is L2-normalized.
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
	// Using two hash positions per token spreads the signal across the
	// vector and reduces the impact of hash collisions.
	for token, count := range tf {
		// TF weight uses sublinear scaling: frequent terms get diminishing returns.
		weight := float32(1.0 + math.Log(float64(count)))

		h1 := fnvHash(token) % e.dim
		h2 := fnvHash(token+"_salt") % e.dim

		// A sign hash allows some tokens to subtract, which reduces
		// systematic positive bias and produces richer vector geometry.
		sign := float32(1.0)
		if fnvHash(token+"_sign")%2 == 0 {
			sign = -1.0
		}

		vec[h1] += weight * sign
		vec[h2] += weight * sign
	}

	// Bigram hashing captures word-pair co-occurrence, giving texts with
	// similar phrases higher similarity than texts sharing only individual words.
	for i := 0; i < len(tokens)-1; i++ {
		bigram := tokens[i] + "_" + tokens[i+1]
		h := fnvHash(bigram) % e.dim
		vec[h] += 0.5
	}

	// L2-normalize so that cosine similarity equals the dot product.
	normalize(vec)

	return vec, nil
}

// tokenize splits text into lowercase tokens, stripping punctuation and
// filtering out stop words and single-character tokens. This preprocessing
// focuses the embedding on content-bearing terms.
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

// fnvHash computes a 32-bit FNV-1a hash of the string. FNV-1a is chosen for
// its simplicity, speed, and good distribution properties for short strings.
func fnvHash(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// normalize applies in-place L2 normalization to a vector, scaling it to unit
// length. After normalization, cosine similarity between two vectors equals
// their dot product. Zero vectors are left unchanged to avoid division by zero.
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

// stopWords contains common English function words that carry little semantic
// meaning and would add noise to the embedding. Removing them improves
// similarity accuracy for content-bearing terms.
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
