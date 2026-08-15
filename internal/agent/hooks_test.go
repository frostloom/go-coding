package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/pkg/llm"
)

// TestBeforeEditHookVetoesAndRepairs verifies the P2 hooks loop: a
// before_edit hook that vetoes an edit routes the agent into REPAIRING with
// the hook's reason fed back as memory — the same repair-before-test loop as
// edit validation — and the agent recovers on the next plan.
func TestBeforeEditHookVetoesAndRepairs(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	// Plan 1 edits mathops.go; the hook vetoes it. Plan 2 edits it again and
	// the hook (one-shot) allows it through.
	voted := false
	mock := newMockChat([]string{
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"plan 1: fix Add"}]}`,
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"plan 2: fix Add"}]}`,
	})
	rc := &recordingClient{ChatClient: mock}
	a := New(rc, Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function",
		MaxRounds:   3,
		Hooks: map[HookPoint][]Hook{
			HookBeforeEdit: {func(_ context.Context, ev Event) error {
				if strings.HasPrefix(ev.Detail, "mathops.go") && !voted {
					voted = true
					return errTestHook("policy: mathops.go is protected this round")
				}
				return nil
			}},
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected agent to pass after the hook veto + repair, got state=%s note=%s", res.State, res.Note)
	}
	if res.Rounds != 2 {
		t.Errorf("expected 2 rounds (plan + repair), got %d", res.Rounds)
	}

	// The repair request must carry the hook's veto reason — proof the loop
	// fed the governance rejection back instead of silently dropping it.
	if len(rc.requests) < 2 {
		t.Fatalf("expected >= 2 LLM requests, got %d", len(rc.requests))
	}
	var joined string
	for _, m := range rc.requests[1].Messages {
		joined += m.Content
	}
	if !strings.Contains(joined, "protected") {
		t.Errorf("repair request should contain the hook veto reason, got:\n%s", joined)
	}

	// The vetoed path must still exist (the edit never landed).
	if _, err := os.Stat(filepath.Join(dir, "mathops.go")); err != nil {
		t.Errorf("mathops.go should still exist: %v", err)
	}
}

// TestUsageAccumulatesAcrossCalls verifies token accounting: every Chat
// response's usage is summed into Session.Usage and surfaced on Result.
func TestUsageAccumulatesAcrossCalls(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	// plan 1 is wrong (test fails) → repair plan 2 is correct → 2 Chat calls.
	mock := newMockChat([]string{
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a - b\n}\n","desc":"wrong: subtract"}]}`,
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"correct: add"}]}`,
	})
	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	uc := &usageClient{ChatClient: mock, perCall: per}
	a := New(uc, Config{RepoPath: dir, Requirement: "Fix the Add function", MaxRounds: 3})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected agent to pass, got state=%s note=%s", res.State, res.Note)
	}
	if res.Usage.TotalTokens != 2*per.TotalTokens {
		t.Errorf("total tokens = %d, want %d (2 calls x 150)", res.Usage.TotalTokens, 2*per.TotalTokens)
	}
	if res.Usage.PromptTokens != 2*per.PromptTokens || res.Usage.CompletionTokens != 2*per.CompletionTokens {
		t.Errorf("usage = %+v, want prompt=%d completion=%d", res.Usage, 2*per.PromptTokens, 2*per.CompletionTokens)
	}
}

type errTestHook string

func (e errTestHook) Error() string { return string(e) }

// usageClient injects a per-call Usage into whatever client it wraps, so the
// accumulation path can be tested without a real gateway.
type usageClient struct {
	llm.ChatClient
	perCall llm.Usage
}

func (u *usageClient) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	resp, err := u.ChatClient.Chat(ctx, req)
	resp.Usage = u.perCall
	return resp, err
}
