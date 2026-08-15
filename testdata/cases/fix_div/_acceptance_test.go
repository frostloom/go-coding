package fixdiv

import "testing"

// Hidden acceptance test: stashed out of the agent's view by the Harness
// and only applied after the run, so the agent cannot edit the test that
// grades it.
func TestMeanAcceptance(t *testing.T) {
	if got := Mean(nil); got != 0 {
		t.Fatalf("Mean(nil) = %v, want 0", got)
	}
	if got := Mean([]float64{7}); got != 7 {
		t.Fatalf("Mean([7]) = %v, want 7", got)
	}
	if got := Mean([]float64{1, 2, 3, 4}); got != 2.5 {
		t.Fatalf("Mean([1,2,3,4]) = %v, want 2.5", got)
	}
	if got := Mean([]float64{100, 200, 300, 400}); got != 250 {
		t.Fatalf("Mean([100,200,300,400]) = %v, want 250", got)
	}
}
