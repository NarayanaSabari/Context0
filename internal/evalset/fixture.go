package evalset

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// The embedding fixture is a flat binary file:
//
//	"KEMB" | uint32 dim | uint32 count | count × ( sha256(text) | dim × float32 )
//
// Little-endian throughout. Keyed by the hash of the text rather than the
// text so the file carries no dataset content, only vectors, and so a lookup
// is exact: a memory embedded from text that differs by one byte is a miss,
// which is what makes the fixture a guarantee that the eval never falls back
// to a model.
const fixtureMagic = "KEMB"

// Embeddings is the in-memory fixture.
type Embeddings struct {
	dim     int
	vectors map[[sha256.Size]byte][]float32
}

// NewEmbeddings returns an empty fixture of the given width.
func NewEmbeddings(dim int) *Embeddings {
	return &Embeddings{dim: dim, vectors: make(map[[sha256.Size]byte][]float32)}
}

// Dim is the vector width.
func (e *Embeddings) Dim() int { return e.dim }

// Len is the number of stored vectors.
func (e *Embeddings) Len() int { return len(e.vectors) }

func keyOf(text string) [sha256.Size]byte { return sha256.Sum256([]byte(text)) }

// Add stores a vector for text, replacing any existing one.
func (e *Embeddings) Add(text string, vec []float32) error {
	if len(vec) != e.dim {
		return fmt.Errorf("vector has %d dimensions, fixture has %d", len(vec), e.dim)
	}
	for _, v := range vec {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return errors.New("vector contains NaN or Inf")
		}
	}
	e.vectors[keyOf(text)] = vec
	return nil
}

// Lookup returns the stored vector for text. The slice is shared; callers
// that mutate it must copy.
func (e *Embeddings) Lookup(text string) ([]float32, bool) {
	v, ok := e.vectors[keyOf(text)]
	return v, ok
}

// ReadEmbeddings loads a fixture file.
func ReadEmbeddings(path string) (*Embeddings, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)

	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if string(magic[:]) != fixtureMagic {
		return nil, fmt.Errorf("%s is not an embedding fixture (magic %q)", path, magic)
	}
	var header struct{ Dim, Count uint32 }
	if err := binary.Read(r, binary.LittleEndian, &header); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if header.Dim == 0 || header.Dim > 16000 {
		return nil, fmt.Errorf("%s: implausible dimension %d", path, header.Dim)
	}

	e := NewEmbeddings(int(header.Dim))
	var key [sha256.Size]byte
	for i := uint32(0); i < header.Count; i++ {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return nil, fmt.Errorf("read %s record %d: %w", path, i, err)
		}
		vec := make([]float32, header.Dim)
		if err := binary.Read(r, binary.LittleEndian, vec); err != nil {
			return nil, fmt.Errorf("read %s record %d: %w", path, i, err)
		}
		e.vectors[key] = vec
	}
	return e, nil
}

// Write stores the fixture atomically, records sorted by key so that the same
// contents always produce the same bytes.
func (e *Embeddings) Write(path string) error {
	keys := make([][sha256.Size]byte, 0, len(e.vectors))
	for k := range e.vectors {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		for b := 0; b < sha256.Size; b++ {
			if keys[i][b] != keys[j][b] {
				return keys[i][b] < keys[j][b]
			}
		}
		return false
	})

	tmp, err := os.CreateTemp(filepath.Dir(path), ".embeddings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriterSize(tmp, 1<<20)
	if _, err := w.WriteString(fixtureMagic); err != nil {
		return err
	}
	// Both fields are uint32 on disk. The dimension is bounded by the
	// reader's own check and pgvector's 16,000 cap; the count is bounded
	// here so a fixture too large for the format fails loudly rather than
	// wrapping.
	dim, count := e.dim, len(keys)
	if dim <= 0 || dim > 16000 {
		return fmt.Errorf("fixture dimension %d is outside 1..16000", dim)
	}
	if count < 0 || count > math.MaxUint32 {
		return fmt.Errorf("fixture holds %d vectors, more than the format can count", count)
	}
	header := struct{ Dim, Count uint32 }{uint32(dim), uint32(count)}
	if err := binary.Write(w, binary.LittleEndian, header); err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := w.Write(k[:]); err != nil {
			return err
		}
		if err := binary.Write(w, binary.LittleEndian, e.vectors[k]); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp already makes the file owner-only, which is what the
	// security scan expects of written files; git records no mode beyond
	// the executable bit, so the committed fixture is unaffected.
	return os.Rename(tmp.Name(), path)
}

// FixtureEmbedder serves vectors from the fixture and nothing else.
//
// It implements embedding.Embedder so the real retrieval engine can be run
// unchanged. A text the fixture does not hold is an error, never a fallback:
// the constraint the harness exists to enforce is that no evaluation calls a
// model, and a silent substitute would break that without a trace.
type FixtureEmbedder struct {
	e *Embeddings

	mu     sync.Mutex
	misses int
}

// NewFixtureEmbedder wraps a fixture.
func NewFixtureEmbedder(e *Embeddings) *FixtureEmbedder {
	return &FixtureEmbedder{e: e}
}

// Embed returns a copy of the stored vector for text.
func (f *FixtureEmbedder) Embed(text string) ([]float32, error) {
	v, ok := f.e.Lookup(text)
	if !ok {
		f.mu.Lock()
		f.misses++
		f.mu.Unlock()
		preview := text
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		return nil, fmt.Errorf("fixture holds no embedding for %q; rebuild it with `go run ./cmd/eval fixtures`", preview)
	}
	out := make([]float32, len(v))
	copy(out, v)
	return out, nil
}

// Dimension is the fixture's width.
func (f *FixtureEmbedder) Dimension() int { return f.e.dim }

// Misses reports how many lookups failed, so a run can refuse to report
// numbers produced with a retriever silently disabled.
func (f *FixtureEmbedder) Misses() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.misses
}
