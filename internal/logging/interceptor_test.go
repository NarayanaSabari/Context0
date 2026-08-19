package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// records runs one RPC through the interceptor and returns the parsed lines.
func records(t *testing.T, level slog.Level, handlerErr error) []map[string]any {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level}))
	interceptor := UnaryServerInterceptor(logger)

	_, err := interceptor(
		context.Background(),
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/context0.v1.MemoryService/Store"},
		func(context.Context, any) (any, error) { return "response", handlerErr },
	)
	if (err != nil) != (handlerErr != nil) {
		t.Fatalf("interceptor changed the error: got %v, want %v", err, handlerErr)
	}

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\nline: %s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// TestInterceptorLogsFailures is the gap this closes: a database outage
// returned errors to every client while the server logged nothing at all.
func TestInterceptorLogsFailures(t *testing.T) {
	recs := records(t, slog.LevelInfo, status.Error(codes.Internal, "failed to create memory: connection refused"))
	if len(recs) != 1 {
		t.Fatalf("a failing RPC produced %d log lines, want 1", len(recs))
	}

	rec := recs[0]
	if rec["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR for an Internal error", rec["level"])
	}
	if rec["method"] != "/context0.v1.MemoryService/Store" {
		t.Errorf("method = %v, want the full RPC method", rec["method"])
	}
	if rec["code"] != "Internal" {
		t.Errorf("code = %v, want Internal", rec["code"])
	}
	if _, ok := rec["duration"]; !ok {
		t.Error("no duration recorded")
	}
}

// TestInterceptorSeparatesClientFaultFromServerFault: a caller sending a bad
// argument is not an incident. If both log at ERROR, the level cannot be used
// to alert on anything.
func TestInterceptorSeparatesClientFaultFromServerFault(t *testing.T) {
	client := records(t, slog.LevelInfo, status.Error(codes.InvalidArgument, "content is required"))
	if len(client) != 1 || client[0]["level"] != "INFO" {
		t.Errorf("client fault logged at %v, want INFO", client[0]["level"])
	}

	server := records(t, slog.LevelInfo, status.Error(codes.Unavailable, "database unreachable"))
	if len(server) != 1 || server[0]["level"] != "ERROR" {
		t.Errorf("server fault logged at %v, want ERROR", server[0]["level"])
	}
}

// TestInterceptorDoesNotLogSuccessAtInfo: this engine sustains ~80 RPCs per
// second in a soak, so a line per successful call at info would bury the
// failures this interceptor exists to surface.
func TestInterceptorDoesNotLogSuccessAtInfo(t *testing.T) {
	if recs := records(t, slog.LevelInfo, nil); len(recs) != 0 {
		t.Errorf("a successful RPC produced %d info lines, want 0: %v", len(recs), recs)
	}
	if recs := records(t, slog.LevelDebug, nil); len(recs) != 1 {
		t.Errorf("a successful RPC produced %d debug lines, want 1", len(recs))
	}
}

// TestInterceptorDoesNotLogRequestContent: this service stores whatever a user
// gives it, so request bodies must not be copied into a log aggregator with a
// different retention policy and a wider audience.
func TestInterceptorDoesNotLogRequestContent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	interceptor := UnaryServerInterceptor(logger)

	secret := "my-therapist-said-something-private"
	_, _ = interceptor(
		context.Background(),
		secret,
		&grpc.UnaryServerInfo{FullMethod: "/context0.v1.MemoryService/Store"},
		func(context.Context, any) (any, error) {
			return nil, status.Error(codes.Internal, "boom")
		},
	)

	if strings.Contains(buf.String(), secret) {
		t.Errorf("request content leaked into the log: %s", buf.String())
	}
}

// TestInterceptorProvidesLoggerToHandler: handlers log through the context, so
// a message emitted deep in a call is attributable to the RPC that caused it.
func TestInterceptorProvidesLoggerToHandler(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	interceptor := UnaryServerInterceptor(logger)

	_, _ = interceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/svc/M"},
		func(ctx context.Context, _ any) (any, error) {
			FromContext(ctx).Info("handler-emitted line")
			return nil, nil
		},
	)

	if !strings.Contains(buf.String(), "handler-emitted line") {
		t.Error("the handler's logger did not reach the configured output")
	}
}

// TestInterceptorHandlesNilLogger: the interceptor is constructed during
// server wiring, where a logger that has not been built yet is easy to pass.
// Falling back to slog.Default() keeps that from panicking on the first RPC.
//
// Without the guard, the nil logger is only dereferenced when a request
// actually fails -- so the process starts, serves healthy traffic, and panics
// the first time something goes wrong, which is the worst possible moment.
func TestInterceptorHandlesNilLogger(t *testing.T) {
	interceptor := UnaryServerInterceptor(nil)

	// A failing handler, because that is the path that logs.
	_, err := interceptor(
		context.Background(),
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/context0.v1.MemoryService/Store"},
		func(ctx context.Context, req any) (any, error) {
			return nil, status.Error(codes.Internal, "boom")
		},
	)
	if err == nil {
		t.Fatal("expected the handler error to propagate")
	}

	// And the success path, which also touches the logger.
	if _, err := interceptor(
		context.Background(),
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/context0.v1.MemoryService/Query"},
		func(ctx context.Context, req any) (any, error) { return "ok", nil },
	); err != nil {
		t.Fatalf("success path returned an error: %v", err)
	}

	// The handler must still receive a usable logger from the context, not a
	// nil one that panics on first use.
	if _, err := interceptor(
		context.Background(),
		"request",
		&grpc.UnaryServerInfo{FullMethod: "/context0.v1.MemoryService/Store"},
		func(ctx context.Context, req any) (any, error) {
			l := FromContext(ctx)
			if l == nil {
				t.Error("FromContext returned nil inside the handler")
				return nil, nil
			}
			l.Info("handler logging through the context logger")
			return "ok", nil
		},
	); err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
}
