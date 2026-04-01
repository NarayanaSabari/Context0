package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	endpoint := envOrDefault("CONTEXT0_ENDPOINT", "localhost:50051")
	apiKey := envOrDefault("CONTEXT0_API_KEY", "")
	projectID := envOrDefault("CONTEXT0_PROJECT", "default")

	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewContext0Client(conn)
	sessionClient := pb.NewSessionServiceClient(conn)
	healthClient := pb.NewHealthServiceClient(conn)

	ctx := context.Background()
	if apiKey != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", apiKey)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "store":
		cmdStore(ctx, client, projectID, args)
	case "query":
		cmdQuery(ctx, client, projectID, args)
	case "connect":
		cmdConnect(ctx, client, args)
	case "delete":
		cmdDelete(ctx, client, args)
	case "graph":
		cmdGraph(ctx, client, args)
	case "stats":
		cmdStats(ctx, healthClient)
	case "session-start":
		cmdSessionStart(ctx, sessionClient, projectID, args)
	case "session-end":
		cmdSessionEnd(ctx, sessionClient, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func cmdStore(ctx context.Context, client pb.Context0Client, projectID string, args []string) {
	if len(args) < 1 {
		fatalf("usage: context0 store <content> [--type semantic] [--tags db,postgres] [--session <id>]")
	}

	content := args[0]
	memType := pb.MemoryType_MEMORY_TYPE_SEMANTIC
	var tags []string
	var sessionID string

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--type":
			i++
			if i < len(args) {
				memType = parseMemoryType(args[i])
			}
		case "--tags":
			i++
			if i < len(args) {
				tags = strings.Split(args[i], ",")
			}
		case "--session":
			i++
			if i < len(args) {
				sessionID = args[i]
			}
		}
	}

	resp, err := client.Store(ctx, &pb.StoreRequest{
		Content:   content,
		Type:      memType,
		ProjectId: projectID,
		Tags:      tags,
		SessionId: sessionID,
	})
	if err != nil {
		fatalf("store failed: %v", err)
	}

	printJSON(resp.Memory)
}

func cmdQuery(ctx context.Context, client pb.Context0Client, projectID string, args []string) {
	if len(args) < 1 {
		fatalf("usage: context0 query <question> [--top-k 5] [--type semantic]")
	}

	query := args[0]
	topK := int32(5)
	var types []pb.MemoryType

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--top-k":
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%d", &topK)
			}
		case "--type":
			i++
			if i < len(args) {
				types = append(types, parseMemoryType(args[i]))
			}
		}
	}

	resp, err := client.Query(ctx, &pb.QueryRequest{
		Query:     query,
		ProjectId: projectID,
		TopK:      topK,
		Types:     types,
	})
	if err != nil {
		fatalf("query failed: %v", err)
	}

	for i, r := range resp.Results {
		fmt.Printf("--- Result %d (score: %.3f) ---\n", i+1, r.Score)
		fmt.Printf("  ID:      %s\n", r.Memory.Id)
		fmt.Printf("  Type:    %s\n", r.Memory.Type)
		fmt.Printf("  Content: %s\n", r.Memory.Content)
		fmt.Printf("  Tags:    %s\n", strings.Join(r.Memory.Tags, ", "))
		fmt.Printf("  Created: %s\n", r.Memory.CreatedAt.AsTime().Format(time.RFC3339))
		if len(r.Context) > 0 {
			fmt.Printf("  Context:\n")
			for _, e := range r.Context {
				fmt.Printf("    -> %s: %s (weight: %.2f)\n", e.Relationship, e.TargetContent, e.Weight)
			}
		}
	}

	if len(resp.Results) == 0 {
		fmt.Println("No results found.")
	}
}

func cmdConnect(ctx context.Context, client pb.Context0Client, args []string) {
	if len(args) < 3 {
		fatalf("usage: context0 connect <from-id> <to-id> <relationship> [--weight 1.0]")
	}

	fromID := args[0]
	toID := args[1]
	rel := parseRelType(args[2])
	weight := 1.0

	for i := 3; i < len(args); i++ {
		if args[i] == "--weight" {
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%f", &weight)
			}
		}
	}

	resp, err := client.Connect(ctx, &pb.ConnectRequest{
		FromId:       fromID,
		ToId:         toID,
		Relationship: rel,
		Weight:       weight,
	})
	if err != nil {
		fatalf("connect failed: %v", err)
	}

	printJSON(resp.Edge)
}

func cmdDelete(ctx context.Context, client pb.Context0Client, args []string) {
	if len(args) < 1 {
		fatalf("usage: context0 delete <memory-id>")
	}

	_, err := client.Delete(ctx, &pb.DeleteRequest{Id: args[0]})
	if err != nil {
		fatalf("delete failed: %v", err)
	}

	fmt.Printf("Deleted memory %s\n", args[0])
}

func cmdGraph(ctx context.Context, client pb.Context0Client, args []string) {
	if len(args) < 1 {
		fatalf("usage: context0 graph <memory-id> [--depth 2]")
	}

	centerID := args[0]
	depth := int32(2)

	for i := 1; i < len(args); i++ {
		if args[i] == "--depth" {
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%d", &depth)
			}
		}
	}

	resp, err := client.GetGraph(ctx, &pb.GetGraphRequest{
		CenterId: centerID,
		Depth:    depth,
	})
	if err != nil {
		fatalf("graph failed: %v", err)
	}

	fmt.Printf("Subgraph around %s (depth=%d):\n", centerID, depth)
	fmt.Printf("  Nodes: %d\n", len(resp.Nodes))
	for _, n := range resp.Nodes {
		fmt.Printf("    [%s] %s: %s\n", n.Type, n.Id[:8], truncate(n.Content, 60))
	}
	fmt.Printf("  Edges: %d\n", len(resp.Edges))
	for _, e := range resp.Edges {
		fmt.Printf("    %s --%s--> %s (w=%.2f)\n", e.FromId[:8], e.Relationship, e.ToId[:8], e.Weight)
	}
}

func cmdStats(ctx context.Context, client pb.HealthServiceClient) {
	resp, err := client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		fatalf("stats failed: %v", err)
	}

	fmt.Printf("Context0 Engine v%s\n", resp.Version)
	fmt.Printf("  Status:     %s\n", resp.Status)
	fmt.Printf("  Nodes:      %d\n", resp.NodeCount)
	fmt.Printf("  Edges:      %d\n", resp.EdgeCount)
}

func cmdSessionStart(ctx context.Context, client pb.SessionServiceClient, projectID string, args []string) {
	agentID := "cli"
	if len(args) > 0 {
		agentID = args[0]
	}

	resp, err := client.StartSession(ctx, &pb.StartSessionRequest{
		ProjectId: projectID,
		AgentId:   agentID,
	})
	if err != nil {
		fatalf("session start failed: %v", err)
	}

	fmt.Printf("Session started: %s\n", resp.Session.Id)
}

func cmdSessionEnd(ctx context.Context, client pb.SessionServiceClient, args []string) {
	if len(args) < 1 {
		fatalf("usage: context0 session-end <session-id>")
	}

	resp, err := client.EndSession(ctx, &pb.EndSessionRequest{Id: args[0]})
	if err != nil {
		fatalf("session end failed: %v", err)
	}

	fmt.Printf("Session ended: %s (duration: %s)\n",
		resp.Session.Id,
		resp.Session.EndedAt.AsTime().Sub(resp.Session.StartedAt.AsTime()),
	)
}

// --- Helpers ---

func printUsage() {
	fmt.Println(`context0 - Memory engine CLI

Usage: context0 <command> [args]

Commands:
  store          Store a new memory
  query          Query memories
  connect        Create a relationship between memories
  delete         Delete a memory
  graph          View subgraph around a memory
  stats          Show engine statistics
  session-start  Start a new session
  session-end    End a session

Environment:
  CONTEXT0_ENDPOINT  gRPC endpoint (default: localhost:50051)
  CONTEXT0_API_KEY   API key for authentication
  CONTEXT0_PROJECT   Project ID (default: default)`)
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseMemoryType(s string) pb.MemoryType {
	switch strings.ToLower(s) {
	case "episodic":
		return pb.MemoryType_MEMORY_TYPE_EPISODIC
	case "semantic":
		return pb.MemoryType_MEMORY_TYPE_SEMANTIC
	case "procedural":
		return pb.MemoryType_MEMORY_TYPE_PROCEDURAL
	default:
		return pb.MemoryType_MEMORY_TYPE_SEMANTIC
	}
}

func parseRelType(s string) pb.RelationshipType {
	switch strings.ToLower(s) {
	case "relates_to":
		return pb.RelationshipType_RELATIONSHIP_TYPE_RELATES_TO
	case "supersedes":
		return pb.RelationshipType_RELATIONSHIP_TYPE_SUPERSEDES
	case "caused_by":
		return pb.RelationshipType_RELATIONSHIP_TYPE_CAUSED_BY
	default:
		return pb.RelationshipType_RELATIONSHIP_TYPE_RELATES_TO
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
