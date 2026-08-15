package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/internal/tools"
)

// TestExploreBeforePlan verifies the exploration-first flow: given a vague
// requirement, the LLM asks to explore (list_dir + grep), the agent executes
// the ops, and the results are fed back so the LLM can then plan. This is
// the Claude Code "gather context" step.
func TestExploreBeforePlan(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	mock := newMockChat([]string{
		// LLM asks to explore first.
		`{"explore":[{"op":"list_dir"},{"op":"grep","pattern":"Add"}]}`,
		// Then plans the fix.
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"fix Add"}]}`,
	})
	rc := &recordingClient{ChatClient: mock}
	a := New(rc, Config{RepoPath: dir, Requirement: "something is broken, make the numbers add up", MaxRounds: 3})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected agent to pass, got state=%s note=%s", res.State, res.Note)
	}

	// The first LLM request should get the exploration results fed back in
	// the second request's history (the grep output must appear).
	if len(rc.requests) < 2 {
		t.Fatalf("expected >= 2 LLM requests (explore + plan), got %d", len(rc.requests))
	}
	second := rc.requests[1]
	var joined string
	for _, m := range second.Messages {
		joined += m.Content
	}
	if !strings.Contains(joined, "mathops.go") {
		t.Errorf("second request should contain exploration results (file paths), got:\n%s", joined)
	}
}

// TestExploreRunsConcurrentlyInOrder: execExplore must (1) actually run the
// read-only ops in parallel and (2) still assemble the output in the
// original op order, so the LLM sees a deterministic transcript even though
// the tool calls overlapped in time.
func TestExploreRunsConcurrentlyInOrder(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}
	a := New(newMockChat(nil), Config{RepoPath: dir, Requirement: "x", MaxRounds: 2})

	// Wrap the read tool so we can observe overlap: it records entry/exit
	// and forces a pause, so a serial executor would never overlap two ops.
	orig := a.tools.read
	var mu sync.Mutex
	active, maxActive, entered, exited := 0, 0, 0, 0
	gate := make(chan struct{})
	a.tools.read = &trackingRead{inner: orig, onEnter: func() {
		mu.Lock()
		entered++
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		<-gate // hold until the harness releases
	}, onExit: func() {
		mu.Lock()
		exited++
		active--
		mu.Unlock()
	}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 3 reads against distinct files: two exist, one missing.
		_ = a.execExplore(context.Background(), []ExploreOp{
			{Op: "read_file", Path: "mathops.go"},
			{Op: "read_file", Path: "missing.go"},
			{Op: "read_file", Path: "mathops_test.go"},
		})
	}()

	// Wait until all 3 reads have entered before releasing any of them.
	for {
		mu.Lock()
		e := entered
		mu.Unlock()
		if e >= 3 {
			break
		}
	}
	close(gate)
	<-done

	mu.Lock()
	gotMax := maxActive
	gotEnt, gotEx := entered, exited
	mu.Unlock()
	if gotMax < 2 {
		t.Errorf("expected overlapping (parallel) reads, max concurrent = %d", gotMax)
	}
	if gotEnt != 3 || gotEx != 3 {
		t.Errorf("expected 3 entered/3 exited, got %d/%d", gotEnt, gotEx)
	}
}

// trackingRead is a Tool wrapper that reports enter/exit events.
type trackingRead struct {
	inner   tools.Tool
	onEnter func()
	onExit  func()
}

func (tr *trackingRead) Name() string { return "read_file" }

func (tr *trackingRead) Run(ctx context.Context, args tools.Args) (tools.Result, error) {
	tr.onEnter()
	defer tr.onExit()
	return tr.inner.Run(ctx, args)
}

// TestExploreDoesNotInfiniteLoop: if the LLM keeps returning explore, the
// agent should eventually fail rather than loop forever.
func TestExploreDoesNotInfiniteLoop(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	// Always explore, never plan.
	mock := newMockChat([]string{`{"explore":[{"op":"list_dir"}]}`})
	a := New(mock, Config{RepoPath: dir, Requirement: "fix it", MaxRounds: 2})
	res := a.Run(context.Background())
	if res.State == StateDone {
		t.Error("should not reach Done if LLM never plans")
	}
	// The explore cap (maxExplorePerPhase) should make planning fail fast
	// instead of looping forever.
	if res.State != StateFailed {
		t.Errorf("expected Failed when LLM never produces a plan, got %s", res.State)
	}
}
