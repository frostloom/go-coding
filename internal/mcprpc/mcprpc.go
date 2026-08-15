// Package mcprpc is the hand-written JSON-RPC 2.0 wire layer shared by the
// MCP server (internal/mcpserver) and the MCP client (internal/mcpclient).
// One protocol, two directions: the server dispatches requests, the client
// issues them, and both speak the same newline-delimited / single-POST JSON
// framing — so a client connecting to go-coding's own server, or to any
// MCP server, costs zero protocol translation.
package mcprpc

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the MCP spec version the server implements. It is kept
// at 2025-03-26 for the legacy initialize handshake (old clients still
// negotiate that way).
const ProtocolVersion = "2025-03-26"

// SupportedProtocolVersions is what server/discover advertises (2026-07-28):
// the legacy 2025-03-26 handshake PLUS the stateless 2026-07-28 core. Our
// dispatch is per-request JSON-RPC with no session state, so both serve from
// the same Handle — the stateless core was already the architecture, which is
// exactly the direction the 2026-07-28 spec moved the protocol.
var SupportedProtocolVersions = []string{"2026-07-28", "2025-03-26"}

// Request is a JSON-RPC 2.0 request or notification. A missing ID marks
// a notification (no response expected).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response (result XOR error).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError follows the JSON-RPC error object shape.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes (reserved range).
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	// CodeUnsupportedProtocolVersion is the 2026-07-28 spec's error for a
	// version the server does not support; the message lists supported versions.
	CodeUnsupportedProtocolVersion = -32022
)

// IsNotification reports whether the request carries no ID.
func (r *Request) IsNotification() bool { return len(r.ID) == 0 }

// MarshalResponse builds the wire bytes for a successful response.
func MarshalResponse(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(Response{JSONRPC: "2.0", ID: id, Result: result})
	return b
}

// MarshalError builds the wire bytes for an error response.
func MarshalError(id json.RawMessage, code int, message string) []byte {
	b, _ := json.Marshal(Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}})
	return b
}

// ParseRequest validates and decodes one JSON-RPC message.
func ParseRequest(line []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if req.JSONRPC != "2.0" {
		return nil, fmt.Errorf("jsonrpc must be \"2.0\"")
	}
	if req.Method == "" {
		return nil, fmt.Errorf("missing method")
	}
	return &req, nil
}
