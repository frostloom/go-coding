package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// httpResult sends one JSON-RPC request over the HTTP transport for a
// session and returns the decoded "result" plus the session id the server
// assigned/echoed.
func httpResult(t *testing.T, wm *WorkspaceManager, sessionID string, method string, params any) (json.RawMessage, string) {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(msg))
	if sessionID != "" {
		req.Header.Set(sessionHeader, sessionID)
	}
	rec := httptest.NewRecorder()
	wm.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("bad response: %v\n%s", err, rec.Body.String())
	}
	if r.Error != nil {
		t.Fatalf("rpc error: %s\n%s", r.Error.Message, rec.Body.String())
	}
	return r.Result, rec.Header().Get(sessionHeader)
}

// httpCall sends a tools/call and returns the concatenated text + isError.
func httpCall(t *testing.T, wm *WorkspaceManager, sessionID, name string, args any) (string, bool) {
	t.Helper()
	result, _ := httpResult(t, wm, sessionID, "tools/call", map[string]any{"name": name, "arguments": args})
	var cr struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &cr); err != nil {
		t.Fatalf("bad tools/call result: %v", err)
	}
	var b strings.Builder
	for _, c := range cr.Content {
		b.WriteString(c.Text)
	}
	return b.String(), cr.IsError
}

// httpInner calls a tool and returns the tool's OWN result JSON (unwrapped
// from the MCP {content:[{text}]} envelope) — e.g. {pending, preview, ...}.
func httpInner(t *testing.T, wm *WorkspaceManager, sessionID, name string, args any) json.RawMessage {
	t.Helper()
	result, _ := httpResult(t, wm, sessionID, "tools/call", map[string]any{"name": name, "arguments": args})
	var cr struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &cr); err != nil || len(cr.Content) == 0 {
		t.Fatalf("bad tools/call envelope: %s (err=%v)", result, err)
	}
	return json.RawMessage(cr.Content[0].Text)
}

// TestWorkspaceManagerHTTPIsolatesSessions is the M8 isolation proof over
// HTTP: two sessions through the SAME mcp-server process get separate
// workspaces. Session A's edits must be invisible to session B, and visible
// to A again (same-session persistence).
func TestWorkspaceManagerHTTPIsolatesSessions(t *testing.T) {
	wm := NewWorkspaceManager("") // no base repo: sessions start empty

	// Mint session A via initialize (no header → server assigns one).
	result, sessA := httpResult(t, wm, "", "initialize", map[string]any{})
	if sessA == "" {
		t.Fatal("initialize over HTTP must mint and return a session id")
	}
	if len(result) == 0 {
		t.Fatal("initialize returned empty result")
	}

	// A stages a new file (edit_file preview → pending id), then applies it.
	prev, isErr := httpCall(t, wm, sessA, "edit_file", map[string]any{
		"path":    "new.go",
		"content": "package demo\n\n// New returns 1.\nfunc New() int { return 1 }\n",
	})
	if isErr || !strings.Contains(prev, "pending") {
		t.Fatalf("edit_file preview failed: isErr=%v text=%s", isErr, prev)
	}
	prevResult := httpInner(t, wm, sessA, "edit_file", map[string]any{
		"path": "new.go", "content": "package demo\n\n// New returns 1.\nfunc New() int { return 1 }\n",
	})
	var pe struct {
		Pending string `json:"pending"`
	}
	if err := json.Unmarshal(prevResult, &pe); err != nil || pe.Pending == "" {
		t.Fatalf("preview should carry a pending id, got %s (err=%v)", prevResult, err)
	}
	applied, isErr := httpCall(t, wm, sessA, "apply_edit", map[string]string{"id": pe.Pending})
	if isErr || !strings.Contains(applied, "applied") {
		t.Fatalf("apply_edit failed: isErr=%v text=%s", isErr, applied)
	}

	// A reads its own file back (same session → same workspace).
	text, isErr := httpCall(t, wm, sessA, "read_file", map[string]string{"path": "new.go"})
	if isErr || !strings.Contains(text, "func New") {
		t.Fatalf("session A should read its own file: isErr=%v text=%s", isErr, text)
	}

	// Session B — different session id — must NOT see A's file.
	text, isErr = httpCall(t, wm, "sess-other", "read_file", map[string]string{"path": "new.go"})
	if !isErr {
		t.Errorf("session B should NOT see session A's file; got text=%s", text)
	}
	if strings.Contains(text, "func New") {
		t.Error("session B leaked session A's file content")
	}
}

// TestWorkspaceManagerSeededFromBaseRepo: with a base repo, each session
// starts as a copy of it — two sessions each get the seeded project without
// sharing edits.
func TestWorkspaceManagerSeededFromBaseRepo(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	wm := NewWorkspaceManager(root)

	for _, sess := range []string{"A", "B"} {
		text, isErr := httpCall(t, wm, sess, "read_file", map[string]string{"path": "calc.go"})
		if isErr || !strings.Contains(text, "func Add") {
			t.Errorf("session %s should see the seeded calc.go: isErr=%v", sess, isErr)
		}
	}

	// A edits calc.go; B must still see the ORIGINAL.
	prevResult := httpInner(t, wm, "A", "edit_file", map[string]any{
		"path": "calc.go",
		"old":  "return a - b // bug",
		"new":  "return a + b",
	})
	var pe struct {
		Pending string `json:"pending"`
	}
	if err := json.Unmarshal(prevResult, &pe); err != nil || pe.Pending == "" {
		t.Fatalf("preview should carry a pending id: %s", prevResult)
	}
	if _, isErr := httpCall(t, wm, "A", "apply_edit", map[string]string{"id": pe.Pending}); isErr {
		t.Fatal("apply_edit for A failed")
	}
	aText, _ := httpCall(t, wm, "A", "read_file", map[string]string{"path": "calc.go"})
	if !strings.Contains(aText, "return a + b") {
		t.Errorf("A should see its own edit, got: %s", aText)
	}
	bText, _ := httpCall(t, wm, "B", "read_file", map[string]string{"path": "calc.go"})
	if !strings.Contains(bText, "return a - b // bug") {
		t.Errorf("B should still see the ORIGINAL calc.go (edit isolation failed): %s", bText)
	}
}
