package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRollback(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.go")
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.EnsureRepo(); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}

	// Edit 1: v1 -> v2, snapshot it.
	if err := os.WriteFile(file, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.Snapshot("edit v2"); err != nil {
		t.Fatalf("snapshot v2: %v", err)
	}

	// Edit 2: v2 -> v3, snapshot it.
	if err := os.WriteFile(file, []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.Snapshot("edit v3"); err != nil {
		t.Fatalf("snapshot v3: %v", err)
	}

	// Rollback once -> back to v2.
	if err := sb.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Normalize CRLF: git checkout may rewrite line endings on Windows
	// (core.autocrlf), so tolerate \r\n when comparing.
	got := strings.ReplaceAll(string(b), "\r\n", "\n")
	if got != "v2\n" {
		t.Errorf("after rollback want v2, got %q", string(b))
	}

	// Diff since baseline should show v1 -> v2.
	diff, err := sb.Diff()
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff == "" {
		t.Error("expected a non-empty diff after the change")
	}
}

func TestEnsureRepoNoChanges(t *testing.T) {
	// An empty tree should still be a valid repo, and --allow-empty gives it
	// a real baseline commit so the first edit's undo (HEAD~1) always exists.
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.EnsureRepo(); err != nil {
		t.Fatalf("EnsureRepo on empty tree: %v", err)
	}
	if out, err := sb.git("rev-parse", "HEAD"); err != nil || strings.TrimSpace(out) == "" {
		t.Errorf("expected a baseline commit on an empty tree (out=%q err=%v)", out, err)
	}
}

// TestEnsureRepoInPlaceExistingRepoNoBaselineCommit: -direct on a project that
// is ALREADY a git repo must not create a baseline commit (the operator's
// history stays untouched) — the existing HEAD becomes the baseline, and the
// agent's snapshots commit on top of it so undo and diff still work.
func TestEnsureRepoInPlaceExistingRepo(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.go")
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The "operator's repo" with one real commit.
	pre, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.EnsureRepo(); err != nil {
		t.Fatal(err)
	}
	headBefore, err := pre.git("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}

	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.EnsureRepoInPlace(); err != nil {
		t.Fatalf("EnsureRepoInPlace: %v", err)
	}
	// No baseline commit was created: HEAD is unchanged.
	headAfter, err := sb.git("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD after: %v", err)
	}
	if headBefore != headAfter {
		t.Errorf("InPlace must not create a baseline commit: HEAD moved %q -> %q", strings.TrimSpace(headBefore), strings.TrimSpace(headAfter))
	}

	// The agent's edit still snapshots and diffs against the original HEAD.
	if err := os.WriteFile(file, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.Snapshot("edit v2"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	diff, err := sb.Diff()
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff == "" {
		t.Error("expected a non-empty diff since the original HEAD")
	}
	// Rollback restores the pre-edit state.
	if err := sb.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	b, _ := os.ReadFile(file)
	got := strings.ReplaceAll(string(b), "\r\n", "\n")
	if got != "v1\n" {
		t.Errorf("after rollback want v1, got %q", string(b))
	}
}

// TestEnsureRepoInPlaceFreshDir: -direct on a plain directory git-inits and
// baselines it exactly like EnsureRepo, so edits are undo-able from the start.
func TestEnsureRepoInPlaceFreshDir(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.go")
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.EnsureRepoInPlace(); err != nil {
		t.Fatalf("EnsureRepoInPlace on a fresh dir: %v", err)
	}
	if err := os.WriteFile(file, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.Snapshot("edit v2"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if diff, err := sb.Diff(); err != nil || diff == "" {
		t.Errorf("expected a diff after the change (err=%v)", err)
	}
}

// TestChangedFilesSince: the deliverable-verification scoping primitive. The
// diff since a turn-start commit must yield exactly the files the task wrote —
// tracked edits, untracked new files, and nothing that pre-dated the baseline.
func TestChangedFilesSince(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib.go", "package lib\nfunc Add(a, b int) int { return a + b }")

	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.EnsureRepo(); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	// Fresh task: nothing changed since the baseline yet.
	if files, err := sb.ChangedFilesSince(sb.Head()); err != nil || len(files) != 0 {
		t.Fatalf("expected empty diff at task start, got %v (err=%v)", files, err)
	}

	// "Task edits lib.go" — committed via Snapshot, then diffed from the
	// pre-edit commit. HEAD() before the edit is the turn-start baseline.
	baseline := sb.Head()
	if err := os.WriteFile(filepath.Join(root, "lib.go"), []byte("package lib\nfunc Add(a, b int) int { return a + b + 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.Snapshot("edit lib.go"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	files, err := sb.ChangedFilesSince(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "lib.go" {
		t.Errorf("ChangedFilesSince after editing lib.go = %v, want [lib.go]", files)
	}

	// An UNTRACKED new file (written after the last snapshot — the exact state
	// an executor leaves a brand-new file in) must also be reported.
	if err := os.MkdirAll(filepath.Join(root, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "static", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err = sb.ChangedFilesSince(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != "lib.go" || files[1] != "static/index.html" {
		t.Errorf("ChangedFilesSince with untracked file = %v, want [lib.go static/index.html]", files)
	}

	// Empty rev (degraded sandbox, no git) → nil, never an error.
	if files, err := sb.ChangedFilesSince(""); err != nil || files != nil {
		t.Errorf("ChangedFilesSince(\"\") = %v (err=%v), want nil,nil", files, err)
	}
}

// TestEnsureRepoInPlaceEmptyDirRollback: -direct on a brand-new EMPTY project
// (the common first-run case) must still give working undo — the baseline
// empty commit makes the first Snapshot a child commit, so Rollback lands back
// on it and removes the created file.
func TestEnsureRepoInPlaceEmptyDirRollback(t *testing.T) {
	root := t.TempDir()
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.EnsureRepoInPlace(); err != nil {
		t.Fatalf("EnsureRepoInPlace on an empty dir: %v", err)
	}
	file := filepath.Join(root, "a.go")
	if err := os.WriteFile(file, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.Snapshot("add a.go"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := sb.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("expected a.go to be gone after rollback (err=%v)", err)
	}
}
