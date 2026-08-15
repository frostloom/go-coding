package mcpclient

import (
	"context"
	"fmt"

	"github.com/zlx05/go-coding/internal/registry"
)

// AsToolDefs lists the remote server's tools and maps each onto a
// registry.ToolDef with zero translation: the MCP inputSchema already has the
// shape registry expects, and Run performs a tools/call over the wire. Names
// are namespaced "mcp:<label>:<tool>" so several servers can be connected at
// once without colliding.
func (c *Client) AsToolDefs(ctx context.Context) ([]registry.ToolDef, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	defs := make([]registry.ToolDef, 0, len(tools))
	for _, t := range tools {
		name := t.Name
		if c.label != "" {
			name = "mcp:" + c.label + ":" + name
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		defs = append(defs, registry.ToolDef{
			Name:        name,
			Description: "remote MCP tool " + t.Name + ": " + t.Description,
			Schema:      schema,
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				res, err := c.CallTool(ctx, t.Name, args)
				if err != nil {
					return nil, err
				}
				if res.IsError {
					return nil, fmt.Errorf("remote tool %q failed: %s", t.Name, res.Text)
				}
				return res.Text, nil
			},
		})
	}
	return defs, nil
}
