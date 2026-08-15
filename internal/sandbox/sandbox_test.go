package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveInsideRoot(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Resolve("file.go"); err != nil {
		t.Errorf("relative path should resolve: %v", err)
	}
	if _, err := sb.Resolve("sub/dir/file.go"); err != nil {
		t.Errorf("nested path should resolve: %v", err)
	}
	if _, err := sb.Resolve(sb.Root); err != nil {
		t.Errorf("root itself should resolve: %v", err)
	}
}

func TestResolveEscapesRejected(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"../escape.go",
		"a/../../escape.go",
		"../../..",
	}
	for _, c := range cases {
		if _, err := sb.Resolve(c); err != ErrEscape {
			t.Errorf("path %q should be rejected with ErrEscape, got %v", c, err)
		}
	}
	// Absolute path outside the root.
	outside := filepath.Join(filepath.Dir(sb.Root), "outside.go")
	if _, err := sb.Resolve(outside); err != ErrEscape {
		t.Errorf("absolute outside path should be rejected, got %v", err)
	}
}

func TestResolveSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err) // needs admin/dev-mode on Windows
	}

	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	// link/file.go must be rejected: its real target is outside the root.
	if _, err := sb.Resolve(filepath.Join("out", "file.go")); err != ErrEscape {
		t.Errorf("symlink escape should be rejected, got %v", err)
	}
}

// TestWithinCaseInsensitive captures the Windows security property: the
// filesystem is case-insensitive, so a case-variant of the root is the SAME
// directory and must be treated as inside (never falsely rejected, and never
// able to "hide" outside by using a different case). Sibling prefixes like
// repo_evil must still be rejected. On Windows, Go's filepath.Rel is already
// case-insensitive, which is exactly the behaviour we need.
func TestWithinCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only concern")
	}
	root := `C:\Repo`
	if !within(root, `c:\repo\a\file.go`) {
		t.Errorf("within must accept case-variant %q under %q (same dir on Windows)", `c:\repo\a\file.go`, root)
	}
	if within(root, `c:\repo_evil\a`) {
		t.Errorf("within must reject a sibling prefix like %q", `c:\repo_evil\a`)
	}
}

func TestWithinPlain(t *testing.T) {
	root := `/sandbox`
	if !within(root, `/sandbox/a.go`) {
		t.Error("child should be inside")
	}
	if within(root, `/sandboxx/a.go`) {
		t.Error("sibling prefix must not count as inside")
	}
	if !within(root, `/sandbox`) {
		t.Error("root itself should be allowed")
	}
}

// helper for tests that want to assert a path stays inside.
func mustResolve(t *testing.T, sb *Sandbox, p string) string {
	t.Helper()
	full, err := sb.Resolve(p)
	if err != nil {
		t.Fatalf("resolve %q: %v", p, err)
	}
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(sb.Root)) {
		t.Fatalf("resolved %q to %q, outside root %q", p, full, sb.Root)
	}
	return full
}
