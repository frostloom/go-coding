//go:build gate

// The mock gate: run the full case set through the deterministic mock LLM and
// assert every case passes. This is the regression the project's "CI-safe"
// claim rests on — no network, no tokens, byte-deterministic. It is kept
// behind the `gate` build tag so a plain `go test ./...` stays fast for
// day-to-day development; run it explicitly (also scripts/gate.sh):
//
//	go test -tags gate ./internal/harness -count=1
package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// chdirToRepoRoot moves the test process to the repository root. go test runs
// with the package directory as CWD, but Cases() reads its fixtures relative
// to the repo root (readFixture) — so the gate must find and enter it first.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "testdata", "cases")); err == nil {
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot locate repo root (no testdata/cases in any parent)")
		}
		dir = parent
	}
}

// TestMockGateAllCases is the full-suite mock gate. It mirrors what
// cmd/harness -mode mock does, but as an automated guard so the "12/12"
// baseline cannot silently drift.
func TestMockGateAllCases(t *testing.T) {
	chdirToRepoRoot(t)
	for _, c := range Cases() {
		if c.Type == "" {
			t.Errorf("case %s has no Type — the P14 per-type cost gate would silently skip it", c.Name)
		}
	}
	h := New()
	h.MaxRounds = 5
	h.RunTimeout = 60 * time.Second
	for _, c := range Cases() {
		h.AddCase(c)
	}
	results := h.RunMockNoTest(context.Background())
	s := h.Summarize(results)
	if s.Passed != s.Total {
		for _, r := range results {
			if !r.Passed {
				t.Errorf("[FAIL] %-16s wantPass=%v state=%s note=%s", r.Name, r.WantPass, r.AgentState, r.Note)
			}
		}
		t.Fatalf("mock gate: %d/%d passed (want 100%%), avgRounds=%v", s.Passed, s.Total, s.AvgRounds)
	}
	t.Logf("mock gate: %d/%d passed avgRounds=%v avgToolCalls=%v avgLLMCalls=%v",
		s.Passed, s.Total, s.AvgRounds, s.AvgToolCalls, s.AvgLLMCalls)
}
