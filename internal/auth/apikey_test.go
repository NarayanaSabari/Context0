package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1", "key2"}, 100)

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-api-key", "key1"))

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	resp, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 100)

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-api-key", "wrong-key"))

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 100)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	_, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestAPIKeyAuth_SkipHealth(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 100)

	// No API key in context — should still pass for health check.
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	resp, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/context0.v1.HealthService/Health"}, handler)
	if err != nil {
		t.Fatalf("health check should skip auth, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestAPIKeyAuth_NoKeysConfigured(t *testing.T) {
	auth := NewAPIKeyAuth(nil, 100)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	resp, err := auth.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("no keys configured should disable auth, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestHTTPMiddleware_ValidKey(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 100)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/v1/memories/query", nil)
	req.Header.Set("X-API-Key", "key1")
	rr := httptest.NewRecorder()

	auth.HTTPMiddleware(handler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestHTTPMiddleware_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 100)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/v1/memories/query", nil)
	req.Header.Set("X-API-Key", "wrong")
	rr := httptest.NewRecorder()

	auth.HTTPMiddleware(handler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestHTTPMiddleware_SkipHealthAndMetrics(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 100)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, path := range []string{"/v1/health", "/metrics"} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()

		auth.HTTPMiddleware(handler).ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("path %s should skip auth, got %d", path, rr.Code)
		}
	}
}

func TestTokenBucket_RateLimit(t *testing.T) {
	auth := NewAPIKeyAuth([]string{"key1"}, 2) // 2 per minute

	// First 2 requests should pass.
	if !auth.allowRequest("key1") {
		t.Fatal("first request should be allowed")
	}
	if !auth.allowRequest("key1") {
		t.Fatal("second request should be allowed")
	}

	// Third should be rate limited.
	if auth.allowRequest("key1") {
		t.Fatal("third request should be rate limited")
	}
}

// TestKeysAreNotStoredInPlaintext is the point of hashing: an attacker who
// reads the process's configuration state should not obtain usable credentials.
func TestKeysAreNotStoredInPlaintext(t *testing.T) {
	const secret = "ctx0_abc123_supersecretvalue"
	a := NewAPIKeyAuth([]string{secret}, 100)

	for stored := range a.validKeys {
		if strings.Contains(stored, "supersecretvalue") || stored == secret {
			t.Fatalf("the raw key is recoverable from stored state: %q", stored)
		}
	}

	// And the hash must still authenticate the real key.
	if _, ok := a.verify(secret); !ok {
		t.Error("hashing broke verification of a valid key")
	}
	if _, ok := a.verify(secret + "x"); ok {
		t.Error("a near-miss key was accepted")
	}
}

// TestVerifyReturnsKeyIdentity checks that requests can be attributed to a
// specific credential, which is what makes per-key revocation and audit logging
// possible.
func TestVerifyReturnsKeyIdentity(t *testing.T) {
	a := NewAPIKeyAuth([]string{"ctx0_aaaa_one", "ctx0_bbbb_two"}, 100)

	id1, ok := a.verify("ctx0_aaaa_one")
	if !ok || id1 != "aaaa" {
		t.Errorf("verify returned id %q ok=%v, want \"aaaa\" true", id1, ok)
	}
	id2, _ := a.verify("ctx0_bbbb_two")
	if id2 == id1 {
		t.Error("two distinct keys share an identity; per-key attribution is impossible")
	}
}

// TestLegacyKeysStillWork: operators upgrading in place must not have their
// deployment stop authenticating because their keys predate the ctx0_ format.
func TestLegacyKeysStillWork(t *testing.T) {
	a := NewAPIKeyAuth([]string{"an-old-freeform-key"}, 100)

	id, ok := a.verify("an-old-freeform-key")
	if !ok {
		t.Fatal("a pre-existing key stopped working after the format change")
	}
	if strings.Contains(id, "freeform") {
		t.Errorf("legacy key identity %q embeds the secret", id)
	}
}

// TestHTTPMiddlewareDeniesByDefault is the regression test for an
// allow-by-default rule that exempted "any path not under /v1/". Under that
// rule every future route -- a second API version, an admin endpoint, a debug
// handler -- would have shipped unauthenticated, silently.
func TestHTTPMiddlewareDeniesByDefault(t *testing.T) {
	a := NewAPIKeyAuth([]string{"ctx0_test_secret"}, 100)
	handler := a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Paths that must be reachable without credentials.
	for _, path := range []string{"/livez", "/readyz", "/startupz", "/metrics", "/v1/health"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d without a key, want 200: probes and scraping must not need credentials",
				path, rec.Code)
		}
	}

	// Everything else must not be.
	for _, path := range []string{
		"/v1/memories",
		"/v2/memories",  // a future API version
		"/admin/keys",   // a future admin surface
		"/debug/pprof/", // an accidentally-mounted profiler
		"/",             // the root
		"/anything/else",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d without a key, want 401: unlisted paths must be denied by default",
				path, rec.Code)
		}
	}
}

// TestUnauthorizedResponsesDoNotDistinguishMissingFromInvalid: a different
// message for "missing" and "wrong" tells an attacker whether a guessed key
// exists.
func TestUnauthorizedResponsesDoNotDistinguishMissingFromInvalid(t *testing.T) {
	a := NewAPIKeyAuth([]string{"ctx0_test_secret"}, 100)
	handler := a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/memories", nil))

	wrong := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/memories", nil)
	req.Header.Set("X-API-Key", "ctx0_test_wrongsecret")
	handler.ServeHTTP(wrong, req)

	if missing.Code != wrong.Code || missing.Body.String() != wrong.Body.String() {
		t.Errorf("missing and invalid keys are distinguishable: %d %q vs %d %q",
			missing.Code, missing.Body.String(), wrong.Code, wrong.Body.String())
	}
}

// TestGeneratedKeysRoundTrip covers the minting path.
func TestGeneratedKeysRoundTrip(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	parsed, ok := ParseKey(k.String())
	if !ok {
		t.Fatalf("a freshly generated key %q does not parse", k.String())
	}
	if parsed.ID != k.ID || parsed.Secret != k.Secret {
		t.Errorf("round trip changed the key: %+v -> %+v", k, parsed)
	}
	if strings.Contains(k.Redacted(), k.Secret) {
		t.Errorf("Redacted() leaks the secret: %q", k.Redacted())
	}

	// Two generated keys must not collide.
	other, _ := GenerateKey()
	if other.Secret == k.Secret {
		t.Error("two generated keys share a secret")
	}

	// Generation is random, so a single round trip only catches a
	// separator-in-secret bug about nine times in ten and would look flaky
	// rather than broken. Pin the case directly.
	underscored := Key{ID: "abc123", Secret: "a_b_c-d_e"}
	back, ok := ParseKey(underscored.String())
	if !ok {
		t.Fatalf("key with underscores in the secret does not parse: %q", underscored.String())
	}
	if back.Secret != underscored.Secret {
		t.Errorf("secret truncated at a separator: got %q, want %q", back.Secret, underscored.Secret)
	}

	// And confirm the generator really can emit such secrets, so the case above
	// is not hypothetical.
	var sawSeparator bool
	for range 200 {
		g, err := GenerateKey()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if strings.Contains(g.Secret, "_") {
			sawSeparator = true
		}
		if _, ok := ParseKey(g.String()); !ok {
			t.Fatalf("generated key does not parse: %q", g.String())
		}
	}
	if !sawSeparator {
		t.Error("expected base64url secrets to sometimes contain \"_\"")
	}
}

func TestParseKeyRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"", "ctx0", "ctx0_", "ctx0_id", "ctx0__secret", "ctx0_id_",
		"wrong_id_secret",
		// Note: "ctx0_id_secret_extra" is deliberately absent. The secret is
		// base64url and may contain "_", so everything past the second
		// separator is part of the secret, not an extra field.
	} {
		if _, ok := ParseKey(s); ok {
			t.Errorf("ParseKey(%q) accepted a malformed key", s)
		}
	}
}

// TestBucketMapIsBounded: the bucket map is keyed by verified key identity, so
// it cannot be grown by an attacker, but a process that has seen many rotated
// keys would otherwise hold a bucket for every key it ever served.
func TestBucketMapIsBounded(t *testing.T) {
	a := NewAPIKeyAuth([]string{"ctx0_test_secret"}, 100)

	for i := range maxBuckets * 2 {
		a.allowRequest(fmt.Sprintf("key-%d", i))
	}

	a.mu.Lock()
	n := len(a.buckets)
	a.mu.Unlock()

	if n > maxBuckets {
		t.Errorf("bucket map holds %d entries, want at most %d", n, maxBuckets)
	}
}

// TestRateLimitAllowsNormalThroughput pins the default against measured service
// cost. The old default of 100/min (1.6/s) was never noticed because rate
// limiting only engages once a key is configured; making authentication
// mandatory would have throttled every deployment to a crawl.
func TestRateLimitAllowsNormalThroughput(t *testing.T) {
	a := NewAPIKeyAuth([]string{"ctx0_test_secret"}, 0) // 0 => default

	// A burst of 1000 requests is well within what a single client does during
	// a bulk import, and must not be rejected.
	for i := range 1000 {
		if !a.allowRequest("id") {
			t.Fatalf("request %d of 1000 was rate limited at the default limit", i)
		}
	}
}

// TestRateLimitSetsRetryAfter: without this header a well-behaved client has no
// way to know how long to wait, so it retries immediately and converts one
// rejection into a hot loop. Observed as 450k rejections in a 120s soak.
func TestRateLimitSetsRetryAfter(t *testing.T) {
	a := NewAPIKeyAuth([]string{"ctx0_test_secret"}, 60) // 1/s
	handler := a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var limited *httptest.ResponseRecorder
	for range 200 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/memories", nil)
		req.Header.Set("X-API-Key", "ctx0_test_secret")
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited = rec
			break
		}
	}
	if limited == nil {
		t.Fatal("never hit the rate limit at 60/min over 200 requests")
	}

	got := limited.Header().Get("Retry-After")
	if got == "" {
		t.Fatal("429 response carries no Retry-After header")
	}
	secs, err := strconv.Atoi(got)
	if err != nil || secs < 1 {
		t.Errorf("Retry-After = %q, want a positive integer number of seconds", got)
	}
}
