package embedding

import (
	"math"
	"testing"
)

func TestBagOfWordsEmbedder_Dimension(t *testing.T) {
	e := NewBagOfWordsEmbedder(384)
	if e.Dimension() != 384 {
		t.Errorf("expected 384, got %d", e.Dimension())
	}
}

func TestBagOfWordsEmbedder_Embed(t *testing.T) {
	e := NewBagOfWordsEmbedder(384)

	vec, err := e.Embed("PostgreSQL database with graph support")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 384 {
		t.Fatalf("expected 384 dims, got %d", len(vec))
	}

	// Should be L2 normalized (magnitude ~= 1.0).
	var mag float64
	for _, v := range vec {
		mag += float64(v) * float64(v)
	}
	mag = math.Sqrt(mag)
	if mag < 0.99 || mag > 1.01 {
		t.Errorf("expected unit vector, got magnitude %f", mag)
	}
}

func TestBagOfWordsEmbedder_Similarity(t *testing.T) {
	e := NewBagOfWordsEmbedder(384)

	v1, _ := e.Embed("PostgreSQL database with graph support")
	v2, _ := e.Embed("Postgres database graph queries")
	v3, _ := e.Embed("Kubernetes deployment with Helm charts")

	sim12 := cosine(v1, v2)
	sim13 := cosine(v1, v3)

	// v1 and v2 (both about postgres/database) should be more similar
	// than v1 and v3 (postgres vs kubernetes).
	if sim12 <= sim13 {
		t.Errorf("expected sim(postgres,postgres) > sim(postgres,k8s), got %.3f <= %.3f", sim12, sim13)
	}
}

func TestBagOfWordsEmbedder_EmptyText(t *testing.T) {
	e := NewBagOfWordsEmbedder(384)

	vec, err := e.Embed("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 384 {
		t.Fatalf("expected 384 dims, got %d", len(vec))
	}

	// All zeros for empty text.
	for _, v := range vec {
		if v != 0 {
			t.Fatal("expected zero vector for empty text")
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// TestEmbedSurvivesDimensionsThatOverflowUint32 guards a divide-by-zero panic.
//
// Embed hashes tokens into the vector with `fnvHash(token) % dim`, and dim was
// an int converted to uint32 in the loop. A value that is an exact multiple of
// 2^32 converts to uint32(0) and the modulo panics.
//
// Scope, stated honestly: this was not reachable through the running server.
// cmd/server sizes the pgvector column from Dimension() during InitSchema,
// before serving, and Postgres rejects a width of 4294967296 as out of range
// for int4, so the process exits first. No dimension pgvector accepts
// (1..16000) can wrap - the smallest wrapping value is 2^32 itself.
//
// The test is still worth keeping: this package's API is callable from
// anywhere in the module, and a caller with no Postgres column in the way -
// a test, a re-embedding tool, a benchmark - would hit the panic directly.
//
// The values below are the ones that actually wrap. 4294967296 is 2^32 and
// 8589934592 is 2^33; both convert to zero. 4294967297 converts to 1, which
// does not panic but produces a useless single-element vector.
func TestEmbedSurvivesDimensionsThatOverflowUint32(t *testing.T) {
	for _, dim := range []int{1 << 32, 1<<32 + 1, 1 << 33, math.MaxInt32, -1, 0} {
		e := NewBagOfWordsEmbedder(dim)

		if got := e.Dimension(); got <= 0 || got > maxDim {
			t.Fatalf("dim %d produced an out-of-range dimension %d", dim, got)
		}

		// The panic was here, not in the constructor.
		vec, err := e.Embed("postgres is the database we use")
		if err != nil {
			t.Fatalf("dim %d: unexpected error: %v", dim, err)
		}
		if len(vec) != e.Dimension() {
			t.Fatalf("dim %d: vector is %d long, want %d", dim, len(vec), e.Dimension())
		}

		// A vector of the right length full of zeros would satisfy the checks
		// above while carrying no signal at all.
		nonZero := false
		for _, v := range vec {
			if v != 0 {
				nonZero = true
				break
			}
		}
		if !nonZero {
			t.Fatalf("dim %d: embedded real text to an all-zero vector", dim)
		}
	}
}

// TestAcceptedDimensionsAreUsedAsGiven checks the clamp does not silently
// rewrite dimensions that are perfectly valid. A cap that quietly replaced 768
// with 384 would corrupt every embedding while looking like it worked.
func TestAcceptedDimensionsAreUsedAsGiven(t *testing.T) {
	for _, dim := range []int{1, 128, 384, 768, 1536, 3072, maxDim} {
		if got := NewBagOfWordsEmbedder(dim).Dimension(); got != dim {
			t.Errorf("dim %d was changed to %d", dim, got)
		}
	}
}
