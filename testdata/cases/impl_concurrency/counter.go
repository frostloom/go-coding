package counter

// Counter counts things. The current implementation is NOT safe for
// concurrent use: when many goroutines call Increment at once, updates get
// lost and the final Value() is wrong.
type Counter struct {
	n int
}

// Increment adds 1 to the counter.
func (c *Counter) Increment() {
	c.n++
}

// Value returns the current count.
func (c *Counter) Value() int {
	return c.n
}
