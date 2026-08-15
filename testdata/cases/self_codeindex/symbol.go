// Package codeindex builds a symbol-level index of a Go codebase:
// go/ast-parsed symbols plus a call graph, supporting precise, semantic
// and graph-based retrieval. It is the "Claude Code context" equivalent,
// but organized by functions/types/call-chains instead of text chunks.
package codeindex

import (
	"go/token"
	"sort"
	"strings"
)

// Kind classifies a symbol.
type Kind int

const (
	KindFunc Kind = iota
	KindMethod
	KindStruct
	KindInterface
	KindType
	KindConst
	KindVar
	KindPackage
)

func (k Kind) String() string {
	switch k {
	case KindFunc:
		return "func"
	case KindMethod:
		return "method"
	case KindStruct:
		return "struct"
	case KindInterface:
		return "interface"
	case KindType:
		return "type"
	case KindConst:
		return "const"
	case KindVar:
		return "var"
	case KindPackage:
		return "package"
	}
	return "unknown"
}

// Symbol is one indexed declaration.
type Symbol struct {
	Name      string
	Kind      Kind
	Package   string
	File      string // path relative to index root (slash-separated)
	Pos, End  token.Position
	Signature string // function/method signature; else the type expression
	Doc       string
	Recv      string // method receiver, e.g. "*Session"
}

// qualify builds a display name: "Method" as "(*Session).Method".
func (s Symbol) qualify() string {
	if s.Recv != "" {
		return "(" + s.Recv + ")." + s.Name
	}
	return s.Name
}

// Desc returns a one-line description used as the embedding text for the
// semantic layer.
func (s Symbol) Desc() string {
	var b strings.Builder
	b.WriteString(s.Package + " ")
	b.WriteString(s.qualify())
	b.WriteString(" " + s.Kind.String() + " ")
	if s.Signature != "" {
		b.WriteString(s.Signature)
	}
	if s.Doc != "" {
		b.WriteString(" // " + firstLine(s.Doc))
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Index holds all symbols and the call graph. It is the in-memory
// representation the retriever queries.
type Index struct {
	byName map[string][]*Symbol // symbol name (case-insensitive) → matches
	byFile map[string][]*Symbol // file → symbols declared in it
	all    []*Symbol
	// Call graph: node key is symbol.ID().
	callers   map[string][]string
	callees   map[string][]string
	ByPackage map[string][]string // package → file list
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{
		byName:    make(map[string][]*Symbol),
		byFile:    make(map[string][]*Symbol),
		callers:   make(map[string][]string),
		callees:   make(map[string][]string),
		ByPackage: make(map[string][]string),
	}
}

// ID is a stable unique key for a symbol (package + name + receiver).
func (s *Symbol) ID() string {
	return s.Package + " " + s.qualify()
}

// Add inserts a symbol into the index.
func (ix *Index) Add(s *Symbol) {
	ix.all = append(ix.all, s)
	key := strings.ToLower(s.Name)
	ix.byName[key] = append(ix.byName[key], s)
	ix.byFile[s.File] = append(ix.byFile[s.File], s)
	ix.callers[s.ID()] = nil
	ix.callees[s.ID()] = nil
	if pkg, ok := ix.ByPackage[s.Package]; ok {
		if !contains(pkg, s.File) {
			ix.ByPackage[s.Package] = append(pkg, s.File)
		}
	} else {
		ix.ByPackage[s.Package] = []string{s.File}
	}
}

// AddEdge records a caller→callee call relationship.
func (ix *Index) AddEdge(callerID, calleeID string) {
	ix.callees[callerID] = appendUnique(ix.callees[callerID], calleeID)
	ix.callers[calleeID] = appendUnique(ix.callers[calleeID], callerID)
}

// Callees returns the direct callees of a symbol.
func (ix *Index) Callees(id string) []string { return ix.callees[id] }

// Callers returns the direct callers of a symbol.
func (ix *Index) Callers(id string) []string { return ix.callers[id] }

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Symbols returns all indexed symbols (for tests/metrics).
func (ix *Index) Symbols() []*Symbol { return ix.all }

// allCount reports how many symbols are indexed.
func (ix *Index) allCount() int { return len(ix.all) }

// ByName returns symbols whose name matches key (case-insensitive).
func (ix *Index) ByName(name string) []*Symbol {
	return ix.byName[strings.ToLower(name)]
}

// SortedFiles returns files in deterministic order.
func (ix *Index) SortedFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ix.all {
		if !seen[s.File] {
			seen[s.File] = true
			out = append(out, s.File)
		}
	}
	sort.Strings(out)
	return out
}
