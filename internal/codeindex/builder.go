package codeindex

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Builder incrementally indexes a Go project. It tracks per-file content
// hashes so a rebuild only re-parses files that actually changed.
type Builder struct {
	Root string

	fset *token.FileSet
	// fileHash remembers the last seen content hash of each file.
	fileHash map[string]string
	// index holds the current symbol index.
	index *Index
	// per-file parse results, kept so a changed file can be spliced in.
	fileSyms  map[string][]*Symbol
	fileEdges map[string][]edge
}

// NewBuilder creates a builder rooted at root (an existing directory).
func NewBuilder(root string) *Builder {
	return &Builder{
		Root:      root,
		fset:      token.NewFileSet(),
		fileHash:  make(map[string]string),
		index:     NewIndex(),
		fileSyms:  make(map[string][]*Symbol),
		fileEdges: make(map[string][]edge),
	}
}

// Index returns the current index (valid after at least one Build).
func (b *Builder) Index() *Index { return b.index }

// Build performs a full rebuild, or an incremental one if called again with
// unchanged files. It returns the number of files re-parsed (0 = no change).
func (b *Builder) Build() (int, error) {
	files, err := goFiles(b.Root)
	if err != nil {
		return 0, err
	}
	var changed []string
	for _, f := range files {
		h, err := hashFile(f)
		if err != nil {
			return 0, err
		}
		if b.fileHash[f] != h {
			changed = append(changed, f)
		}
	}
	if len(changed) == 0 {
		return 0, nil // incremental: nothing changed
	}

	// First build (or many files changed): rebuild from scratch. Cheap at
	// fixture scale; correctness first, splice optimization later.
	if b.index.allCount() == 0 || len(changed) > 2 {
		b.index = NewIndex()
		b.fset = token.NewFileSet()
		b.fileSyms = make(map[string][]*Symbol)
		b.fileEdges = make(map[string][]edge)
		changed = files
	}

	// Two passes so cross-file call edges resolve correctly: all symbols must
	// be in the index before any edge is resolved (Go file order is not
	// guaranteed to put a caller after its callee). P19 also aggregates the
	// package-wide type facts (function returns, struct fields) between the two
	// passes, so a call in one file can be pinned by a signature in another.
	type parsedFile struct {
		rel   string
		file  *ast.File
		facts *pkgFacts
	}
	var parsed []parsedFile
	// Pass 1: parse files and index symbols.
	for _, f := range changed {
		rel, file, syms, facts, err := b.parseFileSymbols(f)
		if err != nil {
			return 0, err
		}
		for _, s := range syms {
			b.index.Add(s)
		}
		parsed = append(parsed, parsedFile{rel: rel, file: file, facts: facts})
	}
	// Fold each file's type facts into its package's table.
	byPkg := map[string]*pkgFacts{}
	for _, pf := range parsed {
		facts := byPkg[pf.file.Name.Name]
		if facts == nil {
			facts = newPkgFacts()
			byPkg[pf.file.Name.Name] = facts
		}
		facts.merge(pf.facts)
	}
	// Pass 2: collect call edges with the aggregated facts, then resolve each
	// against the complete index.
	var raw []rawEdge // (file, caller, calleeCandidate)
	for _, pf := range parsed {
		for _, e := range fileEdges(pf.file, b.fset, pf.rel, pf.file.Name.Name, byPkg[pf.file.Name.Name]) {
			raw = append(raw, rawEdge{file: pf.rel, caller: e.caller, callee: e.callee})
		}
	}
	for _, re := range raw {
		if callee := b.resolveCallee(re.callee); callee != "" {
			b.index.AddEdge(re.caller, callee)
			b.fileEdges[re.file] = append(b.fileEdges[re.file], edge{re.caller, callee})
		}
	}
	for _, f := range changed {
		if h, err := hashFile(f); err == nil {
			b.fileHash[f] = h
		}
	}
	return len(changed), nil
}

// rawEdge is a call relationship recorded before symbol resolution.
type rawEdge struct {
	file   string
	caller string
	callee string
}

// parseFileSymbols parses one file and returns its relative path (slash
// form), AST, symbols and per-file type facts. It does NOT touch the index —
// the caller decides when to splice in. This keeps incremental rebuilds atomic
// (a file that fails to parse leaves the previous state intact). Call edges
// are derived later (in Build) once package-wide facts are aggregated.
func (b *Builder) parseFileSymbols(path string) (string, *ast.File, []*Symbol, *pkgFacts, error) {
	rel, err := filepath.Rel(b.Root, path)
	if err != nil {
		return "", nil, nil, nil, err
	}
	relSlash := filepath.ToSlash(rel)
	// Drop stale entries for this file from the index before re-adding.
	b.index.removeFile(relSlash)
	for _, e := range b.fileEdges[relSlash] {
		b.index.removeEdge(e.caller, e.callee)
	}
	delete(b.fileEdges, relSlash)
	delete(b.fileSyms, relSlash)

	file, syms, facts, err := parseFile(path, relSlash, "", b.fset)
	if err != nil {
		return "", nil, nil, nil, err
	}
	b.fileSyms[relSlash] = syms
	return relSlash, file, syms, facts, nil
}

// resolveCallee maps a call string like "pkg foo", "pkg a.Bar" or the
// receiver-qualified "pkg (*Cache).Get" (P18: emitted when the variable's
// declared type is known) to a symbol ID in the index. A qualified call
// resolves to the method on that exact receiver first; anything else matches
// by name — same-package name match first, then any package.
func (b *Builder) resolveCallee(call string) string {
	sp := strings.Index(call, " ")
	if sp < 0 {
		return ""
	}
	rest := call[sp+1:]

	// P18: "(*Cache).Get" — a type-known call. Pin the method on that exact
	// receiver before degrading to bare-name matching.
	if strings.HasPrefix(rest, "(") && strings.Contains(rest, ").") {
		if id := b.resolveReceiverHint(rest); id != "" {
			return id
		}
	}

	rest = strings.TrimPrefix(rest, "(") // strip "(" for legacy receiver forms
	// Candidate name: for "a.Bar" → "Bar"; for "foo" → "foo".
	name := rest
	if i := strings.LastIndex(rest, "."); i >= 0 {
		name = rest[i+1:]
	}
	if i := strings.Index(name, ")."); i >= 0 {
		name = name[i+2:]
	}
	matches := b.index.byName[strings.ToLower(name)]
	for _, m := range matches {
		if m.Name == name || strings.HasSuffix(m.Name, "."+name) {
			return m.ID()
		}
	}
	return ""
}

// resolveReceiverHint maps a receiver-qualified form like "(*Cache).Get" to a
// symbol ID. The exact form wins; then Go's method-set twin is tried — an
// addressable Cache value can call (*Cache).Get, and a *Cache's method set
// includes (Cache).Get — so a call on either spelling lands on the right
// method even when the other one is the only declaration.
func (b *Builder) resolveReceiverHint(q string) string {
	if syms := b.index.ByQualified(q); len(syms) > 0 {
		return syms[0].ID()
	}
	var twin string
	switch {
	case strings.HasPrefix(q, "(*"):
		twin = "(" + q[2:]
	case strings.HasPrefix(q, "("):
		twin = "(*" + q[1:]
	}
	if twin != "" {
		if syms := b.index.ByQualified(twin); len(syms) > 0 {
			return syms[0].ID()
		}
	}
	return ""
}

// goFiles returns all .go files under root (excluding hidden dirs/vendor).
func goFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// removeFile drops all symbols belonging to a file from the index and
// rebuilds the lookup maps.
func (ix *Index) removeFile(rel string) {
	var keep []*Symbol
	for _, s := range ix.all {
		if s.File != rel {
			keep = append(keep, s)
		}
	}
	ix.all = keep
	ix.byName = make(map[string][]*Symbol)
	ix.byQualified = make(map[string][]*Symbol)
	ix.byFile = make(map[string][]*Symbol)
	ix.ByPackage = make(map[string][]string)
	for _, s := range ix.all {
		key := strings.ToLower(s.Name)
		ix.byName[key] = append(ix.byName[key], s)
		ix.byQualified[strings.ToLower(s.qualify())] = append(ix.byQualified[strings.ToLower(s.qualify())], s)
		ix.byFile[s.File] = append(ix.byFile[s.File], s)
		if pkg, ok := ix.ByPackage[s.Package]; ok {
			if !contains(pkg, s.File) {
				ix.ByPackage[s.Package] = append(pkg, s.File)
			}
		} else {
			ix.ByPackage[s.Package] = []string{s.File}
		}
	}
}

// removeEdge deletes a caller→callee edge from the call graph.
func (ix *Index) removeEdge(caller, callee string) {
	ix.callees[caller] = removeVal(ix.callees[caller], callee)
	ix.callers[callee] = removeVal(ix.callers[callee], caller)
}

func removeVal(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
