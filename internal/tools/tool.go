// Package tools implements the atomic tool set that is the only side-effect
// outlet of the agent: read, edit, run and governance operations all go
// through tools so they can be recorded, intercepted and rolled back.
package tools

import (
	"context"
	"encoding/json"
)

// Args is a raw JSON object passed to a tool.
type Args json.RawMessage

// Result is the normalized outcome of a tool call.
type Result struct {
	Ok     bool            `json:"ok"`
	Output string          `json:"output,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Tool is the atomic operation interface. All tools are bound to a sandbox.
type Tool interface {
	Name() string
	Run(ctx context.Context, args Args) (Result, error)
}

// decode parses tool args into v.
func decode(args Args, v any) error {
	return json.Unmarshal(args, v)
}

// errResult normalizes an execution failure into a failed Result rather than
// a hard error, so the agent can read the output instead of crashing.
func errResult(out string, err error) Result {
	return Result{Ok: false, Output: out + "\n[sandbox: " + err.Error() + "]"}
}

var _ Tool = (*ReadTool)(nil)
var _ Tool = (*EditTool)(nil)
var _ Tool = (*RunTestTool)(nil)
var _ Tool = (*RunCommandTool)(nil)
var _ Tool = (*RollbackTool)(nil)
var _ Tool = (*GitDiffTool)(nil)
