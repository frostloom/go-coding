// Command mcp-demo-server is a minimal MCP server used to smoke-test the MCP
// client end to end: it exposes a tiny "legacy compatibility knowledge" tool
// (get_time_format) that the agent's own nine sandbox tools do not have, so a
// live agent must autonomously call_tool it to learn an obscure wire format.
//
// Spawn it from cmd/agent with the stdio transport:
//
//	go build ./cmd/mcp-demo-server
//	go run ./cmd/agent -mcp "compat ./mcp-demo-server.exe" -repo testdata/cases/fix_legacy_time -req "..."
package main

import (
	"log"
	"os"

	"github.com/zlx05/go-coding/internal/mcpdemo"
)

func main() {
	if err := mcpdemo.New().ServeIO(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("mcp-demo-server: %v", err)
	}
}
