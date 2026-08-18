package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestSetupEmitsParseableJSON is the point of the package: a log line that a
// collector cannot parse into fields is not structured logging, it is a string.
func TestSetupEmitsParseableJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger = logger.With(slog.String("version", "1.2.3"))

	logger.Error("storing embedding failed",
		slog.String("memory_id", "3f2a"),
		slog.String("project_id", "acme"))

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline: %s", err, buf.String())
	}

	for _, key := range []string{"time", "level", "msg", "memory_id", "project_id", "version"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("log record has no %q field: %v", key, rec)
		}
	}
	if rec["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", rec["level"])
	}
	// The id must be its own field, not interpolated into the message, or it
	// cannot be filtered on without re-parsing the string.
	if msg, _ := rec["msg"].(string); strings.Contains(msg, "3f2a") {
		t.Errorf("msg %q inlines a field value instead of attaching it", msg)
	}
}

// TestParseLevel covers the mapping, including the fallback: a typo in
// CONTEXT0_LOG_LEVEL must not silence a deployment.
func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":    slog.LevelDebug,
		"info":     slog.LevelInfo,
		"warn":     slog.LevelWarn,
		"error":    slog.LevelError,
		"WARN":     slog.LevelWarn,
		" info":    slog.LevelInfo,
		"":         slog.LevelInfo,
		"nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestFromContextNeverReturnsNil: a logging call is not worth a panic, and a
// caller forced to nil-check its logger will eventually forget.
func TestFromContextNeverReturnsNil(t *testing.T) {
	if FromContext(t.Context()) == nil {
		t.Error("FromContext returned nil for a context with no logger")
	}

	ctx := WithLogger(t.Context(), nil)
	if FromContext(ctx) == nil {
		t.Error("FromContext returned nil for a context holding a nil logger")
	}

	want := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	if got := FromContext(WithLogger(t.Context(), want)); got != want {
		t.Error("FromContext did not return the logger stored in the context")
	}
}
