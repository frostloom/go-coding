package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runGoTest runs the package tests in dir and reports whether they pass.
func runGoTest(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// TestRestoreAcceptanceGatesGrading is the regression test for the hidden
// acceptance test never actually running. A source that passes the visible
// test but fails the hidden one must be graded FAIL: the Go toolchain ignores
// files whose names start with "_", so if restoreAcceptance puts the stashed
// "_acceptance_test.go" back under that same name, grading silently skips it
// and the run is (wrongly) graded PASS.
func TestRestoreAcceptanceGatesGrading(t *testing.T) {
	dir := t.TempDir()
	stash := t.TempDir()

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module gatecheck\n\ngo 1.25\n")
	write("gate.go", "package gatecheck\n\nfunc Add(a, b int) int { return a + b }\n")
	// Visible test passes on the current source.
	write("gate_test.go", "package gatecheck\n\nimport \"testing\"\n\nfunc TestVisible(t *testing.T) {\n\tif Add(1, 1) != 2 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	// Hidden acceptance test FAILS on the current source (unreachable req).
	write("_acceptance_test.go", "package gatecheck\n\nimport \"testing\"\n\nfunc TestAcceptance(t *testing.T) {\n\tif Add(1, 1) != 99 {\n\t\tt.Fatal(\"acceptance requires 99\")\n\t}\n}\n")

	hidden, err := stashAcceptance(dir, stash)
	if err != nil {
		t.Fatalf("stash: %v", err)
	}
	if len(hidden) != 1 {
		t.Fatalf("stashed %d hidden tests, want 1", len(hidden))
	}

	// Simulate the agent run: the visible tests pass with the hidden test out
	// of the way (the agent should not see the test that grades it).
	if !runGoTest(t, dir) {
		t.Fatal("visible tests should pass while the hidden test is stashed")
	}

	if err := restoreAcceptance(stash, dir, hidden); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Grading now runs the hidden test, and the source fails it, so the run
	// must be graded FAIL. Pre-fix the restored name was "_acceptance_test.go",
	// which Go ignores — grading passed, hiding the anti-cheat bug.
	if runGoTest(t, dir) {
		t.Fatal("hidden acceptance test did not run during grading (restored under a name Go tooling ignores?)")
	}
}
