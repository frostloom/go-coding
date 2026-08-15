package fixreverse

import "testing"

// Hidden acceptance test: stashed out of the agent's view by the Harness
// and only applied after the run, so the agent cannot edit the test that
// grades it.
func TestReverseAcceptance(t *testing.T) {
	if got := Reverse(""); got != "" {
		t.Fatalf("Reverse() = %q, want empty", got)
	}
	if got := Reverse("a"); got != "a" {
		t.Fatalf("Reverse(a) = %q, want a", got)
	}
	if got := Reverse("racecar"); got != "racecar" {
		t.Fatalf("Reverse(racecar) = %q, want racecar", got)
	}
	if got := Reverse("Go"); got != "oG" {
		t.Fatalf("Reverse(Go) = %q, want oG", got)
	}
	if got := Reverse("abcdefghij"); got != "jihgfedcba" {
		t.Fatalf("Reverse(abcdefghij) = %q, want jihgfedcba", got)
	}
}
