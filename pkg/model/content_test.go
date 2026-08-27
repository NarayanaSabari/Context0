package model

import "testing"

// ContentHash is the cheap half of write-time consolidation: an exact-duplicate
// lookup that needs no embedder and no similarity threshold. What it must
// ignore, and what it must never ignore, is the whole contract.
func TestContentHash_IgnoresOnlyFormatting(t *testing.T) {
	base := ContentHash("Caroline is transgender.")

	same := []string{
		"Caroline is transgender.",
		"caroline is transgender",
		"  Caroline   is  transgender!  ",
		"Caroline is transgender",
	}
	for _, s := range same {
		if got := ContentHash(s); got != base {
			t.Errorf("ContentHash(%q) = %s, want %s: case, punctuation and spacing "+
				"are formatting, not content", s, got, base)
		}
	}

	// The negation case is the one that matters most. This value decides which
	// row is discarded, so any normalisation that folded "not" away would make
	// the engine store the opposite of what was said.
	different := []string{
		"Caroline is a transgender person.",
		"Caroline is not transgender.",
		"Melanie is transgender.",
	}
	for _, s := range different {
		if got := ContentHash(s); got == base {
			t.Errorf("ContentHash(%q) collided with a different fact", s)
		}
	}
}

// The hash is stored as a vertex property and inlined into Cypher, so it has to
// be a fixed-width token that IsContentHash can vouch for.
func TestContentHash_IsAFixedWidthHexToken(t *testing.T) {
	h := ContentHash("Caroline adopted a rescue dog named Biscuit.")
	if !IsContentHash(h) {
		t.Errorf("ContentHash returned %q, which IsContentHash rejects; "+
			"the guard and the producer have drifted apart", h)
	}
	if ContentHash("") != "" {
		t.Error("empty content produced a hash; there is nothing to deduplicate")
	}
	if ContentHash("   !!!   ") != "" {
		t.Error("content with no tokens produced a hash")
	}
}

// IsContentHash is what makes inlining a hash into a Cypher literal safe, the
// same role isPlainUUID plays in internal/graph. The first entries are real
// injection attempts: each closes the string literal and appends its own
// clause.
func TestIsContentHash_RejectsAnythingItDoesNotFullyRecognise(t *testing.T) {
	rejected := []string{
		"",
		"abc",
		"' OR '1'='1",
		"deadbeefdeadbeefdeadbeefdeadbee'} RETURN 1 //",
		"DEADBEEFDEADBEEFDEADBEEFDEADBEEF", // uppercase: not what we emit
		"deadbeefdeadbeefdeadbeefdeadbeeg", // 'g' is not hex
		"deadbeefdeadbeefdeadbeefdeadbee",  // one short
		"deadbeefdeadbeefdeadbeefdeadbeef0",
		"deadbeef-dead-beef-dead-beefdeadbeef",
	}
	for _, s := range rejected {
		if IsContentHash(s) {
			t.Errorf("IsContentHash(%q) accepted a value that is not a hash we "+
				"produced; this value is inlined into a Cypher string literal", s)
		}
	}

	if !IsContentHash(ContentHash("a real memory about something")) {
		t.Error("IsContentHash rejected a hash this package just produced")
	}
}

// ContentTokens keeps punctuation that is part of an identifier and drops
// punctuation that is part of a sentence. Both halves matter: the first keeps
// "node.js" searchable, the second stops "group?" from being its own token.
func TestContentTokens_TrimsOnlyAtTheEdges(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"Caroline is transgender.", []string{"caroline", "is", "transgender"}},
		{"the user's api-key and node.js setup",
			[]string{"the", "user's", "api-key", "and", "node.js", "setup"}},
		{"deploy -- now", []string{"deploy", "--", "now"}},
		{"", nil},
	}

	for _, tt := range tests {
		got := ContentTokens(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("ContentTokens(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ContentTokens(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}
