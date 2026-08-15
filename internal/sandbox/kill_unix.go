//go:build !windows

package sandbox

import "os/exec"

// killProcessTree terminates the command on Unix. The child is already
// killed by exec.CommandContext on timeout; killing the process is a
// best-effort no-op safety net (full process-group cleanup would need
// Setpgid, which is out of scope for the Windows-primary target).
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
