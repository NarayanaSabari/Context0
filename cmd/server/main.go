// Kora server is the main entry point for the Kora memory engine.
// It boots the following subsystems in order and then blocks until a
// termination signal is received:
//
// Boot sequence:
//  1. Load configuration from environment variables (see package config).
//  2. Connect to PostgreSQL (with AGE extension) and verify connectivity.
//  3. Initialise the embedding provider (needed early because the vector
//     dimension determines the graph schema).
//  4. Create the Apache AGE graph repository and run schema migrations.
//  5. Register Prometheus metrics.
//  6. Set up API key authentication with per-key rate limiting.
//  7. Start the gRPC server with auth interceptor and reflection enabled.
//  8. Start the grpc-gateway REST proxy with a custom header matcher that
//     forwards the X-API-Key header into gRPC metadata.
//  9. Start the HTTP server that mounts /metrics (Prometheus) and /v1/*
//     (REST gateway), wrapped in the auth middleware.
//  10. Wait for SIGINT or SIGTERM, then gracefully drain both servers
//     and close the database pool.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/auth"
	"github.com/NarayanaSabari/Kora/internal/config"
	emb "github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/extraction"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/logging"
	"github.com/NarayanaSabari/Kora/internal/metrics"
	"github.com/NarayanaSabari/Kora/internal/server"
	"github.com/NarayanaSabari/Kora/internal/service"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

// shutdownTimeout bounds the graceful drain. It must stay comfortably below the
// chart's terminationGracePeriodSeconds, so the process finishes draining and
// closes the connection pool before Kubernetes escalates to SIGKILL.
const shutdownTimeout = 15 * time.Second

func main() {
	// Step 1: Load all configuration from environment variables.
	cfg := config.Load()

	// Structured logging is installed before anything else can fail, so even a
	// startup error is emitted in the same format as everything else.
	logger := logging.Setup(logging.Options{
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
		Version: cfg.Version,
	})

	// Configuration is validated after logging is configured, so a problem is
	// reported in the same structured stream as everything else, and before
	// anything binds a port or opens a connection.
	//
	// A value that was set but unusable is fatal rather than quietly replaced
	// by a default: an operator who mistyped KORA_RATE_LIMIT_PER_MINUTE
	// would otherwise get the default limit with nothing anywhere saying their
	// setting was ignored.
	if err := cfg.Validate(); err != nil {
		fatal("invalid configuration", err)
	}

	// The settings actually in force, so a deployment's behaviour can be
	// attributed to its configuration without guessing which defaults applied.
	slog.Info("configuration",
		slog.Int("grpc_port", cfg.GRPCPort),
		slog.Int("http_port", cfg.HTTPPort),
		slog.Int("api_keys", len(cfg.APIKeys)),
		slog.Bool("auth_enabled", len(cfg.APIKeys) > 0),
		slog.Int("rate_limit_per_minute", cfg.RateLimitPerMinute),
		slog.String("embedding_provider", cfg.EmbeddingProvider),
		slog.Int("embedding_dim", cfg.EmbeddingDim),
		slog.String("log_level", cfg.LogLevel))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 2: Establish a PostgreSQL connection pool and verify reachability.
	slog.Info("connecting to database")
	pool, err := graph.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("failed to connect to database", err)
	}
	defer pool.Close()

	// Pool saturation is this service's most likely failure mode and was
	// previously unobservable: a deadlock here once presented as uniformly slow
	// requests with no metric that named the cause.
	metrics.SetPoolStatsSource(ctx, pool)
	slog.Info("database connected")

	// Step 3: Initialise the embedding provider. This must happen before the
	// graph repository is created because the vector dimension (returned by
	// embedder.Dimension()) is used to size the embedding column in AGE.
	embedder, err := emb.NewFromConfig(emb.ProviderConfig{
		Provider: cfg.EmbeddingProvider,
		Model:    cfg.EmbeddingModel,
		APIKey:   cfg.EmbeddingAPIKey,
		BaseURL:  cfg.EmbeddingBaseURL,
		Dim:      cfg.EmbeddingDim,
	})
	if err != nil {
		fatal("failed to create embedder", err)
	}
	slog.Info("embedding provider ready", slog.String("provider", cfg.EmbeddingProvider), slog.Int("dimension", embedder.Dimension()))

	// Initialise the extraction backend. Like the embedder, an unusable
	// configuration is fatal rather than silently downgraded: an operator who
	// asked for LLM extraction and got the rule-based scanner would see a
	// healthy server quietly storing lower-quality memories.
	extractor, err := extraction.NewFromConfig(extraction.ProviderConfig{
		Provider: cfg.ExtractionProvider,
		Model:    cfg.ExtractionModel,
		APIKey:   cfg.ExtractionAPIKey,
		BaseURL:  cfg.ExtractionBaseURL,
	})
	if err != nil {
		fatal("failed to create extractor", err)
	}
	slog.Info("extraction provider ready", slog.String("provider", cfg.ExtractionProvider))

	// Both providers on their defaults is a supported configuration and a quiet
	// one: the engine starts, answers, and retrieves paraphrases far worse than
	// the same corpus would with a real embedding model. Nothing fails, so
	// nothing would otherwise say so, and the first symptom is a benchmark
	// number or a user wondering why a question they can answer from the
	// transcript comes back empty.
	//
	// Not fatal, and not a "degraded" health status: no network egress on a
	// fresh install is the right default, and paging on it would be wrong.
	// Loud once, at startup, where an operator is looking.
	providers := service.Providers{
		Embedding:            cfg.EmbeddingProvider,
		Extraction:           cfg.ExtractionProvider,
		GraphSignalsDisabled: cfg.GraphSignals == "off",
	}
	if providers.ZeroDependencyDefaults() {
		slog.Warn("running on zero-dependency defaults: hashed bag-of-words embeddings and rule-based extraction",
			slog.String("embedding_provider", cfg.EmbeddingProvider),
			slog.String("extraction_provider", cfg.ExtractionProvider),
			slog.String("effect", "retrieval finds paraphrases poorly and extraction cannot merge facts across turns"),
			slog.String("fix", "set KORA_EMBEDDING_PROVIDER and KORA_EXTRACTION_PROVIDER, or install the chart with charts/kora/values-quality.yaml"))
	}

	// Step 4: Create the graph repository and apply schema migrations.
	repo := graph.NewAGERepository(pool, embedder.Dimension())
	if err := repo.InitSchema(ctx); err != nil {
		fatal("failed to init graph schema", err)
	}
	slog.Info("graph schema initialized", slog.String("graph", graph.GraphName))

	// Step 5: Register Prometheus metrics (counters, histograms, gauges).
	metrics.Register()

	// Step 6: Set up API key authentication with 100 requests/minute per key.
	apiAuth := auth.NewAPIKeyAuth(cfg.APIKeys, cfg.RateLimitPerMinute)

	// Step 7: Build and start the gRPC server.
	//
	// Order matters: authentication runs first so an unauthenticated request is
	// rejected before a handler sees it, and logging wraps the handler so the
	// outcome of everything that gets past auth is recorded.
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			apiAuth.UnaryInterceptor(),
			logging.UnaryServerInterceptor(logger),
		),
	)

	// Register all service implementations on the gRPC server.
	memorySvc := service.NewMemoryServiceWithExtractor(repo, embedder, extractor)
	if cfg.GraphSignals == "off" {
		// The ablation mode, issue #86. Loud for the same reason the
		// zero-dependency warning is: a benchmark number produced against this
		// deployment measures the ablated engine, and the only place that is
		// visible later is this line and the health endpoint.
		memorySvc.DisableGraphSignals()
		slog.Warn("graph signals disabled: retrieving by full-text and vector search alone",
			slog.String("reason", "KORA_GRAPH_SIGNALS=off"),
			slog.String("effect", "this deployment behaves as plain hybrid RAG; entity retrieval is off"))
	}
	sessionSvc := service.NewSessionService(repo)
	healthSvc := service.NewHealthService(repo, cfg.Version, providers)

	pb.RegisterKoraServer(grpcServer, memorySvc)
	pb.RegisterSessionServiceServer(grpcServer, sessionSvc)
	pb.RegisterHealthServiceServer(grpcServer, healthSvc)

	// Enable gRPC server reflection so tools like grpcurl can discover services.
	reflection.Register(grpcServer)

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr())
	if err != nil {
		fatal("failed to listen for gRPC", err, slog.String("addr", cfg.GRPCAddr()))
	}

	go func() {
		slog.Info("gRPC server listening", slog.String("addr", cfg.GRPCAddr()))
		if err := grpcServer.Serve(grpcLis); err != nil {
			fatal("gRPC server failed", err)
		}
	}()

	// Step 8: Set up the grpc-gateway REST proxy. A custom header matcher
	// ensures the X-API-Key HTTP header is forwarded as gRPC metadata so the
	// auth interceptor can read it.
	gwMux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			if strings.EqualFold(key, "X-API-Key") {
				return "x-api-key", true
			}
			// Forwarded so the gRPC interceptor knows this request already
			// spent its rate-limit token in the HTTP middleware. Without it
			// every REST call is charged twice and the effective limit is half
			// what the operator configured.
			//
			// grpc-gateway's default matcher only forwards permanent HTTP
			// headers, so this has to be named explicitly. It is safe to
			// forward because the HTTP middleware overwrites it on every
			// request before the gateway ever sees it: a value supplied by a
			// client is discarded, not honoured.
			if strings.EqualFold(key, auth.RateLimitedHeader) {
				return auth.RateLimitedHeader, true
			}
			return runtime.DefaultHeaderMatcher(key)
		}),
	)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	// Register each service's REST handler, pointing back to the local gRPC server.
	if err := pb.RegisterKoraHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		fatal("failed to register memory gateway", err)
	}
	if err := pb.RegisterSessionServiceHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		fatal("failed to register session gateway", err)
	}
	if err := pb.RegisterHealthServiceHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		fatal("failed to register health gateway", err)
	}

	// Step 9: Build and start the HTTP server.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler()) // Prometheus scrape endpoint
	mux.Handle("/v1/", gwMux)                  // REST gateway for all /v1/* routes

	// Kubernetes probes. These are registered directly on the mux rather than
	// going through the gateway, so they stay cheap and never reach the graph.
	probes := server.NewProbes(pool)
	probes.Register(mux)

	// Wrap the mux with API key + rate limit middleware.
	httpHandler := apiAuth.HTTPMiddleware(mux)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr(),
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("HTTP server listening (REST gateway + metrics)", slog.String("addr", cfg.HTTPAddr()))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("HTTP server failed", err)
		}
	}()

	// Everything is listening and the schema is ready, so the startup probe can
	// pass and readiness can begin reporting on the database.
	probes.MarkStarted()

	// Step 10: Block until SIGINT or SIGTERM, then shut down gracefully.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("signal received, shutting down", slog.String("signal", sig.String()))

	// Fail readiness first. Kubernetes removes the pod from Service endpoints
	// asynchronously, so this window is what stops new requests arriving while
	// in-flight ones are still being served. The chart's preStop sleep covers
	// the same race for clients that bypass readiness.
	probes.MarkDraining()

	// Bound the drain. Without a deadline a single stuck connection can outlive
	// terminationGracePeriodSeconds and get SIGKILLed, skipping pool cleanup.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()

	// HTTP first, then gRPC. The REST gateway proxies to the local gRPC server,
	// so stopping gRPC first would break every in-flight REST request instead of
	// draining it.
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", slog.Any("error", err))
	}
	grpcServer.GracefulStop()

	// Release the database connection pool.
	repo.Close()

	slog.Info("shutdown complete")
}

// fatal logs a structured error and exits non-zero.
//
// slog has no Fatal: it is a logging library, and exiting is a policy decision.
// This keeps startup failures in the same structured stream as everything else
// rather than reverting to the stdlib logger's unstructured format for exactly
// the messages an operator most needs to parse.
func fatal(msg string, err error, attrs ...slog.Attr) {
	args := make([]any, 0, len(attrs)+1)
	for _, a := range attrs {
		args = append(args, a)
	}
	args = append(args, slog.Any("error", err))
	slog.Error(msg, args...)
	os.Exit(1)
}
