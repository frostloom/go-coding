package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates skills/<name>/SKILL.md (+ optional resources) under root.
func writeSkill(t *testing.T, root, name, frontmatter, body string, resources map[string]string) {
	t.Helper()
	dir := filepath.Join(root, ".go-coding", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := frontmatter + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, c := range resources {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadSkills(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	writeSkill(t, proj, "review",
		"---\nname: review\ndescription: Review code against a checklist\n---\nChecklist:\n- correctness\n",
		"", nil)
	writeSkill(t, home, "format",
		"---\ndescription: Format Go files with gofmt\n---\nRun gofmt on the changed files.",
		"", map[string]string{"templates/comment.txt": "a template file"})

	defs, err := Load(proj, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("Load = %d skills, want 2", len(defs))
	}
	if defs[0].Name != "format" || defs[1].Name != "review" { // name-sorted
		t.Errorf("unexpected order: %q, %q", defs[0].Name, defs[1].Name)
	}
	if defs[1].Description != "Review code against a checklist" {
		t.Errorf("description = %q", defs[1].Description)
	}
	if !strings.Contains(defs[0].Render(), "templates/comment.txt") {
		t.Error("Render must append resources")
	}
	// Name falls back to the directory name when frontmatter omits it.
	if defs[0].Name != "format" {
		t.Errorf("name fallback failed: %q", defs[0].Name)
	}
}

func TestLoadEmptyIsNoOp(t *testing.T) {
	defs, err := Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 0 {
		t.Errorf("expected 0 skills, got %d", len(defs))
	}
}

func TestLoadProjectWinsOnCollision(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()
	writeSkill(t, proj, "dup", "---\nname: dup\ndescription: project version\n---\nproject body", "", nil)
	writeSkill(t, home, "dup", "---\nname: dup\ndescription: user version\n---\nuser body", "", nil)
	defs, _ := Load(proj, home)
	if len(defs) != 1 {
		t.Fatalf("expected 1 skill after dedupe, got %d", len(defs))
	}
	if defs[0].Description != "project version" {
		t.Errorf("project skill should win, got %q", defs[0].Description)
	}
}
