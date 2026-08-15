package tools

import (
	"context"
	"encoding/json"
	"os"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// ReadTool implements read_file (read-only, no sandbox side effects).
type ReadTool struct {
	Sandbox *sandbox.Sandbox
}

// Name returns the tool name.
func (t *ReadTool) Name() string { return "read_file" }

// Run reads a file or, if path is a directory, lists its entries.
func (t *ReadTool) Run(ctx context.Context, args Args) (Result, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := decode(args, &a); err != nil {
		return Result{}, err
	}
	full, err := t.Sandbox.Resolve(a.Path)
	if err != nil {
		return Result{}, err
	}

	info, err := os.Stat(full)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return Result{}, err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		b, _ := json.Marshal(names)
		return Result{Ok: true, Data: b}, nil
	}

	b, err := os.ReadFile(full)
	if err != nil {
		return Result{}, err
	}
	return Result{Ok: true, Output: string(b)}, nil
}
