package fixerrcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// Visible test: the agent can see this and it fails on the buggy code — Save
// to an unwritable path must return an error, but the swallowed error makes it
// silently return nil, which is exactly what drives the repair.
func TestSavePropagatesError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-dir", "out.txt")
	if err := Save(path, []byte("x")); err == nil {
		t.Fatalf("Save to %q returned nil; expected an error", path)
	}
}

// TestSaveWrites: the happy path must still work once the error is propagated.
func TestSaveWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := Save(path, []byte("hello")); err != nil {
		t.Fatalf("Save to a valid path returned an error: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "hello" {
		t.Fatalf("file not written correctly: content=%q err=%v", b, err)
	}
}
