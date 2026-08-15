package fixoffbyone

import "testing"

// Visible test: the agent can see this and it fails on the off-by-one loop
// (SumTo(5) skips 5 and returns 10 instead of 15), which drives the repair.
func TestSumTo(t *testing.T) {
	if got := SumTo(5); got != 15 {
		t.Fatalf("SumTo(5) = %d, want 15", got)
	}
}
