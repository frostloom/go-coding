package mcprpc

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseMarshalRoundTrip verifies a request parses and a response round-trips
// through the wire bytes — the invariant both the server's Handle and the
// client's Call rely on.
func TestParseMarshalRoundTrip(t *testing.T) {
	req, err := ParseRequest([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a.go"}}}`))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if req.Method != "tools/call" {
		t.Errorf("method = %q, want tools/call", req.Method)
	}
	if req.IsNotification() {
		t.Error("request with id must not be a notification")
	}

	out := MarshalResponse(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi"}}})
	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	content := resp.Result.(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if content != "hi" {
		t.Errorf("content = %v, want hi", content)
	}
}

// TestParseRequestRejectsInvalid verifies the two wire-level guards: a message
// that is not jsonrpc 2.0, and one with no method, are both rejected.
func TestParseRequestRejectsInvalid(t *testing.T) {
	for _, bad := range []string{
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1}`,
		`not json`,
	} {
		if _, err := ParseRequest([]byte(bad)); err == nil {
			t.Errorf("ParseRequest(%q) should error", bad)
		}
	}
}

// TestNotificationNoID verifies a request without an id is flagged as a
// notification so the caller knows no response will come.
func TestNotificationNoID(t *testing.T) {
	req, err := ParseRequest([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !req.IsNotification() {
		t.Error("missing id should mark notification")
	}
}

// TestMarshalErrorCarriesCode verifies error responses carry the JSON-RPC
// error object (code + message) that the client surfaces.
func TestMarshalErrorCarriesCode(t *testing.T) {
	out := MarshalError(json.RawMessage(`"abc"`), CodeMethodNotFound, "nope")
	if !strings.Contains(string(out), `-32601`) {
		t.Errorf("error response missing code: %s", out)
	}
	if !strings.Contains(string(out), `"nope"`) {
		t.Errorf("error response missing message: %s", out)
	}
}
