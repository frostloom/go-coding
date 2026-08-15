package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ServerInfo is what initialize returns about the remote server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Initialize performs the MCP handshake: negotiates the protocol version and
// learns the server's identity and capabilities. The result is kept on the
// client for inspection (e.g. a tool-description prefix).
func (c *Client) Initialize(ctx context.Context) (ServerInfo, error) {
	raw, err := c.transport.Call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
	})
	if err != nil {
		return ServerInfo{}, err
	}
	var info struct {
		ServerInfo ServerInfo      `json:"serverInfo"`
		Protocol   string          `json:"protocolVersion"`
		Capab      map[string]any  `json:"capabilities"`
		Meta       json.RawMessage `json:"_meta"`
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &info)
	}
	c.info = map[string]any{"protocolVersion": info.Protocol, "serverInfo": info.ServerInfo}
	// Note: the standard post-initialize "notifications/initialized" is a
	// JSON-RPC notification (no response). Our Call waits for a response, so
	// we skip it — tools/list and tools/call work without the ack.
	return info.ServerInfo, nil
}

// ToolInfo mirrors one entry of tools/list. InputSchema is already the JSON
// Schema shape registry.ToolDef uses, so a remote tool maps with zero
// translation.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListTools returns the remote server's tool list.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	raw, err := c.transport.Call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []ToolInfo `json:"tools"`
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp client tools/list: %w", err)
		}
	}
	return res.Tools, nil
}

// CallToolResult is one tools/call outcome. IsError mirrors the MCP flag:
// tool runtime failures are content the host LLM should read, not protocol
// errors.
type CallToolResult struct {
	Text    string
	IsError bool
}

// CallTool invokes a remote tool and flattens its content into the text an
// LLM consumes.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	raw, err := c.transport.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallToolResult{}, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &res); err != nil {
			return CallToolResult{}, fmt.Errorf("mcp client tools/call: %w", err)
		}
	}
	var sb strings.Builder
	for _, item := range res.Content {
		if item.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(item.Text)
		}
	}
	if sb.Len() == 0 && raw != nil {
		// Non-text content (or an empty payload): surface the raw result so
		// the caller still sees something.
		return CallToolResult{Text: string(raw), IsError: res.IsError}, nil
	}
	return CallToolResult{Text: sb.String(), IsError: res.IsError}, nil
}
