package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	apiKeyHeader     = "x-api-key"
	defaultRateLimit = 100 // requests per minute
)

// APIKeyAuth validates API keys and enforces rate limits.
type APIKeyAuth struct {
	validKeys map[string]bool
	rateLimit int

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// NewAPIKeyAuth creates a new API key authenticator.
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

// UnaryInterceptor returns a gRPC unary server interceptor for API key auth.
func (a *APIKeyAuth) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Skip auth for health checks.
		if info.FullMethod == "/context0.v1.HealthService/Health" {
			return handler(ctx, req)
		}

		// No keys configured = auth disabled.
		if len(a.validKeys) == 0 {
			return handler(ctx, req)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		keys := md.Get(apiKeyHeader)
		// grpc-gateway forwards HTTP headers with "grpcgateway-" prefix.
		if len(keys) == 0 {
			keys = md.Get("grpcgateway-" + apiKeyHeader)
		}
		if len(keys) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing API key")
		}

		key := keys[0]
		if !a.validKeys[key] {
			return nil, status.Error(codes.Unauthenticated, "invalid API key")
		}

		if !a.allowRequest(key) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}

		return handler(ctx, req)
	}
}

// HTTPMiddleware wraps an HTTP handler with API key validation.
func (a *APIKeyAuth) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health and metrics endpoints.
		if r.URL.Path == "/v1/health" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		// No keys configured = auth disabled.
		if len(a.validKeys) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, `{"error":"missing API key"}`, http.StatusUnauthorized)
			return
		}

		if !a.validKeys[key] {
			http.Error(w, `{"error":"invalid API key"}`, http.StatusUnauthorized)
			return
		}

		if !a.allowRequest(key) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// allowRequest checks the token bucket for rate limiting.
func (a *APIKeyAuth) allowRequest(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	bucket, ok := a.buckets[key]
	if !ok {
		bucket = newTokenBucket(a.rateLimit)
		a.buckets[key] = bucket
	}

	return bucket.allow()
}

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	tokens   float64
	max      float64
	rate     float64 // tokens per second
	lastTime time.Time
}

func newTokenBucket(perMinute int) *tokenBucket {
	max := float64(perMinute)
	return &tokenBucket{
		tokens:   max,
		max:      max,
		rate:     max / 60.0,
		lastTime: time.Now(),
	}
}

func (b *tokenBucket) allow() bool {
	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.lastTime = now

	b.tokens += elapsed * b.rate
	if b.tokens > b.max {
		b.tokens = b.max
	}

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}
