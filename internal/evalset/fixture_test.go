package evalset

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmbeddings_AddLookupLen(t *testing.T) {
	e := NewEmbeddings(3)
	if e.Dim() != 3 {
		t.Fatalf("Dim() = %d, want 3", e.Dim())
	}
	if e.Len() != 0 {
		t.Fatalf("Len() on a fresh fixture = %d, want 0", e.Len())
	}

	if err := e.Add("hello", []float32{1, 2, 3}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if e.Len() != 1 {
		t.Errorf("Len() after one Add = %d, want 1", e.Len())
	}

	got, ok := e.Lookup("hello")
	if !ok {
		t.Fatal("Lookup(hello) ok = false, want true")
	}
	if !reflect.DeepEqual(got, []float32{1, 2, 3}) {
		t.Errorf("Lookup(hello) = %v, want [1 2 3]", got)
	}

	if _, ok := e.Lookup("never added"); ok {
		t.Error("Lookup(never added) ok = true, want false")
	}
}

func TestEmbeddings_Add_RejectsDimensionMismatch(t *testing.T) {
	e := NewEmbeddings(3)
	if err := e.Add("bad", []float32{1, 2}); err == nil {
		t.Fatal("Add with a 2-vector into a 3-dim fixture returned nil error")
	}
	if _, ok := e.Lookup("bad"); ok {
		t.Error("a rejected vector was stored anyway")
	}
}

func TestEmbeddings_Add_RejectsNaNAndInf(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
	}{
		{"NaN", []float32{1, float32(math.NaN()), 3}},
		{"+Inf", []float32{1, float32(math.Inf(1)), 3}},
		{"-Inf", []float32{1, float32(math.Inf(-1)), 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEmbeddings(3)
			if err := e.Add("bad", tt.vec); err == nil {
				t.Fatalf("Add(%v) returned nil error, want rejection", tt.vec)
			}
			if _, ok := e.Lookup("bad"); ok {
				t.Error("a rejected vector was stored anyway")
			}
		})
	}
}

// TestEmbeddings_WriteReadRoundTrip is the guarantee the whole fixture format
// exists for: a committed file must reproduce exactly the vectors that were
// written, bit for bit, not merely to some float tolerance.
func TestEmbeddings_WriteReadRoundTrip(t *testing.T) {
	e := NewEmbeddings(4)
	vectors := map[string][]float32{
		"alpha": {0.1, -2.5, 3.14159, 0},
		"beta":  {1e-30, -1e30, 1, -1},
		"gamma": {0, 0, 0, 0},
	}
	for text, vec := range vectors {
		if err := e.Add(text, vec); err != nil {
			t.Fatalf("Add(%q): %v", text, err)
		}
	}

	path := filepath.Join(t.TempDir(), "fixture.bin")
	if err := e.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written fixture: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("fixture file mode = %o, want 644 (the fixture is committed and read by everyone)", perm)
	}

	got, err := ReadEmbeddings(path)
	if err != nil {
		t.Fatalf("ReadEmbeddings: %v", err)
	}
	if got.Dim() != e.Dim() {
		t.Errorf("read back Dim() = %d, want %d", got.Dim(), e.Dim())
	}
	if got.Len() != e.Len() {
		t.Errorf("read back Len() = %d, want %d", got.Len(), e.Len())
	}
	for text, want := range vectors {
		gotVec, ok := got.Lookup(text)
		if !ok {
			t.Errorf("read back fixture is missing %q", text)
			continue
		}
		if !reflect.DeepEqual(gotVec, want) {
			t.Errorf("read back %q = %v, want %v (bit-for-bit)", text, gotVec, want)
		}
	}
}

func TestReadEmbeddings_BadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-fixture.bin")
	if err := os.WriteFile(path, []byte("XXXXsome garbage that is not a fixture"), 0o644); err != nil {
		t.Fatalf("write garbage file: %v", err)
	}
	if _, err := ReadEmbeddings(path); err == nil {
		t.Fatal("ReadEmbeddings on a file with the wrong magic returned nil error")
	}
}

// TestFixtureEmbedder_ReturnsACopy pins that Embed's caller can mutate the
// returned slice without corrupting the fixture for the next lookup: the
// fixture is shared across every query in a run.
func TestFixtureEmbedder_ReturnsACopy(t *testing.T) {
	e := NewEmbeddings(3)
	if err := e.Add("hello", []float32{1, 2, 3}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	f := NewFixtureEmbedder(e)

	v1, err := f.Embed("hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	v1[0] = 999

	v2, err := f.Embed("hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if v2[0] != 1 {
		t.Errorf("second Embed() = %v, mutating the first result changed it: the fixture must return copies", v2)
	}
	if f.Dimension() != 3 {
		t.Errorf("Dimension() = %d, want 3", f.Dimension())
	}
}

func TestFixtureEmbedder_MissIncrementsMisses(t *testing.T) {
	f := NewFixtureEmbedder(NewEmbeddings(3))

	if f.Misses() != 0 {
		t.Fatalf("Misses() before any lookup = %d, want 0", f.Misses())
	}

	if _, err := f.Embed("not in the fixture"); err == nil {
		t.Fatal("Embed on a miss returned nil error, want an error (no silent fallback to a model)")
	}

	if f.Misses() != 1 {
		t.Errorf("Misses() after one miss = %d, want 1", f.Misses())
	}

	if _, err := f.Embed("also not in the fixture"); err == nil {
		t.Fatal("second miss returned nil error")
	}
	if f.Misses() != 2 {
		t.Errorf("Misses() after two misses = %d, want 2", f.Misses())
	}
}
