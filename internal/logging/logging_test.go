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
// KORA_LOG_LEVEL must not silence a deployment.
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

// TestSetupAppliesItsOptions drives Setup itself rather than an
// equivalent logger built by hand.
//
// TestSetupEmitsParseableJSON constructs its own slog.JSONHandler, so it
// asserts that slog works, not that Setup configures it correctly. Every
// option Setup reads -- level, format, version -- went unverified: mutation
// testing could delete the version attachment, invert the format choice, or
// change the level threshold without any test noticing.
func TestSetupAppliesItsOptions(t *testing.T) {
	t.Run("version is attached to every record", func(t *testing.T) {
		var buf bytes.Buffer
		logger := setup(Options{Version: "1.2.3"}, &buf)
		logger.Error("something failed")

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, buf.String())
		}
		if rec["version"] != "1.2.3" {
			t.Errorf("version = %v, want 1.2.3: without it, lines from a rolling "+
				"deployment cannot be attributed to the build that emitted them", rec["version"])
		}
	})

	t.Run("no version means no empty version field", func(t *testing.T) {
		var buf bytes.Buffer
		setup(Options{}, &buf).Error("something failed")

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		if v, ok := rec["version"]; ok {
			t.Errorf("version field is present as %q with no version configured; "+
				"an empty field is worse than an absent one because it looks "+
				"like a real value", v)
		}
	})

	t.Run("json is the default format", func(t *testing.T) {
		var buf bytes.Buffer
		setup(Options{}, &buf).Info("hello")
		if !json.Valid(bytes.TrimSpace(buf.Bytes())) {
			t.Errorf("default output is not JSON: %s", buf.String())
		}
	})

	t.Run("text format is honoured", func(t *testing.T) {
		var buf bytes.Buffer
		setup(Options{Format: "text"}, &buf).Info("hello")
		out := buf.String()
		if json.Valid(bytes.TrimSpace(buf.Bytes())) {
			t.Errorf("text format produced JSON: %s", out)
		}
		if !strings.Contains(out, "msg=hello") {
			t.Errorf("text output %q does not look like slog text format", out)
		}
	})

	t.Run("format matching is case-insensitive", func(t *testing.T) {
		for _, f := range []string{"TEXT", "Text", "tExT"} {
			var buf bytes.Buffer
			setup(Options{Format: f}, &buf).Info("hello")
			if json.Valid(bytes.TrimSpace(buf.Bytes())) {
				t.Errorf("format %q produced JSON; matching must be case-insensitive", f)
			}
		}
	})

	t.Run("an unknown format falls back to json", func(t *testing.T) {
		var buf bytes.Buffer
		setup(Options{Format: "yaml"}, &buf).Info("hello")
		if !json.Valid(bytes.TrimSpace(buf.Bytes())) {
			t.Errorf("an unrecognised format did not fall back to JSON: %s", buf.String())
		}
	})
}

// TestSetupLevelFiltering: the configured level must actually filter records.
// A level that does not apply is how a deployment ends up either blind or
// drowning in debug output.
func TestSetupLevelFiltering(t *testing.T) {
	cases := []struct {
		level     string
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
	}{
		{"debug", true, true, true},
		{"info", false, true, true},
		{"warn", false, false, true},
		{"error", false, false, false},
		{"", false, true, true},         // default is info
		{"nonsense", false, true, true}, // fallback is info, never silence
	}

	for _, tc := range cases {
		t.Run("level="+tc.level, func(t *testing.T) {
			emitted := func(fn func(l *slog.Logger)) bool {
				var buf bytes.Buffer
				fn(setup(Options{Level: tc.level}, &buf))
				return buf.Len() > 0
			}

			if got := emitted(func(l *slog.Logger) { l.Debug("d") }); got != tc.wantDebug {
				t.Errorf("debug emitted = %v, want %v", got, tc.wantDebug)
			}
			if got := emitted(func(l *slog.Logger) { l.Info("i") }); got != tc.wantInfo {
				t.Errorf("info emitted = %v, want %v", got, tc.wantInfo)
			}
			if got := emitted(func(l *slog.Logger) { l.Warn("w") }); got != tc.wantWarn {
				t.Errorf("warn emitted = %v, want %v", got, tc.wantWarn)
			}
			// Error must never be filtered out, whatever the configuration.
			if !emitted(func(l *slog.Logger) { l.Error("e") }) {
				t.Errorf("level %q suppressed an Error record; a misconfigured "+
					"level must never silence failures", tc.level)
			}
		})
	}
}

// TestSetupAddsSourceOnlyBelowInfo: source location costs a runtime.Callers on
// every record, which is why it is limited to debug. If that condition
// inverted, a production deployment at info would pay it on every line.
func TestSetupAddsSourceOnlyBelowInfo(t *testing.T) {
	hasSource := func(level string) bool {
		var buf bytes.Buffer
		setup(Options{Level: level}, &buf).Error("boom")
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		_, ok := rec["source"]
		return ok
	}

	if !hasSource("debug") {
		t.Error("debug records carry no source location, which is the reason " +
			"to run at debug in the first place")
	}
	if hasSource("info") {
		t.Error("info records carry source location; that costs a runtime.Callers " +
			"on every record on the hot path")
	}
	if hasSource("error") {
		t.Error("error-level configuration still collects source location")
	}
}

// TestSetupInstallsTheDefaultLogger: packages calling slog directly depend on
// this, so a Setup that built a logger without installing it would leave them
// writing to the pre-configured default.
func TestSetupInstallsTheDefaultLogger(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	logger := Setup(Options{Version: "install-test"})
	if slog.Default() != logger {
		t.Error("Setup did not install its logger as the default; packages that " +
			"call slog directly would not be covered by this configuration")
	}
}
