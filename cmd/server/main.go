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

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/auth"
	"github.com/context0/context0/internal/config"
	emb "github.com/context0/context0/internal/embedding"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/metrics"
	"github.com/context0/context0/internal/service"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Database ---
	log.Printf("connecting to database...")
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("failed to ping database: %v", err)
	}
	log.Printf("database connected")

	// --- Graph ---
	repo := graph.NewAGERepository(pool)
	if err := repo.InitSchema(ctx); err != nil {
		log.Fatalf("failed to init graph schema: %v", err)
	}
	log.Printf("graph schema initialized (graph: %s)", graph.GraphName)

	// --- Metrics ---
	metrics.Register()

	// --- Auth ---
	apiAuth := auth.NewAPIKeyAuth(cfg.APIKeys, 100)

	// --- gRPC Server ---
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(apiAuth.UnaryInterceptor()),
	)

	embedder := emb.NewBagOfWordsEmbedder(384)
	log.Printf("embedding: using BagOfWords embedder (dim=%d)", embedder.Dimension())

	memorySvc := service.NewMemoryService(repo, embedder)
	sessionSvc := service.NewSessionService(repo)
	healthSvc := service.NewHealthService(repo, cfg.Version)

	pb.RegisterContext0Server(grpcServer, memorySvc)
	pb.RegisterSessionServiceServer(grpcServer, sessionSvc)
	pb.RegisterHealthServiceServer(grpcServer, healthSvc)

	// Enable gRPC reflection for debugging with grpcurl.
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

	// --- REST Gateway ---
	gwMux := runtime.NewServeMux(
		// Forward X-API-Key header as gRPC metadata so the interceptor can read it.
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			if strings.EqualFold(key, "X-API-Key") {
				return "x-api-key", true
			}
			return runtime.DefaultHeaderMatcher(key)
		}),
	)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	if err := pb.RegisterContext0HandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		log.Fatalf("failed to register memory gateway: %v", err)
	}
	if err := pb.RegisterSessionServiceHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		log.Fatalf("failed to register session gateway: %v", err)
	}
	if err := pb.RegisterHealthServiceHandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), opts); err != nil {
		log.Fatalf("failed to register health gateway: %v", err)
	}

	// --- HTTP Server ---
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/v1/", gwMux)

	httpHandler := apiAuth.HTTPMiddleware(mux)

	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr(),
		Handler: httpHandler,
	}

	go func() {
		log.Printf("HTTP server listening on %s (REST gateway + metrics)", cfg.HTTPAddr())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// --- Graceful Shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %s, shutting down...", sig)

	grpcServer.GracefulStop()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	repo.Close()

	log.Printf("shutdown complete")
}
