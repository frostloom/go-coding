// Package subagent implements the Claude-Code-style subagent system: a
// subagent is a markdown file agents/<name>.md that declares its identity in
// frontmatter (name, description, read_only, model) and its role instructions
// in the body. The parent agent surfaces each registered subagent as a
// dynamic tool ("subagent:<name>"); when the model calls it, a child agent
// runs in the SAME sandbox workspace and symbol index as the parent (shared
// state), with its own conversation and its own tool set:
//
//   - ReadOnly ("researcher"): explore-only, returns a report in prose.
//   - Full ("worker"): the complete plan→edit→test loop, edits shared files.
//
// The child's Result is fed back to the parent as structured text, so the
// parent reasons about the report and continues.
package subagent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zlx05/go-coding/internal/frontmatter"
)

// Def is one registered subagent.
type Def struct {
	Name        string
	Description string
	// Prompt is the subagent's role instructions (the body of its .md file),
	// prepended to the child's system prompt.
	Prompt   string
	ReadOnly bool   // researcher: explore-only, reports instead of editing
	Model    string // per-role LLM model override (used since P22: rides every
	// child call via modelOverride → a.chat req.Model; empty = inherit the parent's)
}

// Load scans the project (.go-coding/agents) and user (~/.go-coding/agents)
// subagent directories. Project subagents win on name collisions. Missing
// directories are a no-op. Results are name-sorted for deterministic prompts.
func Load(projectRoot, userHome string) ([]Def, error) {
	byName := map[string]Def{}
	projectDir := filepath.Join(projectRoot, ".go-coding", "agents")
	userDir := filepath.Join(userHome, ".go-coding", "agents")
	for _, dir := range []string{projectDir, userDir} {
		defs, err := loadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, d := range defs {
			if _, exists := byName[d.Name]; !exists {
				byName[d.Name] = d
			}
		}
	}
	out := make([]Def, 0, len(byName))
	for _, d := range byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadDir(dir string) ([]Def, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Def
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		meta, body := frontmatter.Parse(string(b))
		name := meta["name"]
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".md")
		}
		if body == "" {
			continue // a subagent needs role instructions
		}
		readOnly := strings.EqualFold(meta["read_only"], "true") || strings.EqualFold(meta["read_only"], "yes")
		out = append(out, Def{
			Name:        name,
			Description: meta["description"],
			Prompt:      body,
			ReadOnly:    readOnly,
			Model:       meta["model"],
		})
	}
	return out, nil
}
