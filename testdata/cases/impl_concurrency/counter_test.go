package counter

import (
	"sync"
	"testing"
)

// Visible test: the agent can see this. TestCounterConcurrent fails on the
// non-safe implementation (lost updates), which drives the repair loop.
func TestCounterBasic(t *testing.T) {
	var c Counter
	c.Increment()
	c.Increment()
	if got := c.Value(); got != 2 {
		t.Fatalf("Value() = %d, want 2", got)
	}
}

func TestCounterConcurrent(t *testing.T) {
	var c Counter
	var wg sync.WaitGroup
	// Heavy enough that a non-atomic read-modify-write reliably loses
	// updates: 100 goroutines x 100_000 increments.
	const goroutines, perGoroutine = 100, 100_000
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				c.Increment()
			}
		}()
	}
	wg.Wait()
	want := goroutines * perGoroutine
	if got := c.Value(); got != want {
		t.Fatalf("Value() = %d, want %d (lost updates: not concurrency-safe)", got, want)
	}
}
