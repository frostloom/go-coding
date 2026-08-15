package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// Memory is the cross-session memory layer — the "does this agent remember me
// across conversations" story. Two persistent files are loaded at startup and
// injected into the system prompt:
//
//	user    ~/.go-coding/memory.md    user-level preferences ("always use tabs",
//	                                  "explain before editing"), survives all projects
//	project <repo>/go-coding.md       project-level notes (decisions, quirks, APIs)
//
// After each turn the driver calls AddNote; on session exit Flush appends the
// session's notes to the project file so the NEXT session loads them. The
// user file is read-only; only the project file grows. This mirrors Claude
// Code's CLAUDE.md (user + project) layering.
type Memory struct {
	userFile    string
	projectFile string
	notes       []string
}

// NewMemory builds the cross-session memory around two file paths. Empty paths
// are ignored.
func NewMemory(userFile, projectFile string) *Memory {
	return &Memory{userFile: userFile, projectFile: projectFile}
}

// Block renders the memory block for the system prompt. It is empty when
// neither file exists and no session notes were added — headless and mock runs
// inject nothing, keeping the prompt byte-identical to the no-memory baseline.
func (m *Memory) Block() string {
	var b strings.Builder
	write := func(title, content string) {
		if content = strings.TrimSpace(content); content == "" {
			return
		}
		b.WriteString("\n[" + title + "]\n" + content + "\n")
	}
	if m.userFile != "" {
		if data, err := os.ReadFile(m.userFile); err == nil {
			write("USER — persistent preferences across all sessions", string(data))
		}
	}
	if m.projectFile != "" {
		if data, err := os.ReadFile(m.projectFile); err == nil {
			write("PROJECT — persistent notes for this project", string(data))
		}
	}
	if len(m.notes) > 0 {
		write("SESSION — this session's progress so far", strings.Join(m.notes, "\n"))
	}
	return strings.TrimSpace(b.String())
}

// AddNote records one turn's outcome; it shows up in Block and is persisted by
// Flush.
func (m *Memory) AddNote(note string) { m.notes = append(m.notes, note) }

// Notes returns the session notes accumulated so far.
func (m *Memory) Notes() []string { return m.notes }

// Flush appends this session's notes to the project memory file so the next
// session sees them. No-op when there is no project file or nothing to write.
func (m *Memory) Flush() error {
	if m.projectFile == "" || len(m.notes) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.projectFile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(m.projectFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + strings.Join(m.notes, "\n") + "\n")
	return err
}
