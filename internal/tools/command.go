package tools

import (
	"context"
	"time"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// RunCommandTool implements run_command: executes a whitelisted binary
// (go/git subcommands only) inside the sandbox with a hard timeout.
type RunCommandTool struct {
	Sandbox *sandbox.Sandbox
	Timeout time.Duration
}

// Name returns the tool name.
func (t *RunCommandTool) Name() string { return "run_command" }

type commandArgs struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Run validates the command against the sandbox whitelist and executes it.
func (t *RunCommandTool) Run(ctx context.Context, args Args) (Result, error) {
	var a commandArgs
	if err := decode(args, &a); err != nil {
		return Result{}, err
	}
	out, err := t.Sandbox.Run(ctx, a.Command, a.Args, t.Timeout)
	if err != nil {
		return errResult(out, err), nil
	}
	return Result{Ok: true, Output: out}, nil
}
