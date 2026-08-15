package parsekv

import "testing"

// Visible test: the agent can see this and it fails on the stub, which is
// what drives the implementation.
func TestParseBasic(t *testing.T) {
	m, err := Parse("a=1;b=2")
	if err != nil {
		t.Fatalf("Parse(a=1;b=2) error = %v, want nil", err)
	}
	if m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("Parse(a=1;b=2) = %v, want a=1 b=2", m)
	}
}
