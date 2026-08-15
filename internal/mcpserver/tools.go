package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zlx05/go-coding/internal/edit"
	"github.com/zlx05/go-coding/internal/tools"
)

// toolDef is one MCP tool: its JSON-Schema input and the Go handler.
type toolDef struct {
	Name        string
	Description string
	Schema      map[string]any
	Call        func(ctx context.Context, args json.RawMessage) (any, error)
}

// toolDefinitions returns the static list exposed via tools/list. The edit
// workflow is split across three tools so a host can (and is prompted to)
// preview before applying — the "like Claude Code, show me the diff first"
// behavior:
//
//	edit_file  → computes new content, returns a diff preview + pending id (no write)
//	apply_edit → writes it through the full validation chain (syntax + call graph)
//	reject_edit → discards the pending edit
func toolDefinitions() []map[string]any {
	defs := []toolDef{
		{
			Name:        "read_file",
			Description: "Read a file (returns full content) or a directory (returns entry names).",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
		},
		{
			Name:        "list_dir",
			Description: "List top-level entries of the workspace (or a subdirectory).",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		},
		{
			Name:        "grep",
			Description: "Case-insensitive search over .go files; returns file:snippet matches.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}}, "required": []string{"pattern"}},
		},
		{
			Name:        "edit_file",
			Description: "Stage an edit and return a diff preview WITHOUT writing. Provide one of: 'content' (full file), 'old'/'new' (precise single-occurrence text edit), 'symbol'/'new' (AST-level: replace the declaration named symbol without knowing its current body, +'recv' e.g. \"*Cache\" to pick a method when the name is shared), or 'symbol'/'fn'/'new' (AST-level body-local: replace the local variable declared as symbol inside function fn, +'recv' for a method body). Returns a 'pending' id — call apply_edit to land it (runs syntax + call-graph validation) or reject_edit to discard.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
				"old":     map[string]any{"type": "string"},
				"new":     map[string]any{"type": "string"},
				"symbol":  map[string]any{"type": "string"},
				"fn":      map[string]any{"type": "string"},
				"recv":    map[string]any{"type": "string"},
			}, "required": []string{"path"}},
		},
		{
			Name:        "apply_edit",
			Description: "Apply a previously previewed edit (by 'pending' id) through the validation chain. Refuses edits that break syntax or a cross-file reference.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}},
		},
		{
			Name:        "reject_edit",
			Description: "Discard a staged edit (by 'pending' id) without writing anything.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"}},
		},
		{
			Name:        "run_test",
			Description: "Run `go test ./...` in the workspace (sandboxed, with timeout).",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "rollback",
			Description: "Revert the last applied edit (git snapshot rollback).",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "git_diff",
			Description: "Show the cumulative diff of all applied edits since the baseline.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"name":        d.Name,
			"description": d.Description,
			"inputSchema": d.Schema,
		})
	}
	// The 2026-07-28 spec asks servers to return tools in deterministic order —
	// stable output feeds client caching and LLM prompt-cache hit rates. Sort by
	// name so determinism is guaranteed by code, not by slice-literal order.
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out
}

// call dispatches a tool call by name to its handler.
func (s *Server) call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	switch name {
	case "read_file":
		return s.readFile(ctx, args)
	case "list_dir":
		return s.listDir(ctx, args)
	case "grep":
		return s.grep(ctx, args)
	case "edit_file":
		return s.editFile(ctx, args)
	case "apply_edit":
		return s.applyEdit(ctx, args)
	case "reject_edit":
		return s.rejectEdit(ctx, args)
	case "run_test":
		return s.runTest(ctx, args)
	case "rollback":
		return s.rollback(ctx, args)
	case "git_diff":
		return s.gitDiff(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func (s *Server) readFile(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
		return nil, errors.New("read_file: missing 'path'")
	}
	full, err := s.sandbox.Resolve(a.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return map[string]any{"entries": names}, nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": string(b)}, nil
}

func (s *Server) listDir(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Path string `json:"path,omitempty"`
	}
	_ = json.Unmarshal(args, &a)
	full := s.sandbox.Root // empty path = workspace root
	if a.Path != "" {
		var err error
		full, err = s.sandbox.Resolve(a.Path)
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name()+"/")
		} else {
			names = append(names, e.Name())
		}
	}
	return map[string]any{"entries": names}, nil
}

func (s *Server) grep(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Pattern == "" {
		return nil, errors.New("grep: missing 'pattern'")
	}
	res, err := s.grepTool.Run(context.Background(), tools.Args(mustJSON(map[string]string{"pattern": a.Pattern})))
	if err != nil {
		return nil, err
	}
	return map[string]any{"matches": res.Output}, nil
}

// editFile stages an edit and returns a preview. It must not touch the disk.
func (s *Server) editFile(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content,omitempty"`
		Old     string `json:"old,omitempty"`
		New     string `json:"new,omitempty"`
		Symbol  string `json:"symbol,omitempty"`
		Fn      string `json:"fn,omitempty"`
		Recv    string `json:"recv,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
		return nil, errors.New("edit_file: missing 'path'")
	}
	full, err := s.sandbox.Resolve(a.Path)
	if err != nil {
		return nil, err
	}

	cur, _ := os.ReadFile(full) // file may not exist yet
	var newContent []byte
	switch {
	case a.Content != "":
		newContent = []byte(a.Content)
	case a.Old != "":
		newContent, err = edit.ApplyText(cur, a.Old, a.New)
		if err != nil {
			return nil, err
		}
	case a.Symbol != "" && a.Fn != "":
		newContent, err = edit.ApplyLocal(cur, a.Fn, a.Recv, a.Symbol, a.New)
		if err != nil {
			return nil, err
		}
	case a.Symbol != "":
		newContent, err = edit.ApplySymbol(cur, a.Symbol, a.Recv, a.New)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("edit_file: provide 'content' (full file), 'old'+'new' (precise edit), 'symbol'+'new' (replace a declaration, +'recv' for a specific method), or 'symbol'+'fn'+'new' (replace a local declaration inside a function, +'recv' for a method body)")
	}

	if isGoFile(a.Path) {
		if err := edit.CheckSyntax(filepath.Base(full), newContent); err != nil {
			return nil, fmt.Errorf("syntax error in proposed content: %w", err)
		}
	}

	id := s.stagePending(a.Path, full, string(cur), string(newContent))
	return map[string]any{
		"pending": id,
		"path":    a.Path,
		"applied": false,
		"preview": unifiedDiff(string(cur), string(newContent)),
	}, nil
}

// applyEdit lands a staged edit through the full validation chain. Single-use:
// a pending id can be applied or rejected once.
func (s *Server) applyEdit(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return nil, errors.New("apply_edit: missing 'id'")
	}
	pe, ok := s.takePending(a.ID)
	if !ok {
		return nil, fmt.Errorf("apply_edit: unknown or already-consumed pending id %q (re-run edit_file)", a.ID)
	}
	// Conflict detection: the file must not have drifted since the preview.
	cur, _ := os.ReadFile(pe.full)
	if string(cur) != pe.old {
		return nil, fmt.Errorf("apply_edit: file changed since the preview (conflict) — re-run edit_file for a fresh preview")
	}

	argsJSON, _ := json.Marshal(map[string]string{"path": pe.pathRel, "content": pe.new})
	res, err := s.editTool.Run(ctx, tools.Args(argsJSON))
	if err != nil {
		var rejected *tools.EditRejectedError
		if errors.As(err, &rejected) {
			// The file is restored by the tool; surface the reason to the host.
			return nil, fmt.Errorf("edit rejected before tests ran: %s", rejected.Reason)
		}
		return nil, err
	}
	// Snapshot AFTER the write so the change is committed: git_diff shows it
	// and rollback (reset --hard HEAD~1) restores the pre-edit baseline. Best
	// effort — without git, governance degrades but the edit still lands.
	if err := s.sandbox.Snapshot("mcp edit " + pe.pathRel); err != nil {
		_ = err
	}
	return map[string]any{"applied": res.Ok, "path": pe.pathRel}, nil
}

func (s *Server) rejectEdit(_ context.Context, args json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return nil, errors.New("reject_edit: missing 'id'")
	}
	if _, ok := s.takePending(a.ID); !ok {
		return nil, fmt.Errorf("reject_edit: unknown pending id %q", a.ID)
	}
	return map[string]any{"discarded": true}, nil
}

func (s *Server) runTest(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := s.runTestTool.Run(ctx, tools.Args(`{}`))
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": res.Ok, "output": res.Output}, nil
}

func (s *Server) rollback(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := s.rollbackTool.Run(ctx, tools.Args(`{}`))
	if err != nil {
		return nil, err
	}
	return map[string]any{"rolled_back": res.Ok}, nil
}

func (s *Server) gitDiff(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := s.diffTool.Run(ctx, tools.Args(`{}`))
	if err != nil {
		return nil, err
	}
	return map[string]any{"diff": res.Output}, nil
}

// --- pending-edit store (mutex-guarded) ---

func (s *Server) stagePending(pathRel, full, old, new string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	id := newEditID(s.counter)
	s.pending[id] = &pendingEdit{pathRel: pathRel, full: full, old: old, new: new}
	return id
}

func (s *Server) takePending(id string) (*pendingEdit, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pe, ok := s.pending[id]
	if ok {
		delete(s.pending, id) // single-use
	}
	return pe, ok
}

func newEditID(n int) string {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("pe-%d", n)
	}
	return "pe-" + hex.EncodeToString(buf[:]) + fmt.Sprintf("-%d", n)
}

func isGoFile(p string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.ToSlash(p)), ".go")
}

func mustJSON(v map[string]string) []byte {
	b, _ := json.Marshal(v)
	return b
}
