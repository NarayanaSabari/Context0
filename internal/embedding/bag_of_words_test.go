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
