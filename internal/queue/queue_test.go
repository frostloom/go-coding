package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zlx05/go-coding/pkg/llm"
)

// TestInlineQueueFIFO verifies the in-memory queue preserves order.
func TestInlineQueueFIFO(t *testing.T) {
	q := NewInlineQueue(4)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := q.Push(ctx, []byte(fmt.Sprintf("task-%d", i))); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		p, err := q.BlockingPop(ctx)
		if err != nil {
			t.Fatalf("pop %d: %v", i, err)
		}
		if string(p) != fmt.Sprintf("task-%d", i) {
			t.Errorf("pop %d = %q, want task-%d", i, p, i)
		}
	}
}

// TestWorkerPoolParallel verifies the pool processes tasks concurrently: with
// N workers and each handler sleeping, wall time must be ~Nx shorter than
// serial — the throughput claim, measured.
func TestWorkerPoolParallel(t *testing.T) {
	const tasks, workers = 8, 4
	const perTaskSleep = 300 * time.Millisecond

	q := NewInlineQueue(tasks)
	for i := 0; i < tasks; i++ {
		_ = q.Push(context.Background(), []byte(fmt.Sprintf("t%d", i)))
	}

	var active atomic.Int32
	var maxActive atomic.Int32
	handler := func(_ context.Context, _ []byte) error {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(perTaskSleep)
		active.Add(-1)
		return nil
	}

	p := NewWorkerPool(workers, q, handler)
	start := time.Now()
	p.Start()
	// Wait for all tasks to be processed BEFORE stopping: stopping early would
	// cancel the workers' consuming and leave queued tasks unconsumed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		done, _ := p.Counts()
		if done >= tasks || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.Stop()
	elapsed := time.Since(start)

	if maxActive.Load() < 2 {
		t.Errorf("expected concurrent execution (maxActive>=2), got %d", maxActive.Load())
	}
	// 8 tasks * 300ms with 4 workers ≈ 600ms; serial would be 2400ms. Allow
	// generous slack for scheduling.
	if elapsed > 1600*time.Millisecond {
		t.Errorf("expected parallel speedup, elapsed=%v (serial would be ~2.4s)", elapsed)
	}
	if got, _ := p.Counts(); got != tasks {
		t.Errorf("processed = %d, want %d", got, tasks)
	}
}

// TestWorkerPoolMultipleProducers is a stress smoke: concurrent producers
// pushing while workers consume must not lose or duplicate any payload.
func TestWorkerPoolMultipleProducers(t *testing.T) {
	const producers, perProducer = 4, 25
	const workers = 3
	total := producers * perProducer

	q := NewInlineQueue(64)
	var processed atomic.Int64
	handler := func(_ context.Context, _ []byte) error {
		processed.Add(1)
		return nil
	}
	p := NewWorkerPool(workers, q, handler)
	p.Start()

	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perProducer; j++ {
				_ = q.Push(context.Background(), []byte(fmt.Sprintf("p%d-%d", id, j)))
			}
		}(i)
	}
	wg.Wait()

	// Give workers a moment to drain the buffer.
	deadline := time.Now().Add(3 * time.Second)
	for processed.Load() < int64(total) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	p.Stop()

	if got := processed.Load(); got != int64(total) {
		t.Errorf("processed = %d, want %d", got, total)
	}
}

// TestRedisQueueRoundTrip runs only if Redis is reachable (the CI/dev box has
// it on localhost:6379; elsewhere it skips). Verifies PING + LPUSH/BRPOP via
// the hand-written RESP client.
func TestRedisQueueRoundTrip(t *testing.T) {
	addr := envOr("REDIS_ADDR", "localhost:6379")
	q := NewRedisQueue(addr, "go-coding:test:"+fmt.Sprint(time.Now().UnixNano()))
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := q.Ping(ctx); err != nil {
		t.Skipf("redis not reachable at %s (%v) — skipping", addr, err)
	}

	if err := q.Push(ctx, []byte("hello")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := q.Push(ctx, []byte("world")); err != nil {
		t.Fatalf("push: %v", err)
	}
	first, err := q.BlockingPop(ctx)
	if err != nil {
		t.Fatalf("blocking pop: %v", err)
	}
	if string(first) != "hello" {
		t.Errorf("first = %q, want hello", first)
	}
	second, err := q.BlockingPop(ctx)
	if err != nil {
		t.Fatalf("blocking pop: %v", err)
	}
	if string(second) != "world" {
		t.Errorf("second = %q, want world", second)
	}
}

// TestRunAgentTaskEndToEnd runs one real agent task through the queue's task
// handler with a scripted (mock) LLM client. It proves the queue can carry a
// full coding task: copy repo → agent loop → result.
func TestRunAgentTaskEndToEnd(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	abs, err := filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	task := NewTask("task-1", abs, "The Add function is broken. Fix it so it returns the correct sum of its two integer arguments.")

	var got TaskResult
	handler := NewAgentTaskHandler(&scriptedLLM{reply: planStep("mathops.go", addFix)}, 60*time.Second, func(r TaskResult) {
		got = r
	})

	payload, err := task.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !got.Passed {
		t.Errorf("task should pass with correct mock plan, got %+v", got)
	}
	if got.Rounds != 1 {
		t.Errorf("rounds = %d, want 1", got.Rounds)
	}
	if got.Duration <= 0 {
		t.Errorf("duration should be positive, got %v", got.Duration)
	}
}

// TestTaskMarshalRoundTrip verifies a task survives the queue wire format.
func TestTaskMarshalRoundTrip(t *testing.T) {
	tk := Task{ID: "t1", BaseRepo: "/x", Requirement: "fix it"}
	b, err := tk.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTask(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t1" || got.BaseRepo != "/x" || got.Requirement != "fix it" {
		t.Errorf("round-trip = %+v", got)
	}
	if _, err := DecodeTask([]byte(`{}`)); err == nil {
		t.Error("expected error for missing fields")
	}
}

// --- test helpers ---

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// scriptedLLM is a minimal llm.ChatClient that always returns one reply.
type scriptedLLM struct {
	reply string
}

func (s *scriptedLLM) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: s.reply},
		FinishReason: "stop",
	}, nil
}

// planStep builds a one-step plan JSON string for the scripted LLM.
func planStep(file, content string) string {
	return `{"steps":[{"file":"` + file + `","content":` + quote(content) + `,"desc":"fix"}]}`
}

// quote JSON-encodes a string for safe embedding in planStep.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const addFix = `package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`
