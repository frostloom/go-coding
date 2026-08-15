package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".go-coding", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAgents(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	writeAgent(t, proj, "researcher.md", `---
name: researcher
description: Read-only codebase investigation
read_only: true
---
You are a researcher. Explore the code and report findings with file paths.`)
	writeAgent(t, home, "coder.md", `---
description: Implement features in Go
---
You are a coder. Plan and apply edits.`)

	defs, err := Load(proj, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("Load = %d agents, want 2", len(defs))
	}
	if defs[0].Name != "coder" || defs[1].Name != "researcher" { // name-sorted
		t.Errorf("unexpected order: %q, %q", defs[0].Name, defs[1].Name)
	}
	if !defs[1].ReadOnly {
		t.Error("researcher should be read_only")
	}
	if defs[0].ReadOnly {
		t.Error("coder should default to full access")
	}
	if defs[1].Prompt == "" || !strings.Contains(defs[1].Prompt, "report findings") {
		t.Errorf("prompt missing: %q", defs[1].Prompt)
	}
}

func TestLoadProjectWinsAndEmptyIsNoOp(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	writeAgent(t, proj, "dup.md", "---\nname: dup\ndescription: project\n---\nproject role")
	writeAgent(t, home, "dup.md", "---\nname: dup\ndescription: user\n---\nuser role")
	defs, _ := Load(proj, home)
	if len(defs) != 1 || defs[0].Description != "project" {
		t.Errorf("project should win on collision, got %+v", defs)
	}

	if defs2, err := Load(t.TempDir(), t.TempDir()); err != nil || len(defs2) != 0 {
		t.Errorf("empty dirs should load nothing, got %v err=%v", defs2, err)
	}
}
