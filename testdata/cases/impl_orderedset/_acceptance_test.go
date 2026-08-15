package implorderedset

import (
	"reflect"
	"testing"
)

// Hidden acceptance test: stashed out of the agent's view by the Harness and
// only applied after the run. A map-backed set with no order tracking would
// pass the visible test but fail here: the hidden test pins insertion order,
// dedup, and — the classic trap — that Values() does NOT alias the internal
// backing slice (mutating the returned slice must not corrupt the set).
func TestSetOrderAndNoAliasing(t *testing.T) {
	s := New()
	for _, v := range []string{"b", "a", "b", "c", "a"} {
		s.Add(v)
	}
	want := []string{"b", "a", "c"}
	if got := s.Values(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %v, want %v (insertion order + dedup)", got, want)
	}
	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3", s.Len())
	}
	if s.Contains("a") != true || s.Contains("b") != true || s.Contains("c") != true {
		t.Fatal("all inserted values should be present")
	}
	if s.Contains("d") {
		t.Fatal("never-inserted value should be absent")
	}

	// Aliasing trap: the caller mutating the returned slice must not corrupt
	// the set's own state.
	vals := s.Values()
	vals[0] = "X"
	if !s.Contains("b") {
		t.Fatal("set corrupted by an aliased Values() return")
	}
	if s.Len() != 3 {
		t.Fatal("Len changed after the caller mutated a Values() result")
	}
}
