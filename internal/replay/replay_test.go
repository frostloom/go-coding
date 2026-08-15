package replay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
)

// sampleEvents builds a representative run history: plan → edit → test →
// repair → edit → test → done (the repair path, so the transcript shows both
// a failure and a success).
func sampleEvents() []agent.Event {
	t := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	return []agent.Event{
		{Time: t, State: agent.StatePlanning, Detail: "task start"},
		{Time: t.Add(time.Second), State: agent.StatePlanning, Tool: "explore", Detail: "## list_dir\nmathops.go"},
		{Time: t.Add(2 * time.Second), State: agent.StatePlanning, Detail: "plan: 1 steps"},
		{Time: t.Add(3 * time.Second), State: agent.StateEditing, Tool: "edit_file", Detail: "mathops.go: attempt multiply"},
		{Time: t.Add(4 * time.Second), State: agent.StateExecuting, Tool: "run_test", Detail: "FAIL: want 5 got 6"},
		{Time: t.Add(5 * time.Second), State: agent.StateRepairing, Detail: "tests failed, entering repair"},
		{Time: t.Add(6 * time.Second), State: agent.StateEditing, Tool: "edit_file", Detail: "mathops.go: fix to add"},
		{Time: t.Add(7 * time.Second), State: agent.StateExecuting, Tool: "run_test", Detail: "ok fixadd 1.002s"},
		{Time: t.Add(8 * time.Second), State: agent.StateDone, Detail: "tests passed"},
	}
}

// TestTextTimeline verifies the human-readable rendering preserves the
// ordering and marks tool calls.
func TestTextTimeline(t *testing.T) {
	events := sampleEvents()
	txt := Text(events)

	for _, want := range []string{"edit_file", "run_test", "repair", "tests passed", "FAIL"} {
		if !strings.Contains(txt, want) {
			t.Errorf("timeline missing %q:\n%s", want, txt)
		}
	}
	// Tool lines must be marked with the arrow.
	if !strings.Contains(txt, "→ edit_file") {
		t.Errorf("tool calls should be arrow-marked:\n%s", txt)
	}
	// Ordering: "repair" must come after the first run_test (failure) and
	// before the second edit.
	if strings.Index(txt, "run_test") > strings.Index(txt, "repair") {
		t.Errorf("repair should follow the failing test:\n%s", txt)
	}
}

// TestJSONRoundTrip verifies the machine-readable log serializes and
// deserializes losslessly.
func TestJSONRoundTrip(t *testing.T) {
	events := sampleEvents()
	b, err := JSON(events)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back []agent.Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != len(events) {
		t.Fatalf("round-trip length = %d, want %d", len(back), len(events))
	}
	// Spot-check one event's fields survive.
	if back[3].Tool != "edit_file" || back[4].Detail != "FAIL: want 5 got 6" {
		t.Errorf("round-trip mangled events: %+v", back[3:5])
	}
}

// TestTimelineElapsed verifies Timeline attaches elapsed-since-start and
// sorts (the events are fed in-order here, but the function must not depend
// on caller ordering).
func TestTimelineElapsed(t *testing.T) {
	events := sampleEvents()
	tl := Timeline(events)
	if len(tl) != len(events) {
		t.Fatalf("timeline length = %d, want %d", len(tl), len(events))
	}
	if tl[0].Elapsed != 0 {
		t.Errorf("first event elapsed = %v, want 0", tl[0].Elapsed)
	}
	if tl[len(tl)-1].Elapsed != 8*time.Second {
		t.Errorf("last event elapsed = %v, want 8s", tl[len(tl)-1].Elapsed)
	}
	// Must be non-decreasing even if input is shuffled.
	shuffled := []agent.Event{events[5], events[1], events[8], events[3]}
	tl2 := Timeline(shuffled)
	for i := 1; i < len(tl2); i++ {
		if tl2[i].Elapsed < tl2[i-1].Elapsed {
			t.Errorf("elapsed not sorted: [%d]=%v < [%d]=%v", i, tl2[i].Elapsed, i-1, tl2[i-1].Elapsed)
		}
	}
}

// TestBuildTranscript verifies Build summarizes the run alongside the log.
func TestBuildTranscript(t *testing.T) {
	res := agent.Result{
		TaskID:    "t1",
		State:     agent.StateDone,
		Rounds:    2,
		ToolCalls: 4,
		Note:      "tests passed",
		Events:    sampleEvents(),
	}
	tr := Build(res)
	s := tr.String()
	for _, want := range []string{"t1", "state=done rounds=2 toolCalls=4", "edit_file"} {
		if !strings.Contains(s, want) {
			t.Errorf("transcript missing %q:\n%s", want, s)
		}
	}
}

// TestTruncateDetail verifies long detail text is capped so transcripts stay
// scannable.
func TestTruncateDetail(t *testing.T) {
	long := strings.Repeat("x", 500)
	txt := Text([]agent.Event{{Time: time.Now(), State: agent.StateExecuting, Tool: "run_test", Detail: long}})
	if len(txt) > 200 {
		t.Errorf("timeline should truncate long detail, got %d chars", len(txt))
	}
	if !strings.Contains(txt, "…") {
		t.Errorf("truncation should leave an ellipsis, got:\n%s", txt)
	}
}
