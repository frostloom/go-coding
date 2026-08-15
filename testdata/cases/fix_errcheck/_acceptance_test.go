package fixerrcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// Hidden acceptance test: stashed out of the agent's view by the Harness and
// only applied after the run. A lazy "fix" that always returns nil would pass
// the visible test's error check by accident, so this asserts BOTH directions:
// errors surface AND valid writes still land.
func TestSaveAcceptance(t *testing.T) {
	dir := t.TempDir()

	// An unwritable target must surface an error — never a silent nil.
	if err := Save(filepath.Join(dir, "no-dir", "x.txt"), []byte("x")); err == nil {
		t.Fatal("expected an error writing to a non-existent directory")
	}

	// A valid write must still succeed.
	path := filepath.Join(dir, "out.txt")
	if err := Save(path, []byte("hello")); err != nil {
		t.Fatalf("Save to a valid path returned an error: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello" {
		t.Fatalf("file content = %q err=%v, want %q", b, err, "hello")
	}
}
