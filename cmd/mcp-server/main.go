// Command mcp-server exposes the go-coding agent's atomic tools over the
// Model Context Protocol (MCP), so any MCP host — VSCode + the Claude
// extension, Claude Desktop, Cursor — can drive the agent against a
// workspace: explore (read/list/grep), edit with diff preview + confirm,
// run tests, rollback. Every call runs inside the same sandbox + symbol
// validation the agent CLI uses.
//
// Usage:
//
//	go build ./cmd/mcp-server
//	./mcp-server -workdir /path/to/project              # stdio (default)
//	./mcp-server -workdir . -transport http -port 8787  # POST /mcp
//
// In VSCode, point an MCP server entry at the built binary.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/zlx05/go-coding/internal/mcpserver"
)

func main() {
	workdir := flag.String("workdir", ".", "workspace root (must be an existing directory)")
	transport := flag.String("transport", "stdio", "transport: stdio (default) or http")
	port := flag.Int("port", 8787, "HTTP port (used only with -transport http)")
	flag.Parse()

	switch *transport {
	case "http":
		// HTTP is multi-client: every session gets its own isolated
		// workspace (copied from -workdir when it points at a real repo).
		wm := mcpserver.NewWorkspaceManager(*workdir)
		http.HandleFunc("/mcp", wm.ServeHTTP)
		log.Printf("go-coding mcp-server listening on :%d/mcp (per-session workspaces, seeded from %s)", *port, *workdir)
		log.Fatal(http.ListenAndServe(":"+strconv.Itoa(*port), nil))
	case "stdio":
		// stdio: one client per process, one workspace.
		srv, err := mcpserver.New(*workdir)
		if err != nil {
			log.Fatalf("mcp-server: %v", err)
		}
		if err := srv.ServeIO(os.Stdin, os.Stdout); err != nil {
			log.Fatalf("mcp-server: %v", err)
		}
	default:
		log.Fatalf("unknown transport %q (want stdio or http)", *transport)
	}
}
