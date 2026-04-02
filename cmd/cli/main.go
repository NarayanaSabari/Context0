// CLI is the command-line interface for the Context0 memory engine. It
// communicates with a running Context0 server over gRPC.
//
// # Commands
//
//	store          Store a new memory with optional type, tags, and session ID.
//	query          Search memories by natural language query with top-k and type filters.
//	connect        Create a typed, weighted relationship (edge) between two memories.
//	delete         Delete a single memory by ID.
//	graph          Visualise the subgraph around a memory up to a given depth.
//	stats          Print engine health, version, and node/edge counts.
//	session-start  Begin a new agent session within a project.
//	session-end    Close an open session and report its duration.
//
// # Environment Variables
//
//	CONTEXT0_ENDPOINT  gRPC server address (default: localhost:50051)
//	CONTEXT0_API_KEY   API key sent as gRPC metadata for authentication
//	CONTEXT0_PROJECT   Project ID used by store/query/session commands (default: default)
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

	// Read connection settings from environment, falling back to defaults.
	endpoint := envOrDefault("CONTEXT0_ENDPOINT", "localhost:50051")
	apiKey := envOrDefault("CONTEXT0_API_KEY", "")
	projectID := envOrDefault("CONTEXT0_PROJECT", "default")

	// Establish an insecure gRPC connection to the server.
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Create typed service clients for each gRPC service.
	client := pb.NewContext0Client(conn)
	sessionClient := pb.NewSessionServiceClient(conn)
	healthClient := pb.NewHealthServiceClient(conn)

	// Attach the API key as gRPC metadata when configured.
	ctx := context.Background()
	if apiKey != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", apiKey)
	}

	// Dispatch to the appropriate sub-command handler.
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

// cmdStore stores a new memory. The first positional argument is the content
// text. Optional flags: --type (semantic|episodic|procedural), --tags
// (comma-separated), --session (session ID to associate with).
func cmdStore(ctx context.Context, client pb.Context0Client, projectID string, args []string) {
	if len(args) < 1 {
		fatalf("usage: context0 store <content> [--type semantic] [--tags db,postgres] [--session <id>]")
	}

	content := args[0]
	memType := pb.MemoryType_MEMORY_TYPE_SEMANTIC
	var tags []string
	var sessionID string

	// Parse optional flags after the positional content argument.
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

// cmdQuery searches memories by a natural-language query. The first positional
// argument is the query text. Optional flags: --top-k (max results, default 5),
// --type (filter by memory type, may be repeated).
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

// cmdConnect creates a directed edge between two memories. Requires three
// positional arguments: source ID, target ID, and relationship type
// (relates_to, supersedes, caused_by). Optional: --weight (float, default 1.0).
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

// cmdDelete removes a single memory by its ID.
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

// cmdGraph prints the subgraph surrounding a memory node. The first positional
// argument is the center memory ID. Optional: --depth (traversal depth,
// default 2). Output shows all reachable nodes and edges with truncated
// content for readability.
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

// cmdStats queries the health endpoint and prints the engine version,
// status, and total node/edge counts.
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

// cmdSessionStart begins a new agent session. An optional positional argument
// specifies the agent ID (defaults to "cli").
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

// cmdSessionEnd closes an open session by its ID and prints the duration.
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// printUsage prints the CLI usage summary to stdout.
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

// printJSON pretty-prints v as indented JSON to stdout.
func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// fatalf prints a formatted error message to stderr and exits with code 1.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// envOrDefault reads an environment variable, returning fallback when the
// variable is unset or empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseMemoryType converts a human-friendly type string (e.g. "episodic")
// into the corresponding protobuf enum value. Defaults to SEMANTIC.
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

// parseRelType converts a human-friendly relationship string (e.g.
// "supersedes") into the corresponding protobuf enum value. Defaults to
// RELATES_TO.
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

// truncate shortens s to maxLen characters, appending "..." when truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
