// Context0 server is the main entry point for the Context0 memory engine.
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
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/auth"
	"github.com/context0/context0/internal/config"
	emb "github.com/context0/context0/internal/embedding"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/metrics"
	"github.com/context0/context0/internal/server"
	"github.com/context0/context0/internal/service"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 2: Establish a PostgreSQL connection pool and verify reachability.
	log.Printf("connecting to database...")
	pool, err := graph.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()
	log.Printf("database connected")

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
		log.Fatalf("failed to create embedder: %v", err)
	}
	log.Printf("embedding: provider=%s dim=%d", cfg.EmbeddingProvider, embedder.Dimension())

	// Step 4: Create the graph repository and apply schema migrations.
	repo := graph.NewAGERepository(pool, embedder.Dimension())
	if err := repo.InitSchema(ctx); err != nil {
		log.Fatalf("failed to init graph schema: %v", err)
	}
	log.Printf("graph schema initialized (graph: %s)", graph.GraphName)

	// Step 5: Register Prometheus metrics (counters, histograms, gauges).
	metrics.Register()

	// Step 6: Set up API key authentication with 100 requests/minute per key.
	apiAuth := auth.NewAPIKeyAuth(cfg.APIKeys, cfg.RateLimitPerMinute)

	// Step 7: Build and start the gRPC server.
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(apiAuth.UnaryInterceptor()),
	)

	// Register all service implementations on the gRPC server.
	memorySvc := service.NewMemoryService(repo, embedder)
	sessionSvc := service.NewSessionService(repo)
	healthSvc := service.NewHealthService(repo, cfg.Version)

	pb.RegisterContext0Server(grpcServer, memorySvc)
	pb.RegisterSessionServiceServer(grpcServer, sessionSvc)
	pb.RegisterHealthServiceServer(grpcServer, healthSvc)

	// Enable gRPC server reflection so tools like grpcurl can discover services.
	reflection.Register(grpcServer)

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr())
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", cfg.GRPCAddr(), err)
	}

	go func() {
		log.Printf("gRPC server listening on %s", cfg.GRPCAddr())
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Fatalf("gRPC server failed: %v", err)
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
			return runtime.DefaultHeaderMatcher(key)
		}),
	)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	// Register each service's REST handler, pointing back to the local gRPC server.
	if err := pb.RegisterContext0HandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		log.Fatalf("failed to register memory gateway: %v", err)
	}
	if err := pb.RegisterSessionServiceHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		log.Fatalf("failed to register session gateway: %v", err)
	}
	if err := pb.RegisterHealthServiceHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		log.Fatalf("failed to register health gateway: %v", err)
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
		log.Printf("HTTP server listening on %s (REST gateway + metrics)", cfg.HTTPAddr())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Everything is listening and the schema is ready, so the startup probe can
	// pass and readiness can begin reporting on the database.
	probes.MarkStarted()

	// Step 10: Block until SIGINT or SIGTERM, then shut down gracefully.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %s, shutting down...", sig)

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
		log.Printf("HTTP server shutdown error: %v", err)
	}
	grpcServer.GracefulStop()

	// Release the database connection pool.
	repo.Close()

	log.Printf("shutdown complete")
}
