// Package e2e contains end-to-end tests for Context0.
// These tests require a running Context0 server with PostgreSQL + AGE.
//
// Run with: go test ./test/e2e/... -v -tags=e2e
//
// Set CONTEXT0_E2E_ENDPOINT and CONTEXT0_E2E_HTTP to override defaults.
//
//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

var (
	httpBase string
	apiKey   string
)

func TestMain(m *testing.M) {
	httpBase = envOrDefault("CONTEXT0_E2E_HTTP", "http://localhost:8080")
	apiKey = envOrDefault("CONTEXT0_E2E_API_KEY", "ctx0_dev_key_1")

	// Wait for server to be ready.
	if !waitForHealth(httpBase, 30*time.Second) {
		fmt.Fprintf(os.Stderr, "context0 server not ready at %s\n", httpBase)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestHealth(t *testing.T) {
	resp := httpGet(t, "/v1/health")

	if resp["status"] != "ok" {
		t.Errorf("health status = %q, want 'ok'", resp["status"])
	}
	if _, ok := resp["version"]; !ok {
		t.Error("health response missing 'version'")
	}
}

func TestStoreAndQuery(t *testing.T) {
	projectID := fmt.Sprintf("e2e-test-%d", time.Now().UnixNano())

	// Store a semantic memory.
	storeResp := httpPost(t, "/v1/memories", map[string]any{
		"content":    "Project uses PostgreSQL 15.x with Apache AGE extension",
		"type":       2, // SEMANTIC
		"project_id": projectID,
		"tags":       []string{"database", "postgres", "age"},
	})

	memoryID, ok := storeResp["memory"].(map[string]any)["id"].(string)
	if !ok || memoryID == "" {
		t.Fatalf("store did not return memory ID: %v", storeResp)
	}
	t.Logf("stored memory: %s", memoryID)

	// Store another memory with overlapping tags.
	storeResp2 := httpPost(t, "/v1/memories", map[string]any{
		"content":    "PostgreSQL runs as a StatefulSet in Kubernetes",
		"type":       2,
		"project_id": projectID,
		"tags":       []string{"database", "postgres", "kubernetes"},
	})

	memoryID2 := storeResp2["memory"].(map[string]any)["id"].(string)
	t.Logf("stored memory 2: %s", memoryID2)

	// Query for database-related memories.
	queryResp := httpGet(t, fmt.Sprintf("/v1/memories/query?query=database+postgres&project_id=%s&top_k=5", projectID))

	results, ok := queryResp["results"].([]any)
	if !ok {
		t.Fatalf("query did not return results: %v", queryResp)
	}

	if len(results) == 0 {
		t.Fatal("query returned 0 results, expected at least 1")
	}
	t.Logf("query returned %d results", len(results))
}

func TestConnect(t *testing.T) {
	projectID := fmt.Sprintf("e2e-connect-%d", time.Now().UnixNano())

	// Store two memories.
	mem1 := storeMemory(t, projectID, "Old: Project uses MySQL", "semantic", []string{"database"})
	mem2 := storeMemory(t, projectID, "New: Project migrated to PostgreSQL", "semantic", []string{"database"})

	// Connect: mem2 supersedes mem1.
	connectResp := httpPost(t, "/v1/memories/connect", map[string]any{
		"from_id":      mem2,
		"to_id":        mem1,
		"relationship": 2, // SUPERSEDES
		"weight":       1.0,
	})

	edge, ok := connectResp["edge"].(map[string]any)
	if !ok || edge["id"] == nil {
		t.Fatalf("connect did not return edge: %v", connectResp)
	}
	t.Logf("created edge: %s", edge["id"])
}

func TestDelete(t *testing.T) {
	projectID := fmt.Sprintf("e2e-delete-%d", time.Now().UnixNano())
	memID := storeMemory(t, projectID, "Temporary memory to delete", "episodic", nil)

	// Delete it.
	req, _ := http.NewRequest("DELETE", httpBase+"/v1/memories/"+memID, nil)
	req.Header.Set("X-API-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete returned %d: %s", resp.StatusCode, body)
	}
	t.Logf("deleted memory: %s", memID)
}

func TestGetGraph(t *testing.T) {
	projectID := fmt.Sprintf("e2e-graph-%d", time.Now().UnixNano())

	mem1 := storeMemory(t, projectID, "Central fact", "semantic", []string{"core"})
	mem2 := storeMemory(t, projectID, "Related fact", "semantic", []string{"core"})

	// Connect them.
	httpPost(t, "/v1/memories/connect", map[string]any{
		"from_id":      mem1,
		"to_id":        mem2,
		"relationship": 1, // RELATES_TO
		"weight":       0.8,
	})

	// Get subgraph.
	graphResp := httpGet(t, fmt.Sprintf("/v1/memories/%s/graph?depth=2", mem1))
	t.Logf("subgraph response: %v", graphResp)
}

func TestSessionLifecycle(t *testing.T) {
	projectID := fmt.Sprintf("e2e-session-%d", time.Now().UnixNano())

	// Start session.
	startResp := httpPost(t, "/v1/sessions", map[string]any{
		"project_id": projectID,
		"agent_id":   "e2e-test-agent",
	})

	session, ok := startResp["session"].(map[string]any)
	if !ok || session["id"] == nil {
		t.Fatalf("start session did not return session: %v", startResp)
	}
	sessionID := session["id"].(string)
	t.Logf("started session: %s", sessionID)

	// Store memory linked to session.
	httpPost(t, "/v1/memories", map[string]any{
		"content":    "Discussed auth architecture during session",
		"type":       1, // EPISODIC
		"project_id": projectID,
		"session_id": sessionID,
		"tags":       []string{"auth", "session-test"},
	})

	// End session.
	endResp := httpPost(t, fmt.Sprintf("/v1/sessions/%s/end", sessionID), map[string]any{})
	endSession := endResp["session"].(map[string]any)
	if endSession["endedAt"] == nil {
		t.Error("ended session should have endedAt set")
	}
	t.Logf("ended session: %s", sessionID)
}

func TestAuthRejectsInvalidKey(t *testing.T) {
	req, _ := http.NewRequest("POST", httpBase+"/v1/memories", bytes.NewBufferString(`{"content":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "invalid-key-12345")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid key, got %d", resp.StatusCode)
	}
}

// --- Helpers ---

func storeMemory(t *testing.T, projectID, content, memType string, tags []string) string {
	t.Helper()

	typeMap := map[string]int{"episodic": 1, "semantic": 2, "procedural": 3}
	storeResp := httpPost(t, "/v1/memories", map[string]any{
		"content":    content,
		"type":       typeMap[memType],
		"project_id": projectID,
		"tags":       tags,
	})

	mem := storeResp["memory"].(map[string]any)
	return mem["id"].(string)
}

func httpGet(t *testing.T, path string) map[string]any {
	t.Helper()

	req, _ := http.NewRequest("GET", httpBase+path, nil)
	req.Header.Set("X-API-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d: %s", path, resp.StatusCode, body)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("GET %s invalid JSON: %v\nBody: %s", path, err, body)
	}
	return result
}

func httpPost(t *testing.T, path string, body map[string]any) map[string]any {
	t.Helper()

	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", httpBase+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s returned %d: %s", path, resp.StatusCode, respBody)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Some endpoints return empty body.
		return map[string]any{}
	}
	return result
}

func waitForHealth(base string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/v1/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
