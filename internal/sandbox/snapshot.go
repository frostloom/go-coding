package sandbox

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EnsureRepo initializes a git repository in the sandbox and commits the
// initial state as the baseline. From then on Snapshot records each change
// and Rollback can undo it. Uses a fixed local identity so commits work
// without any user git config. --allow-empty makes even a brand-new project
// with no files get a baseline commit, so the first edit's undo (HEAD~1)
// always exists instead of being the repo's root commit.
func (s *Sandbox) EnsureRepo() error {
	if _, err := s.git("init"); err != nil {
		return err
	}
	if _, err := s.git("config", "user.email", "ink-ai@local"); err != nil {
		return err
	}
	if _, err := s.git("config", "user.name", "ink-ai"); err != nil {
		return err
	}
	if _, err := s.git("add", "-A"); err != nil {
		return err
	}
	if _, err := s.git("commit", "--allow-empty", "-m", "baseline"); err != nil {
		return err
	}
	if out, err := s.git("rev-parse", "HEAD"); err == nil {
		s.baseline = strings.TrimSpace(out)
	}
	return nil
}

// EnsureRepoInPlace prepares git for DIRECT in-place editing of a project the
// operator owns (cmd/agent -direct). If the project is already a git repo, the
// current HEAD becomes the baseline WITHOUT creating any new commit — the
// operator's history is not polluted by an "ink-ai baseline" commit, and the
// agent's snapshots simply commit on top of their work (undo still works,
// git_diff still shows exactly what the agent changed). If the project has no
// .git, it initializes a fresh repo and baselines it exactly like EnsureRepo
// (Claude Code also git-inits a project that lacks one, so every edit stays
// undo-able). A working tree that is already dirty is reported so the operator
// knows snapshots will sweep their own uncommitted changes in.
func (s *Sandbox) EnsureRepoInPlace() error {
	if _, err := os.Stat(filepath.Join(s.Root, ".git")); err == nil {
		out, err := s.git("rev-parse", "HEAD")
		if err != nil {
			// A repo with no commits yet (git init run, nothing committed): no
			// history to pollute — baseline it like a fresh repo.
			return s.EnsureRepo()
		}
		s.baseline = strings.TrimSpace(out)
		if has, _ := s.hasChanges(); has {
			fmt.Fprintf(os.Stderr, "note: project already has uncommitted changes; agent snapshots will include them\n")
		}
		return nil
	}
	return s.EnsureRepo()
}

// Snapshot commits the current working tree as one reversible checkpoint.
// No-op when nothing changed.
func (s *Sandbox) Snapshot(msg string) error {
	has, err := s.hasChanges()
	if err != nil || !has {
		return err
	}
	if _, err := s.git("add", "-A"); err != nil {
		return err
	}
	_, err = s.git("commit", "-m", msg)
	return err
}

// Rollback reverts the last snapshot (git reset --hard HEAD~1), restoring
// the tree to its state before the most recent edit.
func (s *Sandbox) Rollback() error {
	_, err := s.git("reset", "--hard", "HEAD~1")
	return err
}

// Diff returns everything the agent changed since the baseline commit.
func (s *Sandbox) Diff() (string, error) {
	if s.baseline == "" {
		return "", fmt.Errorf("sandbox: no baseline (EnsureRepo not called)")
	}
	return s.git("diff", s.baseline, "HEAD")
}

// Head returns the current HEAD commit, or "" when git is unavailable (a
// degraded sandbox whose repo setup failed). The agent records it at turn start
// to scope deliverable verification (P23) to exactly the files this task wrote.
func (s *Sandbox) Head() string {
	out, err := s.git("rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ChangedFilesSince lists the files that changed since rev, relative to the
// sandbox root: every tracked working-tree change since rev (git diff shows
// committed and uncommitted alike) plus untracked files (git status porcelain,
// which diff omits). Parent and executor children all commit into this one
// repository, so the set is exactly what the whole task produced. rev is the
// turn-start commit recorded by Head(); a fresh task baselines before any edit,
// so the diff is the task's own changes even when the workspace also contains a
// pre-existing server the task never touched. Empty rev (no git) yields nil.
func (s *Sandbox) ChangedFilesSince(rev string) ([]string, error) {
	if rev == "" {
		return nil, nil
	}
	out, err := s.git("diff", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	files := nonEmptyLines(out)
	// Untracked files never appear in `git diff`; fold them in from status.
	// git reports an untracked DIRECTORY as "?? static/" (collapsed), but the
	// verify layer needs file-level paths — so an untracked dir is expanded to
	// the files it contains, or a freshly created web-asset dir would be missed
	// by both the reference check and the server smoke.
	if st, serr := s.git("status", "--porcelain"); serr == nil {
		for _, line := range nonEmptyLines(st) {
			if !strings.HasPrefix(line, "??") {
				continue
			}
			rel := strings.TrimPrefix(line, "?? ")
			abs := filepath.Join(s.Root, filepath.FromSlash(rel))
			info, err := os.Stat(abs)
			if err != nil || !info.IsDir() {
				files = append(files, filepath.ToSlash(rel))
				continue
			}
			_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if d.Name() == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if r, rerr := filepath.Rel(s.Root, p); rerr == nil {
					files = append(files, filepath.ToSlash(r))
				}
				return nil
			})
		}
	}
	sort.Strings(files)
	return files, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Sandbox) hasChanges() (bool, error) {
	out, err := s.git("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// git runs a git command inside the sandbox root with a hard timeout and no
// terminal prompts (so a credential prompt can never hang the agent).
func (s *Sandbox) git(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.Root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("sandbox: git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
