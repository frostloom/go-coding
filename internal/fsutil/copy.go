// Package fsutil provides filesystem helpers shared by the harness (which
// copies fixtures into sandboxes) and the CLI (which copies a target repo
// into a working directory before running the agent).
package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// CopyDir recursively copies src into dst. dst is created if missing.
func CopyDir(src, dst string) error {
	return CopyDirFiltered(src, dst, nil)
}

// CopyDirFiltered copies like CopyDir but skips any directory whose name is
// in skip (e.g. ".git", ".agent-workspace"). This is what the CLI uses when
// the source is the current directory: a recursive copy must not pull in its
// own sandbox output or VCS metadata.
func CopyDirFiltered(src, dst string, skip []string) error {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[s] = true
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Prune the whole skipped subtree. path != src guards against skipping
		// the root itself when its name happens to match.
		if d.IsDir() && path != src && skipSet[d.Name()] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
