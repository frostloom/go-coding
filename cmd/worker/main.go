// Command worker turns the single-agent coding loop into a concurrent task
// service: it consumes tasks from a queue (inline in-memory or a Redis List)
// and runs each one in its own isolated workspace via the real agent loop.
//
//	worker -mode inline -workers 4 -bench 5 -mock                 # throughput demo, no key needed
//	worker -mode redis  -workers 4 -redis-addr localhost:6379     # consume Redis tasks
//	worker -push -mode redis -repo testdata/cases/fix_add -req "fix Add"  # submit one task
//
// Throughput story: a single agent is vertical capability; a queue + worker
// pool is horizontal capacity. -bench enqueues N tasks then consumes them,
// printing wall time and concurrency so the speedup is measurable.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zlx05/go-coding/internal/queue"
	"github.com/zlx05/go-coding/pkg/llm"
)

func main() {
	mode := flag.String("mode", "inline", "queue transport: inline (in-memory) | redis")
	workers := flag.Int("workers", 2, "number of worker goroutines")
	bench := flag.Int("bench", 0, "if >0: enqueue this many tasks, then consume them and print throughput")
	push := flag.Bool("push", false, "submit a single task and exit (instead of consuming)")
	repo := flag.String("repo", filepath.Join("testdata", "cases", "fix_add"), "base repo copied into each task workspace")
	req := flag.String("req", "The Add function is broken. Fix it so it returns the correct sum of its two integer arguments.", "task requirement")
	mock := flag.Bool("mock", false, "use a scripted LLM (deterministic, no OPENAI_* needed) — for -bench demo")
	redisAddr := flag.String("redis-addr", "localhost:6379", "Redis address (with -mode redis)")
	queueKey := flag.String("queue-key", "go-coding:queue", "Redis List key (with -mode redis)")
	runTimeout := flag.Duration("run-timeout", 120*time.Second, "per-task run tool timeout")
	flag.Parse()

	// Repo-root .env (the user's live keys) feeds buildClient below. Missing
	// file or a shell export wins either way — .env only fills unset vars.
	llm.LoadEnvFile(".env")

	q := buildQueue(*mode, *redisAddr, *queueKey)

	if *push {
		doPush(q, *repo, *req)
		return
	}

	lc := buildClient(*mock)
	handler := queue.NewAgentTaskHandler(lc, *runTimeout, func(r queue.TaskResult) {
		log.Printf("[result] id=%s passed=%v rounds=%d toolCalls=%d dur=%s note=%s",
			r.ID, r.Passed, r.Rounds, r.ToolCalls, r.Duration.Round(time.Millisecond), r.Note)
	})

	if *bench > 0 {
		runBench(q, handler, *bench, *repo, *req)
		return
	}

	// Pure consumer: block until interrupted.
	pool := queue.NewWorkerPool(*workers, q, handler)
	pool.Start()
	log.Printf("worker consuming from %s queue (%d workers); ctrl-c to stop", *mode, *workers)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	pool.Stop()
	log.Printf("stopped: %s", pool.String())
}

// buildQueue constructs the queue for the selected transport.
func buildQueue(mode, redisAddr, queueKey string) queue.Queue {
	if mode == "redis" {
		q := queue.NewRedisQueue(redisAddr, queueKey)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := q.Ping(ctx); err != nil {
			log.Fatalf("redis unreachable at %s: %v", redisAddr, err)
		}
		return q
	}
	return queue.NewInlineQueue(128)
}

// buildClient returns the LLM client: the scripted demo brain when -mock is
// set (so -bench needs no API key), otherwise the real model from env.
func buildClient(mock bool) llm.ChatClient {
	if mock {
		return scriptedLLM{}
	}
	cfg := llm.ConfigFromEnv()
	lc, err := llm.NewOpenAI(cfg)
	if err != nil {
		log.Fatalf("llm: %v (set OPENAI_MODEL/OPENAI_BASE_URL/OPENAI_API_KEY, or use -mock)", err)
	}
	return lc
}

// doPush submits one task to the queue and exits.
func doPush(q queue.Queue, repo, req string) {
	task := queue.NewTask("task-"+fmt.Sprint(time.Now().UnixNano()), repo, req)
	payload, err := task.Marshal()
	if err != nil {
		log.Fatalf("marshal task: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Push(ctx, payload); err != nil {
		log.Fatalf("push: %v", err)
	}
	log.Printf("pushed task %s (repo=%s)", task.ID, repo)
}

// runBench enqueues n tasks then consumes them with the pool, measuring wall
// time and peak concurrency — the throughput proof.
func runBench(q queue.Queue, handler queue.Handler, n int, repo, req string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for i := 0; i < n; i++ {
		task := queue.NewTask(fmt.Sprintf("bench-%d", i), repo, req)
		p, err := task.Marshal()
		if err != nil {
			log.Fatalf("marshal: %v", err)
		}
		if err := q.Push(ctx, p); err != nil {
			log.Fatalf("push %d: %v", i, err)
		}
	}
	log.Printf("enqueued %d tasks; starting workers", n)

	var maxActive atomic.Int32
	var active atomic.Int32
	wrapped := func(ctx context.Context, payload []byte) error {
		cur := active.Add(1)
		defer active.Add(-1)
		for {
			m := maxActive.Load()
			if cur <= m || maxActive.CompareAndSwap(m, cur) {
				break
			}
		}
		return handler(ctx, payload)
	}

	pool := queue.NewWorkerPool(4, q, wrapped)
	start := time.Now()
	pool.Start()
	// Wait for ALL tasks to be consumed and finished before stopping; stopping
	// early would cancel the workers' consuming and leave queued tasks
	// untouched (and skew the throughput numbers).
	deadline := time.Now().Add(10 * time.Minute)
	for {
		processed, _ := pool.Counts()
		if processed >= int64(n) || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	pool.Stop()
	elapsed := time.Since(start)
	processed, failed := pool.Counts()
	log.Printf("bench done: n=%d processed=%d failed=%d wall=%s peakConcurrent=%d",
		n, processed, failed, elapsed.Round(time.Millisecond), maxActive.Load())
}

// scriptedLLM is the deterministic demo brain: it "fixes" fix_add correctly
// in one shot, so -bench runs without a network/API key.
type scriptedLLM struct{}

func (scriptedLLM) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	plan := `{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"fix Add to sum"}]}`
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: plan}, FinishReason: "stop"}, nil
}
