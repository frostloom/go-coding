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

// parseFile parses one Go file into its AST, symbols and per-file type facts.
// Call edges are derived separately (fileEdges) once package-wide type facts
// are aggregated, so a call can be pinned by a signature declared in another
// file. The file must already be inside the index root; rel is the
// root-relative path used for display.
func parseFile(path, rel, pkgName string, fset *token.FileSet) (*ast.File, []*Symbol, *pkgFacts, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("codeindex: parse %s: %w", path, err)
	}
	if pkgName == "" {
		pkgName = f.Name.Name
	}
	symbols, facts := walk(f, fset, rel, pkgName)
	return f, symbols, facts, nil
}

// edge is a caller→callee relationship.
type edge struct {
	caller string // caller symbol ID
	callee string // callee symbol ID
}

// symbolFromFuncDecl builds the symbol for a FuncDecl. Shared by walk (symbol
// extraction) and fileEdges (caller IDs) so a caller ID always matches the
// indexed symbol.
func symbolFromFuncDecl(d *ast.FuncDecl, fset *token.FileSet, rel, pkg string) *Symbol {
	var recv, signature string
	if d.Recv != nil {
		recv = receiverType(d.Recv)
		signature = "func (" + recv + ") " + d.Name.Name + funcTypeParams(d.Type)
	} else {
		signature = "func " + d.Name.Name + funcTypeParams(d.Type)
	}
	s := &Symbol{
		// Name is the bare identifier; qualify() adds the receiver, so a
		// method is "Get" with Recv="*Cache" (P17: bare name keeps methods
		// reachable by Name lookup AND IDs single-qualified, fixing the
		// double-qualification §6#24③).
		Name:      d.Name.Name,
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

// fileEdges extracts call edges from a parsed file using the package-wide type
// facts (P19), so a call in one file can be pinned by a signature declared in
// another. The caller ID is the FuncDecl's symbol ID.
func fileEdges(f *ast.File, fset *token.FileSet, rel, pkg string, pf *pkgFacts) []edge {
	var edges []edge
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		caller := symbolFromFuncDecl(d, fset, rel, pkg).ID()
		for _, c := range collectCalls(fset, d.Body, pkg, receiverEnv(d), pf) {
			edges = append(edges, edge{caller: caller, callee: c})
		}
		return true
	})
	return edges
}

// walk extracts symbols and per-file type facts from a parsed file.
func walk(f *ast.File, fset *token.FileSet, rel, pkg string) ([]*Symbol, *pkgFacts) {
	facts := newPkgFacts()
	var symbols []*Symbol
	ast.Inspect(f, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.FuncDecl:
			s := symbolFromFuncDecl(t, fset, rel, pkg)
			symbols = append(symbols, s)
			// P19: record the function's return signature (methods excluded —
			// a method call can't feed an := initializer here) so a later
			// c := makeCache() in any file can pin c's type from the package
			// table.
			if t.Recv == nil {
				facts.funcReturns[t.Name.Name] = funcReturnTypes(t.Type)
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
			// P19: record struct fields so a p.c.Get() chain can resolve the
			// field's type (p: *Config, c: *Cache → (*Cache).Get).
			if st, ok := t.Type.(*ast.StructType); ok {
				fields := map[string]string{}
				for _, fld := range st.Fields.List {
					for _, fn := range fld.Names {
						fields[fn.Name] = exprString(fld.Type)
					}
				}
				facts.structFields[name] = fields
			}
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
	return symbols, facts
}

// pkgFacts are the package-level type facts used to pin receiver types: the
// return signatures of package functions (so c := makeCache() gives c its
// type) and struct fields (so p.c.Get() resolves the p.c chain). They are
// aggregated across all files of a package, so a call in one file can use a
// signature declared in another.
type pkgFacts struct {
	funcReturns  map[string][]string          // funcName → return types (in order)
	structFields map[string]map[string]string // typeName → field → field type
}

func newPkgFacts() *pkgFacts {
	return &pkgFacts{
		funcReturns:  map[string][]string{},
		structFields: map[string]map[string]string{},
	}
}

// merge folds another file's facts into the package table. First declaration
// wins (a package has one signature per name; file order is deterministic).
func (pf *pkgFacts) merge(o *pkgFacts) {
	if o == nil {
		return
	}
	for name, rt := range o.funcReturns {
		if _, ok := pf.funcReturns[name]; !ok {
			pf.funcReturns[name] = rt
		}
	}
	for tn, fields := range o.structFields {
		if _, ok := pf.structFields[tn]; !ok {
			pf.structFields[tn] = fields
		}
	}
}

// funcReturnTypes lists a function's return types in order, expanding named
// same-type results: func f() (a, b T) → [T, T].
func funcReturnTypes(ft *ast.FuncType) []string {
	if ft.Results == nil {
		return nil
	}
	var out []string
	for _, r := range ft.Results.List {
		typ := exprString(r.Type)
		n := len(r.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, typ)
		}
	}
	return out
}

// typeEnv tracks, within one function body, the declared type of each
// variable currently in scope (receiver, params, := / var bindings) so a later
// c.Get() can resolve to the method on c's type. P18: same receiver language
// as P16 edit 'recv' / P17 ByQualified — the type is rendered with exprString,
// so the derived "(*Cache).Get" lines up with Symbol.Recv.
type typeEnv map[string]string

func (e typeEnv) copy() typeEnv {
	cp := make(typeEnv, len(e))
	for k, v := range e {
		cp[k] = v
	}
	return cp
}

// receiverEnv seeds the type env with the function's receiver and parameters,
// so a call on an argument (e.g. c.Get() for a *Cache param) pins the method.
func receiverEnv(d *ast.FuncDecl) typeEnv {
	env := typeEnv{}
	add := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, f := range list.List {
			for _, n := range f.Names {
				env[n.Name] = exprString(f.Type)
			}
		}
	}
	add(d.Recv)
	add(d.Type.Params)
	return env
}

// typeFromValue derives a declared type from a simple initializer expression:
// &Cache{} → "*Cache", Cache{} → "Cache", new(Cache) → "*Cache", and (P19) a
// call to a package function with a known signature — retPos selects which
// return binds (c, err := makeCache() with makeCache() (*Cache, error) → c is
// *Cache). Anything it can't pin syntactically (unindexed calls, arithmetic,
// := otherVar) returns "" — the caller then falls back to bare-name matching,
// exactly as before P18.
func typeFromValue(e ast.Expr, pkg string, retPos int, pf *pkgFacts) string {
	switch t := e.(type) {
	case *ast.CompositeLit:
		return exprString(t.Type)
	case *ast.UnaryExpr:
		if t.Op == token.AND {
			if inner := typeFromValue(t.X, pkg, 0, pf); inner != "" {
				return "*" + inner
			}
		}
	case *ast.CallExpr:
		// new(Cache) → "*Cache": the argument is syntactically a type, so
		// render it with exprString (unlike := otherVar, which must stay "").
		if id, ok := t.Fun.(*ast.Ident); ok && id.Name == "new" && len(t.Args) == 1 {
			return "*" + exprString(t.Args[0])
		}
		// P19: a package function with a known signature (plain calls only;
		// method/selector calls are not derivable here).
		if id, ok := t.Fun.(*ast.Ident); ok && pf != nil {
			if rt := pf.funcReturns[id.Name]; retPos < len(rt) {
				return rt[retPos]
			}
		}
	}
	return ""
}

// bindVal derives the declared type for the i-th name in a declaration from
// the right-hand values. A single call feeding several names maps positions
// (c, err := makeCache() → c takes return[0], err return[1]).
func bindVal(values []ast.Expr, i int, pkg string, pf *pkgFacts) string {
	if len(values) == 1 {
		if _, ok := values[0].(*ast.CallExpr); ok {
			return typeFromValue(values[0], pkg, i, pf)
		}
		return typeFromValue(values[0], pkg, 0, pf)
	}
	if i < len(values) {
		return typeFromValue(values[i], pkg, 0, pf)
	}
	return ""
}

// typeOfChain resolves the declared type of a selector chain like p.c: an
// Ident's type comes from the env; p.c looks up the field on p's struct type.
// Returns "" when any link is unknown — the caller then skips the edge rather
// than guessing (a wrong edge is worse than none for edit-time validation).
func (env typeEnv) typeOfChain(x ast.Expr, pkg string, pf *pkgFacts) string {
	switch t := x.(type) {
	case *ast.Ident:
		return env[t.Name]
	case *ast.SelectorExpr:
		base := env.typeOfChain(t.X, pkg, pf)
		if base == "" || pf == nil {
			return ""
		}
		// Strip a pointer for the field lookup (fields live on the named
		// type); a dotted external type (time.Duration) has no table entry.
		tn := strings.TrimPrefix(base, "*")
		if strings.Contains(tn, ".") {
			return ""
		}
		if fields, ok := pf.structFields[tn]; ok {
			return fields[t.Sel.Name]
		}
		return ""
	}
	return ""
}

// collectCalls finds callee symbol IDs (package.qualifier.Name) from calls
// and selectors inside a body. Resolution against the actual symbol table
// happens later in build (name → ID lookup). P18: a single source-order pass
// also tracks declared types, so c.Get() on a type-known variable emits the
// receiver-qualified "(*Cache).Get" instead of "c.Get" — the callee then
// resolves to the method on that exact receiver rather than by name. P19: the
// type env is seeded from package-wide facts too — a binding c := makeCache()
// pins *Cache from makeCache's recorded signature, and a chain p.c.Get()
// resolves the field type from the struct table.
func collectCalls(fset *token.FileSet, body *ast.BlockStmt, pkg string, env typeEnv, pf *pkgFacts) []string {
	if body == nil {
		return nil
	}
	var out []string
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.FuncLit:
			// Closure: a nested scope seeded with the outer env, but :=
			// shadowing inside must not leak out (or the outer c.Get() would
			// resolve against the closure's type — a wrong edge).
			saved := env
			env = env.copy()
			ast.Inspect(t.Body, visit)
			env = saved
			return false
		case *ast.ValueSpec:
			for i, vn := range t.Names {
				if t.Type != nil {
					env[vn.Name] = exprString(t.Type)
				} else {
					env[vn.Name] = bindVal(t.Values, i, pkg, pf)
				}
			}
		case *ast.AssignStmt:
			if t.Tok == token.DEFINE {
				for i, lhs := range t.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					env[id.Name] = bindVal(t.Rhs, i, pkg, pf)
				}
			}
		case *ast.CallExpr:
			switch fun := t.Fun.(type) {
			case *ast.Ident:
				out = append(out, pkg+" "+fun.Name)
			case *ast.SelectorExpr:
				// x.Func or pkg.Func: capture the qualified name. A chain
				// receiver (p.c.Get()) resolves the field type if known.
				if p, ok := fun.X.(*ast.Ident); ok {
					callee := pkg + " " + p.Name + "." + fun.Sel.Name
					if t := env[p.Name]; t != "" {
						callee = pkg + " (" + t + ")." + fun.Sel.Name
					}
					out = append(out, callee)
				} else if t := env.typeOfChain(fun.X, pkg, pf); t != "" {
					out = append(out, pkg+" ("+t+")."+fun.Sel.Name)
				}
			}
		}
		return true
	}
	ast.Inspect(body, visit)
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
