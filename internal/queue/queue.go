// Package queue turns the single-agent coding loop into a concurrent task
// service: tasks (requirement + workspace) are pushed to a Queue and a
// WorkerPool consumes them in parallel, each in its own isolated sandbox.
//
// Two backends implement the same Queue interface:
//   - InlineQueue — in-memory, deterministic, zero infrastructure. Used by
//     mock tests and when Redis is unavailable; this keeps the whole system
//     runnable with `go test` and no external service.
//   - RedisQueue — a hand-written RESP2 client (no go-redis SDK) over
//     LPUSH/BRPOP. Production transport; see resp.go.
//
// The throughput story: one agent is vertical capability, a queue + worker
// pool is horizontal capacity.
package queue

import (
	"context"
	"time"
)

// Queue is the task transport. Implementations must be safe for concurrent
// use by multiple workers.
type Queue interface {
	// Push appends one payload. Implementations should block only until the
	// payload is accepted.
	Push(ctx context.Context, payload []byte) error
	// BlockingPop removes and returns the next payload, blocking until one is
	// available or ctx is cancelled. Implementations may return a context
	// error (timeout/cancel) with no payload.
	BlockingPop(ctx context.Context) ([]byte, error)
	// Close releases any resources (connections, conds). Pending payloads may
	// be lost; callers should drain before Close if that matters.
	Close() error
}

// InlineQueue is an in-memory FIFO guarded by a mutex + condition variable.
// BlockingPop waits on the cond when empty, so workers sleep instead of
// spinning. Deterministic and dependency-free — the CI-safe default.
type InlineQueue struct {
	ch    chan []byte
	close chan struct{}
}

// NewInlineQueue builds an in-memory queue with the given buffer depth.
func NewInlineQueue(capacity int) *InlineQueue {
	return &InlineQueue{ch: make(chan []byte, capacity), close: make(chan struct{})}
}

// Push appends a payload; it does not block unless the buffer is full.
func (q *InlineQueue) Push(_ context.Context, payload []byte) error {
	select {
	case q.ch <- payload:
		return nil
	case <-q.close:
		return errClosed
	}
}

// BlockingPop returns the next payload or waits for one.
func (q *InlineQueue) BlockingPop(ctx context.Context) ([]byte, error) {
	select {
	case p := <-q.ch:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-q.close:
		return nil, errClosed
	}
}

// Close stops the queue; pending payloads in the buffer remain readable.
func (q *InlineQueue) Close() error {
	close(q.close)
	return nil
}

// errClosed is returned when an operation hits a closed queue.
var errClosed = &queueClosedError{}

type queueClosedError struct{}

func (e *queueClosedError) Error() string { return "queue: closed" }

var _ Queue = (*InlineQueue)(nil)

// Ping is a tiny health probe used by callers that want to verify a remote
// transport (Redis) is reachable before starting workers.
type Ping func(ctx context.Context) error

// DefaultTimeout bounds a single queue operation. BlockingPop uses the
// caller's context; Push and Close are quick but still bounded.
const DefaultTimeout = 5 * time.Second
