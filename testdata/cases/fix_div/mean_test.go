package fixdiv

import "testing"

// Visible test: the agent can see this and it fails on the buggy code,
// which is what drives the repair loop.
func TestMean(t *testing.T) {
	if got := Mean([]float64{10, 20, 30}); got != 20 {
		t.Fatalf("Mean([10,20,30]) = %v, want 20", got)
	}
	if got := Mean([]float64{2, 4}); got != 3 {
		t.Fatalf("Mean([2,4]) = %v, want 3", got)
	}
}
