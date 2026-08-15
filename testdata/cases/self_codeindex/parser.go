package codeindex

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// parseFile parses one Go file into symbols and call edges. The file must
// already be inside the index root; rel is the root-relative path used for
// display.
func parseFile(path, rel, pkgName string, fset *token.FileSet) (*ast.File, []*Symbol, []edge, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("codeindex: parse %s: %w", path, err)
	}
	if pkgName == "" {
		pkgName = f.Name.Name
	}
	symbols, edges := walk(f, fset, rel, pkgName)
	return f, symbols, edges, nil
}

// edge is a caller→callee relationship.
type edge struct {
	caller string // caller symbol ID
	callee string // callee symbol ID
}

// walk extracts symbols and call edges from a parsed file.
func walk(f *ast.File, fset *token.FileSet, rel, pkg string) ([]*Symbol, []edge) {
	var symbols []*Symbol
	var edges []edge

	// helper to build the symbol for a FuncDecl.
	fnDecl := func(d *ast.FuncDecl) *Symbol {
		name := d.Name.Name
		var recv, signature string
		if d.Recv != nil {
			recv = receiverType(d.Recv)
			name = "(" + recv + ")." + name
			signature = "func (" + recv + ") " + d.Name.Name + funcTypeParams(d.Type)
		} else {
			signature = "func " + d.Name.Name + funcTypeParams(d.Type)
		}
		s := &Symbol{
			Name:      name,
			Kind:      KindFunc,
			Package:   pkg,
			File:      filepath.ToSlash(rel),
			Pos:       fset.Position(d.Pos()),
			End:       fset.Position(d.End()),
			Signature: signature,
			Recv:      recv,
		}
		if d.Recv != nil {
			s.Kind = KindMethod
		}
		if d.Doc != nil {
			s.Doc = d.Doc.Text()
		}
		return s
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.FuncDecl:
			s := fnDecl(t)
			symbols = append(symbols, s)
			// collect call edges within this function body
			for _, e := range collectCalls(fset, t.Body, pkg) {
				edges = append(edges, edge{caller: s.ID(), callee: e})
			}
		case *ast.TypeSpec:
			name := t.Name.Name
			kind := KindType
			var sig string
			switch tt := t.Type.(type) {
			case *ast.StructType:
				kind = KindStruct
				sig = "struct"
			case *ast.InterfaceType:
				kind = KindInterface
				sig = "interface"
			default:
				sig = exprString(tt)
			}
			s := &Symbol{
				Name:      name,
				Kind:      kind,
				Package:   pkg,
				File:      filepath.ToSlash(rel),
				Pos:       fset.Position(t.Pos()),
				End:       fset.Position(t.End()),
				Signature: sig,
			}
			if t.Doc != nil {
				s.Doc = t.Doc.Text()
			}
			symbols = append(symbols, s)
		case *ast.GenDecl:
			if t.Tok == token.CONST || t.Tok == token.VAR {
				for _, spec := range t.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, vn := range vs.Names {
						kind := KindVar
						if t.Tok == token.CONST {
							kind = KindConst
						}
						symbols = append(symbols, &Symbol{
							Name:    vn.Name,
							Kind:    kind,
							Package: pkg,
							File:    filepath.ToSlash(rel),
							Pos:     fset.Position(vn.Pos()),
							End:     fset.Position(vn.End()),
						})
					}
				}
			}
		}
		return true
	})
	return symbols, edges
}

// collectCalls finds callee symbol IDs (package.qualifier.Name) from calls
// and selectors inside a body. Resolution against the actual symbol table
// happens later in build (name → ID lookup).
func collectCalls(fset *token.FileSet, body *ast.BlockStmt, pkg string) []string {
	if body == nil {
		return nil
	}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			out = append(out, pkg+" "+fun.Name)
		case *ast.SelectorExpr:
			// x.Func or pkg.Func: capture the qualified name.
			if p, ok := fun.X.(*ast.Ident); ok {
				out = append(out, pkg+" "+p.Name+"."+fun.Sel.Name)
			}
		}
		return true
	})
	return out
}

// receiverType renders a method receiver like "*Session".
func receiverType(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return exprString(recv.List[0].Type)
}

// funcTypeParams renders "(a, b int) error" — params + results only.
func funcTypeParams(ft *ast.FuncType) string {
	var b strings.Builder
	b.WriteString("(")
	if ft.Params != nil {
		for i, p := range ft.Params.List {
			if i > 0 {
				b.WriteString(", ")
			}
			for j, nm := range p.Names {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(nm.Name)
			}
			if len(p.Names) > 0 {
				b.WriteString(" ")
			}
			b.WriteString(exprString(p.Type))
		}
	}
	b.WriteString(")")
	if ft.Results != nil && len(ft.Results.List) > 0 {
		b.WriteString(" (")
		for i, r := range ft.Results.List {
			if i > 0 {
				b.WriteString(", ")
			}
			if len(r.Names) > 0 {
				b.WriteString(r.Names[0].Name + " ")
			}
			b.WriteString(exprString(r.Type))
		}
		b.WriteString(")")
	}
	return b.String()
}

// exprString renders an ast.Expr without format, using only the source text
// (which preserves the original formatting for embedded types).
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[" + exprString(t.Len) + "]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func" + funcTypeParams(t)
	case *ast.BasicLit:
		return t.Value
	case *ast.IndexExpr:
		return exprString(t.X) + "[" + exprString(t.Index) + "]"
	default:
		return strconv.Quote(fmt.Sprintf("%T", e)) // fallback, should not hit
	}
}
