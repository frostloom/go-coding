// Package verify implements the deliverable-verification layer (P23): the
// agent's own post-edit gate that mirrors Claude Code's "run it and observe"
// habit — read the produced files back for consistency (CheckReferences) and
// boot a generated server to probe real HTTP behaviour (SmokeServer). Both are
// deterministic, consume no LLM calls, and are no-ops when the task produced
// no web assets / no port-binding server (so the mock harness gate is
// unaffected).
package verify

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// referencePatterns maps a file kind to the regexes that pull out file
// references: HTML attribute src/href, CSS url().
var (
	htmlAttrRe = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']([^"']+)["']`)
	cssURLRe   = regexp.MustCompile(`(?i)url\(\s*["']?([^"')]+)["']?\s*\)`)
)

// CheckReferences walks root for HTML and CSS files, extracts every file
// reference (script src, link href, img src, CSS url()), and reports the ones
// whose target does not exist in the workspace. Deterministic, read-only.
// Equivalent to CheckReferencesIn(root, nil): every HTML/CSS file is scanned.
func CheckReferences(root string) []string {
	return CheckReferencesIn(root, nil)
}

// CheckReferencesIn is the task-scoped variant: only the files in scan (paths
// relative to root, the set the task actually produced) are read for
// references. The basename index still covers the WHOLE workspace, because an
// absolute ref ("/app.js") is matched against every file the server could
// serve, not just the files the task touched. nil scan = scan everything.
//
// Resolution rules:
//   - Relative refs ("./x", "../x", bare "x") resolve strictly against the
//     referencing file's directory and must stay inside root.
//   - Absolute refs ("/x") match by basename anywhere under root. A browser
//     page served from a subdirectory (e.g. /static/) references "/x" relative
//     to the SERVER root, which a static walk cannot know — so we search for a
//     file named x anywhere and only flag when none exists. This is what
//     catches `src="/script.js"` when only app.js exists.
func CheckReferencesIn(root string, scan []string) []string {
	baseFiles := map[string]string{} // basename -> one path, for absolute refs
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; the reference scan is best-effort
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".html", ".css", ".js":
			baseFiles[filepath.Base(path)] = path
		}
		return nil
	})

	var issues []string
	if scan == nil {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			issues = append(issues, refsInFile(path, root, baseFiles)...)
			return nil
		})
		return issues
	}
	for _, rel := range scan {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if !within(root, path) {
			continue
		}
		issues = append(issues, refsInFile(path, root, baseFiles)...)
	}
	return issues
}

// refsInFile extracts the file references of one HTML/CSS file and reports the
// ones whose target is missing. Not a web asset (or unreadable) → no issues.
func refsInFile(path, root string, baseFiles map[string]string) []string {
	var issues []string
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".html" && ext != ".css" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(path)
	seen := map[string]bool{}
	var re *regexp.Regexp
	if ext == ".html" {
		re = htmlAttrRe
	} else {
		re = cssURLRe
	}
	for _, m := range re.FindAllSubmatch(data, -1) {
		ref := string(m[1])
		if !referenceIsFile(ref) {
			continue
		}
		key := dir + "|" + ref
		if seen[key] {
			continue
		}
		seen[key] = true
		if !resolved(ref, dir, root, baseFiles) {
			issues = append(issues, filepath.ToSlash(path)+": references "+ref+", which does not exist in the workspace")
		}
	}
	return issues
}

// referenceIsFile filters out refs that are not filesystem lookups: fragments,
// query-only refs, mailto:, external http(s) URLs, protocol-relative //, and
// data: URIs.
func referenceIsFile(ref string) bool {
	if ref == "" || strings.HasPrefix(ref, "#") {
		return false
	}
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(ref, "//") || strings.HasPrefix(ref, "?") {
		return false
	}
	return true
}

// resolved reports whether ref exists in the workspace relative to dir (the
// referencing file's directory) under root. Absolute refs match by basename.
func resolved(ref, dir, root string, baseFiles map[string]string) bool {
	// Strip query/hash suffixes that are not part of the file name.
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" {
		return false
	}
	if strings.HasPrefix(ref, "/") {
		_, ok := baseFiles[filepath.Base(ref)]
		return ok
	}
	target := filepath.Join(dir, filepath.FromSlash(ref))
	clean := filepath.Clean(target)
	if !within(root, clean) {
		return false // escaped the workspace — treat as not our concern
	}
	if _, err := os.Stat(clean); err != nil {
		return false
	}
	return true
}

// within reports whether p stays lexically inside root.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
