package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zlx05/go-coding/internal/tools"
)

// ExploreOp is one exploration action the LLM can request before planning.
// ops: "list_dir" | "grep" | "read_file" | "glob".
type ExploreOp struct {
	Op      string `json:"op"` // "list_dir" | "grep" | "read_file" | "glob"
	Pattern string `json:"pattern,omitempty"`
	Path    string `json:"path,omitempty"`
}

// parseExplore detects whether the LLM's reply is an exploration request
// ({"explore":[...]}) rather than a plan ({"steps":[...]}). Returns ok=false
// when it is a plan. Like parseSteps, it tolerates code fences and
// concatenated JSON: the first complete object wins.
func parseExplore(content string) ([]ExploreOp, bool) {
	jsonStr, ok := extractFirstJSON(stripCodeFence(content))
	if !ok {
		return nil, false
	}
	var probe struct {
		Explore []ExploreOp `json:"explore"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &probe); err != nil {
		return nil, false
	}
	if len(probe.Explore) > 0 {
		return probe.Explore, true
	}
	return nil, false
}

// execExplore runs the requested exploration ops inside the sandbox and
// returns a single text block with the results, ready to feed back to the
// LLM. It is the "gather context" step of the agent loop.
//
// All ops are read-only (list_dir / grep / read_file), so they run
// concurrently (latency win) and their results are re-assembled in the
// original op order (deterministic output, no interleaving).
func (a *Agent) execExplore(ctx context.Context, ops []ExploreOp) string {
	results := make([]string, len(ops))
	var wg sync.WaitGroup
	for i, op := range ops {
		wg.Add(1)
		go func(i int, op ExploreOp) {
			defer wg.Done()
			results[i] = a.execOneExplore(ctx, op)
		}(i, op)
	}
	wg.Wait()

	var b strings.Builder
	for i, op := range ops {
		b.WriteString("## " + op.Op)
		if op.Pattern != "" {
			b.WriteString(" " + op.Pattern)
		}
		if op.Path != "" {
			b.WriteString(" " + op.Path)
		}
		b.WriteString("\n")
		b.WriteString(results[i] + "\n")
	}
	return b.String()
}

// execOneExplore runs a single exploration op and returns its text output.
func (a *Agent) execOneExplore(ctx context.Context, op ExploreOp) string {
	switch op.Op {
	case "list_dir":
		res, err := a.tools.list.Run(ctx, tools.Args(`{}`))
		if err != nil {
			return "error: " + err.Error()
		}
		return res.Output
	case "grep":
		arg, _ := json.Marshal(map[string]string{"pattern": op.Pattern})
		res, err := a.tools.grep.Run(ctx, tools.Args(arg))
		if err != nil {
			return "error: " + err.Error()
		}
		return res.Output
	case "read_file":
		arg, _ := json.Marshal(map[string]string{"path": op.Path})
		res, err := a.tools.read.Run(ctx, tools.Args(arg))
		if err != nil {
			return "error: " + err.Error()
		}
		return res.Output
	case "glob":
		if op.Pattern == "" {
			return "error: glob requires a pattern"
		}
		matches, err := globFiles(a.sandbox.Root, op.Pattern)
		if err != nil {
			return "error: glob: " + err.Error()
		}
		if len(matches) == 0 {
			return "(no matches)"
		}
		return strings.Join(matches, "\n")
	default:
		return fmt.Sprintf("unknown op %q", op.Op)
	}
}

// globFiles returns files under root matching pattern, where "**" matches
// zero or more directory levels (the semantics Claude Code's Glob and git
// ignore files share). Paths are returned relative to root, slash-separated.
func globFiles(root, pattern string) ([]string, error) {
	patSegs := strings.Split(filepath.ToSlash(pattern), "/")
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if globSegmentsMatch(patSegs, strings.Split(filepath.ToSlash(rel), "/")) {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out, err
}

// globSegmentsMatch matches a split path against a split pattern where the
// literal "**" segment consumes zero or more leading path segments.
func globSegmentsMatch(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(path); skip++ {
			if globSegmentsMatch(pat[1:], path[skip:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, _ := filepath.Match(pat[0], path[0])
	if !ok {
		return false
	}
	return globSegmentsMatch(pat[1:], path[1:])
}
