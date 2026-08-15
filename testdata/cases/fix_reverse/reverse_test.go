package fixreverse

import "testing"

// Visible test: the agent can see this and it fails on the buggy code,
// which is what drives the repair loop.
func TestReverse(t *testing.T) {
	if got := Reverse("ab"); got != "ba" {
		t.Fatalf("Reverse(ab) = %q, want %q", got, "ba")
	}
	if got := Reverse("hello"); got != "olleh" {
		t.Fatalf("Reverse(hello) = %q, want %q", got, "olleh")
	}
}
