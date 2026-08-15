package implorderedset

import "testing"

// Visible test: the agent can see this and it fails on the stub (Add always
// reports "not new", Contains is false, Len is 0), which drives the
// implementation.
func TestSetBasics(t *testing.T) {
	s := New()
	if !s.Add("a") {
		t.Fatal("first Add should report newly added")
	}
	if s.Add("a") {
		t.Fatal("duplicate Add should report not-new")
	}
	if !s.Contains("a") {
		t.Fatal("Contains(a) should be true after Add")
	}
	if s.Contains("zz") {
		t.Fatal("Contains(zz) should be false")
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
}
