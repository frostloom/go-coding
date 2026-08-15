// Package replay turns a finished agent run's structured event log back
// into a readable timeline: state transitions, every tool call, and the
// final outcome. It is the "what did the agent actually do" answer for a
// completed task — used to debug a failure, present a run, or audit edits.
//
// It is intentionally tiny: the agent already records every meaningful
// action as an Event (session.go), so replay is a pure function from
// []agent.Event to a transcript — no separate logging pipeline to keep in
// sync. This is the M6 "logs + replay" milestone: structured logs in,
// readable timeline out.
package replay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
)

// Transcript is the human-readable rendering of one run's events plus its
// outcome summary.
type Transcript struct {
	TaskID    string
	Requirement string
	Outcome   string // final state description
	Lines     []string
}

// Text renders the events as an indented, timestamped timeline. Tool calls
// are marked; detail text is shown but truncated so a transcript stays
// scannable.
func Text(events []agent.Event) string {
	var b strings.Builder
	for _, e := range events {
		ts := e.Time.Format("15:04:05.000")
		if e.Tool != "" {
			fmt.Fprintf(&b, "[%s] %-12s → %s %s\n", ts, e.State, e.Tool, truncate(e.Detail, 120))
		} else {
			fmt.Fprintf(&b, "[%s] %-12s %s\n", ts, e.State, truncate(e.Detail, 120))
		}
	}
	return b.String()
}

// JSON renders the full event log as pretty JSON — the machine-readable
// form for post-run analysis or feeding other tools.
func JSON(events []agent.Event) ([]byte, error) {
	return json.MarshalIndent(events, "", "  ")
}

// Build assembles a complete Transcript from a run result: the timeline
// plus a one-line outcome summary.
func Build(res agent.Result) *Transcript {
	return &Transcript{
		TaskID:      res.TaskID,
		Requirement: res.Note,
		Outcome:     fmt.Sprintf("state=%s rounds=%d toolCalls=%d %s", res.State, res.Rounds, res.ToolCalls, res.Note),
		Lines:       splitLines(Text(res.Events)),
	}
}

// String renders the transcript as a single block.
func (t *Transcript) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "task %s — %s\n", t.TaskID, t.Outcome)
	for _, l := range t.Lines {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

// TimelineEvent is one event enriched with elapsed time since the run start,
// so a replay shows pacing (e.g. "0.3s: go test").
type TimelineEvent struct {
	Time    time.Time     `json:"time"`
	Elapsed time.Duration `json:"elapsedNs"`
	State   agent.State   `json:"state"`
	Tool    string        `json:"tool,omitempty"`
	Detail  string        `json:"detail,omitempty"`
}

// Timeline returns events sorted by time with elapsed-since-start attached.
func Timeline(events []agent.Event) []TimelineEvent {
	sorted := make([]agent.Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	start := time.Time{}
	out := make([]TimelineEvent, 0, len(sorted))
	for _, e := range sorted {
		if start.IsZero() {
			start = e.Time
		}
		out = append(out, TimelineEvent{
			Time:    e.Time,
			Elapsed: e.Time.Sub(start),
			State:   e.State,
			Tool:    e.Tool,
			Detail:  e.Detail,
		})
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
