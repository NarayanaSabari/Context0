package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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
