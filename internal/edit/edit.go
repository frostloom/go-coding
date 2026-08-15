// Package edit implements precise, validated code edits. It is the M4
// layer on top of the whole-file replacement from M1: text-level edits
// (old → new) with conflict detection, plus fast pre-write gates (syntax,
// format) so a bad edit is refused before it touches disk.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
)

// ApplyText replaces the single unique occurrence of old with new in src.
// It is the "text-level" precise edit: no full-file rewrite, so nothing
// outside the target text can drift. If old appears zero or multiple times
// the edit is refused rather than applied to the wrong place — the caller
// must disambiguate. This mirrors how a careful human (and Claude Code)
// edits: match exactly one target, change only that.
func ApplyText(src []byte, old, new string) ([]byte, error) {
	if old == "" {
		return nil, errors.New("edit: 'old' text is empty")
	}
	needle := []byte(old)
	idx := bytes.Index(src, needle)
	if idx < 0 {
		return nil, errors.New("edit: 'old' text not found in file")
	}
	if bytes.Index(src[idx+len(needle):], needle) >= 0 {
		return nil, errors.New("edit: 'old' text matches multiple locations; make it more specific")
	}
	out := make([]byte, 0, len(src)-len(old)+len(new))
	out = append(out, src[:idx]...)
	out = append(out, new...)
	out = append(out, src[idx+len(old):]...)
	return out, nil
}

// ApplySymbol replaces the single top-level declaration named symbol with new.
// It is the "AST-level" precise edit, the enhancement layer over ApplyText:
// instead of matching raw text (which the caller must reproduce byte-for-byte),
// it locates the declaration node in the AST and splices exactly its byte
// range — the caller only names the symbol (a function, method, type, var or
// const) and supplies the replacement text. recv disambiguates same-name
// methods: pass the receiver type rendered exactly like the symbol engine
// ("*Cache" for `func (c *Cache) ...`, "Cache" for a value receiver); the
// empty string matches any receiver (and plain functions). If the name is
// absent, still matches multiple declarations, or lives inside a grouped
// declaration, the edit is refused — the same "make it specific" principle as
// ApplyText. This is what lets an LLM say "replace Add with …" without knowing
// the current body.
func ApplySymbol(src []byte, symbol, recv, new string) ([]byte, error) {
	if symbol == "" {
		return nil, errors.New("edit: 'symbol' is empty")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "symbol.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("edit: file does not parse: %w", err)
	}
	var spans [][2]int // (start, end) byte offsets of matching declarations
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.Name != symbol {
				continue
			}
			if recv != "" && recvOf(d) != recv {
				continue // a receiver targets a method, never a plain function
			}
			spans = append(spans, spanOf(fset, d))
		case *ast.GenDecl:
			// A name inside a grouped declaration ("var ( a; b )") cannot be
			// spliced alone without leaving stray semicolons, so only support
			// single-spec declarations — refuse anything grouped.
			if recv != "" || len(d.Specs) != 1 {
				continue // a receiver targets methods; types/consts have none
			}
			if hasName(d.Specs[0], symbol) {
				spans = append(spans, spanOf(fset, d))
			}
		}
	}
	switch len(spans) {
	case 0:
		if recv != "" {
			return nil, fmt.Errorf("edit: symbol %q with receiver %q not found in file", symbol, recv)
		}
		return nil, fmt.Errorf("edit: symbol %q not found in file", symbol)
	case 1:
		return splice(src, spans[0], new), nil
	default:
		return nil, fmt.Errorf("edit: symbol %q matches %d declarations; the name alone is ambiguous — provide 'recv' (e.g. \"*Cache\") to target one method, or use a text-level 'old'+'new' edit", symbol, len(spans))
	}
}

// ApplyLocal replaces the single local declaration named symbol inside the
// body of function fn. It is the body-local companion to ApplySymbol: instead
// of naming a top-level declaration, the caller names the enclosing function
// and a local variable's (or local type's) declaration site. The splice unit
// is the whole declaration statement — a `:=`, `var`/`const`, for-init or
// if-init declaration — so the caller supplies the replacement statement and
// never needs the current one. recv disambiguates same-name methods exactly as
// in ApplySymbol ("*Cache" picks the `func (c *Cache)` body); empty matches any.
//
// Safety mirrors ApplySymbol: a multi-name statement ("x, y := ...") and a
// grouped declaration ("var ( a; b )") cannot be spliced without leaving the
// sibling names dangling, and the same symbol declared at more than one site
// inside fn (shadowing, sequential reuse, func literals) is ambiguous — all are
// refused with the reason, never guessed. Scope is isolated: a package-level
// declaration with the same name is untouched.
func ApplyLocal(src []byte, fn, recv, symbol, new string) ([]byte, error) {
	if fn == "" || symbol == "" {
		return nil, errors.New("edit: 'fn' and 'symbol' are required for a body-local edit")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "local.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("edit: file does not parse: %w", err)
	}

	var body *ast.BlockStmt
	matches := 0
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		if recv != "" && recvOf(fd) != recv {
			continue // a receiver targets a method body, never a plain function
		}
		matches++
		if matches > 1 {
			return nil, fmt.Errorf("edit: function %q matches %d declarations; the name alone is ambiguous — provide 'recv' (e.g. \"*Cache\") to target one method, or use a text-level 'old'+'new' edit", fn, matches)
		}
		body = fd.Body
	}
	if body == nil {
		if recv != "" {
			return nil, fmt.Errorf("edit: function %q with receiver %q not found in file", fn, recv)
		}
		return nil, fmt.Errorf("edit: function %q not found in file", fn)
	}

	var spans [][2]int // splittable single-name declaration sites
	var sites int      // every declaration site, spliceable or not (drives the >1 refusal)
	var grouped bool   // the symbol sits inside a grouped "var ( a; b )" declaration
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE {
				return true
			}
			if names := lhsNames(s.Lhs); len(names) == 1 && names[0] == symbol {
				spans = append(spans, spanOf(fset, s))
				sites++
			} else if containsName(names, symbol) {
				sites++
			}
		case *ast.RangeStmt:
			// `for i := range xs` declares its loop variables; a multi-name form
			// (`for k, v := range m`) is unsplittable like `x, y :=`. The blank
			// index (`for _, v := range m`) is not a name, so it stays a
			// single-name spliceable site. `for k, v = range m` reassigns and
			// declares nothing.
			if s.Tok != token.DEFINE {
				return true
			}
			var names []string
			if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
				names = append(names, id.Name)
			}
			if id, ok := s.Value.(*ast.Ident); ok && id.Name != "_" {
				names = append(names, id.Name)
			}
			if len(names) == 1 && names[0] == symbol {
				spans = append(spans, spanOf(fset, s))
				sites++
			} else if containsName(names, symbol) {
				sites++
			}
		case *ast.DeclStmt:
			gd, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			if len(gd.Specs) != 1 {
				// A name inside a grouped "var ( a; b )" cannot be spliced alone
				// without leaving the siblings dangling. It IS declared, so report
				// it accurately instead of falling through to "not declared".
				if specsContain(gd.Specs, symbol) {
					sites++
					grouped = true
				}
				return true
			}
			switch spec := gd.Specs[0].(type) {
			case *ast.ValueSpec:
				if len(spec.Names) == 1 && spec.Names[0].Name == symbol {
					spans = append(spans, spanOf(fset, gd))
					sites++
				} else if containsIdent(spec.Names, symbol) {
					sites++
				}
			case *ast.TypeSpec:
				if spec.Name.Name == symbol {
					spans = append(spans, spanOf(fset, gd))
					sites++
				}
			}
		}
		return true
	})

	switch {
	case sites > 1:
		// The contract: declared at more than one site is ambiguous, no matter
		// whether the extra sites are single-name, multi-name or grouped.
		return nil, fmt.Errorf("edit: local %q is declared at %d sites inside %q (shadowing, reuse or func literals); make it more specific", symbol, sites, fn)
	case len(spans) == 1:
		return splice(src, spans[0], new), nil
	case grouped:
		return nil, fmt.Errorf("edit: local %q is declared inside %q in a grouped declaration; it cannot be spliced alone — use a text-level 'old'+'new' edit instead", symbol, fn)
	case sites > 0:
		return nil, fmt.Errorf("edit: local %q is declared inside %q in a multi-name statement; split the declaration or use 'old'+'new'", symbol, fn)
	default:
		return nil, fmt.Errorf("edit: local %q is not declared inside %q", symbol, fn)
	}
}

// splice returns src with the byte range [span[0], span[1]) replaced by new.
func splice(src []byte, span [2]int, new string) []byte {
	start, end := span[0], span[1]
	out := make([]byte, 0, len(src)-end+start+len(new))
	out = append(out, src[:start]...)
	out = append(out, new...)
	out = append(out, src[end:]...)
	return out
}

// lhsNames returns the identifiers on the left-hand side of an assignment.
func lhsNames(exprs []ast.Expr) []string {
	names := make([]string, 0, len(exprs))
	for _, e := range exprs {
		if id, ok := e.(*ast.Ident); ok {
			names = append(names, id.Name)
		}
	}
	return names
}

// containsName reports whether names includes name.
func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// containsIdent reports whether idents includes name.
func containsIdent(idents []*ast.Ident, name string) bool {
	for _, id := range idents {
		if id.Name == name {
			return true
		}
	}
	return false
}

// hasName reports whether a type/value spec declares the given name.
func hasName(spec ast.Spec, name string) bool {
	switch sp := spec.(type) {
	case *ast.TypeSpec:
		return sp.Name.Name == name
	case *ast.ValueSpec:
		for _, n := range sp.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// specsContain reports whether any spec of a (grouped) declaration declares name.
func specsContain(specs []ast.Spec, name string) bool {
	for _, sp := range specs {
		if hasName(sp, name) {
			return true
		}
	}
	return false
}

// recvOf renders a FuncDecl's receiver exactly like the symbol engine indexes
// it: "*Cache" for `func (c *Cache) ...`, "Cache" for a value receiver,
// "Cache[T]" for a generic one. The empty string means no receiver (a
// package-level function). Callers pass this exact string as 'recv' to pick
// one method out of same-name candidates.
func recvOf(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	return exprString(fd.Recv.List[0].Type)
}

// exprString renders an AST type expression to source text — a compact subset
// of go/printer covering the receiver forms the symbol engine indexes. The
// fallback returns "" for exotic receivers the engine also cannot render; the
// callers then treat the method as receiverless, so a bare-name edit still
// matches it (the same drift the symbol engine accepts).
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return exprString(t.X) + "[" + exprString(t.Index) + "]"
	case *ast.ParenExpr:
		return "(" + exprString(t.X) + ")"
	default:
		return ""
	}
}

// spanOf returns the byte offsets [start, end) of a declaration.
func spanOf(fset *token.FileSet, n ast.Node) [2]int {
	return [2]int{fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset}
}

// CheckSyntax parses src as a Go source file. It is the first gate of the
// edit validation chain: a file that does not even parse must never be
// written, regardless of what the tests would say later.
func CheckSyntax(filename string, src []byte) error {
	_, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ParseComments)
	return err
}

// CheckFormat reports whether src is gofmt-clean. Used as a hygiene WARNING,
// not a hard reject: full-file rewrites from an LLM are rarely canonical,
// and we must not introduce unrelated formatting churn by auto-rewriting
// the whole file. The agent surfaces the warning so it can tidy up.
func CheckFormat(src []byte) bool {
	out, err := format.Source(src)
	return err == nil && bytes.Equal(out, src)
}
