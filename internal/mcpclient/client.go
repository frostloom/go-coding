// Package mcpclient is the hand-written MCP client, the mirror of
// internal/mcpserver. It speaks the same JSON-RPC 2.0 wire protocol
// (internal/mcprpc) over either MCP transport:
//
//   - stdio: spawn an MCP server as a child process, newline-delimited JSON
//     over stdin/stdout (the local transport — the same shape the server's
//     ServeIO implements);
//   - HTTP:  POST /mcp against a running server (the server's ServeHTTP).
//
// A client discovers a remote server's tools via tools/list and registers
// them into the agent's dynamic tool registry (internal/registry) with zero
// translation, so the agent can call any MCP server's tools with the same
// {"call_tool":{...}} primitive it uses for skills and subagents.
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/zlx05/go-coding/internal/mcprpc"
)

// RPCError is a JSON-RPC error surfaced from the server (e.g. method not
// found, invalid params). Code follows the JSON-RPC reserved range.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp rpc %d: %s", e.Code, e.Message) }

// transport is one wire backend. Call performs one JSON-RPC round trip.
type transport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Close() error
}

// Client talks to one MCP server. It is not safe for concurrent use — an
// agent issues tool calls sequentially, one at a time.
type Client struct {
	transport transport
	label     string // name prefix for registered tool defs ("mcp:<label>:<tool>")
	info      map[string]any
}

// stdioTransport drives a child process over its stdin/stdout pipes.
type stdioTransport struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	sc     *bufio.Scanner
	mu     sync.Mutex
	nextID int
}

// newStdioTransport wires a transport over existing pipes (used by the
// self-dogfooding tests, where the server runs in-process behind io.Pipes).
func newStdioTransport(in io.WriteCloser, out io.Reader) *stdioTransport {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	return &stdioTransport{in: in, sc: sc}
}

// dialStdioProcess spawns a server subprocess and wires its pipes.
func dialStdioProcess(ctx context.Context, name string, args []string, env []string) (*stdioTransport, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp client stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp client stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp client start %q: %w", name, err)
	}
	return newStdioTransport(stdin, stdout), nil
}

// Call performs one JSON-RPC round trip and returns the raw result.
func (t *stdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	t.nextID++
	req := mcprpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.Itoa(t.nextID)),
		Method:  method,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp client marshal params: %w", err)
		}
		req.Params = b
	}
	line, _ := json.Marshal(req)
	if _, err := t.in.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("mcp client write: %w", err)
	}
	if !t.sc.Scan() {
		if err := t.sc.Err(); err != nil {
			return nil, fmt.Errorf("mcp client read: %w", err)
		}
		return nil, io.EOF
	}
	var resp mcprpc.Response
	if err := json.Unmarshal(bytes.TrimSpace(t.sc.Bytes()), &resp); err != nil {
		return nil, fmt.Errorf("mcp client decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, &RPCError{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if resp.Result == nil {
		return nil, nil
	}
	return json.Marshal(resp.Result)
}

// Close terminates the child process.
func (t *stdioTransport) Close() error {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_, _ = t.cmd.Process.Wait()
	}
	return t.in.Close()
}

// httpTransport drives a running server over POST /mcp.
type httpTransport struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	nextID int
}

// Call performs one JSON-RPC round trip against /mcp.
func (t *httpTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	req := mcprpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(strconv.Itoa(t.nextID)),
		Method:  method,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp client marshal params: %w", err)
		}
		req.Params = b
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp client request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp client http: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("mcp client read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("mcp client http %d: %s", resp.StatusCode, respBody)
	}
	var rpc mcprpc.Response
	if err := json.Unmarshal(respBody, &rpc); err != nil {
		return nil, fmt.Errorf("mcp client decode response: %w", err)
	}
	if rpc.Error != nil {
		return nil, &RPCError{Code: rpc.Error.Code, Message: rpc.Error.Message}
	}
	if rpc.Result == nil {
		return nil, nil
	}
	return json.Marshal(rpc.Result)
}

// Close is a no-op for HTTP transports (the server is independent).
func (t *httpTransport) Close() error { return nil }

// DialStdio spawns an MCP server subprocess and returns a client for it.
// label prefixes registered tool names ("mcp:<label>:<tool>"). env is extra
// environment for the child (KEY=VALUE pairs), appended after os.Environ().
func DialStdio(ctx context.Context, label, name string, args, env []string) (*Client, error) {
	t, err := dialStdioProcess(ctx, name, args, env)
	if err != nil {
		return nil, err
	}
	return &Client{transport: t, label: label}, nil
}

// DialHTTP connects to a running MCP server at url (the /mcp endpoint).
func DialHTTP(ctx context.Context, label, url string) (*Client, error) {
	return &Client{
		transport: &httpTransport{url: url, client: &http.Client{}},
		label:     label,
	}, nil
}

// newClient wraps a pre-built transport (used by tests running the server
// in-process).
func newClient(label string, t transport) *Client {
	return &Client{transport: t, label: label}
}

// Close releases the transport (kills the child process for stdio).
func (c *Client) Close() error { return c.transport.Close() }
