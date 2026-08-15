package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChatHonorsPerRequestModel proves a non-empty ChatRequest.Model overrides
// the client's configured model (the per-role model tiering seam), while an
// empty one falls back to the configured model — the behavior of every existing
// caller, so nothing downstream changes.
func TestChatHonorsPerRequestModel(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	client, err := NewOpenAI(Config{BaseURL: srv.URL, APIKey: "k", Model: "base", Stream: false})
	if err != nil {
		t.Fatal(err)
	}

	// Per-request model wins.
	if _, err := client.Chat(context.Background(), ChatRequest{
		Model:    "child",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("chat with override: %v", err)
	}
	if gotModel != "child" {
		t.Errorf("model with override = %q, want child", gotModel)
	}

	// Empty request model falls back to the client's configured model.
	if _, err := client.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("chat without override: %v", err)
	}
	if gotModel != "base" {
		t.Errorf("model without override = %q, want base", gotModel)
	}
}
