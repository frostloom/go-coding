package shop

import "testing"

// Hidden acceptance test: stashed out of the agent's view. Checks Rebate
// directly (so a lazy fix that hardcodes the discount in ApplyDiscount still
// fails) and exercises the full chain on edge inputs.
func TestRebateDirect(t *testing.T) {
	if got := Rebate(100); got != 10 {
		t.Fatalf("Rebate(100) = %v, want 10", got)
	}
}

func TestTotalEdge(t *testing.T) {
	if got := Total([]float64{10}); got != 9 {
		t.Fatalf("Total([10]) = %v, want 9", got)
	}
	if got := Total([]float64{}); got != 0 {
		t.Fatalf("Total([]) = %v, want 0", got)
	}
	if got := Total([]float64{1, 2, 3, 4}); got != 9 { // sum 10, minus 10% = 9
		t.Fatalf("Total([1,2,3,4]) = %v, want 9", got)
	}
}
