package codeindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixture writes a small multi-file Go project to a temp dir and
// returns its root.
func buildFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
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
`)
	write("math/helper.go", `package math

// Square returns x squared.
func Square(x int) int {
	return x * x
}
`)
	write("app/main.go", `package main

import "demo"
import "demo/math"

func main() {
	demo.Add(1, 2)
	math.Square(3)
}

// runPipeline calls Add and Square.
func runPipeline(a, b int) int {
	x := demo.Add(a, b)
	return math.Square(x)
}
`)
	return root
}

func TestBuildSymbols(t *testing.T) {
	root := buildFixture(t)
	b := NewBuilder(root)
	n, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected files to be parsed")
	}
	ix := b.Index()

	// Add and Mul in demo.
	if got := len(ix.ByName("Add")); got == 0 {
		t.Error("expected Add to be indexed")
	}
	// Square in demo/math.
	if got := len(ix.ByName("Square")); got == 0 {
		t.Error("expected Square to be indexed")
	}
	// main + runPipeline in main package.
	if got := len(ix.ByName("runPipeline")); got == 0 {
		t.Error("expected runPipeline to be indexed")
	}
}

func TestCallGraph(t *testing.T) {
	root := buildFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	ix := b.Index()

	// runPipeline (in package main) calls Add and Square. Same-file/package
	// resolution: runPipeline's own package is "main"; the calls are
	// "main demo.Add" and "main math.Square". M3's resolveCallee matches
	// by name across the index, so both should be edges.
	runID := "main runPipeline"
	callees := ix.Callees(runID)
	found := map[string]bool{}
	for _, c := range callees {
		found[c] = true
	}
	if !found["demo Add"] {
		t.Errorf("runPipeline should call Add, callees=%v", callees)
	}
	if !found["math Square"] {
		t.Errorf("runPipeline should call Square, callees=%v", callees)
	}
}

func TestIncrementalBuild(t *testing.T) {
	root := buildFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}

	// Second build with no changes: 0 files re-parsed.
	n, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 files re-parsed on no change, got %d", n)
	}

	// Change one file: exactly 1 file re-parsed.
	calc := filepath.Join(root, "calc.go")
	if err := os.WriteFile(calc, []byte(`package demo

// Add returns a+b.
func Add(a, b int) int {
	return a + b
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err = b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 file re-parsed after edit, got %d", n)
	}
	// Mul should have been removed by the edit.
	if got := len(b.Index().ByName("Mul")); got != 0 {
		t.Error("Mul should no longer be indexed after its removal from calc.go")
	}
}

func TestRetrieveSymbolOnly(t *testing.T) {
	root := buildFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(b.Index(), root) // symbol-only mode
	results := r.Retrieve(context.Background(), "Add sum two numbers", 5)

	if len(results) == 0 {
		t.Fatal("expected at least one result for 'Add'")
	}
	// The precise layer should surface Add.
	foundAdd := false
	for _, res := range results {
		if res.Symbol.ID() == "demo Add" {
			foundAdd = true
		}
	}
	if !foundAdd {
		t.Errorf("expected demo Add in results, got %+v", results)
	}
}

// methodFixture writes a project with a method (*Cache).Get, a plain func
// Get, and a caller use() that invokes c.Get() — the ambiguity the P17
// receiver-aware retrieval resolves. The method is declared before the plain
// func so bare-name call resolution is deterministic.
func methodFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

// Get returns a plain value.
func Get() int { return 0 }
`)
	write("user.go", `package demo

func use() int {
	c := &Cache{}
	return c.Get()
}
`)
	return root
}

// TestMethodSymbolSingleQualifiedID: a method's ID is "(*Cache).Get" — exactly
// one receiver rendering. §6#24③ recorded the double-qualified "(*Cache).
// (*Cache).Get" from qualify() prepending to an already-qualified Name; P17
// stores the bare name so the ID is clean.
func TestMethodSymbolSingleQualifiedID(t *testing.T) {
	root := methodFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	ix := b.Index()
	var m *Symbol
	for _, s := range ix.Symbols() {
		if s.Name == "Get" && s.Recv == "*Cache" {
			m = s
		}
	}
	if m == nil {
		t.Fatal("expected (*Cache).Get to be indexed")
	}
	if want := "demo (*Cache).Get"; m.ID() != want {
		t.Errorf("method ID = %q, want %q (no double qualification)", m.ID(), want)
	}
}

// TestMethodByNameAndByQualified: methods are reachable by bare name AND by
// the receiver-qualified form P16's edit 'recv' renders — the exact-layer gap
// in §6#24③ is closed both ways.
func TestMethodByNameAndByQualified(t *testing.T) {
	root := methodFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	ix := b.Index()
	var hasMethod, hasFunc bool
	for _, s := range ix.ByName("Get") {
		if s.Recv == "*Cache" {
			hasMethod = true
		}
		if s.Recv == "" {
			hasFunc = true
		}
	}
	if !hasMethod || !hasFunc {
		t.Errorf(`ByName("Get") should include the method and the plain func, got %+v`, ix.ByName("Get"))
	}
	q := ix.ByQualified("(*Cache).Get")
	if len(q) != 1 || q[0].Recv != "*Cache" {
		t.Errorf(`ByQualified("(*Cache).Get") should return exactly the method, got %+v`, q)
	}
}

// TestRetrieveReceiverQualifiedExact: a receiver-qualified query pins the exact
// method in the precise layer (Reason="exact", O(1)) rather than relying on
// the graph BFS — and the plain func with the same name stays reachable via the
// bare-name token.
func TestRetrieveReceiverQualifiedExact(t *testing.T) {
	root := methodFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(b.Index(), root)
	results := r.Retrieve(context.Background(), "fix (*Cache).Get", 0)
	var methodRes, funcRes *Result
	for i := range results {
		if results[i].Symbol.Name == "Get" && results[i].Symbol.Recv == "*Cache" {
			methodRes = &results[i]
		}
		if results[i].Symbol.Name == "Get" && results[i].Symbol.Recv == "" {
			funcRes = &results[i]
		}
	}
	if methodRes == nil {
		t.Fatalf("expected (*Cache).Get in results, got %+v", results)
	}
	if methodRes.Reason != "exact" {
		t.Errorf("(*Cache).Get should come from the precise layer, reason=%q", methodRes.Reason)
	}
	if funcRes == nil {
		t.Error("the bare-name token should still surface the plain func Get")
	}
}

// TestMethodCallResolvesToMethod: a call c.Get() inside use() edges to a
// method symbol, not a same-named plain func. Before P17, methods were never
// bare-name candidates so the edge fell to the plain func Get.
func TestMethodCallResolvesToMethod(t *testing.T) {
	root := methodFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	ix := b.Index()
	for _, id := range ix.Callees("demo use") {
		if s := ix.symbolByID(id); s != nil && s.Name == "Get" && s.Recv == "*Cache" {
			return // the method edge is present
		}
	}
	t.Errorf("use() should edge to (*Cache).Get, callees=%v", ix.Callees("demo use"))
}

// callFixture writes a project with a plain func Get declared BEFORE the
// (*Cache).Get method, so bare-name resolution (P17-era) would pick the plain
// func by declaration order. P18's type-aware resolution must still land on
// the method — proof the call graph no longer depends on declaration order.
func callFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }
`)
	write("user.go", `package demo

func use() int {
	c := &Cache{}
	return c.Get()
}
`)
	return root
}

func calleeSet(ix *Index, id string) map[string]bool {
	m := map[string]bool{}
	for _, c := range ix.Callees(id) {
		m[c] = true
	}
	return m
}

// TestMethodCallResolvesToMethodOrderIndependent: the plain func Get is
// declared FIRST, so a name-approximation resolve would edge c.Get() to it.
// P18 tracks c := &Cache{} → *Cache, so the call must edge to the method.
func TestMethodCallResolvesToMethodOrderIndependent(t *testing.T) {
	root := callFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	ix := b.Index()
	callees := calleeSet(ix, "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("use() should edge to (*Cache).Get, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get (declaration-order accident), callees=%v", callees)
	}
}

// TestMethodCallValueReceiver: a value-typed var c Cache calls a value-
// receiver method — the (Cache).Get form must resolve exactly, not via name.
func TestMethodCallValueReceiver(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value (value receiver).
func (c Cache) Get() int { return c.n }

func use() int {
	var c Cache
	return c.Get()
}
`)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (Cache).Get"] {
		t.Errorf("value c Cache should edge to (Cache).Get, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallNewPointer: c := new(Cache) derives "*Cache" — the call lands
// on (*Cache).Get even though the plain func is declared first.
func TestMethodCallNewPointer(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

func use() int {
	c := new(Cache)
	return c.Get()
}
`)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("use() should edge to (*Cache).Get via new(Cache), callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallParam: the receiver type comes from a parameter declaration —
// func use(c *Cache) seeds the env before the body is walked.
func TestMethodCallParam(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

func use(c *Cache) int {
	return c.Get()
}
`)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("use() should edge to (*Cache).Get via the *Cache param, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallMethodSetTwin: only (*Cache).Get exists; an addressable value
// c Cache can still call it (Go auto-takes the address). P18 tries the exact
// (Cache).Get form, misses, then the (*Cache).Get twin — and must not fall
// back to the plain func declared first.
func TestMethodCallMethodSetTwin(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value (pointer receiver only).
func (c *Cache) Get() int { return c.n }

func use() int {
	var c Cache
	return c.Get()
}
`)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("value c Cache should edge to (*Cache).Get via the method-set twin, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallClosureScoping: a closure shadows c with a different type; the
// shadow must not leak out — the outer c.Get() still edges to (*Cache).Get and
// the closure's c.Get() edges to (*Other).Get.
func TestMethodCallClosureScoping(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("cache.go", `package demo

type Cache struct{ n int }
type Other struct{ m int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

// Get returns the other value.
func (o *Other) Get() int { return o.m }

func use() int {
	c := &Cache{}
	func() {
		c := &Other{}
		_ = c.Get()
	}()
	return c.Get()
}
`)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("outer c.Get() should edge to (*Cache).Get, callees=%v", callees)
	}
	if !callees["demo (*Other).Get"] {
		t.Errorf("closure c.Get() should edge to (*Other).Get, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// writeFiles writes several files under a fresh temp dir and returns its root.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestMethodCallFunctionReturnValue: c := makeCache() derives *Cache from the
// package-wide signature table (makeCache is declared in cache.go, use() in
// user.go — cross-file), so c.Get() edges to the method even though the plain
// func Get is declared first. P19 turns the P18 fallback fixture positive.
func TestMethodCallFunctionReturnValue(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

func makeCache() *Cache { return nil }
`,
		"user.go": `package demo

func use() int {
	c := makeCache()
	return c.Get()
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("c := makeCache() should pin *Cache → edge to (*Cache).Get, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallFunctionReturnMulti: a multi-return call feeds positional
// types — c, err := makeCache() pins c to the first return, so c.Get() still
// resolves precisely.
func TestMethodCallFunctionReturnMulti(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

func Get() int { return 0 }

func (c *Cache) Get() int { return c.n }

func makeCache() (*Cache, error) { return &Cache{}, nil }
`,
		"user.go": `package demo

func use() (int, error) {
	c, err := makeCache()
	return c.Get(), err
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("c.Get() should edge to (*Cache).Get via the multi-return binding, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallFunctionReturnUnknownFallsBack: a call whose signature is NOT
// in the package table (here: a local function variable) stays underivable, so
// c.Get() degrades to bare-name resolution — the plain func declared first
// wins. No crash, no false method edge. (P19 only adds precision it can prove.)
func TestMethodCallFunctionReturnUnknownFallsBack(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }
`,
		"user.go": `package demo

func use() int {
	mk := func() *Cache { return nil }
	c := mk()
	return c.Get()
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo Get"] {
		t.Errorf("unknown c.Get() should fall back to the plain func, callees=%v", callees)
	}
}

// TestMethodCallStructFieldChain: p.c.Get() resolves the chain — p is *Config
// (param seed), field c is *Cache (struct table) → the call edges to the
// method on *Cache, not the plain func.
func TestMethodCallStructFieldChain(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

type Config struct{ c *Cache }
`,
		"user.go": `package demo

func use(p *Config) int {
	return p.c.Get()
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("p.c.Get() should edge to (*Cache).Get, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallStructFieldChainValueField: the field is a value Cache, so the
// chain derives "(Cache).Get" and the method-set twin lands on the pointer
// receiver (*Cache).Get (p.c is addressable through the *Config param).
func TestMethodCallStructFieldChainValueField(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value (pointer receiver).
func (c *Cache) Get() int { return c.n }

type Config struct{ c Cache }
`,
		"user.go": `package demo

func use(p *Config) int {
	return p.c.Get()
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("p.c.Get() (value field, pointer receiver) should edge via the method-set twin, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallStructFieldChainDeep: multi-level chains resolve too —
// a.cfg.c.Get() follows App.cfg (*Config) then Config.c (*Cache).
func TestMethodCallStructFieldChainDeep(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

type Config struct{ c *Cache }
type App struct{ cfg *Config }
`,
		"user.go": `package demo

func use(a *App) int {
	return a.cfg.c.Get()
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	callees := calleeSet(b.Index(), "demo use")
	if !callees["demo (*Cache).Get"] {
		t.Errorf("a.cfg.c.Get() should edge to (*Cache).Get, callees=%v", callees)
	}
	if callees["demo Get"] {
		t.Errorf("use() must NOT edge to the plain func Get, callees=%v", callees)
	}
}

// TestMethodCallStructFieldChainUnknown: when a chain link can't be pinned
// (here: Config has no field c), NO edge is emitted — a guessed name match
// would be worse than none for edit-time validation.
func TestMethodCallStructFieldChainUnknown(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"go.mod": "module demo\n\ngo 1.25\n",
		"cache.go": `package demo

type Cache struct{ n int }

// Get returns a plain value.
func Get() int { return 0 }

// Get returns the cached value.
func (c *Cache) Get() int { return c.n }

type Config struct{ n int }
`,
		"user.go": `package demo

func use(q *Config) int {
	return q.c.Get()
}
`,
	})
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	if callees := calleeSet(b.Index(), "demo use"); len(callees) != 0 {
		t.Errorf("unknown chain q.c.Get() should emit no edge (guessing is worse than none), callees=%v", callees)
	}
}

func TestRenderBudget(t *testing.T) {
	root := buildFixture(t)
	b := NewBuilder(root)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	r := NewRetriever(b.Index(), root)
	query := "Add Mul Square runPipeline"

	text := r.Render(context.Background(), query, 10, 4000)
	if text == "" {
		t.Fatal("expected rendered context")
	}
	if !strings.Contains(text, "func Add") {
		t.Errorf("rendered context should contain Add signature, got:\n%s", text)
	}
	// A generous budget renders every symbol; a tight budget stops early.
	big := r.Render(context.Background(), query, 10, 4000)
	small := r.Render(context.Background(), query, 10, 100)
	if len(small) >= len(big) {
		t.Errorf("small budget should trim context, small=%d big=%d", len(small), len(big))
	}
	// Small budget must still include at least one symbol (never empty).
	if !strings.Contains(small, "──") {
		t.Errorf("small budget should still include one symbol block, got:\n%s", small)
	}
}
