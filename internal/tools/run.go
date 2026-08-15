package tools

import (
	"context"
	"time"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// RunTestTool implements run_test: it executes the workspace's test command
// inside the sandbox with a hard timeout and process-tree cleanup. The command
// is configurable (Cmd) so non-Go projects can drive pytest / npm test / etc.;
// the default is `go test ./...`.
type RunTestTool struct {
	Sandbox *sandbox.Sandbox
	Timeout time.Duration
	// Cmd is the test command argv (e.g. []string{"pytest", "-q"}). Empty
	// means the default []string{"go", "test", "./..."}.
	Cmd []string
}

// Name returns the tool name.
func (t *RunTestTool) Name() string { return "run_test" }

// Run executes the test command and reports pass/fail plus the captured output.
func (t *RunTestTool) Run(ctx context.Context, args Args) (Result, error) {
	cmd := t.Cmd
	if len(cmd) == 0 {
		cmd = []string{"go", "test", "./..."}
	}
	out, err := t.Sandbox.Run(ctx, cmd[0], cmd[1:], t.Timeout)
	if err != nil {
		// Non-zero exit or timeout: still a usable result, not a crash.
		return errResult(out, err), nil
	}
	return Result{Ok: true, Output: out}, nil
}
