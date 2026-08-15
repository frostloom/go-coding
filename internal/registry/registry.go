// Package registry is the dynamic tool registry — the place external tools
// plug into the agent. Built-in tools (read/grep/list_dir/edit/…) stay on
// their own hardcoded path; anything a skill, an MCP client or a subagent
// makes available is a ToolDef registered here and exposed to the LLM via the
// {"call_tool":{...}} protocol primitive.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ToolDef declares one callable tool: its name, a description and input schema
// the LLM sees (so it knows how to call it), and the Go handler that runs it.
// The schema shape matches MCP's tools/list format, so an MCP client can
// register remote tools with zero translation.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"inputSchema"` // JSON Schema ({"type":"object","properties":…})
	Run         func(ctx context.Context, args map[string]any) (any, error)
}

// Registry is a concurrency-safe set of named tools.
type Registry struct {
	mu   sync.RWMutex
	tool map[string]ToolDef
}

// New builds an empty registry.
func New() *Registry {
	return &Registry{tool: map[string]ToolDef{}}
}

// Register adds a tool. A duplicate name is an error — later registration
// must not silently shadow earlier ones.
func (r *Registry) Register(def ToolDef) error {
	if def.Name == "" {
		return fmt.Errorf("registry: tool name is required")
	}
	if def.Run == nil {
		return fmt.Errorf("registry: tool %q has no Run handler", def.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tool[def.Name]; exists {
		return fmt.Errorf("registry: tool %q already registered", def.Name)
	}
	r.tool[def.Name] = def
	return nil
}

// List returns all registered tools in deterministic (name-sorted) order —
// stable output for prompt caching, same rationale as MCP's 2026-07-28 spec.
func (r *Registry) List() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDef, 0, len(r.tool))
	for _, d := range r.tool {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns one tool by name.
func (r *Registry) Get(name string) (ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.tool[name]
	return d, ok
}

// Call runs a registered tool with already-decoded JSON args and returns its
// result. Unknown tools and handler errors are reported as errors; the caller
// decides how to feed them back to the LLM.
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	d, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("registry: unknown tool %q", name)
	}
	return d.Run(ctx, args)
}

// Describe renders one tool as a compact prompt line the LLM can call: the
// name, what it does, and its argument schema flattened to types.
func Describe(d ToolDef) string {
	return "- " + d.Name + ": " + d.Description + "  args: " + schemaSummary(d.Schema)
}

// DescribeAll renders a list of tools as one prompt block, or "" when there
// are none (so a prompt built from it stays identical when no external tools
// are registered — mock runs never see a change).
func DescribeAll(defs []ToolDef) string {
	if len(defs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, d := range defs {
		b.WriteString(Describe(d))
		b.WriteString("\n")
	}
	return b.String()
}

// schemaSummary flattens a JSON Schema object into "name: type (required) | …".
func schemaSummary(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return "{}"
	}
	req := map[string]bool{}
	if list, ok := schema["required"].([]any); ok {
		for _, r := range list {
			if s, ok := r.(string); ok {
				req[s] = true
			}
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',', ' ')
		}
		p, _ := props[k].(map[string]any)
		t, _ := p["type"].(string)
		if t == "" {
			t = "any"
		}
		b = append(b, k...)
		b = append(b, ':', ' ')
		b = append(b, t...)
		if req[k] {
			b = append(b, " (required)"...)
		}
	}
	b = append(b, '}')
	return string(b)
}

// ToJSON is a helper for handlers that want to return a plain value as JSON
// text; it never fails for the value types handlers produce.
func ToJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("error: marshal result: %v", err)
	}
	return string(b)
}
