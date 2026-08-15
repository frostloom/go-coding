package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestRunTimeoutKillsProcessTree runs a real `go test` against a fixture
// whose test hangs forever. The sandbox must return with ErrTimeout (not
// hang forever) and include the timeout marker in its output — proving the
// whole process tree (go + the sleeping test binary) was terminated.
func TestRunTimeoutKillsProcessTree(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "hang")
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	out, err := sb.run(context.Background(), "go", []string{"test", "./..."}, 3*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("test took too long (%s); process tree was not killed", elapsed)
	}
	if out == "" {
		t.Error("expected some output before the timeout marker")
	}
}

// TestRunRejectsNonWhitelisted ensures even the low-level Run surface blocks
// disallowed commands before they could execute.
func TestRunRejectsNonWhitelisted(t *testing.T) {
	sb, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Run(context.Background(), "bash", []string{"-c", "echo pwned"}, time.Second); err == nil {
		t.Error("bash must be rejected")
	}
	if _, err := sb.Run(context.Background(), "go", []string{"run", "."}, time.Second); err == nil {
		t.Error("go run must be rejected")
	}
}

func TestRunValidCommand(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "sample")
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := sb.Run(context.Background(), "go", []string{"test", "./..."}, 30*time.Second)
	if err != nil {
		t.Fatalf("go test should pass: %v", err)
	}
	if out == "" {
		t.Error("expected test output")
	}
}

// TestStartKillReapsTree verifies the non-blocking Start + process-tree Kill
// path: a long-lived `go test` against the hanging fixture must start, then be
// reaped (with its sleeping test binary) when Kill is called — it must not
// hang and must not leave the process alive.
func TestStartKillReapsTree(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "hang")
	sb, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := sb.Start(context.Background(), "go", []string{"test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // let go test spawn its hanging test binary
	if r.Exited() {
		t.Fatal("process exited before Kill")
	}
	done := make(chan struct{})
	go func() { r.Kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Kill hung; process tree was not reaped")
	}
	if !r.Exited() {
		t.Fatal("process not reaped after Kill")
	}
}
