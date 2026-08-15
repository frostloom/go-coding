package agent

import (
	"context"
	"encoding/json"

	"github.com/zlx05/go-coding/internal/registry"
)

// ToolCallReq is the request form of the {"call_tool":{...}} protocol
// primitive — how the LLM invokes a dynamically-registered external tool
// (from a skill, an MCP client, or a subagent's toolset).
type ToolCallReq struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// parseToolCall detects whether the LLM's reply is an external-tool call
// ({"call_tool":{"name":..,"args":{..}}}) rather than a plan or an explore
// request. Tolerates the same deviations the other parsers do (code fences,
// concatenated JSON, malformed wrapper).
func parseToolCall(content string) (ToolCallReq, bool) {
	jsonStr, ok := extractFirstJSON(stripCodeFence(content))
	if !ok {
		return ToolCallReq{}, false
	}
	var probe struct {
		CallTool ToolCallReq `json:"call_tool"`
	}
	if err := json.Unmarshal([]byte(repairJSON(jsonStr)), &probe); err != nil {
		return ToolCallReq{}, false
	}
	if probe.CallTool.Name != "" {
		return probe.CallTool, true
	}
	return ToolCallReq{}, false
}

// execToolCall runs a registered external tool and returns its result as a
// text block to feed back to the LLM. Unknown tools and handler errors become
// error text — the model sees the failure and can recover, exactly like an
// explore op that failed.
func (a *Agent) execToolCall(ctx context.Context, tc ToolCallReq) string {
	if a.reg == nil {
		return "error: no external tools registered; you must produce a plan"
	}
	out, err := a.reg.Call(ctx, tc.Name, tc.Args)
	if err != nil {
		return "error: " + err.Error()
	}
	return registry.ToJSON(out)
}
