package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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

// TestRateLimitBucketEviction covers the bound that keeps the per-key rate
// limit map from growing without limit.
//
// The bucket map is keyed by the verified API key, so it only grows when a key
// is accepted -- but a deployment that rotates keys, or one running with many
// valid keys, accumulates one bucket per key for the life of the process. The
// eviction path had no test at all: mutation testing removed the `oldestKey !=
// ""` guard and every test still passed.
func TestRateLimitBucketEviction(t *testing.T) {
	t.Run("map stays bounded", func(t *testing.T) {
		// A high rate limit so nothing is rejected for being over quota; this
		// test is about map growth, not throttling.
		a := NewAPIKeyAuth([]string{"k"}, 1000000)

		// Push well past the cap. Each distinct key creates a bucket.
		for i := 0; i < maxBuckets+500; i++ {
			a.allowRequest(fmt.Sprintf("key-%d", i))
		}

		a.mu.Lock()
		n := len(a.buckets)
		a.mu.Unlock()

		if n > maxBuckets {
			t.Errorf("bucket map grew to %d entries, above the %d cap: "+
				"an unbounded map is a memory leak on the auth path", n, maxBuckets)
		}
	})

	t.Run("evicts the idlest key, not an active one", func(t *testing.T) {
		a := NewAPIKeyAuth([]string{"k"}, 1000000)

		// Seed the map to exactly the cap.
		for i := 0; i < maxBuckets; i++ {
			a.allowRequest(fmt.Sprintf("seed-%d", i))
		}

		// Make one key clearly the most recently used, and one clearly idlest.
		active := "seed-0"
		a.allowRequest(active)

		a.mu.Lock()
		idlest := ""
		var oldest time.Time
		for k, b := range a.buckets {
			if idlest == "" || b.lastTime.Before(oldest) {
				idlest, oldest = k, b.lastTime
			}
		}
		a.mu.Unlock()

		// One more distinct key forces exactly one eviction.
		a.allowRequest("newcomer")

		a.mu.Lock()
		_, idlestPresent := a.buckets[idlest]
		_, activePresent := a.buckets[active]
		_, newcomerPresent := a.buckets["newcomer"]
		a.mu.Unlock()

		if idlestPresent {
			t.Errorf("key %q was the idlest but survived eviction", idlest)
		}
		if !activePresent {
			t.Errorf("the most recently used key %q was evicted; "+
				"evicting an active key resets its allowance mid-burst", active)
		}
		if !newcomerPresent {
			t.Error("the new key was not inserted after eviction")
		}
	})

	t.Run("a continuously active key is never evicted", func(t *testing.T) {
		// Eviction resets the victim's allowance, so evicting a key that is
		// mid-burst would let it bypass the rate limit. LRU is what prevents
		// that: a key that keeps making requests is never the idlest.
		const limit = 5
		a := NewAPIKeyAuth([]string{"k"}, limit)

		victim := "heavy-user"
		for i := 0; i < limit; i++ {
			if !a.allowRequest(victim) {
				t.Fatalf("request %d was rejected before the quota was spent", i)
			}
		}
		if a.allowRequest(victim) {
			t.Fatal("quota was not enforced on the first pass")
		}

		// Churn other keys through the map while the victim keeps requesting,
		// which is what a real over-quota caller does. Each touch refreshes the
		// victim's lastTime, so it stays off the eviction list.
		for i := 0; i < maxBuckets+50; i++ {
			a.allowRequest(fmt.Sprintf("churn-%d", i))
			if a.allowRequest(victim) {
				t.Fatalf("over-quota key was allowed at churn iteration %d: "+
					"its bucket was evicted and reset, bypassing the rate limit", i)
			}
		}
	})

	t.Run("map is bounded by configured keys on the real request path", func(t *testing.T) {
		// The property that actually protects the process: allowRequest is
		// only reached with an ID returned by verify(), so the map is bounded
		// by the number of configured keys regardless of attacker input. The
		// maxBuckets eviction above is defense in depth behind this.
		a := NewAPIKeyAuth([]string{"real-key-1", "real-key-2"}, 1000000)

		handler := a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		// Hammer with distinct invalid keys, which is the input an attacker
		// controls. None of them may create a bucket.
		for i := 0; i < 2000; i++ {
			req := httptest.NewRequest(http.MethodGet, "/v1/memories", nil)
			req.Header.Set("X-API-Key", fmt.Sprintf("attacker-key-%d", i))
			handler.ServeHTTP(httptest.NewRecorder(), req)
		}
		// Plus legitimate traffic on the real keys.
		for _, k := range []string{"real-key-1", "real-key-2"} {
			req := httptest.NewRequest(http.MethodGet, "/v1/memories", nil)
			req.Header.Set("X-API-Key", k)
			handler.ServeHTTP(httptest.NewRecorder(), req)
		}

		a.mu.Lock()
		n := len(a.buckets)
		a.mu.Unlock()

		if n > 2 {
			t.Errorf("bucket map holds %d entries for 2 configured keys: "+
				"unauthenticated input must not allocate buckets", n)
		}
	})
}

// TestRestRequestSpendsOneToken covers a rate limit that charged twice.
//
// A REST call traverses both auth layers: HTTPMiddleware authenticates it,
// then grpc-gateway dials the local gRPC server where UnaryInterceptor
// authenticates it again. Both called allowRequest, so every REST request
// consumed two tokens and the effective limit was half the configured one.
// Measured against the deployed API with the limit set to 60/minute: the first
// 429 arrived after 30 successes.
//
// Halving the limit is the smaller problem. The layers can also disagree --
// the HTTP layer admits a request and the gRPC layer rejects it -- so a caller
// gets a 429 for a request that was already accepted and partly processed.
func TestRestRequestSpendsOneToken(t *testing.T) {
	const limit = 20
	a := NewAPIKeyAuth([]string{"test-key"}, limit)

	// Stand in for the gateway: after the HTTP middleware runs, the same
	// request reaches the gRPC interceptor carrying whatever headers the
	// gateway forwards.
	var reachedGRPC int
	handler := a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		md := metadata.New(map[string]string{})
		if v := r.Header.Get(RateLimitedHeader); v != "" {
			md.Set(RateLimitedHeader, v)
		}
		md.Set("x-api-key", "test-key")
		ctx := metadata.NewIncomingContext(r.Context(), md)

		_, err := a.UnaryInterceptor()(ctx, nil,
			&grpc.UnaryServerInfo{FullMethod: "/context0.v1.Context0/Query"},
			func(context.Context, any) (any, error) { return nil, nil })
		if err != nil {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		reachedGRPC++
		w.WriteHeader(http.StatusOK)
	}))

	accepted := 0
	for i := 0; i < limit*2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/memories/query?query=x", nil)
		req.Header.Set("X-API-Key", "test-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			accepted++
		}
	}

	// A full bucket of `limit` tokens must admit `limit` requests, not half.
	if accepted != limit {
		t.Errorf("a %d-request budget admitted %d REST requests; each request "+
			"is being charged more than one token, so the effective limit is "+
			"not the configured one", limit, accepted)
	}
	if reachedGRPC != accepted {
		t.Errorf("%d requests passed the HTTP layer but %d reached the handler: "+
			"the two layers disagree, so a caller can be rejected after its "+
			"request was already accepted", accepted, reachedGRPC)
	}
}

// TestForgedRateLimitHeaderCannotBypassTheLimit is the security half of the
// fix above.
//
// The marker is only safe because the HTTP middleware sets it on every request
// it admits, overwriting anything the client sent. If a caller could supply it,
// the rate limit would be advisory: send the header, skip the bucket.
func TestForgedRateLimitHeaderCannotBypassTheLimit(t *testing.T) {
	const limit = 10
	a := NewAPIKeyAuth([]string{"test-key"}, limit)

	handler := a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	accepted := 0
	for i := 0; i < limit*5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/memories/query?query=x", nil)
		req.Header.Set("X-API-Key", "test-key")
		// The client claims its token was already spent.
		req.Header.Set(RateLimitedHeader, "1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			accepted++
		}
	}

	if accepted > limit {
		t.Errorf("a client that set %s bypassed the rate limit: %d requests "+
			"admitted against a budget of %d", RateLimitedHeader, accepted, limit)
	}
}

// TestDirectGRPCCallerIsStillLimited: an external gRPC client never passes
// through the HTTP middleware, so it must be limited normally.
func TestDirectGRPCCallerIsStillLimited(t *testing.T) {
	const limit = 15
	a := NewAPIKeyAuth([]string{"test-key"}, limit)
	interceptor := a.UnaryInterceptor()

	accepted := 0
	for i := 0; i < limit*3; i++ {
		md := metadata.New(map[string]string{"x-api-key": "test-key"})
		ctx := metadata.NewIncomingContext(context.Background(), md)
		if _, err := interceptor(ctx, nil,
			&grpc.UnaryServerInfo{FullMethod: "/context0.v1.Context0/Query"},
			func(context.Context, any) (any, error) { return nil, nil }); err == nil {
			accepted++
		}
	}

	if accepted != limit {
		t.Errorf("a direct gRPC caller was admitted %d times against a budget "+
			"of %d", accepted, limit)
	}

	// A direct gRPC caller must not be able to forge its way past the bucket.
	//
	// The gRPC listener is a service port, reachable by anything in the
	// cluster, so a marker that was merely present would let exactly the
	// callers most worth limiting skip the bucket. The marker carries a
	// per-process random value instead, which an external caller cannot
	// produce.
	b := NewAPIKeyAuth([]string{"test-key"}, limit)
	bInterceptor := b.UnaryInterceptor()
	forged := 0
	for i := 0; i < limit*3; i++ {
		md := metadata.New(map[string]string{
			"x-api-key":       "test-key",
			RateLimitedHeader: "1", // a guess
		})
		ctx := metadata.NewIncomingContext(context.Background(), md)
		if _, err := bInterceptor(ctx, nil,
			&grpc.UnaryServerInfo{FullMethod: "/context0.v1.Context0/Query"},
			func(context.Context, any) (any, error) { return nil, nil }); err == nil {
			forged++
		}
	}
	if forged != limit {
		t.Errorf("a direct gRPC caller presenting a forged %s was admitted %d "+
			"times against a budget of %d; the rate limit can be bypassed by "+
			"setting a header", RateLimitedHeader, forged, limit)
	}
}

// TestRateLimitTokenIsUnguessable: the marker's value is what makes it
// trustworthy, so it must be long, random, and not a constant.
func TestRateLimitTokenIsUnguessable(t *testing.T) {
	if rateLimitToken == "" {
		t.Fatal("the rate-limit token is empty; the marker would never be believed")
	}
	if len(rateLimitToken) < 32 {
		t.Errorf("the rate-limit token is only %d characters; it must not be "+
			"feasible to guess", len(rateLimitToken))
	}
	for _, obvious := range []string{"1", "true", "yes", "context0"} {
		if rateLimitToken == obvious {
			t.Errorf("the rate-limit token is the guessable constant %q", obvious)
		}
	}
	// Two generations must differ, or it is not random.
	if a, b := newRateLimitToken(), newRateLimitToken(); a == b {
		t.Error("newRateLimitToken returned the same value twice")
	}
}
