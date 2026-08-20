// keys.go implements API key hashing and constant-time verification.
//
// Two properties matter, and they pull in different directions:
//
//  1. Keys must not be recoverable from what the server stores. An operator's
//     Kubernetes Secret, a config dump, or a leaked backup should not hand an
//     attacker working credentials.
//  2. Verification runs on EVERY request inside a small CPU budget, so it must
//     be fast. This is the reason NOT to reach for bcrypt or Argon2 here: those
//     are deliberately slow to make password guessing expensive, and applying
//     them per request turns the auth path into a denial-of-service amplifier.
//
// The resolution is that an API key is not a password. A password is short,
// low-entropy, and chosen by a human, so it needs a slow KDF to survive offline
// guessing. A generated 256-bit key has enough entropy that brute force is
// infeasible regardless of hash speed, so a single SHA-256 is sufficient and
// costs microseconds.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// KeyPrefix marks a Kora API key. A fixed, searchable prefix lets
	// secret scanners (GitHub, gitleaks) recognise a leaked key on sight, which
	// is the entire reason providers like Stripe and GitHub adopted the
	// convention.
	KeyPrefix = "ctx0"

	// secretBytes is the entropy behind each key. 256 bits makes brute force
	// infeasible independent of how fast the hash is, which is what justifies
	// using a fast hash.
	secretBytes = 32
)

// Key is a parsed API key: a public identifier and a secret.
//
// Splitting them is what makes keys individually revocable and attributable.
// The id is safe to log, appears in audit trails, and identifies which
// credential acted; the secret never leaves the client after issuance.
type Key struct {
	ID     string
	Secret string
}

// String renders the wire format: ctx0_<id>_<secret>.
func (k Key) String() string {
	return fmt.Sprintf("%s_%s_%s", KeyPrefix, k.ID, k.Secret)
}

// Redacted renders the key for logs: the identifier, never the secret.
func (k Key) Redacted() string {
	return fmt.Sprintf("%s_%s_...", KeyPrefix, k.ID)
}

// GenerateKey mints a new API key with a random id and secret.
func GenerateKey() (Key, error) {
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return Key{}, fmt.Errorf("generate key id: %w", err)
	}
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return Key{}, fmt.Errorf("generate key secret: %w", err)
	}
	return Key{
		ID: hex.EncodeToString(id),
		// URL-safe and unpadded so the key survives being pasted into headers,
		// query strings, and YAML without escaping.
		Secret: base64.RawURLEncoding.EncodeToString(secret),
	}, nil
}

// ParseKey splits a presented key into its parts.
//
// A malformed key is rejected here rather than being compared, so the hot path
// never hashes obvious garbage.
//
// The split is bounded at three fields because the secret is base64url, whose
// alphabet includes the "_" separator itself. Splitting on every "_" rejects
// roughly nine of every ten valid keys, which is the kind of failure that looks
// like a flaky test rather than a bug -- the id is hex, so only the secret can
// contain a separator, and everything after the second one belongs to it.
func ParseKey(s string) (Key, bool) {
	parts := strings.SplitN(s, "_", 3)
	if len(parts) != 3 || parts[0] != KeyPrefix || parts[1] == "" || parts[2] == "" {
		return Key{}, false
	}
	return Key{ID: parts[1], Secret: parts[2]}, true
}

// HashKey returns the stored form of a key.
//
// SHA-256 of the whole presented string: fast enough for the request path, and
// preimage-resistant, so the stored value cannot be turned back into a working
// credential. See the package comment for why this is not a slow KDF.
func HashKey(presented string) string {
	sum := sha256.Sum256([]byte(presented))
	return hex.EncodeToString(sum[:])
}

// hashesEqual compares two hex-encoded hashes in constant time.
//
// A byte-by-byte comparison leaks how many leading characters matched through
// its timing, which is enough to reconstruct a secret one character at a time.
// The lengths are equal for well-formed hashes, and ConstantTimeCompare returns
// 0 for mismatched lengths anyway.
func hashesEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
