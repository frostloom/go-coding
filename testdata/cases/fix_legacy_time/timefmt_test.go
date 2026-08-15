package timefmt

import "testing"

// Visible test: fails on the buggy code (raw integer), which is what drives
// the repair loop. It deliberately does NOT pin the exact wire layout — that
// is known only to the compatibility knowledge base (an MCP tool), and the
// hidden acceptance test asserts it.
func TestFormatTimestampNonEmpty(t *testing.T) {
	if got := FormatTimestamp(1600000000); got == "" {
		t.Fatal("FormatTimestamp(1600000000) returned empty string")
	}
}
