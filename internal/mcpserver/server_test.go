package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a small Go project into root and returns it. calc.go has a
// bug (Add returns a-b); use.go calls Add from ANOTHER file, so deleting Add
// must be caught by the call-graph regression gate.
func fixture(t *testing.T, root string) {
	t.Helper()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("calc.go", `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b // bug
}

// Mul returns the product.
func Mul(a, b int) int {
	return a * b
}
`)
	write("use.go", `package demo

// useAdd calls Add from another file.
func useAdd(a, b int) int {
	return Add(a, b)
}
`)
	write("calc_test.go", `package demo

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add broken")
	}
}
`)
}

func newServer(t *testing.T, root string) *Server {
	t.Helper()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// request sends a JSON-RPC request and returns the raw response.
func request(t *testing.T, s *Server, method string, params any) []byte {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	resp := s.Handle(msg)
	if resp == nil {
		t.Fatalf("expected a response for %s", method)
	}
	return resp
}

// rpcResult decodes the "result" field of a response.
func rpcResult(t *testing.T, resp []byte) json.RawMessage {
	t.Helper()
	var r struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("bad response: %v\n%s", err, resp)
	}
	if r.Error != nil {
		t.Fatalf("rpc error: %s\n%s", r.Error.Message, resp)
	}
	return r.Result
}

// callTool invokes tools/call and returns the text the host LLM would see
// plus whether it was flagged as an error.
func callTool(t *testing.T, s *Server, name string, args any) (text string, isError bool) {
	t.Helper()
	resp := request(t, s, "tools/call", map[string]any{"name": name, "arguments": args})
	result := rpcResult(t, resp)
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

func TestInitialize(t *testing.T) {
	s := newServer(t, t.TempDir())
	resp := request(t, s, "initialize", map[string]any{})
	res := rpcResult(t, resp)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(res, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion == "" {
		t.Error("initialize must return a protocolVersion")
	}
	if init.ServerInfo.Name != "go-coding" {
		t.Errorf("serverInfo.name = %q, want go-coding", init.ServerInfo.Name)
	}
}

// TestDiscover is the 2026-07-28 spec's replacement for the initialize
// handshake: a stateless client asks server/discover once and learns our
// supported protocol versions, capabilities and identity with no session.
func TestDiscover(t *testing.T) {
	s := newServer(t, t.TempDir())
	res := rpcResult(t, request(t, s, "server/discover", map[string]any{}))
	var d struct {
		ResultType        string   `json:"resultType"`
		SupportedVersions []string `json:"supportedVersions"`
		Capabilities      struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		Meta struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"io.modelcontextprotocol/serverInfo"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(res, &d); err != nil {
		t.Fatal(err)
	}
	if d.ResultType != "complete" {
		t.Errorf("resultType = %q, want complete (MRTR field)", d.ResultType)
	}
	hasModern, hasLegacy := false, false
	for _, v := range d.SupportedVersions {
		switch v {
		case "2026-07-28":
			hasModern = true
		case "2025-03-26":
			hasLegacy = true
		}
	}
	if !hasModern {
		t.Errorf("supportedVersions %v must include the stateless 2026-07-28 version", d.SupportedVersions)
	}
	if !hasLegacy {
		t.Errorf("supportedVersions %v must include the legacy 2025-03-26 version", d.SupportedVersions)
	}
	if d.Capabilities.Tools.ListChanged {
		t.Error("listChanged should be false (we have no change notifications)")
	}
	if d.Meta.ServerInfo.Name != "go-coding" {
		t.Errorf("_meta.serverInfo.name = %q, want go-coding", d.Meta.ServerInfo.Name)
	}
}

func TestToolsList(t *testing.T) {
	s := newServer(t, t.TempDir())
	res := rpcResult(t, request(t, s, "tools/list", map[string]any{}))
	var tl struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema any    `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res, &tl); err != nil {
		t.Fatal(err)
	}
	// Deterministic (2026-07-28 spec: stable tool order feeds client + prompt
	// caches) — sorted by name, not slice-literal order.
	want := []string{"apply_edit", "edit_file", "git_diff", "grep", "list_dir", "read_file", "reject_edit", "rollback", "run_test"}
	if len(tl.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tl.Tools), len(want))
	}
	for i, w := range want {
		if tl.Tools[i].Name != w {
			t.Errorf("tool[%d] = %q, want %q", i, tl.Tools[i].Name, w)
		}
	}
}

func TestReadAndListAndGrep(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	text, isErr := callTool(t, s, "read_file", map[string]string{"path": "calc.go"})
	if isErr || !strings.Contains(text, "func Add") {
		t.Errorf("read_file failed: isErr=%v text=%s", isErr, text)
	}

	text, isErr = callTool(t, s, "list_dir", map[string]string{})
	if isErr || !strings.Contains(text, "calc.go") {
		t.Errorf("list_dir failed: isErr=%v text=%s", isErr, text)
	}

	text, isErr = callTool(t, s, "grep", map[string]string{"pattern": "Add"})
	if isErr || !strings.Contains(text, "calc.go") {
		t.Errorf("grep failed: isErr=%v text=%s", isErr, text)
	}
}

// TestEditPreviewDoesNotWrite: edit_file returns a diff preview + pending id
// but must leave the disk untouched — that is the "show me before you change
// it" contract.
func TestEditPreviewDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	before, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	text, isErr := callTool(t, s, "edit_file", map[string]any{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})
	if isErr {
		t.Fatalf("edit_file errored: %s", text)
	}
	if !strings.Contains(text, `"pending"`) ||
		!strings.Contains(text, "return a - b") || // removed line
		!strings.Contains(text, "return a + b") { // added line
		t.Errorf("preview should carry a pending id and a diff, got:\n%s", text)
	}
	after, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(after) != string(before) {
		t.Errorf("edit_file must NOT write; disk changed:\n%s", after)
	}
}

// TestEditApplyThenTest: preview → apply → run_test is the full closed loop
// a host LLM would drive against the MCP server.
func TestEditApplyThenTest(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	preview, _ := callTool(t, s, "edit_file", map[string]any{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})
	id := extractPending(t, preview)

	text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id})
	if isErr {
		t.Fatalf("apply_edit errored: %s", text)
	}
	if !strings.Contains(text, `"applied": true`) {
		t.Errorf("apply_edit should report applied=true, got: %s", text)
	}

	// The fix should make the visible test pass.
	text, isErr = callTool(t, s, "run_test", map[string]any{})
	if isErr {
		t.Errorf("run_test failed after apply:\n%s", text)
	}
	if !strings.Contains(text, "ok") && !strings.Contains(text, "PASS") {
		t.Errorf("run_test output should indicate pass, got:\n%s", text)
	}
}

func TestEditApplySymbolByDeclaration(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	preview, isErr := callTool(t, s, "edit_file", map[string]any{
		"path":   "calc.go",
		"symbol": "Add",
		"new":    "func Add(a, b int) int {\n\treturn a + b\n}",
	})
	if isErr {
		t.Fatalf("edit_file symbol mode errored: %s", preview)
	}
	if !strings.Contains(preview, "return a - b") || !strings.Contains(preview, "return a + b") {
		t.Errorf("preview should diff old vs new Add body, got:\n%s", preview)
	}
	id := extractPending(t, preview)
	text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id})
	if isErr {
		t.Fatalf("apply_edit errored: %s", text)
	}
	if !strings.Contains(text, `"applied": true`) {
		t.Errorf("apply_edit should report applied=true, got: %s", text)
	}
	if text, isErr = callTool(t, s, "run_test", map[string]any{}); isErr {
		t.Errorf("run_test failed after symbol apply:\n%s", text)
	}
}

func TestEditApplySymbolLocalByFn(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	// A dedicated file with a local variable to target.
	if err := os.WriteFile(filepath.Join(root, "sum.go"), []byte(`package demo

// Sum adds xs and returns the total.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Body-local edit: replace the local `total` inside Sum by name.
	preview, isErr := callTool(t, s, "edit_file", map[string]any{
		"path":   "sum.go",
		"symbol": "total",
		"fn":     "Sum",
		"new":    "total := 100",
	})
	if isErr {
		t.Fatalf("edit_file body-local mode errored: %s", preview)
	}
	if !strings.Contains(preview, "total := 0") || !strings.Contains(preview, "total := 100") {
		t.Errorf("preview should diff the local declaration, got:\n%s", preview)
	}
	id := extractPending(t, preview)
	text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id})
	if isErr || !strings.Contains(text, `"applied": true`) {
		t.Fatalf("apply_edit failed: isErr=%v text=%s", isErr, text)
	}
	got, _ := os.ReadFile(filepath.Join(root, "sum.go"))
	if !strings.Contains(string(got), "total := 100") || strings.Contains(string(got), "total := 0") {
		t.Errorf("body-local edit not landed:\n%s", got)
	}
}

// TestEditApplySymbolRecvDisambiguates: two methods share the name Get on
// different receivers; 'recv' picks the requested method through the MCP
// edit_file entry.
func TestEditApplySymbolRecvDisambiguates(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	if err := os.WriteFile(filepath.Join(root, "methods.go"), []byte(`package demo

// (A) Get returns A's value.
func (a *A) Get() int {
	return 1
}

// (B) Get returns B's value.
func (b *B) Get() int {
	return 2
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	preview, isErr := callTool(t, s, "edit_file", map[string]any{
		"path":   "methods.go",
		"symbol": "Get",
		"recv":   "*B",
		"new":    "func (b *B) Get() int {\n\treturn 3\n}",
	})
	if isErr {
		t.Fatalf("edit_file recv mode errored: %s", preview)
	}
	id := extractPending(t, preview)
	text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id})
	if isErr || !strings.Contains(text, `"applied": true`) {
		t.Fatalf("apply_edit failed: isErr=%v text=%s", isErr, text)
	}
	got, _ := os.ReadFile(filepath.Join(root, "methods.go"))
	src := string(got)
	if !strings.Contains(src, "return 3") || !strings.Contains(src, "return 1") || strings.Contains(src, "return 2") {
		t.Errorf("recv should target only the *B method:\n%s", src)
	}
}

func TestEditRejectDiscards(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	before, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	preview, _ := callTool(t, s, "edit_file", map[string]any{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})
	id := extractPending(t, preview)

	text, isErr := callTool(t, s, "reject_edit", map[string]string{"id": id})
	if isErr || !strings.Contains(text, "discarded") {
		t.Fatalf("reject_edit failed: isErr=%v text=%s", isErr, text)
	}
	after, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(after) != string(before) {
		t.Errorf("reject_edit must not write; disk changed")
	}

	// A consumed id cannot be applied.
	_, isErr = callTool(t, s, "apply_edit", map[string]string{"id": id})
	if !isErr {
		t.Error("applying a rejected id should error")
	}
}

// TestEditCrossFileRegressionRejected is the MCP proof of M4's headline: an
// edit that removes a symbol still called from another file is refused at
// apply time — the same gate the agent CLI uses, now enforced over MCP.
func TestEditCrossFileRegressionRejected(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	before, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	preview, _ := callTool(t, s, "edit_file", map[string]any{
		"path":    "calc.go",
		"content": "package demo\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})
	id := extractPending(t, preview)

	text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id})
	if !isErr {
		t.Fatalf("expected apply to be rejected, got isErr=%v text=%s", isErr, text)
	}
	if !strings.Contains(text, "still called") {
		t.Errorf("rejection reason should mention the surviving caller, got: %s", text)
	}
	after, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(after) != string(before) {
		t.Errorf("rejected apply must restore the file")
	}
}

// TestEditConflictDetected: if the file drifts between preview and apply,
// apply_edit must refuse rather than clobber.
func TestEditConflictDetected(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	preview, _ := callTool(t, s, "edit_file", map[string]any{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})
	id := extractPending(t, preview)

	// Drift the file behind the server's back.
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte("// touched elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id})
	if !isErr || !strings.Contains(text, "conflict") {
		t.Errorf("expected a conflict error, got isErr=%v text=%s", isErr, text)
	}
}

func TestUnknownTool(t *testing.T) {
	s := newServer(t, t.TempDir())
	_, isErr := callTool(t, s, "nope", map[string]any{})
	if !isErr {
		t.Error("unknown tool should return isError")
	}
}

// TestGitDiffAndRollback proves the governance tools work over MCP when git
// is available: an applied edit shows up in git_diff, and rollback reverts
// the last snapshot.
func TestGitDiffAndRollback(t *testing.T) {
	root := t.TempDir()
	fixture(t, root)
	s := newServer(t, root)

	preview, _ := callTool(t, s, "edit_file", map[string]any{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})
	id := extractPending(t, preview)
	if text, isErr := callTool(t, s, "apply_edit", map[string]string{"id": id}); isErr {
		t.Fatalf("apply_edit failed: %s", text)
	}

	text, isErr := callTool(t, s, "git_diff", map[string]any{})
	if isErr || !strings.Contains(text, "return a + b") {
		t.Errorf("git_diff should show the applied edit, isErr=%v text=%s", isErr, text)
	}

	text, isErr = callTool(t, s, "rollback", map[string]any{})
	if isErr {
		t.Fatalf("rollback failed: %s", text)
	}
	b, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if !strings.Contains(string(b), "return a - b") {
		t.Errorf("rollback should restore the buggy Add, got:\n%s", b)
	}
}

// extractPending pulls the "pending" id out of an edit_file result text.
func extractPending(t *testing.T, text string) string {
	t.Helper()
	var v struct {
		Pending string `json:"pending"`
	}
	if err := json.Unmarshal([]byte(text), &v); err != nil || v.Pending == "" {
		t.Fatalf("cannot extract pending id from:\n%s", text)
	}
	return v.Pending
}
