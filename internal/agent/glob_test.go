package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestGlobFilesRecursive(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "x")
	write("sub/b.go", "x")
	write("sub/deep/c.go", "x")
	write("sub/deep/d.txt", "x")

	cases := []struct {
		pattern string
		want    []string
	}{
		{"*.go", []string{"a.go"}},
		{"**/*.go", []string{"a.go", "sub/b.go", "sub/deep/c.go"}},
		{"sub/*.go", []string{"sub/b.go"}},
		{"sub/**/*.go", []string{"sub/b.go", "sub/deep/c.go"}},
		{"**/*.txt", []string{"sub/deep/d.txt"}},
		{"nope/*.go", nil},
	}
	for _, c := range cases {
		got, err := globFiles(root, c.pattern)
		if err != nil {
			t.Fatalf("%s: %v", c.pattern, err)
		}
		sort.Strings(got)
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s = %v, want %v", c.pattern, got, c.want)
		}
	}
}

// TestGlobOpRoundTrip drives the {"op":"glob"} explore primitive through the
// real agent loop: explore finds the file, then the model plans with it.
func TestGlobOpRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	mock := newMockChat([]string{
		`{"explore":[{"op":"glob","pattern":"**/*.go"}]}`,
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`,
	})
	a := New(mock, Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it.",
		MaxRounds:   2,
	})
	if res := a.Run(context.Background()); !res.Passed() {
		t.Fatalf("run failed: %s", res.Note)
	}
}
