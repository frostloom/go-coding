package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// TestRunTestToolCustomCmd proves the configured test command actually runs:
// an empty directory has no go.mod, so the DEFAULT `go test ./...` would fail
// with "go.mod file not found" — a passing `go version` can only mean the
// custom Cmd was used instead. `go version` is not in the built-in allowlist,
// so the test also exercises AllowCommands (P20: the multi-language escape
// hatch for the sandbox's Go+git whitelist).
func TestRunTestToolCustomCmd(t *testing.T) {
	root := t.TempDir()
	sb, err := sandbox.New(root)
	if err != nil {
		t.Fatal(err)
	}
	sb.AllowCommands(map[string][]string{"go": {"version"}})
	at := &RunTestTool{Sandbox: sb, Timeout: 30 * time.Second, Cmd: []string{"go", "version"}}
	res, err := at.Run(context.Background(), Args(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatalf("custom command 'go version' should pass in an empty dir, got output:\n%s", res.Output)
	}
}

// TestRunTestToolDefaultStillRunsGoTest locks the default: with no Cmd set, a
// minimal Go project must still pass through `go test ./...` — the mock gate's
// byte-identical baseline depends on this default being untouched.
func TestRunTestToolDefaultStillRunsGoTest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello_test.go"), []byte(`package demo

import "testing"

func TestHello(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math is broken")
	}
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(root)
	if err != nil {
		t.Fatal(err)
	}
	at := &RunTestTool{Sandbox: sb, Timeout: 30 * time.Second}
	res, err := at.Run(context.Background(), Args(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatalf("default go test should pass the trivial project, got output:\n%s", res.Output)
	}
}
