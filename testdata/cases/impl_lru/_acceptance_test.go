package lru

import "testing"

// Hidden acceptance test: stashed out of the agent's view. Targets the LRU
// semantics a naive implementation skips — especially that Get refreshes
// recency, which is the classic trap.
func TestLRUEviction(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // evicts a (least recently used)
	if _, ok := c.Get("a"); ok {
		t.Fatal("Get(a) after overflow: want evicted")
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Fatalf("Get(c) = %v,%v want 3,true", v, ok)
	}
}

func TestLRUCapacityOne(t *testing.T) {
	c := New(1)
	c.Put("x", 1)
	c.Put("y", 2) // evicts x
	if _, ok := c.Get("x"); ok {
		t.Fatal("Get(x) capacity-1: want evicted")
	}
	if v, ok := c.Get("y"); !ok || v != 2 {
		t.Fatalf("Get(y) = %v,%v want 2,true", v, ok)
	}
}

// TestLRUGetRefreshesRecency is THE trap: a correct LRU marks an item most
// recently used on Get, so "a" survives even though "b" was inserted after it.
func TestLRUGetRefreshesRecency(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a") // a is now most recently used
	c.Put("c", 3) // evicts b, not a
	if _, ok := c.Get("b"); ok {
		t.Fatal("Get(b): want evicted (a was refreshed by Get)")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %v,%v want 1,true", v, ok)
	}
}

func TestLRUDuplicatePut(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10) // update value, a stays
	if v, ok := c.Get("a"); !ok || v != 10 {
		t.Fatalf("Get(a) after update = %v,%v want 10,true", v, ok)
	}
	c.Put("c", 3) // evicts b
	if _, ok := c.Get("b"); ok {
		t.Fatal("Get(b): want evicted")
	}
}

func TestLRUMissing(t *testing.T) {
	if _, ok := New(2).Get("zzz"); ok {
		t.Fatal("Get(missing) ok = true, want false")
	}
}
