package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Handler processes one task payload. It runs outside the queue package's
// concern — here it is the agent loop (see NewAgentTaskHandler in task.go),
// but the WorkerPool is intentionally generic.
type Handler func(ctx context.Context, payload []byte) error

// WorkerPool runs N goroutines that each consume tasks from a shared Queue.
// Each task is handled concurrently and independently — the horizontal
// throughput half of the story (a single agent is vertical capability).
//
// Stop cancels the workers' *consuming* context so BlockingPop returns and
// they stop pulling new tasks, then waits for in-flight handlers to finish —
// a graceful shutdown never aborts a task mid-run (each task has its own
// RunTimeout as a backstop).
type WorkerPool struct {
	workers int
	queue   Queue
	handler Handler

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	processed atomic.Int64
	failed    atomic.Int64
}

// NewWorkerPool builds a pool with n workers consuming from q.
func NewWorkerPool(n int, q Queue, h Handler) *WorkerPool {
	return &WorkerPool{workers: n, queue: q, handler: h}
}

// Start launches the workers. It returns immediately; Stop must be called to
// shut them down.
func (p *WorkerPool) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return
	}
	p.started = true
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.run(ctx)
	}
}

// run is one worker's loop: block on the queue, handle the task.
//
// BlockingPop gets the cancellable ctx (Stop makes it return); the handler
// gets a fresh background ctx so it always runs to completion even after the
// pool has been told to stop consuming.
func (p *WorkerPool) run(consumeCtx context.Context) {
	defer p.wg.Done()
	for {
		payload, err := p.queue.BlockingPop(consumeCtx)
		if err != nil {
			if consumeCtx.Err() != nil {
				return // Stop() called → stop consuming
			}
			time.Sleep(50 * time.Millisecond) // transient error: back off, don't spin
			continue
		}
		if err := p.handler(context.Background(), payload); err != nil {
			p.failed.Add(1)
		} else {
			p.processed.Add(1)
		}
	}
}

// Stop signals the workers to stop consuming and waits for in-flight tasks
// to finish. Safe to call more than once.
func (p *WorkerPool) Stop() {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// Counts reports how many tasks completed successfully / failed.
func (p *WorkerPool) Counts() (processed, failed int64) {
	return p.processed.Load(), p.failed.Load()
}

// String renders a one-line status for dashboards/logs.
func (p *WorkerPool) String() string {
	return fmt.Sprintf("workers=%d processed=%d failed=%d", p.workers, p.processed.Load(), p.failed.Load())
}
