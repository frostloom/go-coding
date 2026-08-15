package timefmt

import "testing"

// Hidden acceptance test: stashed out of the agent's view by the Harness and
// only applied after the run, so the agent cannot edit the test that grades
// it. Asserts the legacy wire layout "06_002_150405" (year2_dayOfYear_HHMMSS)
// — the compact format only the compatibility MCP tool reveals.
func TestLegacyWireFormatAcceptance(t *testing.T) {
	if got := FormatTimestamp(1600000000); got != "20_257_122640" {
		t.Fatalf("FormatTimestamp(1600000000) = %q, want legacy wire format %q", got, "20_257_122640")
	}
}
