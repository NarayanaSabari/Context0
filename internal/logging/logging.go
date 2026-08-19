// Package logging configures the process-wide structured logger.
//
// The engine previously logged six lines for an entire run: startup banners and
// nothing else. A three-minute soak driving ~19,000 operations, including
// thousands of rate-limited requests and several silently discarded write
// failures, produced no output at all. That is not quiet, it is blind: the only
// evidence a deployment could offer about a failure was the absence of
// evidence.
//
// Output is JSON because the destination is a log aggregator, not a terminal.
// Text logs from a Kubernetes deployment have to be re-parsed with regexes that
// break whenever a message is reworded, and a message like "failed to store
// embedding for 3f2a...: timeout" cannot be filtered by memory id or grouped by
// error without that parsing. Structured fields survive the trip.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options configures the logger.
type Options struct {
	// Level is one of debug, info, warn, error. Anything else falls back to
	// info: an unparseable level should not silence a deployment.
	Level string

	// Format is "json" or "text". Text exists for local development, where a
	// human is reading the output directly.
	Format string

	// Version is attached to every record so lines from a rolling deployment
	// can be attributed to the build that emitted them.
	Version string
}

// Setup builds a logger from Options and installs it as the default, so
// packages that call slog directly are covered without threading a logger
// through every constructor.
func Setup(opts Options) *slog.Logger {
	return setup(opts, os.Stdout)
}

// setup is Setup with an injectable destination, so tests can assert on the
// records this configuration actually produces rather than rebuilding an
// equivalent logger by hand and testing that instead.
func setup(opts Options, w io.Writer) *slog.Logger {
	level := parseLevel(opts.Level)

	handlerOpts := &slog.HandlerOptions{
		Level: level,
		// Source location only below info: it costs a runtime.Callers on every
		// record, which is not worth paying on a hot path where the message
		// already identifies the site.
		AddSource: level < slog.LevelInfo,
	}

	var handler slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		handler = slog.NewTextHandler(w, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(w, handlerOpts)
	}

	logger := slog.New(handler)
	if opts.Version != "" {
		logger = logger.With(slog.String("version", opts.Version))
	}

	slog.SetDefault(logger)
	return logger
}

// parseLevel maps a configured level name to a slog level.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextKey is unexported so no other package can collide with it.
type contextKey struct{}

// WithLogger returns a context carrying logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext returns the logger stored in ctx, or the default logger.
//
// Never returns nil: a logging call is not worth a panic, and a caller that has
// to nil-check its logger will eventually forget.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
