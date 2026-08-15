package counter

import (
	"sync"
	"testing"
)

// Hidden acceptance test: stashed out of the agent's view by the Harness
// and only applied after the run. Heavier contention than the visible test
// so a sloppy "close enough" fix still fails here.
func TestCounterConcurrentAcceptance(t *testing.T) {
	var c Counter
	var wg sync.WaitGroup
	// Even heavier than the visible test: 150 goroutines x 100_000.
	const goroutines, perGoroutine = 150, 100_000
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
		t.Fatalf("Value() = %d, want %d — concurrent increments lost updates", got, want)
	}
}
