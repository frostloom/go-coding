// Package frontmatter extracts the YAML-ish header block (--- ... ---) at the
// top of a markdown file. Skills (skills/<name>/SKILL.md) and subagents
// (agents/<name>.md) both declare their identity this way — one parser, both
// consumers, matching the format Claude Code uses for its skill/agent files.
package frontmatter

import "strings"

// Parse splits content into frontmatter metadata and body. Values are
// name: value pairs; the first "---" at the start opens the block, the next
// "---" closes it. No frontmatter → nil meta, whole content as body.
func Parse(content string) (meta map[string]string, body string) {
	s := strings.TrimLeft(content, "\ufeff \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return nil, s
	}
	end := strings.Index(s[3:], "---")
	if end < 0 {
		return nil, s
	}
	fm := s[3 : 3+end]
	rest := s[3+end+3:]
	meta = map[string]string{}
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) != "" {
			meta[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return meta, strings.TrimSpace(rest)
}
