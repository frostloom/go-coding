package agent

import (
	"time"

	"github.com/zlx05/go-coding/pkg/llm"
)

// Step is one edit produced by the LLM. In M1 it is whole-file content;
// M4 replaces Content with precise byte-range edits. In P22 executor mode the
// planner emits OUTLINE steps — File + Desc + Accept (acceptance criteria) with
// empty Content — and editing() dispatches each to a worker executor that
// derives the content; Content is only present in the classic atomic protocol.
type Step struct {
	File    string `json:"file"`
	Content string `json:"content"`
	Desc    string `json:"desc"`
	// Accept is the step's acceptance criteria: how correctness of the produced
	// file is verified (executor mode). Empty in the classic content protocol.
	Accept string `json:"accept"`
}

// Event is one structured log entry for the full history — the basis for
// replay and for Harness metrics.
type Event struct {
	Time   time.Time `json:"time"`
	State  State     `json:"state"`
	Tool   string    `json:"tool,omitempty"`
	Detail string    `json:"detail,omitempty"`
	// Tokens is the session's cumulative token cost at the moment this event
	// was recorded. It lets governance hooks read cost without tracking it
	// themselves, and lets a replay show spend over time. Zero until the first
	// Chat returns usage (mock runs stay 0, so the baseline is unchanged).
	Tokens int `json:"tokens,omitempty"`
}

// Session carries all state of one task run.
type Session struct {
	TaskID      string
	RepoPath    string // sandbox root
	Requirement string
	State       State
	Plan        []Step
	History     []Event
	Round       int
	MaxRounds   int
	TestOutput  string
	Usage       llm.Usage // accumulated token consumption across all Chat calls
	LLMCalls    int        // number of Chat calls (producer + evaluator) in this run
	lastTestOK  bool       // private: outcome of the last run_test
	// Evaluator–Optimizer state (only used when a Config.Evaluator is wired).
	SymbolBase *SymbolInventory // objective symbol baseline at task start
	// onEvent, when set, is fired synchronously for every event as it happens,
	// letting an interactive driver render the agent's actions live instead of
	// only after the run completes. Headless callers (harness, worker, MCP)
	// leave it nil.
	onEvent func(Event)
}

// AddUsage accumulates one completion's tokens into the session total.
func (s *Session) AddUsage(u llm.Usage) {
	s.Usage.PromptTokens += u.PromptTokens
	s.Usage.CompletionTokens += u.CompletionTokens
	s.Usage.TotalTokens += u.TotalTokens
}

// Log appends a structured event and, when a live renderer is wired, fires it
// immediately. The two paths share the same Event so replay and the terminal
// always describe the same run.
func (s *Session) Log(state State, tool, detail string) {
	ev := Event{Time: time.Now(), State: state, Tool: tool, Detail: detail, Tokens: s.Usage.TotalTokens}
	s.History = append(s.History, ev)
	if s.onEvent != nil {
		s.onEvent(ev)
	}
}

// ToolCalls counts tool invocations across the history.
func (s *Session) ToolCalls() int {
	n := 0
	for _, e := range s.History {
		if e.Tool != "" {
			n++
		}
	}
	return n
}
