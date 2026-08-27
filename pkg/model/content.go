// content.go holds the canonical way to tokenize and fingerprint a memory's
// text. It lives in the domain package because three layers need to agree on
// it exactly: the repository persists the fingerprint as a vertex property,
// extraction compares memories with it to decide what is redundant, and any
// disagreement between the two would mean a hash that says "duplicate" while
// the comparison says "distinct".

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// contentHashLen is the width of a ContentHash in characters. A truncated
// SHA-256: 16 bytes of digest is far beyond collision range for a per-project
// fact store, and half the property size of the full digest, which matters
// because it is stored on every vertex.
const contentHashLen = 32

// ContentTokens splits memory text into lowercase word tokens, dropping
// punctuation at a token's edge while keeping punctuation inside one.
//
// The edge-only trim mirrors extractKeywords in internal/service: `api-key`,
// `node.js` and `user's` are single identifiers, not punctuation to be
// stripped. Nothing else is normalised. Stemming or stop-word removal here
// would make "Caroline is not a teacher" indistinguishable from "Caroline is a
// teacher", and these tokens decide which memory gets discarded.
func ContentTokens(text string) []string {
	fields := strings.Fields(strings.ToLower(text))
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, `.,;:!?"'()[]{}<>`)
		if f == "" {
			continue
		}
		tokens = append(tokens, f)
	}
	return tokens
}

// ContentHash is a stable fingerprint of a memory's content, ignoring case,
// punctuation and whitespace but nothing else.
//
// It is the cheap half of write-time consolidation: an exact-duplicate lookup
// needs no embedder, no candidate query and no similarity threshold. Stored as
// a vertex property so the check is an indexed lookup rather than a scan.
//
// Returns "" for content with no tokens at all, which is not a hash any real
// memory can collide with -- callers treat it as "not deduplicable".
func ContentHash(content string) string {
	normalized := strings.Join(ContentTokens(content), " ")
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:contentHashLen/2])
}

// IsContentHash reports whether s is exactly the shape ContentHash produces:
// fixed-length lowercase hex.
//
// This is the guard that makes inlining a hash into Cypher safe, mirroring
// isPlainUUID in internal/graph. Deliberately strict: it rejects anything it
// does not fully recognise rather than trying to sanitise it.
func IsContentHash(s string) bool {
	if len(s) != contentHashLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
