package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// GrepTool implements grep: a case-insensitive substring/word search over
// .go files inside the sandbox. It returns file:line matches, bounded to
// avoid flooding the model with huge output.
type GrepTool struct {
	Sandbox *sandbox.Sandbox
	MaxHits int // cap on total matches returned (default 200)
}

// Name returns the tool name.
func (t *GrepTool) Name() string { return "grep" }

type grepArgs struct {
	Pattern string `json:"pattern"`
}

// Run searches .go files for the pattern and returns matching lines.
func (t *GrepTool) Run(ctx context.Context, args Args) (Result, error) {
	var a grepArgs
	if err := decode(args, &a); err != nil {
		return Result{}, err
	}
	if a.Pattern == "" {
		return Result{}, nil
	}
	max := t.MaxHits
	if max <= 0 {
		max = 200
	}
	pat := strings.ToLower(a.Pattern)

	var b strings.Builder
	hits := 0
	root := t.Sandbox.Root

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if err == nil && d.IsDir() && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if bytes.Contains(bytes.ToLower(line), []byte(pat)) {
				rel, _ := filepath.Rel(root, path)
				idx := bytes.Index(bytes.ToLower(line), []byte(pat))
				lo, hi := idx-40, idx+len(pat)+40
				if lo < 0 {
					lo = 0
				}
				if hi > len(line) {
					hi = len(line)
				}
				// trim partial first line to avoid cutting mid-slice
				snippet := string(line[lo:hi])
				if i := strings.Index(snippet, " "); i >= 0 && lo > 0 {
					snippet = snippet[i+1:]
				}
				b.WriteString(filepath.ToSlash(rel) + ":" + snippet + "\n")
				hits++
				if hits >= max {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})

	return Result{Ok: true, Output: b.String()}, nil
}

// ListDirTool implements list_dir: shows the top-level entries of the
// sandbox (or a given subdir) so the model can understand structure before
// reading files — the "explore" step of the agent loop.
type ListDirTool struct {
	Sandbox *sandbox.Sandbox
}

// Name returns the tool name.
func (t *ListDirTool) Name() string { return "list_dir" }

// Run lists entries of a directory (default: sandbox root).
func (t *ListDirTool) Run(ctx context.Context, args Args) (Result, error) {
	var a struct {
		Path string `json:"path,omitempty"`
	}
	_ = decode(args, &a)
	full, err := t.Sandbox.Resolve(a.Path)
	if err != nil {
		full = t.Sandbox.Root
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return Result{}, err
	}
	var b strings.Builder
	for _, e := range entries {
		marker := " "
		if e.IsDir() {
			marker = "/"
		}
		b.WriteString(e.Name() + marker + "\n")
	}
	return Result{Ok: true, Output: b.String()}, nil
}
