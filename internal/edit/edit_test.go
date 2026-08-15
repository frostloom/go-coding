package edit

import (
	"strings"
	"testing"
)

func TestApplyText(t *testing.T) {
	src := []byte("func Add(a, b int) int {\n\treturn a - b\n}\n")
	out, err := ApplyText(src, "a - b", "a + b")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "return a + b") {
		t.Errorf("replacement not applied: %q", got)
	}
	if strings.Contains(got, "a - b") {
		t.Errorf("original still present: %q", got)
	}
	// Rest of the file must be untouched — nothing but the target text moved.
	if !strings.HasPrefix(got, "func Add(a, b int) int {") {
		t.Errorf("edit drifted outside the target: %q", got)
	}
}

func TestApplyTextNotFound(t *testing.T) {
	if _, err := ApplyText([]byte("package x\n"), "func Add", "func Sum"); err == nil {
		t.Error("expected an error when 'old' is not found")
	}
}

func TestApplyTextAmbiguous(t *testing.T) {
	src := []byte("a - b\n// a - b\n")
	_, err := ApplyText(src, "a - b", "a + b")
	if err == nil {
		t.Fatal("expected an error when 'old' matches multiple locations")
	}
	if !strings.Contains(err.Error(), "multiple") {
		t.Errorf("error should mention ambiguity, got: %v", err)
	}
}

// --- AST-level edit (ApplySymbol) ---

const symbolSrc = `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b
}

// Cache keeps items.
type Cache struct {
	capacity int
}

const MaxItems = 10
`

func TestApplySymbolReplacesFuncDecl(t *testing.T) {
	out, err := ApplySymbol([]byte(symbolSrc), "Add", "", "func Add(a, b int) int {\n\treturn a + b\n}")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "return a + b") {
		t.Errorf("new body missing: %q", got)
	}
	if strings.Contains(got, "return a - b") {
		t.Errorf("old body still present: %q", got)
	}
	// Only the declaration's byte range moved: the doc comment and the rest of
	// the file are untouched.
	if !strings.Contains(got, "// Add returns the sum of a and b.") {
		t.Errorf("doc comment was dropped: %q", got)
	}
	if !strings.Contains(got, "type Cache struct") || !strings.Contains(got, "const MaxItems = 10") {
		t.Errorf("unrelated declarations drifted: %q", got)
	}
}

func TestApplySymbolReplacesTypeDecl(t *testing.T) {
	out, err := ApplySymbol([]byte(symbolSrc), "Cache", "", "type Cache struct {\n\tmu sync.Mutex\n}")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "mu sync.Mutex") {
		t.Errorf("new type body missing: %q", got)
	}
	if strings.Contains(got, "capacity int") {
		t.Errorf("old type body still present: %q", got)
	}
	// The function survived untouched.
	if !strings.Contains(got, "func Add(a, b int) int") {
		t.Errorf("unrelated function drifted: %q", got)
	}
}

func TestApplySymbolReplacesMethod(t *testing.T) {
	src := "package c\n\nfunc (c *Cache) Get(k string) (int, bool) {\n\treturn 0, false\n}\n"
	out, err := ApplySymbol([]byte(src), "Get", "", "func (c *Cache) Get(k string) (int, bool) {\n\treturn c.items[k], true\n}")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "return c.items[k], true") {
		t.Errorf("method body not replaced: %q", got)
	}
	if strings.Contains(got, "return 0, false") {
		t.Errorf("old method body still present: %q", got)
	}
}

func TestApplySymbolNotFound(t *testing.T) {
	if _, err := ApplySymbol([]byte(symbolSrc), "Missing", "", "func Missing() {}"); err == nil {
		t.Error("expected an error for an unknown symbol")
	}
}

func TestApplySymbolAmbiguous(t *testing.T) {
	src := "package c\n\nfunc (a *A) Get() int { return 1 }\nfunc (b *B) Get() int { return 2 }\n"
	_, err := ApplySymbol([]byte(src), "Get", "", "func (a *A) Get() int { return 3 }")
	if err == nil {
		t.Fatal("expected an error when the name matches multiple methods")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity, got: %v", err)
	}
	// P15 deleted the false promise; P16 delivers the real capability, so the
	// error now points at the actual 'recv' parameter that disambiguates.
	if !strings.Contains(err.Error(), "'recv'") {
		t.Errorf("error should point at the 'recv' field: %v", err)
	}
}

func TestApplySymbolRefusesGroupedDecl(t *testing.T) {
	src := "package c\n\nvar (\n\tA = 1\n\tB = 2\n)\n"
	if _, err := ApplySymbol([]byte(src), "A", "", "var A = 9"); err == nil {
		t.Error("expected a grouped declaration to be refused (no safe splice)")
	}
}

// --- receiver disambiguation (recv) ---

func TestApplySymbolRecvTargetsMethod(t *testing.T) {
	// Two methods share the name Get; 'recv' picks which declaration to replace.
	src := "package c\n\nfunc (a *A) Get() int { return 1 }\nfunc (b *B) Get() int { return 2 }\n"
	out, err := ApplySymbol([]byte(src), "Get", "*B", "func (b *B) Get() int { return 3 }")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "return 3") {
		t.Errorf("new *B body missing: %q", got)
	}
	if strings.Contains(got, "return 2") {
		t.Errorf("old *B body still present: %q", got)
	}
	if !strings.Contains(got, "return 1") {
		t.Errorf("*A method drifted: %q", got)
	}
}

func TestApplySymbolRecvSkipsPlainFunc(t *testing.T) {
	// A package-level func Get and a method (*B).Get coexist (different
	// namespaces); 'recv' targets the method and leaves the function untouched.
	src := "package c\n\nfunc Get() int { return 0 }\nfunc (b *B) Get() int { return 2 }\n"
	out, err := ApplySymbol([]byte(src), "Get", "*B", "func (b *B) Get() int { return 3 }")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "func Get() int { return 0 }") {
		t.Errorf("plain function drifted: %q", got)
	}
	if !strings.Contains(got, "return 3") || strings.Contains(got, "return 2") {
		t.Errorf("method not replaced in place: %q", got)
	}
}

func TestApplySymbolRecvNotFound(t *testing.T) {
	src := "package c\n\nfunc (a *A) Get() int { return 1 }\n"
	_, err := ApplySymbol([]byte(src), "Get", "*Missing", "func (x *Missing) Get() int { return 3 }")
	if err == nil || !strings.Contains(err.Error(), "receiver") {
		t.Errorf("expected not-found error naming the receiver, got: %v", err)
	}
}

// --- body-local edit (ApplyLocal) ---

const localSrc = `package demo

var total = 100 // package-level: must never be touched by a body-local edit

// Compute sums xs and multiplies by scale.
func Compute(xs []int, scale int) int {
	total := 0
	for i := 0; i < len(xs); i++ {
		total += xs[i]
	}
	return total * scale
}
`

func TestApplyLocalReplacesShortVarDecl(t *testing.T) {
	// The local `total` (not the package-level `total`) is spliced; the rest of
	// the file — including the package var and the loop — stays byte-identical.
	out, err := ApplyLocal([]byte(localSrc), "Compute", "", "total", "total := 1")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "total := 1") {
		t.Errorf("replacement missing: %q", got)
	}
	if strings.Contains(got, "total := 0") {
		t.Errorf("old declaration still present: %q", got)
	}
	if !strings.Contains(got, "var total = 100") {
		t.Errorf("package-level var drifted: %q", got)
	}
	if !strings.Contains(got, "for i := 0; i < len(xs); i++") {
		t.Errorf("loop drifted: %q", got)
	}
}

func TestApplyLocalReplacesVarStmt(t *testing.T) {
	src := "package c\n\nfunc f() {\n\tvar x int\n\treturn x\n}\n"
	out, err := ApplyLocal([]byte(src), "f", "", "x", "var x = 42")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "var x = 42") || strings.Contains(got, "var x int") {
		t.Errorf("var statement not replaced: %q", got)
	}
}

func TestApplyLocalReplacesForInit(t *testing.T) {
	// The splice unit is the for-init: only "i := 0" moves, the loop skeleton stays.
	src := "package c\n\nfunc f(n int) int {\n\tfor i := 0; i < n; i++ {\n\t}\n\treturn n\n}\n"
	out, err := ApplyLocal([]byte(src), "f", "", "i", "i := 1")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "for i := 1; i < n; i++") {
		t.Errorf("for-init not replaced in place: %q", got)
	}
}

func TestApplyLocalReplacesIfInit(t *testing.T) {
	src := "package c\n\nfunc f() {\n\tif x := compute(); x > 0 {\n\t\tuse(x)\n\t}\n}\n"
	out, err := ApplyLocal([]byte(src), "f", "", "x", "x := compute() + 1")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "if x := compute() + 1; x > 0") {
		t.Errorf("if-init not replaced in place: %q", got)
	}
}

func TestApplyLocalReplacesLocalType(t *testing.T) {
	src := "package c\n\nfunc f() {\n\ttype pair struct{ a, b int }\n\tvar p pair\n\t_ = p\n}\n"
	out, err := ApplyLocal([]byte(src), "f", "", "pair", "type pair struct{ a, b, c int }")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "type pair struct{ a, b, c int }") || strings.Contains(got, "a, b int }") {
		t.Errorf("local type not replaced: %q", got)
	}
}

func TestApplyLocalNotFound(t *testing.T) {
	_, err := ApplyLocal([]byte(localSrc), "Compute", "", "missing", "missing := 1")
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Errorf("expected 'not declared' error, got: %v", err)
	}
}

func TestApplyLocalUnknownFunction(t *testing.T) {
	_, err := ApplyLocal([]byte(localSrc), "NoSuchFunc", "", "total", "total := 1")
	if err == nil || !strings.Contains(err.Error(), "NoSuchFunc") {
		t.Errorf("expected unknown-function error, got: %v", err)
	}
}

func TestApplyLocalRefusesShadowed(t *testing.T) {
	// `total` is declared twice inside Compute (outer + nested block): ambiguous.
	src := "package c\n\nfunc Compute() {\n\ttotal := 0\n\tif true {\n\t\ttotal := 9\n\t\t_ = total\n\t}\n}\n"
	_, err := ApplyLocal([]byte(src), "Compute", "", "total", "total := 1")
	if err == nil || !strings.Contains(err.Error(), "2 sites") {
		t.Errorf("expected ambiguity error, got: %v", err)
	}
}

func TestApplyLocalRefusesMultiNameDecl(t *testing.T) {
	// `x, y := ...` declares two names; splicing would leave `y` dangling.
	src := "package c\n\nfunc f() {\n\tx, y := 1, 2\n\t_ = y\n}\n"
	_, err := ApplyLocal([]byte(src), "f", "", "x", "x := 3")
	if err == nil || !strings.Contains(err.Error(), "multi-name") {
		t.Errorf("expected multi-name refusal, got: %v", err)
	}
}

func TestApplyLocalRefusesSinglePlusMultiName(t *testing.T) {
	// x is declared at TWO sites: a single-name site AND a multi-name
	// redeclaration. The "declared at more than one site → refused" contract must
	// hold even though one of the sites is spliceable — previously the single
	// site won and got spliced (multi was only consulted when len(spans)==0).
	src := "package c\n\nfunc f() {\n\tx := 1\n\tx, y := 2, 3\n\t_ = y\n}\n"
	_, err := ApplyLocal([]byte(src), "f", "", "x", "x := 9")
	if err == nil || !strings.Contains(err.Error(), "2 sites") {
		t.Errorf("expected '2 sites' refusal, got: %v", err)
	}
}

func TestApplyLocalRefusesGroupedDecl(t *testing.T) {
	// A name inside "var ( a; b )" cannot be spliced alone without leaving the
	// sibling dangling. It IS declared, so the message must say so (grouped),
	// not fall through to "not declared".
	src := "package c\n\nfunc f() {\n\tvar (\n\t\ta = 1\n\t\tb = 2\n\t)\n\t_ = b\n}\n"
	_, err := ApplyLocal([]byte(src), "f", "", "a", "a = 9")
	if err == nil || !strings.Contains(err.Error(), "grouped") {
		t.Errorf("expected grouped-declaration refusal, got: %v", err)
	}
	if strings.Contains(err.Error(), "not declared") {
		t.Errorf("grouped declaration misreported as not declared: %v", err)
	}
}

func TestApplyLocalRefusesGroupedPlusSingle(t *testing.T) {
	// a is declared in a grouped decl AND inside a multi-name statement: two
	// sites total. The grouped flag alone must not let a splice through.
	src := "package c\n\nfunc f() {\n\tvar (\n\t\ta = 1\n\t\tb = 2\n\t)\n\ta, c := 3, 4\n\t_ = b\n\t_ = c\n}\n"
	_, err := ApplyLocal([]byte(src), "f", "", "a", "a := 9")
	if err == nil || !strings.Contains(err.Error(), "2 sites") {
		t.Errorf("expected '2 sites' refusal, got: %v", err)
	}
}

func TestApplyLocalReplacesRangeVar(t *testing.T) {
	// The splice unit is the whole range loop — `for i := range xs` declares i,
	// so replacing it swaps the loop while the rest of the file stays intact.
	src := "package c\n\nfunc f(xs []int) int {\n\ttotal := 0\n\tfor i := range xs {\n\t\ttotal += i\n\t}\n\treturn total\n}\n"
	out, err := ApplyLocal([]byte(src), "f", "", "i", "for i := 1; i < len(xs); i++")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "for i := 1; i < len(xs); i++") || strings.Contains(got, "for i := range xs") {
		t.Errorf("range loop not replaced in place: %q", got)
	}
}

func TestApplyLocalReplacesRangeBlankIndex(t *testing.T) {
	// `for _, v := range m` declares one real variable (v) — the blank index is
	// not a name, so the loop stays a single-name spliceable site.
	src := "package c\n\nfunc f(m map[string]int) int {\n\tsum := 0\n\tfor _, v := range m {\n\t\tsum += v\n\t}\n\treturn sum\n}\n"
	out, err := ApplyLocal([]byte(src), "f", "", "v", "for _, v := range m {\n\t\tsum += v * 2\n\t}")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "sum += v * 2") {
		t.Errorf("range blank-index loop not replaced: %q", got)
	}
	if strings.Contains(got, "sum += v\n") {
		t.Errorf("old range body still present: %q", got)
	}
}

func TestApplyLocalRefusesRangeMultiVar(t *testing.T) {
	// `for k, v := range m` declares two names; splicing would leave one dangling.
	src := "package c\n\nfunc f(m map[string]int) {\n\tfor k, v := range m {\n\t\t_ = k\n\t\t_ = v\n\t}\n}\n"
	_, err := ApplyLocal([]byte(src), "f", "", "v", "for _, v := range m { _ = v }")
	if err == nil || !strings.Contains(err.Error(), "multi-name") {
		t.Errorf("expected multi-name refusal for range vars, got: %v", err)
	}
}

func TestApplyLocalRecvTargetsMethod(t *testing.T) {
	// Two methods Get on different receivers each declare a local 'x'; 'recv'
	// picks which function body to edit.
	src := "package c\n\nfunc (a *A) Get() int {\n\tx := 1\n\treturn x\n}\nfunc (b *B) Get() int {\n\tx := 2\n\treturn x\n}\n"
	out, err := ApplyLocal([]byte(src), "Get", "*B", "x", "x := 9")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "x := 9") {
		t.Errorf("method body not edited: %q", got)
	}
	if strings.Contains(got, "x := 2") {
		t.Errorf("old *B body still present: %q", got)
	}
	if !strings.Contains(got, "x := 1") {
		t.Errorf("*A method body drifted: %q", got)
	}
}

func TestApplyLocalRecvNotFound(t *testing.T) {
	src := "package c\n\nfunc (a *A) Get() int {\n\tx := 1\n\treturn x\n}\n"
	_, err := ApplyLocal([]byte(src), "Get", "*Missing", "x", "x := 9")
	if err == nil || !strings.Contains(err.Error(), "receiver") {
		t.Errorf("expected not-found error naming the receiver, got: %v", err)
	}
}

func TestApplyLocalRefusesAmbiguousFnWithoutRecv(t *testing.T) {
	// Two methods share the name Get; without 'recv' the function name alone is
	// ambiguous and the edit is refused instead of guessing a body.
	src := "package c\n\nfunc (a *A) Get() int {\n\tx := 1\n\treturn x\n}\nfunc (b *B) Get() int {\n\tx := 2\n\treturn x\n}\n"
	_, err := ApplyLocal([]byte(src), "Get", "", "x", "x := 9")
	if err == nil || !strings.Contains(err.Error(), "2 declarations") {
		t.Errorf("expected ambiguity refusal naming the declaration count, got: %v", err)
	}
}

func TestCheckSyntax(t *testing.T) {
	if err := CheckSyntax("x.go", []byte("package x\nfunc f() {}\n")); err != nil {
		t.Errorf("valid file rejected: %v", err)
	}
	if err := CheckSyntax("x.go", []byte("package x\nfunc f( {")); err == nil {
		t.Error("broken file accepted")
	}
}

func TestCheckFormat(t *testing.T) {
	if !CheckFormat([]byte("package x\n\nfunc f() {\n\tx := 1\n}\n")) {
		t.Error("gofmt-clean file reported dirty")
	}
	if CheckFormat([]byte("package x\nfunc f() {\nx:=1\n}\n")) {
		t.Error("non-gofmt file reported clean")
	}
}
