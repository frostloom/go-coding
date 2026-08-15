package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantIssue(t *testing.T, issues []string, fragment string) {
	t.Helper()
	for _, s := range issues {
		if strings.Contains(s, fragment) {
			return
		}
	}
	t.Errorf("want an issue containing %q, got:\n%s", fragment, strings.Join(issues, "\n"))
}

func noIssue(t *testing.T, issues []string, fragment string) {
	t.Helper()
	for _, s := range issues {
		if strings.Contains(s, fragment) {
			t.Errorf("unexpected issue %q", s)
		}
	}
}

func TestCheckReferencesMissingAsset(t *testing.T) {
	root := t.TempDir()
	write(t, root, "static/index.html",
		`<html><head><link href="/style.css"><script src="/script.js"></script>
<script src="/app.js"></script></head><body></body></html>`)
	write(t, root, "static/app.js", "console.log(1)")
	write(t, root, "static/style.css", "body{}")

	issues := CheckReferences(root)
	// /script.js exists nowhere → flagged; /style.css and /app.js match by basename.
	wantIssue(t, issues, "/script.js")
	noIssue(t, issues, "app.js")
	noIssue(t, issues, "style.css")
}

func TestCheckReferencesRelative(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pages/index.html", `<script src="app.js"></script><script src="nope.js"></script>`)
	write(t, root, "pages/app.js", "1")
	issues := CheckReferences(root)
	noIssue(t, issues, "app.js")
	wantIssue(t, issues, "nope.js")
}

func TestCheckReferencesDotDot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "static/page.html", `<link href="../assets/a.css">`)
	write(t, root, "assets/a.css", "a{}")
	if issues := CheckReferences(root); len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckReferencesCSSURL(t *testing.T) {
	root := t.TempDir()
	write(t, root, "static/style.css",
		`body{background:url(img/logo.png)} .x{background:url(img/missing.png)}`)
	write(t, root, "static/img/logo.png", "png")
	issues := CheckReferences(root)
	wantIssue(t, issues, "missing.png")
	noIssue(t, issues, "logo.png")
}

func TestCheckReferencesSkipsNonFileRefs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html",
		`<a href="#section">x</a><a href="mailto:a@b.c">m</a>
<script src="https://cdn.example.com/lib.js"></script>
<script src="//cdn.example.com/lib2.js"></script>
<link href="data:text/css,x{}">`)
	if issues := CheckReferences(root); len(issues) != 0 {
		t.Errorf("expected non-file refs skipped, got %v", issues)
	}
}

func TestCheckReferencesQuerySuffix(t *testing.T) {
	root := t.TempDir()
	write(t, root, "index.html", `<script src="/app.js?v=3#x"></script>`)
	write(t, root, "app.js", "1")
	if issues := CheckReferences(root); len(issues) != 0 {
		t.Errorf("expected query/hash stripped, got %v", issues)
	}
}

func TestCheckReferencesCleanDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main")
	if issues := CheckReferences(root); len(issues) != 0 {
		t.Errorf("no web assets → no issues, got %v", issues)
	}
}
