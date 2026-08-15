package fixoffbyone

import "testing"

// Hidden acceptance test: stashed out of the agent's view by the Harness and
// only applied after the run. Pins the loop-boundary cases the visible test
// does not: n=1 (the classic off-by-one trap), n=0, and a large n.
func TestSumToAcceptance(t *testing.T) {
	if got := SumTo(1); got != 1 {
		t.Fatalf("SumTo(1) = %d, want 1 (off-by-one boundary)", got)
	}
	if got := SumTo(0); got != 0 {
		t.Fatalf("SumTo(0) = %d, want 0", got)
	}
	if got := SumTo(3); got != 6 {
		t.Fatalf("SumTo(3) = %d, want 6", got)
	}
	if got := SumTo(100); got != 5050 {
		t.Fatalf("SumTo(100) = %d, want 5050", got)
	}
}
