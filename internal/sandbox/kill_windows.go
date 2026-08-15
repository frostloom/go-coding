//go:build windows

package sandbox

import (
	"os/exec"
	"strconv"
)

// killProcessTree terminates the command's whole process tree.
//
// On Windows exec.Cmd.Kill only terminates the direct child; a `go test`
// invokes a test binary, so killing only `go` would leak the compiled test
// process and it would keep running forever. taskkill /T /F kills the tree.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
}
