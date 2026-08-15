package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSummarizeMetrics verifies the aggregated capability metrics are
// computed correctly from per-case results.
func TestSummarizeMetrics(t *testing.T) {
	h := New()
	h.AddCase(Case{Name: "a"})
	h.AddCase(Case{Name: "b"})
	h.AddCase(Case{Name: "c"})

	results := []CaseResult{
		{Name: "a", Passed: true, Rounds: 1, ToolCalls: 2, LLMCalls: 3, Duration: time.Second},
		{Name: "b", Passed: true, Rounds: 3, ToolCalls: 6, LLMCalls: 4, Duration: 2 * time.Second},
		{Name: "c", Passed: false, Rounds: 5, ToolCalls: 10, LLMCalls: 8, Duration: 3 * time.Second},
	}
	s := h.Summarize(results)

	if s.Total != 3 || s.Passed != 2 {
		t.Fatalf("total/passed = %d/%d, want 3/2", s.Total, s.Passed)
	}
	if s.SuccessRate != 2.0/3.0 {
		t.Errorf("success rate = %v, want %v", s.SuccessRate, 2.0/3.0)
	}
	if s.AvgRounds != 3 || s.AvgToolCalls != 6 {
		t.Errorf("avgRounds/avgToolCalls = %.1f/%.1f, want 3/6", s.AvgRounds, s.AvgToolCalls)
	}
	if s.AvgLLMCalls != 5 {
		t.Errorf("avgLLMCalls = %.1f, want 5", s.AvgLLMCalls)
	}
	if s.AvgDuration != 2*time.Second {
		t.Errorf("avgDuration = %v, want 2s", s.AvgDuration)
	}
}

// TestWriteReportRoundTrip verifies the machine-readable report (mode +
// cases + summary) survives a JSON round-trip intact.
func TestWriteReportRoundTrip(t *testing.T) {
	h := New()
	results := []CaseResult{
		{Name: "fix_add", Passed: true, Rounds: 1, ToolCalls: 2, Duration: 2 * time.Second},
	}
	s := h.Summarize(results)
	path := filepath.Join(t.TempDir(), "report.json")

	if err := WriteReport(path, "live", results, s); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var rep Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if rep.Mode != "live" {
		t.Errorf("mode = %q, want live", rep.Mode)
	}
	if len(rep.Cases) != 1 || !rep.Cases[0].Passed || rep.Cases[0].Rounds != 1 {
		t.Errorf("cases in report = %+v, want 1 passed case", rep.Cases)
	}
	if rep.Summary.SuccessRate != 1 {
		t.Errorf("summary rate = %v, want 1", rep.Summary.SuccessRate)
	}
}
