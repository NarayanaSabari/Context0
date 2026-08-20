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
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Key generation is deliberately handled before any connection is opened:
	// an operator needs a key in order to have a server to connect to, so
	// requiring a running server first would be circular.
	if os.Args[1] == "keys" {
		runKeys(os.Args[2:])
		return
	}

	// Read connection settings from environment, falling back to defaults.
	endpoint := cmp.Or(os.Getenv("CONTEXT0_ENDPOINT"), "localhost:50051")
	apiKey := os.Getenv("CONTEXT0_API_KEY")
	projectID := cmp.Or(os.Getenv("CONTEXT0_PROJECT"), "default")

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
		cmdStats(ctx, healthClient, apiKey)
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
	const usage = "usage: context0 store <content> [--type semantic] [--tags db,postgres] [--session <id>]"
	if len(args) < 1 {
		fatalf(usage)
	}
	content := args[0]

	fs := flag.NewFlagSet("store", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	typeFlag := fs.String("type", "semantic", "memory type (semantic|episodic|procedural)")
	tagsFlag := fs.String("tags", "", "comma-separated tags")
	sessionFlag := fs.String("session", "", "session ID to associate with")
	if err := fs.Parse(args[1:]); err != nil {
		fatalf(usage)
	}

	var tags []string
	if *tagsFlag != "" {
		tags = strings.Split(*tagsFlag, ",")
	}

	resp, err := client.Store(ctx, &pb.StoreRequest{
		Content:   content,
		Type:      parseMemoryType(*typeFlag),
		ProjectId: projectID,
		Tags:      tags,
		SessionId: *sessionFlag,
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
	const usage = "usage: context0 query <question> [--top-k 5] [--type semantic]"
	if len(args) < 1 {
		fatalf(usage)
	}
	query := args[0]

	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	topK := fs.Int("top-k", 5, "maximum number of results")
	var typeStrs stringSliceFlag
	fs.Var(&typeStrs, "type", "memory type filter (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		fatalf(usage)
	}

	var types []pb.MemoryType
	for _, t := range typeStrs {
		types = append(types, parseMemoryType(t))
	}

	resp, err := client.Query(ctx, &pb.QueryRequest{
		Query:     query,
		ProjectId: projectID,
		TopK:      int32(*topK),
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
	const usage = "usage: context0 connect <from-id> <to-id> <relationship> [--weight 1.0]"
	if len(args) < 3 {
		fatalf(usage)
	}
	fromID := args[0]
	toID := args[1]
	rel := parseRelType(args[2])

	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	weight := fs.Float64("weight", 1.0, "edge weight")
	if err := fs.Parse(args[3:]); err != nil {
		fatalf(usage)
	}

	resp, err := client.Connect(ctx, &pb.ConnectRequest{
		FromId:       fromID,
		ToId:         toID,
		Relationship: rel,
		Weight:       *weight,
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
	const usage = "usage: context0 graph <memory-id> [--depth 2]"
	if len(args) < 1 {
		fatalf(usage)
	}
	centerID := args[0]

	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	depth := fs.Int("depth", 2, "traversal depth")
	if err := fs.Parse(args[1:]); err != nil {
		fatalf(usage)
	}

	resp, err := client.GetGraph(ctx, &pb.GetGraphRequest{
		CenterId: centerID,
		Depth:    int32(*depth),
	})
	if err != nil {
		fatalf("graph failed: %v", err)
	}

	fmt.Printf("Subgraph around %s (depth=%d):\n", centerID, *depth)
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
func cmdStats(ctx context.Context, client pb.HealthServiceClient, apiKey string) {
	resp, err := client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		fatalf("stats failed: %v", err)
	}

	// Health answers without a credential so Kubernetes probes can reach it,
	// and it withholds the version and graph counts from anyone it could not
	// authenticate. A rejected key therefore comes back as a successful
	// response full of zeros rather than as an error.
	//
	// Printing that verbatim told a user with a typo in CONTEXT0_API_KEY that
	// their engine was healthy and empty -- indistinguishable from real data
	// loss, and exiting 0 so no script would catch it. If a key was presented
	// and the server still treated us as anonymous, that is an auth failure.
	if apiKey != "" && resp.Version == "" {
		fatalf("stats failed: the API key was rejected\n" +
			"The engine answered, but as an unauthenticated caller: it withholds\n" +
			"the version and graph counts from callers it cannot authenticate.\n" +
			"Check CONTEXT0_API_KEY.")
	}

	if apiKey == "" {
		fmt.Fprintln(os.Stderr,
			"note: no CONTEXT0_API_KEY set; the engine withholds statistics from\n"+
				"unauthenticated callers, so the counts below are not real totals.")
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

// stringSliceFlag implements flag.Value for a repeatable string flag, e.g.
// `--type semantic --type episodic`.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

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
  keys generate  Generate a new API key (no server required)

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

// parseMemoryType converts a human-friendly type string into the protobuf
// enum, rejecting anything it does not recognise.
//
// An unrecognised value used to fall through to SEMANTIC. A typo in --type
// therefore stored the memory under the wrong type and said nothing: the write
// succeeded, the CLI printed success, and the memory was simply filed wrongly
// -- invisible until a later type-filtered query failed to return it. Guessing
// is worse than refusing, because the caller never learns they were guessed at.
func parseMemoryType(s string) pb.MemoryType {
	switch strings.ToLower(s) {
	case "episodic":
		return pb.MemoryType_MEMORY_TYPE_EPISODIC
	case "semantic":
		return pb.MemoryType_MEMORY_TYPE_SEMANTIC
	case "procedural":
		return pb.MemoryType_MEMORY_TYPE_PROCEDURAL
	default:
		fatalf("unknown memory type %q (expected: semantic, episodic, procedural)", s)
		return pb.MemoryType_MEMORY_TYPE_SEMANTIC // unreachable; fatalf exits
	}
}

// parseRelType converts a human-friendly relationship string (e.g.
// "supersedes") into the corresponding protobuf enum value. Defaults to
// RELATES_TO.
// parseRelType converts a relationship name into the protobuf enum, rejecting
// anything it does not recognise.
//
// As with parseMemoryType, silently defaulting meant a typo created an edge of
// the wrong kind. That is worse here: supersedes edges are followed when
// resolving which fact is current, so a mistyped "supersedes" that became
// "relates_to" leaves the superseded fact live alongside its replacement.
func parseRelType(s string) pb.RelationshipType {
	switch strings.ToLower(s) {
	case "relates_to":
		return pb.RelationshipType_RELATIONSHIP_TYPE_RELATES_TO
	case "supersedes":
		return pb.RelationshipType_RELATIONSHIP_TYPE_SUPERSEDES
	case "caused_by":
		return pb.RelationshipType_RELATIONSHIP_TYPE_CAUSED_BY
	default:
		fatalf("unknown relationship type %q (expected: relates_to, supersedes, caused_by)", s)
		return pb.RelationshipType_RELATIONSHIP_TYPE_RELATES_TO // unreachable
	}
}

// truncate shortens s to maxLen characters, appending "..." when truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// runKeys implements the `keys` command group.
//
// Generation happens locally and offline: the server stores only the SHA-256 of
// a key, so it can never print one back, and a key that was transmitted to be
// generated would have been exposed in transit for no reason.
func runKeys(args []string) {
	if len(args) == 0 || args[0] != "generate" {
		fmt.Println(`Usage: context0 keys generate

Generates an API key for this deployment. The key is shown once and is not
recoverable: the server stores only its hash, so a lost key must be replaced
rather than looked up.

Pass it to the server as CONTEXT0_API_KEYS (comma-separated for several), or to
the chart as --set auth.apiKeys=...`)
		os.Exit(1)
	}

	key, err := auth.GenerateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate key: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(key.String())
	fmt.Fprintln(os.Stderr, "\nStore this now: it cannot be recovered from the server.")
	fmt.Fprintf(os.Stderr, "Key id %s will identify this credential in logs and metrics.\n", key.ID)
}
