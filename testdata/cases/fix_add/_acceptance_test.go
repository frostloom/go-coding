package fixadd

import "testing"

// Hidden acceptance test: stashed out of the agent's view by the Harness
// and only applied after the run, so the agent cannot edit the test that
// grades it.
func TestAddAcceptance(t *testing.T) {
	if got := Add(10, 20); got != 30 {
		t.Fatalf("Add(10,20) = %d, want 30", got)
	}
	if got := Add(-5, 5); got != 0 {
		t.Fatalf("Add(-5,5) = %d, want 0", got)
	}
}
