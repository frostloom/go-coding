package hang

import (
	"testing"
	"time"
)

// TestHang deliberately never completes. It exists so the sandbox timeout
// + process-tree-kill logic can be verified against a real go test child.
func TestHang(t *testing.T) {
	time.Sleep(time.Hour)
}
