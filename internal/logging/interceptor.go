// interceptor.go provides the gRPC server interceptor that logs every RPC.
//
// Without it a failing request produced no output whatsoever: a database
// outage returned errors to clients while the server logged nothing at all, so
// the only record of an incident lived in the client that experienced it. The
// handlers return rich gRPC status errors and, before this, threw them away at
// the boundary.

package logging

import (
	"context"
	"log/slog"
	"time"

	"github.com/context0/context0/internal/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor logs the outcome of every unary RPC.
//
// Successful calls log at debug, because one line per request at info would
// bury real problems under routine traffic at any real request rate -- this
// engine sustains ~80 operations per second in a soak. Failures log at a level
// that reflects who is at fault: a client sending an invalid argument is not an
// incident, while an Internal error is.
//
// Only metadata is logged, never memory content: this service stores whatever
// a user tells it to, so logging request bodies would copy personal data into a
// log aggregator with a different retention policy and a wider audience.
func UnaryServerInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()

		// Make the logger available to handlers, so a message emitted deep in a
		// call can be attributed to the RPC that caused it.
		resp, err := handler(WithLogger(ctx, logger), req)

		// RED metrics come from the same place as the log line: every RPC
		// passes through here, so instrumenting it once covers methods added
		// later without anyone remembering to add a timer by hand.
		code := status.Code(err)
		elapsed := time.Since(start)
		metrics.RequestsTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
		metrics.RequestDuration.WithLabelValues(info.FullMethod).Observe(elapsed.Seconds())

		attrs := []any{
			slog.String("method", info.FullMethod),
			slog.String("code", code.String()),
			slog.Duration("duration", elapsed),
		}

		switch {
		case err == nil:
			logger.LogAttrs(ctx, slog.LevelDebug, "rpc", toAttrs(attrs)...)
		case isClientFault(code):
			// The caller made a mistake. Worth recording, not worth alerting on.
			logger.LogAttrs(ctx, slog.LevelInfo, "rpc rejected",
				toAttrs(append(attrs, slog.Any("error", err)))...)
		default:
			logger.LogAttrs(ctx, slog.LevelError, "rpc failed",
				toAttrs(append(attrs, slog.Any("error", err)))...)
		}

		return resp, err
	}
}

// isClientFault reports whether a status code describes a bad request rather
// than a broken server. The distinction decides whether a line is routine or an
// incident, so it is stated explicitly rather than inferred from the code's
// numeric value.
func isClientFault(c codes.Code) bool {
	switch c {
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists,
		codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition,
		codes.OutOfRange, codes.Canceled, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// toAttrs converts the []any accumulator into []slog.Attr for LogAttrs, which
// avoids the reflection-based path in the variadic API.
func toAttrs(args []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(args))
	for _, a := range args {
		if attr, ok := a.(slog.Attr); ok {
			attrs = append(attrs, attr)
		}
	}
	return attrs
}
