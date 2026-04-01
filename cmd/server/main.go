package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/context0/context0/internal/config"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
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

	// --- gRPC Server ---
	grpcServer := grpc.NewServer()

	// TODO: Register Context0 service, SessionService, HealthService

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

	// --- HTTP Server (REST gateway + metrics) ---
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		nodeCount, _ := repo.NodeCount(r.Context())
		edgeCount, _ := repo.EdgeCount(r.Context())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s","node_count":%d,"edge_count":%d}`,
			cfg.Version, nodeCount, edgeCount)
	})

	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr(),
		Handler: mux,
	}

	go func() {
		log.Printf("HTTP server listening on %s", cfg.HTTPAddr())
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
