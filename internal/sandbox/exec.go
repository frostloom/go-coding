package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// ErrTimeout is returned when a command exceeds its budget. The process
// tree is killed before returning (see kill_windows.go / kill_unix.go).
var ErrTimeout = errors.New("sandbox: command timed out")

// Run validates a command against the whitelist and executes it inside the
// sandbox root. os/exec receives the argument list directly — never a shell
// string — so `;`, `&&`, redirection and other shell metacharacters have no
// way to become active. On timeout the whole process tree is killed.
func (s *Sandbox) Run(ctx context.Context, name string, args []string, timeout time.Duration) (string, error) {
	if err := s.CheckCommand(name, args); err != nil {
		return "", err
	}
	return s.run(ctx, name, args, timeout)
}

// run executes name+args with a hard timeout and process-tree cleanup.
// It is unexported so internal plumbing (e.g. git snapshotting) can call it
// without a whitelist round-trip — the whitelist guards the agent-facing
// run_command/run_test surface, not trusted internal code.
func (s *Sandbox) run(ctx context.Context, name string, args []string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Dir = s.Root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	output := string(out)

	if cmdCtx.Err() == context.DeadlineExceeded {
		// CommandContext killed the direct child; kill the tree too so
		// grandchildren (e.g. go test's test binaries) cannot leak.
		killProcessTree(cmd)
		return output + "\n[sandbox: killed after " + timeout.String() + "]", ErrTimeout
	}
	if err != nil {
		return output, err
	}
	return output, nil
}

// Running is a handle to a process launched with Start. The child's combined
// output is captured into an internal buffer; read it after the process is
// reaped (Kill or Wait) to avoid a read/write race.
type Running struct {
	s      *Sandbox
	cmd    *exec.Cmd
	buf    bytes.Buffer
	done   chan struct{} // closed when the Wait goroutine reaps the process
	wErr   error         // Wait's error, set before done closes
}

// Start launches name inside the sandbox root and returns immediately; the
// process keeps running until Kill or Wait is called (or ctx is cancelled).
//
// It is a TRUSTED framework path — like the internal git plumbing it does NOT
// go through the LLM-facing command whitelist. It exists so the agent's own
// verification can boot a generated deliverable (e.g. a produced web server)
// and probe it with real HTTP, mirroring how `go test` compiles and runs the
// fixture. It must never be reachable from a tool the model can call.
func (s *Sandbox) Start(ctx context.Context, name string, args []string) (*Running, error) {
	r := &Running{s: s, done: make(chan struct{})}
	r.cmd = exec.CommandContext(ctx, name, args...)
	r.cmd.Dir = s.Root
	r.cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	r.cmd.Stdout = &r.buf
	r.cmd.Stderr = &r.buf
	if err := r.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		r.wErr = r.cmd.Wait()
		close(r.done)
	}()
	return r, nil
}

// Exited reports whether the process has already been reaped (exited or was
// killed). Poll-safe: never blocks.
func (r *Running) Exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// Kill reaps the whole process tree (taskkill /T /F on Windows) and waits for
// the Wait goroutine to drain the output buffer. Safe to call when the process
// already exited. Do not call Wait after Kill — the reap happens here.
func (r *Running) Kill() {
	if r.cmd.Process == nil {
		return
	}
	killProcessTree(r.cmd)
	<-r.done
}

// Wait blocks until the process exits and returns its combined output. Only
// one of Kill / Wait should be used per process.
func (r *Running) Wait() (string, error) {
	<-r.done
	return r.buf.String(), r.wErr
}

// Output returns the combined output captured so far. Read it only after the
// process has been reaped (Kill or Wait) to avoid a read/write race.
func (r *Running) Output() string { return r.buf.String() }
