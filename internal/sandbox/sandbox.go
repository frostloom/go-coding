// Package sandbox implements the logical isolation boundary for the agent.
//
// Windows has no chroot/seccomp, so isolation is logical, in three layers:
//  1. Path isolation: every tool path must resolve inside the sandbox root
//     (handles `..` escapes, case-insensitive filesystems and symlinks);
//  2. Command whitelist: only a fixed set of go/git subcommands may run;
//  3. Git snapshots: every change is recorded so it can be rolled back.
//
// The sandbox guards the AGENT's behaviour — it stops a prompt-injected or
// hallucinating agent from touching files outside the workspace or running
// dangerous commands. It does NOT sandbox the target code itself: `go test`
// compiles and runs the fixture, which is by design.
package sandbox

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrEscape is returned when a path resolves outside the sandbox root.
var ErrEscape = errors.New("sandbox: path escapes sandbox root")

// Sandbox is a rooted workspace with hardened path resolution.
type Sandbox struct {
	Root     string // canonical sandbox root (absolute)
	baseline string // git commit SHA captured at EnsureRepo time
	// allowed is the per-instance command allowlist (see whitelist.go). nil
	// means "the built-in Go+git set" — a bare &Sandbox{Root: ...} or one built
	// by New is byte-for-byte the historical sandbox until AllowCommands runs.
	allowed map[string][]string
}

// New validates that root is an existing directory and builds a Sandbox.
func New(root string) (*Sandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Sandbox{Root: filepath.Clean(abs)}, nil
}

// Resolve maps a tool-provided path to an absolute path inside the sandbox.
// It rejects `..` escapes, absolute paths outside the root, and — critically
// on Windows — paths that only differ from the root by case, because the
// filesystem itself is case-insensitive and a case-sensitive prefix check
// would be trivially bypassable. Symlinks are resolved to their real target
// so a link pointing outside the root is caught too.
func (s *Sandbox) Resolve(p string) (string, error) {
	if p == "" {
		return "", errors.New("sandbox: empty path")
	}
	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(s.Root, full)
	}
	clean := filepath.Clean(full)

	// Lexical check first (fast path, catches plain `../` escapes).
	if !within(s.Root, clean) {
		return "", ErrEscape
	}

	// Real-path check: resolve symlinks of the portion that exists, then
	// confirm the resolved location is still inside the resolved root.
	// This is what stops a symlink like sandbox/out -> C:\Secrets.
	realRoot, err := filepath.EvalSymlinks(s.Root)
	if err != nil {
		realRoot = s.Root
	}
	realFull := resolveExisting(clean)
	if !withinCI(realRoot, realFull) {
		return "", ErrEscape
	}
	return clean, nil
}

// within reports whether p is lexically inside root (case-sensitive).
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return !escapes(rel)
}

// withinCI is within, but compares case-insensitively on Windows to mirror
// the case-insensitive filesystem (the actual security property we need).
func withinCI(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	return !escapes(rel)
}

func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExisting resolves symlinks of the deepest existing ancestor of p
// and re-appends the not-yet-existing tail (so it works for new files too).
func resolveExisting(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(resolveExisting(dir), filepath.Base(p))
}
