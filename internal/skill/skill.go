// Package skill implements the Claude-Code-style skill system: a skill is a
// directory skills/<name>/SKILL.md (plus optional sibling files) that the
// agent surfaces as a dynamic tool. The model sees the skill's one-line
// description in the tool list and, when it calls the skill, the full
// instructions (body + resources) enter its context. Skills are free-form —
// the user creates a directory anywhere in .go-coding/skills/ and the agent
// picks it up at startup.
package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zlx05/go-coding/internal/frontmatter"
)

// Def is one loaded skill. Body is the SKILL.md instructions; Resources are
// the other files in the skill's directory (rel path → content), so a skill
// can ship a checklist, a template, example code, etc.
type Def struct {
	Name        string
	Description string
	Body        string
	Resources   map[string]string
}

// Render returns the full instructions the model should follow when the skill
// is invoked: the body plus any referenced resources.
func (d Def) Render() string {
	var sb strings.Builder
	sb.WriteString(d.Body)
	if len(d.Resources) > 0 {
		names := make([]string, 0, len(d.Resources))
		for n := range d.Resources {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			sb.WriteString("\n\n--- resource " + n + " ---\n")
			sb.WriteString(d.Resources[n])
		}
	}
	return sb.String()
}

// Load scans the project (.go-coding/skills) and user (~/.go-coding/skills)
// skill directories. Project skills win on name collisions. Missing
// directories are a no-op. Results are name-sorted for deterministic prompts.
func Load(projectRoot, userHome string) ([]Def, error) {
	byName := map[string]Def{}
	projectDir := filepath.Join(projectRoot, ".go-coding", "skills")
	userDir := filepath.Join(userHome, ".go-coding", "skills")
	// Project first so it overrides the user on collision.
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
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, e.Name())
		md, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err != nil {
			continue // a skill without SKILL.md is not a skill
		}
		meta, body := frontmatter.Parse(string(md))
		name := meta["name"]
		if name == "" {
			name = e.Name()
		}
		out = append(out, Def{
			Name:        name,
			Description: meta["description"],
			Body:        body,
			Resources:   readResources(skillDir),
		})
	}
	return out, nil
}

// readResources loads every non-SKILL.md file under a skill directory,
// recursively, keyed by its slash-separated path relative to the skill dir.
func readResources(skillDir string) map[string]string {
	res := map[string]string{}
	_ = filepath.WalkDir(skillDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "SKILL.md" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(skillDir, path)
		res[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return res
}
