// Package auth provides API key authentication and per-key rate limiting
// for both gRPC and HTTP endpoints. It uses a token bucket algorithm to
// enforce configurable requests-per-minute limits on each API key independently.
package auth

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// apiKeyHeader is the canonical lowercase HTTP/gRPC metadata header
	// used to transmit the API key.
	apiKeyHeader = "x-api-key"

	// defaultRateLimit is the fallback rate limit (requests per minute)
	// when a non-positive value is supplied to NewAPIKeyAuth.
	//
	// 6000/min (100/s) is chosen against measured service behaviour: a store
	// costs ~4ms and an unscoped query ~3ms, so a single client can drive far
	// more than the old 100/min (1.6/s) without the server breaking a sweat.
	// The limit exists to stop a runaway client from monopolising the pool, not
	// to ration normal use. The previous value was never felt because rate
	// limiting only runs once a key is configured, and no key was configured
	// until authentication became mandatory -- enabling auth would otherwise
	// have silently throttled every deployment to 1.6 requests per second.
	defaultRateLimit = 6000

	// maxBuckets caps the per-key bucket map. Buckets are keyed by verified key
	// identity, so an attacker cannot grow this map -- but a deployment that
	// rotates keys often would still accumulate entries for the lifetime of the
	// process. Evicting the idlest entry past this bound keeps that bounded.
	maxBuckets = 4096
)

// APIKeyAuth validates API keys and enforces per-key rate limits using
// a token bucket algorithm. Each unique API key gets its own bucket,
// allowing burst traffic up to the configured limit while smoothly
// refilling at a constant rate.
//
// When no keys are configured (empty slice passed to NewAPIKeyAuth),
// authentication is effectively disabled and all requests are allowed.
type APIKeyAuth struct {
	// validKeys maps the SHA-256 of each accepted key to the key's public
	// identifier.
	//
	// Keys are stored hashed so that a leaked config dump, memory snapshot, or
	// backup does not hand over working credentials. The identifier is kept so
	// requests can be attributed to a specific key in logs and metrics without
	// ever recording the secret.
	validKeys map[string]string

	// rateLimit is the maximum number of requests per minute per key.
	rateLimit int

	// mu protects the buckets map from concurrent access.
	mu sync.Mutex

	// buckets maps each API key to its own token bucket instance.
	// Buckets are lazily created on the first request for a given key.
	buckets map[string]*tokenBucket
}

// NewAPIKeyAuth creates a new API key authenticator with the given set of
// valid keys and a per-key rate limit (requests per minute). If rateLimit
// is zero or negative, defaultRateLimit (100 req/min) is used.
//
// Keys are hashed immediately and the plaintext is not retained.
func NewAPIKeyAuth(keys []string, rateLimit int) *APIKeyAuth {
	if rateLimit <= 0 {
		rateLimit = defaultRateLimit
	}
	valid := make(map[string]string, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		// Keys in the documented ctx0_<id>_<secret> form get their real
		// identifier. Anything else is still accepted -- operators upgrading in
		// place should not have their deployment stop working -- but is
		// identified by a hash prefix rather than by a parsed id.
		id := "legacy-" + HashKey(k)[:8]
		if parsed, ok := ParseKey(k); ok {
			id = parsed.ID
		}
		valid[HashKey(k)] = id
	}
	return &APIKeyAuth{
		validKeys: valid,
		rateLimit: rateLimit,
		buckets:   make(map[string]*tokenBucket),
	}
}

// verify checks a presented key and returns its identifier.
//
// The comparison is constant time. A plain map lookup on the hash would already
// avoid leaking the secret through timing, but the explicit comparison keeps
// that property from depending on the map implementation, and the loop is over
// the (small) configured key set rather than anything attacker-controlled.
func (a *APIKeyAuth) verify(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	sum := HashKey(presented)
	for stored, id := range a.validKeys {
		if hashesEqual(stored, sum) {
			return id, true
		}
	}
	return "", false
}

// authedKey is the context key marking a request that presented a valid API
// key. Unexported so no other package can forge it.
type authedKey struct{}

// WithAuthenticated marks ctx as carrying a verified credential.
func WithAuthenticated(ctx context.Context) context.Context {
	return context.WithValue(ctx, authedKey{}, true)
}

// IsAuthenticated reports whether the request presented a valid API key.
//
// Needed because a handful of endpoints must answer without credentials --
// Kubernetes probes cannot present one -- while still not volunteering
// everything they know to an anonymous caller. Defaults to false, so a handler
// that forgets to check discloses less rather than more.
func IsAuthenticated(ctx context.Context) bool {
	v, _ := ctx.Value(authedKey{}).(bool)
	return v
}

// UnaryInterceptor returns a gRPC unary server interceptor that validates
// the API key from incoming metadata and enforces rate limits. Health check
// RPCs are exempt from authentication.
//
// The interceptor checks two metadata key prefixes because grpc-gateway
// forwards HTTP headers with a "grpcgateway-" prefix.
func (a *APIKeyAuth) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Health answers without a credential so probes always succeed, but it
		// is not exempt from being *identified*: an anonymous caller reaches
		// the handler with IsAuthenticated false, and the handler withholds the
		// graph statistics. Before this, `context0 stats` with no key at all
		// returned the version, node count and edge count to anyone who could
		// reach the port.
		if info.FullMethod == "/context0.v1.HealthService/Health" {
			if keyID, ok := a.verify(apiKeyFromMetadata(ctx)); ok && a.allowRequest(keyID) {
				return handler(WithAuthenticated(ctx), req)
			}
			return handler(ctx, req)
		}

		// No keys configured means auth is disabled; allow all requests.
		if len(a.validKeys) == 0 {
			return handler(ctx, req)
		}

		presented := apiKeyFromMetadata(ctx)
		if presented == "" {
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}

		// Validate the key against the allow-list.
		keyID, ok := a.verify(presented)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}

		// Enforce per-key rate limiting via token bucket, keyed on identity so
		// the map never holds credentials.
		if !a.allowRequest(keyID) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}

		return handler(ctx, req)
	}
}

// apiKeyFromMetadata pulls the presented key out of gRPC metadata, returning
// "" when absent.
//
// Two header names are checked because grpc-gateway forwards HTTP headers with
// a "grpcgateway-" prefix, so a REST caller and a gRPC caller present the same
// credential under different keys.
func apiKeyFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	keys := md.Get(apiKeyHeader)
	if len(keys) == 0 {
		keys = md.Get("grpcgateway-" + apiKeyHeader)
	}
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// publicPaths are the only endpoints served without an API key.
//
// An explicit allowlist rather than a pattern. The previous rule exempted
// "anything not under /v1/", which meant every route added in future -- an
// admin endpoint, a second API version, a debug handler -- would be
// unauthenticated by default and nothing would say so. Deny-by-default makes
// the failure mode a 401 on a route someone forgot to list, instead of an open
// door nobody noticed.
var publicPaths = map[string]bool{
	// Kubernetes probes. These must answer before any credential is available
	// and deliberately expose nothing about stored data.
	"/livez":    true,
	"/readyz":   true,
	"/startupz": true,

	// Retained for backward compatibility: earlier chart versions pointed
	// probes here. It reports graph counts, so it is the one public endpoint
	// that leaks anything, and it leaks only totals.
	"/v1/health": true,

	// Prometheus scrapes without credentials. Keep the Service ClusterIP in
	// production: this endpoint is readable by anything that can reach it.
	"/metrics": true,
}

// HTTPMiddleware wraps an HTTP handler with API key validation and rate
// limiting. Every path not in publicPaths requires a valid key.
func (a *APIKeyAuth) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPaths[r.URL.Path] {
			// A probe presents no credential, but a real client hitting
			// /v1/health may. Mark it so the handler can decide how much to
			// disclose rather than treating every caller here as anonymous.
			if keyID, ok := a.verify(r.Header.Get("X-API-Key")); ok && a.allowRequest(keyID) {
				r = r.WithContext(WithAuthenticated(r.Context()))
			}
			next.ServeHTTP(w, r)
			return
		}

		// No keys configured means auth is disabled; allow all requests.
		if len(a.validKeys) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		keyID, ok := a.verify(r.Header.Get("X-API-Key"))
		if !ok {
			// One message for both "missing" and "wrong", so the response does
			// not tell an attacker whether a key exists.
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Rate limit per key identity rather than per secret, so the bucket map
		// never holds credentials.
		if !a.allowRequest(keyID) {
			// Retry-After tells a client how long to wait instead of leaving
			// it to guess. Without it, well-behaved clients retry immediately
			// and turn one rejection into a hot loop against the limiter --
			// observed as 450k rejections in a 120s run.
			w.Header().Set("Retry-After", strconv.Itoa(a.retryAfterSeconds()))
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// allowRequest checks whether the given API key has remaining capacity in
// its token bucket. Buckets are created lazily on first use.
func (a *APIKeyAuth) allowRequest(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	bucket, ok := a.buckets[key]
	if !ok {
		// Reclaim before inserting, so the map cannot grow without bound across
		// a long-lived process that has seen many rotated keys.
		if len(a.buckets) >= maxBuckets {
			a.evictIdlestLocked()
		}
		// First request for this key; create a full bucket.
		bucket = newTokenBucket(a.rateLimit)
		a.buckets[key] = bucket
	}

	return bucket.allow()
}

// evictIdlestLocked drops the least recently used bucket. The caller holds mu.
//
// Evicting a bucket resets that key's allowance, so the victim is the one that
// has gone longest without a request: it is both the least likely to be mid-
// burst and the one whose bucket has most likely refilled anyway.
func (a *APIKeyAuth) evictIdlestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, b := range a.buckets {
		if oldestKey == "" || b.lastTime.Before(oldest) {
			oldestKey, oldest = k, b.lastTime
		}
	}
	if oldestKey != "" {
		delete(a.buckets, oldestKey)
	}
}

// retryAfterSeconds is how long a rejected client should wait before retrying:
// the time for the bucket to refill one token, rounded up, and at least 1 since
// Retry-After has second granularity.
func (a *APIKeyAuth) retryAfterSeconds() int {
	if a.rateLimit <= 0 {
		return 1
	}
	secs := int(math.Ceil(60.0 / float64(a.rateLimit)))
	if secs < 1 {
		return 1
	}
	return secs
}

// tokenBucket implements a token bucket rate limiter. The algorithm works as
// follows:
//
//  1. The bucket starts full with `max` tokens (equal to the per-minute limit).
//  2. On each request, elapsed time since the last request is calculated and
//     tokens are added at a constant refill rate (max / 60 tokens per second).
//  3. Tokens are capped at `max` so the bucket never exceeds the burst limit.
//  4. If at least 1 token is available, the request is allowed and 1 token is
//     consumed. Otherwise the request is rejected.
//
// This provides a smooth rate limit that allows short bursts up to `max`
// while averaging to `max` requests per minute over time.
type tokenBucket struct {
	tokens   float64   // current number of available tokens
	max      float64   // maximum tokens (burst capacity)
	rate     float64   // refill rate in tokens per second
	lastTime time.Time // timestamp of the last allow() call
}

// newTokenBucket creates a token bucket configured for the given requests-
// per-minute rate. The bucket starts full, permitting an initial burst.
func newTokenBucket(perMinute int) *tokenBucket {
	max := float64(perMinute)
	return &tokenBucket{
		tokens:   max,
		max:      max,
		rate:     max / 60.0, // convert per-minute to per-second refill rate
		lastTime: time.Now(),
	}
}

// allow checks whether a single request can proceed. It refills the bucket
// based on elapsed time, then attempts to consume one token.
func (b *tokenBucket) allow() bool {
	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.lastTime = now

	// Refill tokens proportional to elapsed time.
	b.tokens += elapsed * b.rate
	if b.tokens > b.max {
		b.tokens = b.max
	}

	// Reject if fewer than 1 token remains.
	if b.tokens < 1 {
		return false
	}

	// Consume one token and allow the request.
	b.tokens--
	return true
}
