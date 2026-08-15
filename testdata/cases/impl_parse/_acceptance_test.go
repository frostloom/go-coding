package parsekv

import "testing"

// Hidden acceptance test: stashed out of the agent's view by the Harness
// and only applied after the run. Covers whitespace, empty segments, and
// the malformed-input error paths that a naive implementation skips.
func TestParseAcceptance(t *testing.T) {
	// whitespace-tolerant parsing
	m, err := Parse("a = 1 ; b = 2")
	if err != nil {
		t.Fatalf("Parse with spaces error = %v, want nil", err)
	}
	if m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("Parse with spaces = %v, want a=1 b=2", m)
	}

	// empty input -> empty map, no error
	m, err = Parse("")
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	if len(m) != 0 {
		t.Fatalf("Parse() = %v, want empty map", m)
	}

	// trailing semicolon -> ignored empty segment
	m, err = Parse("a=1;")
	if err != nil || m["a"] != "1" {
		t.Fatalf("Parse(a=1;) = %v err=%v, want a=1 nil", m, err)
	}

	// malformed: segment missing '='
	if _, err := Parse("a=1;y"); err == nil {
		t.Fatal("Parse(a=1;y) error = nil, want non-nil")
	}

	// malformed: empty key
	if _, err := Parse("a=1;=2"); err == nil {
		t.Fatal("Parse(a=1;=2) error = nil, want non-nil")
	}
}
