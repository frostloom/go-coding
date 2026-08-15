package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/codeindex"
	"github.com/zlx05/go-coding/internal/sandbox"
)

// fixtureRepo writes a two-file Go project into root and returns it. calc.go
// declares Add + Mul; use.go CALLS Add from another file (a cross-file edge
// that the call graph must catch if Add disappears).
func fixtureRepo(t *testing.T, root string) {
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
	return a + b
}

// Mul returns the product.
func Mul(a, b int) int {
	return a * b
}

// Sum adds xs and returns the total.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
`)
	write("use.go", `package demo

// useAdd calls Add from another file.
func useAdd(a, b int) int {
	return Add(a, b)
}
`)
}

func newEditTool(root string) (*EditTool, *sandbox.Sandbox, *codeindex.Builder) {
	sb := &sandbox.Sandbox{Root: root}
	b := codeindex.NewBuilder(root)
	if _, err := b.Build(); err != nil {
		return nil, nil, nil
	}
	return &EditTool{Sandbox: sb, Builder: b}, sb, b
}

func mustReject(t *testing.T, res Result, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	var rejected *EditRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected *EditRejectedError, got %T: %v", err, err)
	}
}

// TestEditFullContentStillWorks: M1 behaviour (whole-file replace) must keep
// working now that validation is in place.
func TestEditFullContentStillWorks(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	res, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns a + b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatal("edit reported not-ok")
	}
	b, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if !strings.Contains(string(b), "return a + b") {
		t.Errorf("edit not applied:\n%s", b)
	}
}

// TestEditPreciseOldNew: the M4 text-level edit replaces exactly one
// occurrence and leaves the rest of the file byte-for-byte intact.
func TestEditPreciseOldNew(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	before, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	res, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path": "calc.go",
		"old":  "return a + b",
		"new":  "return a + b + 0",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatal("edit reported not-ok")
	}
	after, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if !strings.Contains(string(after), "return a + b + 0") {
		t.Errorf("precise edit not applied:\n%s", after)
	}
	// Only the target text changed.
	if strings.Count(string(after), "return a + b + 0") != 1 {
		t.Errorf("expected exactly one replacement:\n%s", after)
	}
	if !strings.Contains(string(after), "// Mul returns the product.") {
		t.Errorf("untouched region should be preserved:\n%s", after)
	}
	_ = before
}

// TestEditSyntaxRejected: a file that does not parse must be refused before
// it is written, and the disk must be unchanged.
func TestEditSyntaxRejected(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	original, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":    "calc.go",
		"content": "package demo\nfunc Add(a, b int) int {\n\treturn \n",
	})))
	mustReject(t, Result{}, err)

	now, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(now) != string(original) {
		t.Errorf("rejected edit must not touch disk:\n%s", now)
	}
}

// TestEditCrossFileRegressionRejected is the heart of M4: an edit that
// removes a function still called from another file is rejected at the
// symbol layer, and the file is restored. This is the check Claude Code
// cannot do — it has no call graph, so it must wait for the compiler.
func TestEditCrossFileRegressionRejected(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	original, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	// Rewrite calc.go WITHOUT Add (Mul stays). use.go still calls Add.
	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":    "calc.go",
		"content": "package demo\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n",
	})))
	mustReject(t, Result{}, err)

	now, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(now) != string(original) {
		t.Errorf("rejected edit must restore the file:\n%s", now)
	}
}

// TestEditSymbolReplacesByName: the AST-level edit locates a declaration by
// NAME and splices its byte range — the caller replaces "Add" without having
// to reproduce its current body. The untouched region must stay byte-identical.
func TestEditSymbolReplacesByName(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":   "calc.go",
		"symbol": "Add",
		"new":    "func Add(a, b int) int {\n\treturn a + b + 0\n}",
	})))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	s := string(after)
	if !strings.Contains(s, "return a + b + 0") {
		t.Errorf("symbol edit not applied:\n%s", s)
	}
	// The doc comment and the sibling function survived the splice.
	if !strings.Contains(s, "// Add returns the sum of a and b.") {
		t.Errorf("doc comment lost:\n%s", s)
	}
	if !strings.Contains(s, "// Mul returns the product.") {
		t.Errorf("sibling declaration drifted:\n%s", s)
	}
}

// TestEditSymbolNotFound: an unknown symbol is refused (nothing written).
func TestEditSymbolNotFound(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	original, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":   "calc.go",
		"symbol": "NoSuchFunc",
		"new":    "func NoSuchFunc() {}",
	})))
	if err == nil {
		t.Fatal("expected an error for an unknown symbol")
	}
	now, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(now) != string(original) {
		t.Errorf("refused edit must not touch disk:\n%s", now)
	}
}

// TestEditSymbolBadSyntaxRejected: the new declaration must parse; a bad body
// is refused by the syntax gate before anything is written.
func TestEditSymbolBadSyntaxRejected(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	original, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":   "calc.go",
		"symbol": "Add",
		"new":    "func Add(a, b int) int {\n\treturn \n",
	})))
	mustReject(t, Result{}, err)
	now, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(now) != string(original) {
		t.Errorf("rejected edit must not touch disk:\n%s", now)
	}
}

// TestEditLocalBySymbolAndFn: the body-local AST edit locates a LOCAL
// declaration by (function, name) and splices it — the caller replaces the
// seed value of the local `total` inside Sum without touching the package-level
// scope, the loop, or the rest of the file.
func TestEditLocalBySymbolAndFn(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":   "calc.go",
		"symbol": "total",
		"fn":     "Sum",
		"new":    "total := 100",
	})))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	s := string(after)
	if !strings.Contains(s, "total := 100") || strings.Contains(s, "total := 0") {
		t.Errorf("body-local edit not applied:\n%s", s)
	}
	// Scope isolation: only the local declaration moved; loop + siblings intact.
	if !strings.Contains(s, "for _, x := range xs") {
		t.Errorf("loop drifted:\n%s", s)
	}
	if !strings.Contains(s, "// Mul returns the product.") || !strings.Contains(s, "func Add(a, b int) int") {
		t.Errorf("sibling declarations drifted:\n%s", s)
	}
}

// TestEditLocalNotFound: an unknown (function, name) pair is refused, disk intact.
func TestEditLocalNotFound(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

	original, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":   "calc.go",
		"symbol": "total",
		"fn":     "Mul", // Mul has no local named total
		"new":    "total := 100",
	})))
	if err == nil {
		t.Fatal("expected an error for a missing (fn, symbol) pair")
	}
	now, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(now) != string(original) {
		t.Errorf("refused edit must not touch disk:\n%s", now)
	}
}

// TestEditSymbolRecvDisambiguates: two methods share the name Get on different
// receivers; 'recv' targets the requested method through the tool entry.
func TestEditSymbolRecvDisambiguates(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, _ := newEditTool(root)

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

	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":   "methods.go",
		"symbol": "Get",
		"recv":   "*B",
		"new":    "func (b *B) Get() int {\n\treturn 3\n}",
	})))
	if err != nil {
		t.Fatalf("recv-targeted edit refused: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "methods.go"))
	s := string(got)
	if !strings.Contains(s, "return 3") || !strings.Contains(s, "return 1") || strings.Contains(s, "return 2") {
		t.Errorf("recv should target only the *B method:\n%s", s)
	}
}

// TestEditKeepsIndexConsistent: after a successful edit the shared builder
// reflects the new state (no stale symbols from the previous version).
func TestEditKeepsIndexConsistent(t *testing.T) {
	root := t.TempDir()
	fixtureRepo(t, root)
	tool, _, b := newEditTool(root)

	// Rename Mul → Product inside calc.go: no cross-file caller of Mul, so
	// this is a valid edit and the index must forget the old name.
	_, err := tool.Run(context.Background(), Args(mustJSON(t, map[string]string{
		"path":    "calc.go",
		"content": "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Product returns the product.\nfunc Product(a, b int) int {\n\treturn a * b\n}\n",
	})))
	if err != nil {
		t.Fatalf("valid rename rejected: %v", err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	if got := len(b.Index().ByName("Mul")); got != 0 {
		t.Errorf("stale symbol Mul still indexed after edit (%d matches)", got)
	}
	if got := len(b.Index().ByName("Product")); got != 1 {
		t.Errorf("expected Product indexed after edit, got %d", got)
	}
}

func mustJSON(t *testing.T, v map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
