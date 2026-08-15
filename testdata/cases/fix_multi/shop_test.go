package shop

import "testing"

// Visible test: fails on the buggy chain (Total is too low), which drives
// the agent to trace invoice.go → discount.go → rebate.go.
func TestTotal(t *testing.T) {
	if got := Total([]float64{100, 200}); got != 270 {
		t.Fatalf("Total([100,200]) = %v, want 270", got)
	}
}
