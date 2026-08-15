package tools

import (
	"context"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// RollbackTool implements rollback: reverts the last sandbox snapshot so
// the agent can undo a bad edit.
type RollbackTool struct {
	Sandbox *sandbox.Sandbox
}

// Name returns the tool name.
func (t *RollbackTool) Name() string { return "rollback" }

// Run reverts the last snapshot.
func (t *RollbackTool) Run(ctx context.Context, args Args) (Result, error) {
	if err := t.Sandbox.Rollback(); err != nil {
		return Result{}, err
	}
	return Result{Ok: true}, nil
}

// GitDiffTool implements git_diff: shows everything the agent has changed
// since the baseline (useful for review and for interactive confirmation).
type GitDiffTool struct {
	Sandbox *sandbox.Sandbox
}

// Name returns the tool name.
func (t *GitDiffTool) Name() string { return "git_diff" }

// Run returns the cumulative diff since the sandbox baseline.
func (t *GitDiffTool) Run(ctx context.Context, args Args) (Result, error) {
	diff, err := t.Sandbox.Diff()
	if err != nil {
		return Result{}, err
	}
	return Result{Ok: true, Output: diff}, nil
}
