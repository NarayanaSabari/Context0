// Package auth provides API key authentication and per-key rate limiting
// for both gRPC and HTTP endpoints. It uses a token bucket algorithm to
// enforce configurable requests-per-minute limits on each API key independently.
package auth

import (
	"context"
	"net/http"
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
	defaultRateLimit = 100
)

// APIKeyAuth validates API keys and enforces per-key rate limits using
// a token bucket algorithm. Each unique API key gets its own bucket,
// allowing burst traffic up to the configured limit while smoothly
// refilling at a constant rate.
//
// When no keys are configured (empty slice passed to NewAPIKeyAuth),
// authentication is effectively disabled and all requests are allowed.
type APIKeyAuth struct {
	// validKeys is a set of accepted API keys for O(1) lookup.
	validKeys map[string]bool

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
func NewAPIKeyAuth(keys []string, rateLimit int) *APIKeyAuth {
	if rateLimit <= 0 {
		rateLimit = defaultRateLimit
	}
	valid := make(map[string]bool, len(keys))
	for _, k := range keys {
		valid[k] = true
	}
	return &APIKeyAuth{
		validKeys: valid,
		rateLimit: rateLimit,
		buckets:   make(map[string]*tokenBucket),
	}
}

// UnaryInterceptor returns a gRPC unary server interceptor that validates
// the API key from incoming metadata and enforces rate limits. Health check
// RPCs are exempt from authentication.
//
// The interceptor checks two metadata key prefixes because grpc-gateway
// forwards HTTP headers with a "grpcgateway-" prefix.
func (a *APIKeyAuth) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Skip auth for health checks so liveness probes always succeed.
		if info.FullMethod == "/context0.v1.HealthService/Health" {
			return handler(ctx, req)
		}

		// No keys configured means auth is disabled; allow all requests.
		if len(a.validKeys) == 0 {
			return handler(ctx, req)
		}

		// Extract gRPC metadata (carries HTTP headers in gateway mode).
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		// Try the canonical header first, then the grpc-gateway prefixed form.
		keys := md.Get(apiKeyHeader)
		if len(keys) == 0 {
			keys = md.Get("grpcgateway-" + apiKeyHeader)
		}
		if len(keys) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing API key")
		}

		// Validate the key against the allow-list.
		key := keys[0]
		if !a.validKeys[key] {
			return nil, status.Error(codes.Unauthenticated, "invalid API key")
		}

		// Enforce per-key rate limiting via token bucket.
		if !a.allowRequest(key) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}

		return handler(ctx, req)
	}
}

// HTTPMiddleware wraps an HTTP handler with API key validation and rate
// limiting. Paths exempt from authentication:
//   - /v1/health -- liveness/readiness probes
//   - /metrics   -- Prometheus scraping
//   - any path not under /v1/ -- serves the web UI or static assets
func (a *APIKeyAuth) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow health, metrics, and non-API paths without authentication.
		if r.URL.Path == "/v1/health" || r.URL.Path == "/metrics" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		// No keys configured means auth is disabled; allow all requests.
		if len(a.validKeys) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		// Read the API key from the standard HTTP header.
		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, `{"error":"missing API key"}`, http.StatusUnauthorized)
			return
		}

		if !a.validKeys[key] {
			http.Error(w, `{"error":"invalid API key"}`, http.StatusUnauthorized)
			return
		}

		// Enforce per-key rate limiting via token bucket.
		if !a.allowRequest(key) {
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
		// First request for this key; create a full bucket.
		bucket = newTokenBucket(a.rateLimit)
		a.buckets[key] = bucket
	}

	return bucket.allow()
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
