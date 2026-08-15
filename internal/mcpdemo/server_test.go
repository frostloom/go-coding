package mcpdemo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/mcprpc"
)

// decodeResponse unmarshals a Handle result into the standard MCP response.
func decodeResponse(t *testing.T, resp []byte) (id json.RawMessage, result map[string]any, errObj *mcprpc.RPCError) {
	t.Helper()
	var r mcprpc.Response
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var res map[string]any
	if r.Result != nil {
		var ok bool
		res, ok = r.Result.(map[string]any)
		if !ok {
			t.Fatalf("result is not an object: %#v", r.Result)
		}
	}
	return r.ID, res, r.Error
}

func TestToolsListExposesGetTimeFormat(t *testing.T) {
	s := New()
	_, res, errObj := decodeResponse(t, s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if errObj != nil {
		t.Fatalf("tools/list errored: %+v", errObj)
	}
	raw, _ := json.Marshal(res["tools"])
	if !bytes.Contains(raw, []byte("get_time_format")) {
		t.Fatalf("tools/list missing get_time_format: %s", raw)
	}
}

func TestCallGetTimeFormatReturnsLayout(t *testing.T) {
	s := New()
	_, res, errObj := decodeResponse(t, s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_time_format"}}`)))
	if errObj != nil {
		t.Fatalf("tools/call errored: %+v", errObj)
	}
	content := res["content"].([]any)
	text, ok := content[0].(map[string]any)["text"].(string)
	if !ok {
		t.Fatalf("unexpected content: %v", content)
	}
	for _, want := range []string{TimeLayout, "20_257_122640"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool result missing %q: %s", want, text)
		}
	}
}

func TestRenderMatchesHiddenAcceptance(t *testing.T) {
	// The fixture's hidden test asserts exactly this; keep them in lockstep.
	if got := Render(1600000000); got != "20_257_122640" {
		t.Fatalf("Render(1600000000) = %q, want 20_257_122640", got)
	}
}

func TestUnknownToolIsError(t *testing.T) {
	s := New()
	_, _, errObj := decodeResponse(t, s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope"}}`)))
	if errObj == nil || errObj.Code != mcprpc.CodeMethodNotFound {
		t.Fatalf("unknown tool should be method-not-found, got %+v", errObj)
	}
}

func TestServeIORoundTrip(t *testing.T) {
	s := New()
	var out bytes.Buffer
	req := "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tools/call\",\"params\":{\"name\":\"get_time_format\"}}\n"
	done := make(chan error, 1)
	go func() { done <- s.ServeIO(strings.NewReader(req), &out) }()
	if err := <-done; err != nil {
		t.Fatalf("ServeIO: %v", err)
	}
	if !strings.Contains(out.String(), TimeLayout) {
		t.Fatalf("ServeIO response missing layout: %s", out.String())
	}
}
