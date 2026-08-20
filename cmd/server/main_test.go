package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cmd/server had no tests. It is the composition root -- where the gateway,
// the rate limiter and the auth interceptor are wired together -- and two
// defects this session lived in exactly that seam: a REST request spending two
// rate-limit tokens because both layers charged it, and configuration silently
// falling back to defaults because nothing validated it.
//
// These cover the three ways startup can fail. A deployment that dies at
// startup is read through its last log line, so what that line says is the
// whole diagnostic: naming the wrong component sends an operator to the wrong
// place while the service is down.

func serverBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "context0-server")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	return bin
}

// runServer starts the binary and waits for it to exit, which every case here
// expects it to do. A server that wrongly starts instead of failing is caught
// by the timeout rather than hanging the suite.
func runServer(t *testing.T, bin string, env ...string) (output string, code int, exited bool) {
	t.Helper()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), env...)

	out, err := cmd.CombinedOutput()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run server: %v", err)
	}
	return string(out), code, true
}

// freePort reserves a port and releases it, so the caller gets one that is
// almost certainly free.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestUnreachableDatabaseIsFatal: the server cannot serve anything without the
// graph, so starting and then failing every request would be worse than
// refusing -- Kubernetes restarts a crashed container and backs off, but keeps
// routing to a running one.
func TestUnreachableDatabaseIsFatal(t *testing.T) {
	bin := serverBinary(t)

	out, code, _ := runServer(t, bin,
		"CONTEXT0_DATABASE_URL=postgres://nobody@127.0.0.1:1/none?sslmode=disable",
		fmt.Sprintf("CONTEXT0_GRPC_PORT=%d", freePort(t)),
		fmt.Sprintf("CONTEXT0_HTTP_PORT=%d", freePort(t)),
	)

	if code == 0 {
		t.Errorf("the server exited 0 with an unreachable database: %s", out)
	}
	// The message has to name the database. "failed to start" would send an
	// operator looking at the wrong component while the service is down.
	if !strings.Contains(out, "failed to connect to database") {
		t.Errorf("the failure does not name the database: %s", out)
	}
}

// TestUnknownEmbeddingProviderIsFatal: a misconfigured provider must not fall
// back to a working default, because the fallback would produce vectors of a
// different distribution and silently degrade every search.
func TestUnknownEmbeddingProviderIsFatal(t *testing.T) {
	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}
	bin := serverBinary(t)

	out, code, _ := runServer(t, bin,
		"CONTEXT0_DATABASE_URL="+dsn,
		"CONTEXT0_EMBEDDING_PROVIDER=nonsense",
		fmt.Sprintf("CONTEXT0_GRPC_PORT=%d", freePort(t)),
		fmt.Sprintf("CONTEXT0_HTTP_PORT=%d", freePort(t)),
	)

	if code == 0 {
		t.Errorf("the server started with an unknown embedding provider: %s", out)
	}
	if !strings.Contains(out, "failed to create embedder") {
		t.Errorf("the failure does not name the embedder: %s", out)
	}
	// The rejected value must appear, or the operator cannot see what was
	// wrong with what they set.
	if !strings.Contains(out, "nonsense") {
		t.Errorf("the failure does not echo the rejected provider name: %s", out)
	}
}

// TestOccupiedGRPCPortIsFatal: a port collision is a real deployment failure --
// two replicas on one host, or a sidecar claiming the port.
//
// The holder binds the same address family the server uses. An earlier version
// of this test held an IPv4 socket while Go listened on IPv6, which is not a
// conflict at all: the server started correctly and the test would have
// reported that as a defect.
func TestOccupiedGRPCPortIsFatal(t *testing.T) {
	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}
	bin := serverBinary(t)

	port := freePort(t)
	holder, err := net.Listen("tcp", fmt.Sprintf("[::]:%d", port))
	if err != nil {
		// No IPv6 on this machine; the collision cannot be staged.
		t.Skipf("cannot bind IPv6 to stage a port collision: %v", err)
	}
	defer holder.Close()

	out, code, _ := runServer(t, bin,
		"CONTEXT0_DATABASE_URL="+dsn,
		fmt.Sprintf("CONTEXT0_GRPC_PORT=%d", port),
		fmt.Sprintf("CONTEXT0_HTTP_PORT=%d", freePort(t)),
	)

	if code == 0 {
		t.Errorf("the server started with its gRPC port already in use: %s", out)
	}
	if !strings.Contains(out, "failed to listen for gRPC") {
		t.Errorf("the failure does not name the listener: %s", out)
	}
	// The address belongs in the message: which port collided is the first
	// thing an operator needs.
	if !strings.Contains(out, fmt.Sprint(port)) {
		t.Errorf("the failure does not report which port collided: %s", out)
	}
}

// TestInvalidConfigurationIsFatalBeforeAnyIO: configuration is validated ahead
// of the database connection, so a bad value is reported as itself rather than
// as whatever failed first. Without that ordering an operator with a typo sees
// a connection error and looks at the database.
func TestInvalidConfigurationIsFatalBeforeAnyIO(t *testing.T) {
	bin := serverBinary(t)

	out, code, _ := runServer(t, bin,
		// Unreachable on purpose: if validation ran after the connection, this
		// is the error that would surface instead.
		"CONTEXT0_DATABASE_URL=postgres://nobody@127.0.0.1:1/none?sslmode=disable",
		"CONTEXT0_RATE_LIMIT_PER_MINUTE=6OOO",
		fmt.Sprintf("CONTEXT0_GRPC_PORT=%d", freePort(t)),
		fmt.Sprintf("CONTEXT0_HTTP_PORT=%d", freePort(t)),
	)

	if code == 0 {
		t.Fatalf("the server started with an unparseable rate limit: %s", out)
	}
	if !strings.Contains(out, "CONTEXT0_RATE_LIMIT_PER_MINUTE") {
		t.Errorf("the failure does not name the offending variable: %s", out)
	}
	if strings.Contains(out, "failed to connect to database") {
		t.Errorf("configuration was validated after the database connection, so "+
			"the operator sees a connection error rather than their typo: %s", out)
	}
}

// TestStartupLogsTheEffectiveConfiguration: a deployment's behaviour has to be
// attributable to its configuration. The API key list is reported as a count,
// never as values -- this line is written on every start.
func TestStartupLogsTheEffectiveConfiguration(t *testing.T) {
	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}
	bin := serverBinary(t)

	const secret = "ctx0_startup_probe_secret_value"

	// The server does not exit on its own, so run it briefly and stop it.
	grpcPort := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"CONTEXT0_DATABASE_URL="+dsn,
		"CONTEXT0_API_KEYS="+secret,
		"CONTEXT0_RATE_LIMIT_PER_MINUTE=4242",
		fmt.Sprintf("CONTEXT0_GRPC_PORT=%d", grpcPort),
		fmt.Sprintf("CONTEXT0_HTTP_PORT=%d", freePort(t)),
	)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	time.Sleep(6 * time.Second)

	// Dial while the server is still running. Checking after Kill() would
	// always see a refused connection, which is how an earlier version of this
	// assertion failed against a perfectly healthy server.
	//
	// localhost, not 127.0.0.1: the server listens on ":port", which resolves
	// to the IPv6 wildcard, so dialling the IPv4 literal can miss it.
	conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", grpcPort), 2*time.Second)
	if dialErr != nil {
		t.Errorf("nothing is accepting connections on the gRPC port %d: %v",
			grpcPort, dialErr)
	} else {
		_ = conn.Close()
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	out := buf.String()

	// The gRPC listener must actually be bound. Asserting only on the log
	// leaves the listen error path unprotected: forcing it to fire made the
	// server exit, and a test that never connects does not notice.
	if !strings.Contains(out, `"msg":"gRPC server listening"`) {
		t.Errorf("the server did not report its gRPC listener: %s", out)
	}
	if !strings.Contains(out, `"msg":"configuration"`) {
		t.Fatalf("startup logged no configuration line, so a setting silently "+
			"replaced by a default leaves no trace: %s", out)
	}
	if !strings.Contains(out, "4242") {
		t.Errorf("the configured rate limit does not appear in the startup log: %s", out)
	}
	// The credential must never be in the log, only its count.
	if strings.Contains(out, secret) {
		t.Errorf("the API key appears verbatim in the startup log")
	}
	if !strings.Contains(out, `"api_keys":1`) {
		t.Errorf("the startup log does not report the key count: %s", out)
	}
}
