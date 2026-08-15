// Package mcpdemo is a minimal MCP server used to smoke-test the MCP client
// end to end against a real LLM. It exposes a small "legacy compatibility
// knowledge" tool that go-coding's own nine sandbox tools do not have, so a
// live agent must autonomously emit {"call_tool":{...}} to learn the obscure
// wire format the fixture's hidden acceptance test requires.
//
// It is deliberately tiny (one tool) and stateless per request — the same
// per-request JSON-RPC dispatch shape as internal/mcpserver, proving "any MCP
// server plugs in" via the client's zero-translation AsToolDefs.
package mcpdemo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/zlx05/go-coding/internal/mcprpc"
)

// TimeLayout is the legacy reporting API's compact wire layout
// (year2_dayOfYear_HHMMSS). It is deliberately non-guessable — a live agent
// cannot improvise it from the fixture source, so the MCP tool is the only
// way to learn it. 1600000000 → "20_257_222640" (the fixture's hidden test).
const TimeLayout = "06_002_150405"

// Render formats a unix timestamp in the legacy wire layout.
func Render(ts int64) string {
	return time.Unix(ts, 0).UTC().Format(TimeLayout)
}

// Server is the demo MCP server: one knowledge tool over JSON-RPC 2.0.
type Server struct {
	name    string
	version string
}

// New builds the demo server.
func New() *Server {
	return &Server{name: "go-coding-compat-demo", version: "0.1.0"}
}

// Handle processes one JSON-RPC message and returns the response bytes, or
// nil for notifications (no response expected).
func (s *Server) Handle(line []byte) []byte {
	req, err := mcprpc.ParseRequest(line)
	if err != nil {
		return mcprpc.MarshalError(nil, mcprpc.CodeParseError, err.Error())
	}
	switch req.Method {
	case "initialize":
		return mcprpc.MarshalResponse(req.ID, map[string]any{
			"protocolVersion": mcprpc.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		})

	case "server/discover":
		return mcprpc.MarshalResponse(req.ID, map[string]any{
			"resultType":        "complete",
			"supportedVersions": mcprpc.SupportedProtocolVersions,
			"capabilities":      map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions":      "Legacy compatibility knowledge base: get_time_format returns the reporting API's timestamp wire layout.",
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": map[string]any{"name": s.name, "version": s.version},
			},
			"ttlMs":     3600000,
			"cacheScope": "public",
		})

	case "notifications/initialized", "notifications/cancelled":
		return nil // acknowledged silently

	case "tools/list":
		return mcprpc.MarshalResponse(req.ID, map[string]any{"tools": []any{
			map[string]any{
				"name":        "get_time_format",
				"description": "Returns the legacy reporting API's exact timestamp wire format, as a Go time layout string. The project's FormatTimestamp must output this exact compact layout; it is not derivable from the source code and is defined only by the compatibility knowledge base.",
				"inputSchema": map[string]any{"type": "object"},
			},
		}})

	case "tools/call":
		return s.handleToolCall(req)

	case "resources/list":
		return mcprpc.MarshalResponse(req.ID, map[string]any{"resources": []any{}})
	case "prompts/list":
		return mcprpc.MarshalResponse(req.ID, map[string]any{"prompts": []any{}})

	case "ping":
		return mcprpc.MarshalResponse(req.ID, map[string]any{})

	case "shutdown":
		return mcprpc.MarshalResponse(req.ID, map[string]any{})

	default:
		return mcprpc.MarshalError(req.ID, mcprpc.CodeMethodNotFound, "method not found: "+req.Method)
	}
}

// handleToolCall runs the single knowledge tool. Runtime failures become
// isError content (so the host LLM reads the message), matching mcpserver.
func (s *Server) handleToolCall(req *mcprpc.Request) []byte {
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return mcprpc.MarshalError(req.ID, mcprpc.CodeInvalidParams, "tools/call requires {name}")
	}
	if params.Name != "get_time_format" {
		return mcprpc.MarshalError(req.ID, mcprpc.CodeMethodNotFound, "unknown tool: "+params.Name)
	}
	text := fmt.Sprintf(
		"The legacy reporting API's timestamp wire layout (Go time layout) is %q — use time.Unix(ts, 0).UTC().Format(%q). Example: FormatTimestamp(1600000000) = %q.",
		TimeLayout, TimeLayout, Render(1600000000))
	return mcprpc.MarshalResponse(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	})
}

// ServeIO runs the stdio transport: read newline-delimited JSON-RPC messages
// from r, write responses (also newline-delimited) to w. Same framing as
// mcpserver.ServeIO.
func (s *Server) ServeIO(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := s.Handle(line)
		if resp == nil {
			continue // notification — no response expected
		}
		if _, err := w.Write(append(resp, '\n')); err != nil {
			return err
		}
	}
	return sc.Err()
}
