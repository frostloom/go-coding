package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/zlx05/go-coding/internal/codeindex"
	"github.com/zlx05/go-coding/internal/edit"
	"github.com/zlx05/go-coding/internal/sandbox"
)

// EditRejectedError marks an edit refused by a validation gate (syntax or
// symbol-level reference regression) BEFORE tests ran. The agent treats it
// as a repair signal, not a fatal error: the rejection reason goes back to
// the LLM and it re-plans. This is the "repair before test" fast loop —
// milliseconds of symbol-engine checking instead of a compiler run.
type EditRejectedError struct{ Reason string }

// Error implements error.
func (e *EditRejectedError) Error() string { return e.Reason }

// EditTool implements edit_file: full-file replacement (M1), precise
// text-level edits (M4, via {path, old, new}) and AST-level edits (via
// {path, symbol, new} — replace a declaration by its name, the enhancement
// layer; the caller names "Add" without reproducing its current body). Every
// Go-file edit passes a validation chain at write time:
//
//  1. syntax  — parse the new content; a file that doesn't parse is refused
//  2. format  — gofmt drift is reported as a warning (never enforced)
//  3. symbols — re-index and reject edits that break cross-file references
//     (a removed symbol still called by another file). The one
//     check a grep-based tool cannot do: it needs the call graph.
//
// The validation is powered by the same Builder the ContextProvider uses, so
// a rejected edit rolls the index back and nothing is left half-written.
type EditTool struct {
	Sandbox *sandbox.Sandbox
	Builder *codeindex.Builder // optional; enables edit-time symbol validation
}

// Name returns the tool name.
func (t *EditTool) Name() string { return "edit_file" }

type editArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"` // full-file replacement (M1)
	Old     string `json:"old"`     // precise text edit (M4)
	New     string `json:"new"`
	Symbol  string `json:"symbol"` // AST-level edit: replace the declaration named symbol with New
	Fn      string `json:"fn"`     // with Symbol: body-local edit — the local declared as symbol inside function Fn
	Recv    string `json:"recv"`   // with Symbol/Fn: receiver type (e.g. "*Cache") to disambiguate same-name methods
}

type editData struct {
	Path     string   `json:"path"`
	Old      string   `json:"old"`
	New      string   `json:"new"`
	Warnings []string `json:"warnings,omitempty"`
}

// Run applies the edit and validates it. On rejection the file is restored
// and an *EditRejectedError is returned with the exact reason.
func (t *EditTool) Run(ctx context.Context, args Args) (Result, error) {
	var a editArgs
	if err := decode(args, &a); err != nil {
		return Result{}, err
	}
	if a.Path == "" {
		return Result{}, errors.New("edit_file: missing path")
	}
	full, err := t.Sandbox.Resolve(a.Path)
	if err != nil {
		return Result{}, err
	}
	if dir := filepath.Dir(full); dir != t.Sandbox.Root {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, err
		}
	}

	old, _ := os.ReadFile(full) // may not exist yet
	var newContent []byte
	switch {
	case a.Content != "":
		newContent = []byte(a.Content)
	case a.Old != "":
		newContent, err = edit.ApplyText(old, a.Old, a.New)
		if err != nil {
			return Result{}, err
		}
	case a.Symbol != "" && a.Fn != "":
		newContent, err = edit.ApplyLocal(old, a.Fn, a.Recv, a.Symbol, a.New)
		if err != nil {
			return Result{}, err
		}
	case a.Symbol != "":
		newContent, err = edit.ApplySymbol(old, a.Symbol, a.Recv, a.New)
		if err != nil {
			return Result{}, err
		}
	default:
		return Result{}, errors.New("edit_file: provide 'content' (full file), 'old'+'new' (precise text), 'symbol'+'new' (replace a declaration, +'recv' for a specific method), or 'symbol'+'fn'+'new' (replace a local declaration inside a function, +'recv' for a method body)")
	}

	isGo := strings.HasSuffix(filepath.ToSlash(a.Path), ".go")
	var warnings []string
	if isGo {
		// Gate 1: syntax. Refuse before anything is written.
		if err := edit.CheckSyntax(filepath.Base(full), newContent); err != nil {
			return Result{}, &EditRejectedError{Reason: "syntax error in new content: " + firstLine(err.Error())}
		}
		if !edit.CheckFormat(newContent) {
			warnings = append(warnings, "content is not gofmt-clean (format is not enforced, but tidying it is recommended)")
		}
	}

	// Snapshot the pre-edit symbol state (only meaningful for Go files).
	var sn *codeindex.FileSnapshot
	rel := ""
	if isGo && t.Builder != nil {
		if _, err := t.Builder.Build(); err != nil {
			return Result{}, err
		}
		if relAbs, err := filepath.Rel(t.Sandbox.Root, full); err == nil {
			rel = filepath.ToSlash(relAbs)
		}
		sn = t.Builder.CaptureFile(rel)
	}

	if err := os.WriteFile(full, newContent, 0o644); err != nil {
		return Result{}, err
	}

	// Gate 2: call-graph regression against the rebuilt index.
	if isGo && t.Builder != nil && sn != nil {
		if _, err := t.Builder.Build(); err != nil {
			return Result{}, err
		}
		if issues := sn.VerifyAfter(t.Builder); len(issues) > 0 {
			// Restore the original file and roll the index back so a rejected
			// edit leaves no trace. The reason is returned to the LLM.
			_ = os.WriteFile(full, old, 0o644)
			_, _ = t.Builder.Build()
			return Result{}, &EditRejectedError{Reason: "breaks a cross-file reference: " + strings.Join(issues, "; ") + " — keep the symbol or update its callers first"}
		}
	}

	data, _ := json.Marshal(editData{Path: a.Path, Old: string(old), New: string(newContent), Warnings: warnings})
	return Result{Ok: true, Data: data}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
