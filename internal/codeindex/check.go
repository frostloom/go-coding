package codeindex

import (
	"fmt"
	"strings"
)

// FileSnapshot captures the pre-edit symbol state of one file so an edit can
// be checked for cross-file reference regressions after it is re-indexed.
//
// The idea behind edit-time validation: before an edit, record which symbols
// live in the file and who (from OTHER files) calls them. After the edit, if
// one of those symbols is gone but a surviving caller still exists, the edit
// broke a reference. We can reject it at the symbol layer — milliseconds, no
// compile — instead of waiting for the compiler or tests (seconds). This is
// the one check a grep-based tool like Claude Code cannot do: it has no call
// graph.
type FileSnapshot struct {
	rel string
	// refs maps a symbol ID to the cross-file caller IDs that reference it.
	// Only symbols WITH cross-file callers are retained (cheap filter).
	refs map[string][]string
}

// CaptureFile snapshots rel (root-relative, slash form) against the CURRENT
// index. Must be called BEFORE the edit is written.
func (b *Builder) CaptureFile(rel string) *FileSnapshot {
	sn := &FileSnapshot{rel: rel, refs: map[string][]string{}}
	if b.index == nil {
		return sn
	}
	// Build an ID → file lookup once so we can tell cross-file callers apart.
	fileOf := make(map[string]string, len(b.index.all))
	for _, s := range b.index.all {
		if _, ok := fileOf[s.ID()]; !ok {
			fileOf[s.ID()] = s.File
		}
	}
	for _, s := range b.index.byFile[rel] {
		var xf []string
		for _, c := range b.index.Callers(s.ID()) {
			if f, ok := fileOf[c]; ok && f != rel {
				xf = append(xf, c)
			}
		}
		if len(xf) > 0 {
			sn.refs[s.ID()] = xf
		}
	}
	return sn
}

// VerifyAfter checks the snapshot against a REBUILT index (the post-edit
// state). It returns human-readable regressions: symbols removed by the edit
// that surviving callers in other files still reference. An empty result
// means the edit is reference-safe.
func (sn *FileSnapshot) VerifyAfter(b *Builder) []string {
	if sn == nil || len(sn.refs) == 0 {
		return nil
	}
	exists := make(map[string]bool, len(b.index.all))
	for _, s := range b.index.all {
		exists[s.ID()] = true
	}
	var issues []string
	for id, callers := range sn.refs {
		if exists[id] {
			continue // symbol still present — fine
		}
		var alive []string
		for _, c := range callers {
			if exists[c] {
				alive = append(alive, c)
			}
		}
		if len(alive) > 0 {
			issues = append(issues, fmt.Sprintf("%s was removed by this edit but is still called by %s", id, strings.Join(alive, ", ")))
		}
	}
	return issues
}
