package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/pkg/llm"
)

// TestMemoryBlockLoadsUserAndProject verifies the cross-session block pulls in
// both the user-level and project-level memory files.
func TestMemoryBlockLoadsUserAndProject(t *testing.T) {
	dir := t.TempDir()
	userFile := filepath.Join(dir, "memory.md")
	projFile := filepath.Join(dir, "go-coding.md")
	os.WriteFile(userFile, []byte("always use tabs, explain before editing"), 0o644)
	os.WriteFile(projFile, []byte("fix_add fixture: Add is a - b; return a + b"), 0o644)

	m := NewMemory(userFile, projFile)
	blk := m.Block()
	if !strings.Contains(blk, "USER") || !strings.Contains(blk, "always use tabs") {
		t.Fatalf("block missing user memory: %q", blk)
	}
	if !strings.Contains(blk, "PROJECT") || !strings.Contains(blk, "fix_add fixture") {
		t.Fatalf("block missing project memory: %q", blk)
	}
}

// TestMemoryBlockEmptyWhenNoFiles verifies the gate: with no memory files and
// no notes, the block is empty so headless/mock prompts stay byte-identical.
func TestMemoryBlockEmptyWhenNoFiles(t *testing.T) {
	m := NewMemory("", "")
	if blk := m.Block(); blk != "" {
		t.Fatalf("block = %q, want empty", blk)
	}
}

// TestMemoryNotesThenFlush verifies notes accumulate in the session block and
// Flush persists them to the project file for the next session.
func TestMemoryNotesThenFlush(t *testing.T) {
	dir := t.TempDir()
	projFile := filepath.Join(dir, "go-coding.md")
	m := NewMemory("", projFile)
	m.AddNote("- [10:30] fix Add → done (1 rounds)")
	m.AddNote("- [10:31] add Subtract → failed (3 rounds)")
	if blk := m.Block(); !strings.Contains(blk, "SESSION") || !strings.Contains(blk, "fix Add") {
		t.Fatalf("session notes not in block: %q", blk)
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(projFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fix Add") || !strings.Contains(string(data), "add Subtract") {
		t.Fatalf("flushed notes missing: %q", data)
	}
	// A second session now loads what the first session flushed.
	m2 := NewMemory("", projFile)
	if blk := m2.Block(); !strings.Contains(blk, "fix Add") {
		t.Fatalf("next session did not load flushed notes: %q", blk)
	}
}

// TestContextManagerSessionMemoryInjected verifies the memory block lands in
// the system prompt only when set — the empty case keeps the baseline prompt.
func TestContextManagerSessionMemoryInjected(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	sess := &Session{Requirement: "Fix Add"}
	cm := NewContextManager(nil)
	msgs := cm.Messages(context.Background(), sess, llm.Message{Role: llm.RoleUser, Content: "Q"})
	if strings.Contains(msgs[0].Content, "SESSION MEMORY") {
		t.Fatal("memory block injected before SetSessionMemory")
	}
	cm.SetSessionMemory("user prefers tabs")
	msgs = cm.Messages(context.Background(), sess, llm.Message{Role: llm.RoleUser, Content: "Q"})
	if !strings.Contains(msgs[0].Content, "SESSION MEMORY") || !strings.Contains(msgs[0].Content, "user prefers tabs") {
		t.Fatalf("memory block not injected: %q", msgs[0].Content)
	}
}
