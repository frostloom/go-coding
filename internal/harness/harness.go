// Package harness wraps the Agent in a measurable test harness on top of
// plain testing: deterministic mock mode for CI, live mode for real
// baselines, and acceptance via hidden tests so the agent cannot "self-fill"
// its own prophecy.
package harness

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
	"github.com/zlx05/go-coding/internal/registry"
	"github.com/zlx05/go-coding/pkg/llm"
)

// CaseType classifies an evaluation case so a per-type token budget can be
// applied (P14). The three classes reflect real complexity: a bugfix is a
// surgical one-file change, an implementation writes a whole data structure,
// and a self-hosted case edits go-coding's own multi-file engine.
type CaseType string

const (
	TypeBugfix CaseType = "bugfix" // fix_*: surgical, cheapest
	TypeImpl   CaseType = "impl"   // impl_*: whole feature, mid
	TypeSelf   CaseType = "self"   // self_codeindex: dogfooding, priciest
)

// Case is one evaluation fixture: a Go project with a bug/missing feature,
// a requirement, and (optionally) a mock script for deterministic runs.
type Case struct {
	Name        string
	Type        CaseType // budget class; "" means unclassified (no gate)
	FixtureDir  string   // directory copied into the sandbox
	Requirement string
	MockScript  []string // replies for mock mode (one per Chat call)
	EvalScript  []string // replies for the evaluator role in P6 mock mode
	WantPass    bool
}

// defaultEvalPass is the mock evaluator's first-review approval, so the P6
// path on the existing 8 cases keeps the deterministic baseline (one extra
// Chat call that approves immediately).
const defaultEvalPass = `{"evaluation":"PASS","feedback":"requirement met","findings":[]}`

// evalScript returns the case's evaluator script, defaulting to an immediate
// pass. A case can override it to exercise reject→repair→approve cycles.
func (c Case) evalScript() []string {
	if len(c.EvalScript) > 0 {
		return c.EvalScript
	}
	return []string{defaultEvalPass}
}

// CaseResult is the measured outcome of one case.
type CaseResult struct {
	Name       string        `json:"name"`
	Type       CaseType      `json:"type,omitempty"` // budget class this case ran under
	Passed     bool          `json:"passed"`
	WantPass   bool          `json:"wantPass"`
	Rounds     int           `json:"rounds"`
	ToolCalls  int           `json:"toolCalls"`
	LLMCalls   int           `json:"llmCalls"` // Chat calls (producer + evaluator); single vs pe contrast
	Duration   time.Duration `json:"durationNs"`
	AgentState agent.State   `json:"agentState"`
	Note       string        `json:"note,omitempty"`
	Tokens     int           `json:"tokens"` // total tokens consumed by this case (0 in mock mode)
}

// Summary aggregates metrics across cases.
type Summary struct {
	Total             int           `json:"total"`
	Passed            int           `json:"passed"`
	SuccessRate       float64       `json:"successRate"`
	AvgRounds         float64       `json:"avgRounds"`
	AvgToolCalls      float64       `json:"avgToolCalls"`
	AvgLLMCalls       float64       `json:"avgLLMCalls"` // producer+evaluator Chat calls per case
	AvgDuration       time.Duration `json:"avgDurationNs"`
	TotalDuration     time.Duration `json:"totalDurationNs"`
	SandboxIntercepts int           `json:"sandboxIntercepts"`
	AvgTokens         int           `json:"avgTokens"`   // total tokens / cases (0 in mock mode)
	TotalTokens       int           `json:"totalTokens"` // sum across all cases
}

// Harness owns the case set and the run configuration.
type Harness struct {
	Cases      []Case
	MaxRounds  int
	RunTimeout time.Duration
	// UsePE enables the P6 evaluator–optimizer architecture (a second role
	// reviews after tests pass). false keeps the classic single-agent path.
	UsePE bool
	// Tools are external dynamic tools (skills / MCP server tools) injected
	// into every case's agent via Config.Tools — the "external knowledge" a
	// live agent may call_tool (e.g. mcp:compat:get_time_format). Nil for the
	// deterministic mock gate, which never emits a call_tool, so the 12/12
	// baseline is unchanged by Tools being present.
	Tools []registry.ToolDef
	// MaxTokens is the P14 per-task-type cost budget: case type -> token
	// ceiling for the whole run. When a type has a budget, runOne registers a
	// before_chat hook that vetoes once the session passes it, failing the
	// case instead of spending further (the same P11 cost-gate mechanism, now
	// keyed by case complexity instead of one flat CLI number). An empty map
	// sets no gate at all — the default, so mock (never spends tokens) and any
	// run without budgets stay byte-identical to the P13 baseline.
	MaxTokens map[CaseType]int
	// ExecutorModel is the per-role model override for executor workers (P22
	// hierarchical plan-and-execute): planner runs on the case's main client
	// model, each step's executor worker on this one. Empty = executor
	// inherits the planner's model.
	ExecutorModel string
}

// New builds a harness with default run settings.
func New() *Harness {
	return &Harness{MaxRounds: 5, RunTimeout: 60 * time.Second}
}

// AddCase registers an evaluation case.
func (h *Harness) AddCase(c Case) {
	h.Cases = append(h.Cases, c)
}

// RunMock runs every case in deterministic mode with the given scripted
// client, reporting failures through the testing.T subtests.
func (h *Harness) RunMock(t *testing.T, ctx context.Context) []CaseResult {
	results := make([]CaseResult, 0, len(h.Cases))
	for _, c := range h.Cases {
		t.Run(c.Name, func(t *testing.T) {
			mc := NewMockClient(c.MockScript)
			var evl llm.ChatClient
			if h.UsePE {
				evl = NewMockClient(c.evalScript())
			}
			res := h.runOne(ctx, c, mc, evl)
			results = append(results, res)
			if res.Passed != c.WantPass {
				t.Errorf("case %s: got passed=%v want %v (state=%s, note=%s)",
					c.Name, res.Passed, c.WantPass, res.AgentState, res.Note)
			}
		})
	}
	return results
}

// RunMockNoTest runs every case in mock mode without a testing.T — used by
// the CLI (cmd/harness) so mock and live produce the same result shape.
func (h *Harness) RunMockNoTest(ctx context.Context) []CaseResult {
	results := make([]CaseResult, 0, len(h.Cases))
	for _, c := range h.Cases {
		mc := NewMockClient(c.MockScript)
		var evl llm.ChatClient
		if h.UsePE {
			evl = NewMockClient(c.evalScript())
		}
		results = append(results, h.runOne(ctx, c, mc, evl))
	}
	return results
}

// RunLive runs every case against a real LLM client.
func (h *Harness) RunLive(t *testing.T, ctx context.Context, lc llm.ChatClient) []CaseResult {
	results := make([]CaseResult, 0, len(h.Cases))
	for _, c := range h.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var evl llm.ChatClient
			if h.UsePE {
				evl = lc // same model, separate role
			}
			res := h.runOne(ctx, c, lc, evl)
			results = append(results, res)
			if res.Passed != c.WantPass {
				t.Errorf("case %s: got passed=%v want %v", c.Name, res.Passed, c.WantPass)
			}
		})
	}
	return results
}

// RunLiveNoTest runs every case against a real LLM client without a
// testing.T — the CLI path.
func (h *Harness) RunLiveNoTest(ctx context.Context, lc llm.ChatClient) []CaseResult {
	results := make([]CaseResult, 0, len(h.Cases))
	for _, c := range h.Cases {
		var evl llm.ChatClient
		if h.UsePE {
			evl = lc
		}
		results = append(results, h.runOne(ctx, c, lc, evl))
	}
	return results
}

// Summarize aggregates CaseResults into metrics.
func (h *Harness) Summarize(results []CaseResult) Summary {
	s := Summary{Total: len(results)}
	var rounds, calls, llmCalls, tokens int
	var dur time.Duration
	for _, r := range results {
		if r.Passed {
			s.Passed++
		}
		rounds += r.Rounds
		calls += r.ToolCalls
		llmCalls += r.LLMCalls
		tokens += r.Tokens
		dur += r.Duration
	}
	if s.Total > 0 {
		s.SuccessRate = float64(s.Passed) / float64(s.Total)
		s.AvgRounds = float64(rounds) / float64(s.Total)
		s.AvgToolCalls = float64(calls) / float64(s.Total)
		s.AvgLLMCalls = float64(llmCalls) / float64(s.Total)
		s.AvgDuration = dur / time.Duration(s.Total)
		s.AvgTokens = tokens / s.Total
	}
	s.TotalTokens = tokens
	s.TotalDuration = dur
	return s
}

// String renders a human-readable summary line.
func (s Summary) String() string {
	return fmt.Sprintf(
		"total=%d passed=%d rate=%.0f%% avgRounds=%.1f avgToolCalls=%.1f avgLLMCalls=%.1f avgDuration=%s avgTokens=%d total=%s",
		s.Total, s.Passed, s.SuccessRate*100, s.AvgRounds, s.AvgToolCalls, s.AvgLLMCalls, s.AvgDuration, s.AvgTokens, s.TotalDuration)
}
