package lru

import "testing"

// Visible test: the agent can see this. It fails on the stub (Get always
// returns false), which drives the implementation.
func TestCacheBasic(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %v,%v want 1,true", v, ok)
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Fatalf("Get(b) = %v,%v want 2,true", v, ok)
	}
}
