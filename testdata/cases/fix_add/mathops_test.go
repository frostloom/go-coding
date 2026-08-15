package fixadd

import "testing"

// Visible test: the agent can see this and it fails on the buggy code,
// which is what drives the repair loop.
func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2,3) = %d, want 5", got)
	}
	if got := Add(0, 0); got != 0 {
		t.Fatalf("Add(0,0) = %d, want 0", got)
	}
}
