package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/zlx05/go-coding/internal/codeindex"
	"github.com/zlx05/go-coding/internal/mcprpc"
	"github.com/zlx05/go-coding/internal/sandbox"
	"github.com/zlx05/go-coding/internal/tools"
)

// pendingEdit is an edit staged by edit_file (diff preview) that has not
// been applied yet. It stores the resolved path and on-disk state at preview
// time so apply_edit can detect conflicts (file changed since preview →
// refuse).
type pendingEdit struct {
	pathRel string // sandbox-relative path, as given by the caller
	full    string // resolved absolute path (sandbox-validated)
	old     string // on-disk content at preview time
	new     string // proposed content
}

// Server owns one sandboxed workspace and exposes the atomic tools over MCP.
// For the stdio transport there is exactly one client per process, so a
// single workspace is the correct isolation boundary (M8 adds per-client
// workspaces over HTTP).
type Server struct {
	sandbox       *sandbox.Sandbox
	builder       *codeindex.Builder // shared: edit-time symbol validation + (future) retrieval
	editTool      *tools.EditTool
	runTestTool   *tools.RunTestTool
	readTool      *tools.ReadTool
	grepTool      *tools.GrepTool
	listTool      *tools.ListDirTool
	rollbackTool  *tools.RollbackTool
	diffTool      *tools.GitDiffTool

	mu      sync.Mutex
	pending map[string]*pendingEdit
	counter int

	name    string
	version string
}

// New builds an MCP server rooted at workdir. Git init and symbol indexing
// are best-effort: if git or indexing fails the server still works, just
// without rollback / symbol validation respectively.
func New(workdir string) (*Server, error) {
	sb, err := sandbox.New(workdir)
	if err != nil {
		return nil, err
	}
	// EnsureRepo makes every later edit snapshot-able (rollback). Failure is
	// degraded mode, matching the agent CLI.
	_ = sb.EnsureRepo()

	b := codeindex.NewBuilder(workdir)
	_, _ = b.Build() // non-Go repos just get an empty index

	return &Server{
		sandbox:      sb,
		builder:      b,
		editTool:     &tools.EditTool{Sandbox: sb, Builder: b},
		runTestTool:  &tools.RunTestTool{Sandbox: sb},
		readTool:     &tools.ReadTool{Sandbox: sb},
		grepTool:     &tools.GrepTool{Sandbox: sb},
		listTool:     &tools.ListDirTool{Sandbox: sb},
		rollbackTool: &tools.RollbackTool{Sandbox: sb},
		diffTool:     &tools.GitDiffTool{Sandbox: sb},
		pending:      map[string]*pendingEdit{},
		name:         "go-coding",
		version:      "0.1.0",
	}, nil
}

// Handle processes one JSON-RPC message and returns the response bytes, or
// nil for notifications (no response expected).
func (s *Server) Handle(line []byte) []byte {
	req, err := mcprpc.ParseRequest(line)
	if err != nil {
		// No usable ID → invalid request per JSON-RPC spec.
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
		// 2026-07-28 spec: stateless clients call this BEFORE anything else to
		// learn our supported protocol versions, capabilities and identity — no
		// initialize handshake required. The response also carries the
		// MRTR-required resultType plus cache hints so clients/proxies can cache
		// discovery.
		return mcprpc.MarshalResponse(req.ID, map[string]any{
			"resultType":        "complete",
			"supportedVersions": mcprpc.SupportedProtocolVersions,
			"capabilities":      map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions":      "Go editing tools: read_file/list_dir/grep to explore, edit_file→apply_edit (or reject_edit) for diff-previewed edits validated by a Go symbol engine, run_test, rollback, git_diff.",
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": map[string]any{"name": s.name, "version": s.version},
			},
			"ttlMs":     3600000,
			"cacheScope": "public",
		})

	case "notifications/initialized", "notifications/cancelled":
		return nil // acknowledged silently

	case "tools/list":
		return mcprpc.MarshalResponse(req.ID, map[string]any{"tools": toolDefinitions()})

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

// handleToolCall runs a tool and wraps the outcome in the MCP call-result
// shape. Tool runtime failures become isError content (so the host LLM reads
// the message) rather than JSON-RPC errors — protocol errors stay protocol.
func (s *Server) handleToolCall(req *mcprpc.Request) []byte {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return mcprpc.MarshalError(req.ID, mcprpc.CodeInvalidParams, "tools/call requires {name, arguments}")
	}

	result, err := s.call(context.Background(), params.Name, params.Arguments)
	if err != nil {
		return mcprpc.MarshalResponse(req.ID, map[string]any{
			"content":  []map[string]any{{"type": "text", "text": err.Error()}},
			"isError":  true,
		})
	}
	return mcprpc.MarshalResponse(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": toolResultText(result)}},
		"isError": false,
	})
}

// toolResultText flattens a tool's structured result into the text the MCP
// host LLM actually sees.
func toolResultText(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("<unrepresentable result: %v>", v)
	}
	return string(b)
}
