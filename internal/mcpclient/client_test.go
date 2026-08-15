package mcpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/mcpserver"
)

// fixture writes a minimal Go project into root.
func fixture(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte("package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSelfDogfoodStdio runs go-coding's OWN MCP server in-process behind
// io.Pipes and drives it with the client's stdio transport — the client and
// server are two halves of one hand-written protocol, proven end to end.
func TestSelfDogfoodStdio(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	srv, err := mcpserver.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()   // requests: client writes, server reads
	outR, outW := io.Pipe() // responses: server writes, client reads
	go func() {
		_ = srv.ServeIO(inR, outW)
	}()

	c := newClient("own", newStdioTransport(inW, outR))
	defer c.Close()
	assertRoundTrip(t, c, dir)
}

// TestSelfDogfoodHTTP drives the same server over its Streamable-HTTP /mcp
// handler with the client's HTTP transport.
func TestSelfDogfoodHTTP(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	srv, err := mcpserver.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.ServeHTTP(w, r)
	}))
	defer ts.Close()

	c, err := DialHTTP(context.Background(), "own", ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	assertRoundTrip(t, c, dir)
}

// assertRoundTrip runs the full client flow against a live server: handshake,
// tool discovery, and one real tool call whose result is the file's content.
func assertRoundTrip(t *testing.T, c *Client, dir string) {
	t.Helper()
	ctx := context.Background()

	info, err := c.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info.Name == "" {
		t.Error("initialize must return serverInfo.name")
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) != 9 {
		t.Fatalf("tools/list = %d tools, want 9", len(tools))
	}

	res, err := c.CallTool(ctx, "read_file", map[string]any{"path": "calc.go"})
	if err != nil {
		t.Fatalf("tools/call read_file: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_file failed: %s", res.Text)
	}
	if want := "func Add(a, b int) int"; !strings.Contains(res.Text, want) {
		t.Errorf("read_file result missing %q:\n%s", want, res.Text)
	}

	// A failing remote tool surfaces as isError content, not a protocol error.
	bad, err := c.CallTool(ctx, "read_file", map[string]any{"path": "nope.go"})
	if err != nil {
		t.Fatalf("tools/call on missing file should not be a protocol error: %v", err)
	}
	if !bad.IsError {
		t.Error("missing file should mark isError")
	}

	// The registry mapping is zero-translation and name-spaced.
	defs, err := c.AsToolDefs(ctx)
	if err != nil {
		t.Fatalf("AsToolDefs: %v", err)
	}
	if len(defs) != 9 {
		t.Fatalf("AsToolDefs = %d, want 9", len(defs))
	}
	if defs[0].Name != "mcp:own:apply_edit" && defs[0].Name != "mcp:own:edit_file" {
		t.Logf("tool defs: %q... (first)", defs[0].Name)
	}
	found := false
	for _, d := range defs {
		if d.Name == "mcp:own:read_file" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mcp:own:read_file in defs, got %d defs", len(defs))
	}
}
