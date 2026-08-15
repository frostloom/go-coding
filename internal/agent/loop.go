package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zlx05/go-coding/internal/codeindex"
	"github.com/zlx05/go-coding/internal/registry"
	"github.com/zlx05/go-coding/internal/sandbox"
	"github.com/zlx05/go-coding/internal/subagent"
	"github.com/zlx05/go-coding/internal/tools"
	"github.com/zlx05/go-coding/internal/verify"
	"github.com/zlx05/go-coding/pkg/llm"
)

// Config controls one agent run.
type Config struct {
	RepoPath    string // sandbox root (the target project)
	Requirement string
	MaxRounds   int // default 5
	RunTimeout  time.Duration
	// TestCommand overrides the run_test command (argv, e.g. []string{"pytest",
	// "-q"}). It is how the agent drives non-Go projects' test suites; empty
	// means the default `go test ./...`. P20: de-hardcodes Go from the loop.
	TestCommand []string
	// AllowedCommands extends the sandbox's command allowlist for non-Go
	// tooling the operator explicitly opts into (e.g. {"python": {"*"}} so
	// `python -m pytest` can run). nil keeps the strict Go+git default — the
	// security baseline is unchanged unless the operator widens it. P20.
	AllowedCommands map[string][]string
	Hooks           map[HookPoint][]Hook // optional lifecycle hooks
	// Evaluator–Optimizer (P6). When Evaluator is non-nil, tests passing no
	// longer ends the task: a separate evaluator role (objective symbol gate +
	// LLM review) must approve first, otherwise the feedback is fed back and
	// the producer repairs. nil keeps the classic single-agent path untouched.
	Evaluator    llm.ChatClient // the evaluator role's chat client (nil = single-agent)
	MaxEvalRounds int            // max evaluator-driven repair iterations (default 1)
	// OnStream, when set, receives every content delta the LLM streams back —
	// the "thinking out loud" an interactive terminal renders. nil = silent.
	OnStream func(string)
	// OnEvent, when set, is fired synchronously for each session event as it
	// happens, so a driver can render the agent's actions live instead of only
	// after the run completes. nil = silent (headless: harness, worker, MCP).
	OnEvent func(Event)
	// Tools are external tools registered into the agent's dynamic tool
	// registry (skills, MCP clients, subagent toolsets). The LLM invokes them
	// via the {"call_tool":{...}} protocol primitive. nil = none.
	Tools []registry.ToolDef
	// SessionMemory is the cross-session memory block (user + project memory
	// files, see Memory in session_memory.go), injected into the system prompt.
	// Empty means none — headless and mock runs stay prompt-identical.
	SessionMemory string
	// Confirm is the permission gate — the "dangerous command asks you" layer
	// of the defense-in-depth story. It is consulted before every write action
	// (edit_file) and before running tests; returning false vetoes the action
	// and routes the agent into REPAIRING (like a before_* hook veto). nil
	// means fully automatic — headless harness/worker/MCP runs never ask, so
	// the baseline behavior is untouched. The interactive REPL wires it to a
	// real [y/N] prompt; the gate can be made as strict or as permissive as
	// the operator wants.
	Confirm func(action PermissionAction) bool
	// InPlace marks direct in-place editing (cmd/agent -direct): the repo is the
	// operator's own project, edited without a sandbox copy. The sandbox then
	// snapshots via EnsureRepoInPlace — reusing the existing git history without
	// adding a baseline commit, so undo and git_diff still work. false keeps the
	// copy-isolated workspace. P21.
	InPlace bool
	// SubAgents are registered child agents (see internal/subagent) surfaced to
	// the LLM as dynamic tools "subagent:<name>". The model dispatches a task
	// to one; the child runs in the SAME sandbox workspace and symbol index as
	// the parent (shared state) and its Result is fed back as text. nil = none.
	SubAgents []subagent.Def
	// Executor is the hierarchical plan-and-execute mode (P22): the parent
	// (planner) produces an OUTLINE plan and editing() dispatches each step to
	// a worker subagent (the executor) that implements the file and runs tests.
	// Children are always built with Executor unset, so they use the classic
	// content plan — no recursion. true by default at the CLI/harness entry
	// points; false keeps the legacy atomic whole-content path.
	Executor bool
	// ExecutorModel names the LLM model executor workers run on (per-role model
	// tiering: planner on the main model, cheaper executor on this one). Empty
	// inherits the planner's model. The override rides ChatRequest.Model, which
	// the OpenAI client honors when non-empty.
	ExecutorModel string
}

// PermissionAction describes one action passing through the permission gate.
type PermissionAction struct {
	Op     string // "edit_file" | "run_test" | ...
	Detail string // the file/path or command being acted on
}

// Result is the terminal outcome of a run, consumed by CLI and Harness alike.
type Result struct {
	TaskID     string        `json:"taskId"`
	State      State         `json:"state"`
	Rounds     int           `json:"rounds"`
	ToolCalls  int           `json:"toolCalls"`
	LLMCalls   int           `json:"llmCalls"`
	TestOutput string        `json:"testOutput,omitempty"`
	Note       string        `json:"note,omitempty"`
	Events     []Event       `json:"events"`
	Usage      llm.Usage     `json:"usage"`
}

// Passed reports whether the task finished in the Done state.
func (r Result) Passed() bool { return r.State == StateDone }

// Agent drives one Session through the state machine. The loop is decoupled
// from any driving environment: the CLI and Harness both call Run, which is
// why Harness can test the real closed loop without a simulated environment.
type Agent struct {
	llm     llm.ChatClient
	sess    *Session
	sandbox *sandbox.Sandbox
	// builder is the symbol index shared by retrieval (ContextProvider) and the
	// EditTool's edit-time validation. Subagents inherit the parent's pointer,
	// so a child edit is immediately visible to the parent's retrieval — the
	// "shared state" of subagent dispatch.
	builder *codeindex.Builder
	ctxMgr  *ContextManager
	tools   *agentTools
	hooks   map[HookPoint][]Hook
	evaluator  *Evaluator   // non-nil only in P6 evaluator–optimizer mode
	maxEvalRounds int       // stop criterion for evaluator-driven repairs
	stream   func(string)   // optional live renderer for LLM stream deltas
	reg      *registry.Registry // dynamic tool registry (skills / MCP / subagents)
	toolErr  error             // a Config.Tools registration failure, surfaced at plan time
	confirmFn func(PermissionAction) bool // permission gate; nil = fully automatic
	// extTools is the original external tool set (skills / MCP), re-seeded into
	// child agents so they share the toolset without inheriting the subagent
	// tools themselves (which would recurse).
	extTools []registry.ToolDef
	// testCmdDisplay is the human-readable test command for logs/permission
	// prompts: "go test" by default, the joined override otherwise. Kept on the
	// Agent so run_test's Detail/hook messages say what actually runs.
	testCmdDisplay string
	// modelOverride is the per-role LLM model a child agent runs on
	// (agentShared.modelOverride). Empty on the top-level agent.
	modelOverride string
	// executor is hierarchical plan-and-execute mode: the parent outlines and
	// editing() dispatches each step to a worker subagent. Children are always
	// false (their cfg.Executor is unset), so a worker runs the classic content
	// plan — the recursion terminates at one level.
	executor bool
	// executorModel names the LLM model executor workers run on; empty inherits
	// the parent's model (per-role model tiering).
	executorModel string
	// child marks a subagent (shared != nil). Deliverable verification (P23)
	// runs only on the TOP-LEVEL agent: a per-step worker would boot a server
	// before sibling steps have written its assets, and the parent's
	// after-all-steps gate is the cross-file integration point anyway.
	child bool
	// verifyBaseline is the commit at the start of the current turn. The
	// deliverable gate diffs against it (sandbox.ChangedFilesSince) so it only
	// verifies the files THIS task produced — a pre-existing server the task
	// never touched is not booted, and a follow-up REPL turn does not re-boot a
	// server a previous turn built. Claude Code verifies what it built, not the
	// whole repository.
	verifyBaseline string
}

// confirm asks the permission gate before a write/test action. A nil gate is
// "allow everything" — headless runs (harness, worker, MCP) stay automatic.
func (a *Agent) confirm(pa PermissionAction) bool {
	if a.confirmFn == nil {
		return true
	}
	return a.confirmFn(pa)
}

// chat runs one LLM call. When the client supports streaming and a live
// renderer is wired, the response is streamed (deltas delivered to OnStream);
// otherwise it is the plain call. Scripted mock clients do not implement
// ChatStreamer, so mock mode stays deterministic regardless of OnStream.
//
// before_chat fires first: it is the one hook that can refuse the call BEFORE
// any token is spent (a cost gate). A veto here returns an error, which fails
// the task rather than routing to REPAIRING — repairing would spend more
// tokens on exactly the thing the gate is trying to stop.
func (a *Agent) chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	if err := a.fire(ctx, HookBeforeChat, Event{State: StatePlanning, Tool: "chat", Detail: "LLM call"}); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("cost gate: %w", err)
	}
	// Per-role model tiering: a child agent (executor worker / subagent) with a
	// model override requests that model explicitly; the OpenAI client honors
	// non-empty ChatRequest.Model over its own configured one.
	if a.modelOverride != "" {
		req.Model = a.modelOverride
	}
	if sc, ok := a.llm.(llm.ChatStreamer); ok && a.stream != nil {
		return sc.ChatStream(ctx, req, a.stream)
	}
	return a.llm.Chat(ctx, req)
}

// ClearMemory drops the conversation history (the /clear command). The
// workspace and the symbol index are untouched, so the agent keeps the files
// it edited but forgets the conversation.
func (a *Agent) ClearMemory() {
	a.ctxMgr.Reset()
}

// Workspace returns the sandbox root the agent edits.
func (a *Agent) Workspace() string { return a.sess.RepoPath }

// SessionTokens returns the token consumption accumulated across all turns of
// this session (the /tokens command reads it).
func (a *Agent) SessionTokens() int { return a.sess.Usage.TotalTokens }

// SetAutoCompact enables Claude-Code-style auto-compaction on this agent:
// when the conversation history passes thresholdRunes, the oldest rounds are
// summarized by one LLM call instead of being truncated. The CLI enables it
// for live sessions (the REPL above all); headless/harness runs leave it off
// so mock determinism and the baseline numbers are untouched.
func (a *Agent) SetAutoCompact(thresholdRunes int) {
	a.ctxMgr.SetCompactor(thresholdRunes)
}

// maybeCompact runs before each planning LLM call. If the history overflowed
// since the last call, the oldest rounds are compressed into one LLM summary —
// one extra LLM call, and the gist of earlier attempts survives (the Claude
// Code auto-compact policy). If the summary fails, the overflow is dropped
// (classic truncation) rather than blocking the task.
func (a *Agent) maybeCompact(ctx context.Context) {
	if a.ctxMgr.PendingCount() == 0 {
		return
	}
	oldest := a.ctxMgr.PendingMessages()
	summary, err := a.summarize(ctx, oldest)
	if err != nil || strings.TrimSpace(summary) == "" {
		a.ctxMgr.DiscardPending()
		reason := "empty summary"
		if err != nil {
			reason = err.Error()
		}
		a.sess.Log(StatePlanning, "compact", fmt.Sprintf("warn: summary failed (%s), dropped %d old messages", reason, len(oldest)))
		return
	}
	a.ctxMgr.InsertSummary(summary)
	a.sess.Log(StatePlanning, "compact", fmt.Sprintf("auto-compacted %d old messages into a summary", len(oldest)))
}

// summarize is the one LLM call that turns dropped history into dense notes.
// It deliberately uses the plain Chat path (not the streamer) so a compaction
// does not flash through an interactive renderer mid-reasoning.
func (a *Agent) summarize(ctx context.Context, oldest []llm.Message) (string, error) {
	var b strings.Builder
	b.WriteString("Compress the following coding-agent conversation history into dense notes:\n" +
		"- what was attempted\n- what failed and why (keep the key error)\n" +
		"- what is still outstanding\n" +
		"Preserve file paths and function names. Keep it under 400 words.\n\nHISTORY:\n")
	for _, m := range oldest {
		b.WriteString(string(m.Role) + ": " + truncate(m.Content, 2000) + "\n\n")
	}
	resp, err := a.llm.Chat(ctx, llm.ChatRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "You are a session summarizer for a coding agent."},
		{Role: llm.RoleUser, Content: b.String()},
	}})
	if err != nil {
		return "", err
	}
	a.sess.AddUsage(resp.Usage)
	a.sess.LLMCalls++
	if resp.Truncated {
		return "", errors.New("summary response truncated by max_tokens")
	}
	return resp.Message.Content, nil
}

type agentTools struct {
	// Read-only explore tools are bound to the Tool interface (not the
	// concrete types) so callers — and tests — can wrap or substitute them
	// (e.g. to observe parallel execution).
	read tools.Tool
	grep tools.Tool
	list tools.Tool
	edit     *tools.EditTool
	run      *tools.RunTestTool
	cmd      *tools.RunCommandTool
	rollback *tools.RollbackTool
	diff     *tools.GitDiffTool
}

// New builds a top-level agent bound to a chat client and a sandbox root. The
// sandbox is git-initialized so every edit is snapshot-able; if git is
// unavailable the agent still runs (degraded: no rollback) rather than
// failing hard.
func New(lc llm.ChatClient, cfg Config) *Agent {
	return newAgent(lc, cfg, nil)
}

// newAgent builds an agent, sharing parent state when shared is non-nil. A
// child agent (subagent dispatch) reuses the parent's sandbox workspace and
// symbol index — the shared state that makes a child's edit immediately
// visible to the parent's retrieval — while getting its own session,
// conversation memory and tools.
func newAgent(lc llm.ChatClient, cfg Config, shared *agentShared) *Agent {
	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 5
	}
	timeout := cfg.RunTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	testCmdDisplay := "go test"
	if len(cfg.TestCommand) > 0 {
		testCmdDisplay = strings.Join(cfg.TestCommand, " ")
	}
	maxEvalRounds := cfg.MaxEvalRounds
	if maxEvalRounds <= 0 {
		maxEvalRounds = 1
	}
	// One symbol index is shared by retrieval (ContextProvider) and by the
	// EditTool's edit-time validation, so both see the same world.
	builder := codeindex.NewBuilder(cfg.RepoPath)
	sb, err := sandbox.New(cfg.RepoPath)
	if err != nil {
		sb = &sandbox.Sandbox{Root: cfg.RepoPath}
	}
	if shared != nil {
		// Child agent: inherit the parent's workspace and symbol index. The
		// parent already ensured the repo, so skip the git init here.
		sb = shared.sb
		builder = shared.builder
	} else if cfg.InPlace {
		// Direct in-place editing (P21): reuse the project's own git history as
		// the snapshot baseline instead of baselining a fresh copy.
		if err := sb.EnsureRepoInPlace(); err != nil {
			// degraded mode: snapshot/rollback unavailable, core loop still runs
			sb = &sandbox.Sandbox{Root: cfg.RepoPath}
		}
	} else if err := sb.EnsureRepo(); err != nil {
		// degraded mode: snapshot/rollback unavailable, core loop still runs
		sb = &sandbox.Sandbox{Root: cfg.RepoPath}
	}
	// P20: operator-approved non-Go tooling (e.g. python/pytest for the test
	// command). nil is a no-op — the strict Go+git allowlist is untouched.
	sb.AllowCommands(cfg.AllowedCommands)
	sess := &Session{
		TaskID:      "task-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		RepoPath:    cfg.RepoPath,
		Requirement: cfg.Requirement,
		State:       StatePlanning,
		Round:       1,
		MaxRounds:   maxRounds,
		onEvent:     cfg.OnEvent,
	}
	var evl *Evaluator
	if cfg.Evaluator != nil {
		evl = newEvaluator(cfg.Evaluator, cfg.OnStream)
	}
	// Dynamic tool registry: external tools (skills / MCP) are injected via
	// Config.Tools and exposed to the LLM through the {"call_tool":{...}}
	// primitive. Subagents are registered afterwards, once the Agent exists,
	// so their Run closures can capture the parent pointer. A registration
	// error (e.g. duplicate name) is surfaced at plan time rather than
	// silently swallowed.
	reg := registry.New()
	var toolErr error
	for _, t := range cfg.Tools {
		if err := reg.Register(t); err != nil && toolErr == nil {
			toolErr = err
		}
	}
	cm := NewContextManager(NewContextProviderWithBuilder(cfg.RepoPath, builder))
	if shared != nil {
		// The child carries its role instructions and, for researchers, the
		// read-only system prompt — injected into the system prompt at
		// Messages-assembly time. Its per-role model override rides every LLM
		// call via ChatRequest.Model.
		cm.SetRolePrompt(shared.rolePrompt)
		if shared.readOnly {
			cm.SetResearch(true)
		}
	}
	// Executor mode flips the PARENT into the planner role (outline plans +
	// per-step worker dispatch). Children are built with cfg.Executor unset, so
	// they keep the classic content protocol — recursion terminates at one level.
	if cfg.Executor {
		cm.SetExecutorMode(true)
	}
	at := &agentTools{
		read: &tools.ReadTool{Sandbox: sb},
		grep: &tools.GrepTool{Sandbox: sb},
		list: &tools.ListDirTool{Sandbox: sb},
	}
	if shared == nil || !shared.readOnly {
		// Read-only (researcher) subagents get explore tools only: they report,
		// they never touch disk. Everyone else gets the full toolset.
		at.edit = &tools.EditTool{Sandbox: sb, Builder: builder}
		at.run = &tools.RunTestTool{Sandbox: sb, Timeout: timeout, Cmd: cfg.TestCommand}
		at.cmd = &tools.RunCommandTool{Sandbox: sb, Timeout: timeout}
		at.rollback = &tools.RollbackTool{Sandbox: sb}
		at.diff = &tools.GitDiffTool{Sandbox: sb}
	}
	a := &Agent{
		llm:     lc,
		sess:    sess,
		sandbox: sb,
		builder: builder,
		ctxMgr:  cm,
		hooks:   cfg.Hooks,
		evaluator:  evl,
		maxEvalRounds: maxEvalRounds,
		stream:  cfg.OnStream,
		reg:     reg,
		toolErr: toolErr,
		confirmFn: cfg.Confirm,
		extTools: cfg.Tools,
		tools:   at,
		testCmdDisplay: testCmdDisplay,
		child:   shared != nil,
	}
	if shared != nil {
		a.modelOverride = shared.modelOverride
	}
	a.executor = cfg.Executor
	a.executorModel = cfg.ExecutorModel
	// Subagents surface to the model as dynamic tools "subagent:<name>". The
	// Run closure captures the parent Agent, so dispatch runs a child in the
	// shared workspace. cfg.SubAgents is nil for headless/harness/mock runs,
	// keeping the 8-case gate byte-identical.
	for _, def := range cfg.SubAgents {
		d := def
		name := "subagent:" + d.Name
		if err := reg.Register(registry.ToolDef{
			Name:        name,
			Description: fmt.Sprintf("Dispatch a task to the %q subagent (read_only=%v). Give a self-contained task: what to investigate or implement, and how to verify.", d.Name, d.ReadOnly),
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{"type": "string"},
				},
				"required": []string{"task"},
			},
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				return a.runSubagent(ctx, d, args)
			},
		}); err != nil && a.toolErr == nil {
			a.toolErr = err
		}
	}
	if desc := registry.DescribeAll(reg.List()); desc != "" {
		cm.SetExternalTools(desc)
	}
	if cfg.SessionMemory != "" {
		cm.SetSessionMemory(cfg.SessionMemory)
	}
	return a
}

// Run drives the loop until Done or Failed (one task).
func (a *Agent) Run(ctx context.Context) Result {
	a.sess.Log(StatePlanning, "", "task start")
	return a.run(ctx)
}

// RunTurn executes one user request as a fresh turn on the same agent. Only
// the per-task state is reset (requirement, round, plan); the sandbox
// workspace, the symbol index, and the conversation memory all carry over.
// The interactive REPL drives a long-lived agent this way, so a follow-up
// like "now also handle negative numbers" builds on the previous turn's edits
// and history instead of starting over.
func (a *Agent) RunTurn(ctx context.Context, requirement string) Result {
	s := a.sess
	s.Requirement = requirement
	s.State = StatePlanning
	s.Round = 1
	s.Plan = nil
	s.TestOutput = ""
	s.lastTestOK = false
	s.Log(StatePlanning, "", "turn start: "+truncate(requirement, 200))
	return a.run(ctx)
}

// run is the shared state-machine loop body for one task/turn.
func (a *Agent) run(ctx context.Context) Result {
	s := a.sess
	// Baseline the symbol inventory so the evaluator's objective gate can diff
	// it against the state after editing (P6).
	if inv, err := symbolInventory(s.RepoPath); err == nil {
		s.SymbolBase = inv
	}
	// Scope deliverable verification (P23) to THIS turn: the gate diffs the
	// workspace against this commit, so it sees only the files this task wrote
	// (or, in a REPL, this turn wrote) — not a server a previous turn built,
	// and not an unrelated pre-existing server in the repository. "" when git
	// is unavailable: the gate no-ops on an empty diff.
	a.verifyBaseline = a.sandbox.Head()
	for {
		select {
		case <-ctx.Done():
			return a.result(StateFailed, "context cancelled: "+ctx.Err().Error())
		default:
		}

		switch s.State {
		case StatePlanning:
			if err := a.planning(ctx); err != nil {
				return a.result(StateFailed, err.Error())
			}
		case StateEditing:
			if err := a.editing(ctx); err != nil {
				return a.result(StateFailed, err.Error())
			}
		case StateExecuting:
			a.executing(ctx)
		case StateValidating:
			a.validating()
		case StateRepairing:
			if err := a.repairing(ctx); err != nil {
				return a.result(StateFailed, err.Error())
			}
		case StateDone:
			return a.result(StateDone, "tests passed")
		case StateFailed:
			return a.result(StateFailed, "max rounds exceeded or unrecoverable")
		}
	}
}

// maxExplorePerPhase caps how many exploration rounds the LLM may request
// before we force it to produce a plan. This prevents an LLM that keeps
// emitting {"explore":...} from looping forever.
//
// Tuned from 5 to 10 after a live run (gpt-5.6-terra) on the 500-line
// self_codeindex fixture with a fuzzy requirement: the model burned 5 explore
// rounds on real code (grep+read, ~15k tokens) and was cut off right before
// planning. Mock never explores, so this only affects live runs.
const maxExplorePerPhase = 10

// maxFormatRetries caps how many times a reply that is not JSON at all (plain
// prose instead of the {"steps":[...]} protocol) is fed back and retried,
// before failing. A live model that correctly diagnosed the task but wrote its
// plan as markdown prose (observed: gpt-5.6-terra on self_codeindex) is
// rescued by a format reminder — one LLM call, no extra rounds. This is the
// "not even JSON" sibling of repairJSON's "malformed JSON" fix.
const maxFormatRetries = 2

func (a *Agent) planning(ctx context.Context) error {
	return a.obtainPlan(ctx, "Plan the implementation.")
}

// obtainPlan drives the producer LLM until it emits a plan. When an evaluator
// is wired (P6), every candidate plan is reviewed BEFORE it lands: the
// objective symbol gate first (zero-token), then the LLM review. A rejected
// plan is sent back with feedback for a rewrite — the evaluation target is the
// generated plan, matching the official evaluator-optimizer pattern, and
// nothing touches disk until approved.
//
// The two gates have different stop semantics:
//   - symbol gate (objective): a plan that deletes existing symbols or renames
//     a package is structurally broken and must NEVER land. It routes to
//     REPAIRING instead of the rewrite loop, so the Round/MaxRounds backstop
//     applies and no amount of "good enough" overrides it.
//   - LLM review (subjective): bounded by evalRounds; past maxEvalRounds the
//     latest plan is accepted — "know when good enough is good enough"
//     (official guidance) — and the tests + repair loop backstop it.
func (a *Agent) obtainPlan(ctx context.Context, question string) error {
	if a.toolErr != nil {
		return fmt.Errorf("plan: external tool registration: %w", a.toolErr)
	}
	evalRounds := 0
	formatRetries := 0
	for planAttempt := 0; ; planAttempt++ {
		// Auto-compact any history that overflowed since the last call before
		// the prompt is built, so the LLM sees the summary, not the drop.
		a.maybeCompact(ctx)
		resp, err := a.chat(ctx, llm.ChatRequest{
			Messages: a.ctxMgr.Messages(ctx, a.sess, llm.Message{Role: llm.RoleUser, Content: question}),
		})
		if err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		a.sess.AddUsage(resp.Usage)
		a.sess.LLMCalls++
		if resp.Truncated {
			return fmt.Errorf("plan: response truncated by max_tokens")
		}

		// If the model asks to explore, execute the requests and feed the
		// results back, then let it continue planning.
		if exp, ok := parseExplore(resp.Message.Content); ok {
			if planAttempt >= maxExplorePerPhase {
				return fmt.Errorf("plan: LLM requested explore %d times without producing a plan", planAttempt)
			}
			results := a.execExplore(ctx, exp)
			a.ctxMgr.RecordExplore(results)
			a.sess.Log(StatePlanning, "explore", truncate(results, 500))
			continue
		}

		// External dynamic tools (skills / MCP / subagents): {"call_tool":…}
		// is executed through the registry and the result fed back, in the
		// same "look then act" loop as explore. The shared planAttempt cap
		// prevents an LLM that keeps calling tools from looping forever.
		if tc, ok := parseToolCall(resp.Message.Content); ok {
			if planAttempt >= maxExplorePerPhase {
				return fmt.Errorf("plan: LLM called tools %d times without producing a plan", planAttempt)
			}
			result := a.execToolCall(ctx, tc)
			// Tool outputs (skills/MCP/subagent results) use the explore cap so
			// a long report isn't truncated to the failure cap the model can't
			// reason about.
			a.ctxMgr.RecordExternal(tc.Name, result)
			a.sess.Log(StatePlanning, "call_tool", tc.Name)
			continue
		}

		steps, err := parseSteps(resp.Message.Content)
		if err != nil {
			if formatRetries < maxFormatRetries {
				formatRetries++
				a.ctxMgr.RecordOutcome(planFormatReminder(a.executor, err))
				continue
			}
			return err
		}

		// P6 gate 1 — objective symbol gate. Structurally destructive plans are
		// rejected before any LLM review (zero tokens) and before any edit.
		// Routing to REPAIRING (not the rewrite loop) keeps MaxRounds as the
		// backstop: the producer gets chances to revise, but a destructive plan
		// can never be accepted by the subjective override.
		if a.evaluator != nil {
			if problems := a.symbolPlanGate(steps); len(problems) > 0 {
				feedback := "The plan would remove or break existing code. Revise it to keep these:\n- " + strings.Join(problems, "\n- ")
				a.ctxMgr.RecordOutcome(feedback)
				a.sess.TestOutput = "plan rejected by symbol gate: " + feedback
				a.sess.State = StateRepairing
				a.sess.Log(StateRepairing, "symbol_gate_reject", truncate(feedback, 300))
				return nil
			}
		}

		// P6 gate 2 — subjective LLM review, bounded and overridable.
		var feedback string
		if a.evaluator != nil {
			if verdict, verr := a.evalPlan(ctx, steps); verr != nil {
				// An evaluator failure must not block the task: accept the plan.
				a.sess.Log(StatePlanning, "evaluator_error", verr.Error())
			} else if !verdict.Approves() {
				feedback = verdict.Feedback
				if len(verdict.Findings) > 0 {
					feedback += "\nFindings:\n- " + strings.Join(verdict.Findings, "\n- ")
				}
			}
		}
		if feedback != "" {
			evalRounds++
			a.sess.Log(StatePlanning, "eval_reject", truncate(feedback, 300))
			if evalRounds > a.maxEvalRounds {
				a.sess.Log(StatePlanning, "eval_max_rounds", "plan still rejected; accepting to proceed")
			} else {
				a.ctxMgr.RecordOutcome(feedback)
				question = "The evaluator rejected the plan:\n" + feedback + "\n\nProduce a revised plan."
				continue
			}
		}

		a.sess.Plan = steps
		// Remember the assistant's plan so repair rounds can see the intent.
		a.ctxMgr.RecordPlan(resp.Message)
		a.sess.Log(StateEditing, "", fmt.Sprintf("plan: %d steps", len(steps)))
		a.sess.State = StateEditing
		return nil
	}
}

func (a *Agent) editing(ctx context.Context) error {
	for _, step := range a.sess.Plan {
		// A before_edit hook can veto the change before it touches disk —
		// e.g. "never edit vendor/ or generated files". A veto routes the
		// agent into REPAIRING exactly like an edit-validation rejection.
		if err := a.fire(ctx, HookBeforeEdit, Event{State: StateEditing, Tool: "edit_file", Detail: step.File + ": " + step.Desc}); err != nil {
			a.ctxMgr.RecordOutcome("Your edit to " + step.File + " was blocked by a hook before it ran:\n" + err.Error())
			a.sess.TestOutput = "edit blocked by hook: " + err.Error()
			a.sess.State = StateRepairing
			a.sess.Log(StateRepairing, "edit_blocked", step.File+": "+err.Error())
			return nil
		}
		// Permission gate: a write action needs the operator's OK in interactive
		// mode. A denial is exactly like a hook veto — the agent repairs and
		// proposes a different change instead of pushing through.
		if !a.confirm(PermissionAction{Op: "edit_file", Detail: step.File + ": " + step.Desc}) {
			a.ctxMgr.RecordOutcome("Your edit to " + step.File + " was denied by the user (permission gate). Propose a different change or scope it differently.")
			a.sess.TestOutput = "edit denied by permission gate"
			a.sess.State = StateRepairing
			a.sess.Log(StateRepairing, "edit_denied", step.File+": "+step.Desc)
			return nil
		}
		// P22 executor mode: the parent is the planner, so each step is
		// implemented by a worker executor (potentially on a cheaper model) that
		// reads the code, writes the file, and runs tests itself. Its report
		// feeds back into the parent context; a non-Done result means this step
		// failed, so the whole plan repairs — mirroring the edit-rejection path
		// below. The worker's own edits already carry the syntax + call-graph
		// validation (shared EditTool + symbol index), so structural safety is
		// preserved inside the delegation.
		if a.executor {
			res := a.runExecutor(ctx, step)
			if res.State != StateDone {
				report := res.Note
				if report == "" {
					report = res.TestOutput
				}
				a.ctxMgr.RecordOutcome("The executor for step " + step.File + " failed (state=" + string(res.State) + "):\n" + report)
				a.sess.TestOutput = "executor failed: " + report
				a.sess.State = StateRepairing
				a.sess.Log(StateRepairing, "executor_failed", step.File+": "+truncate(report, 300))
				return nil
			}
			// after_edit is observation-only, fired after the step's worker
			// landed its edit — same semantics as the atomic path below.
			if err := a.fire(ctx, HookAfterEdit, Event{State: StateEditing, Tool: "edit_file", Detail: step.File + ": " + step.Desc}); err != nil {
				a.sess.Log(StateEditing, "hook_after_edit", step.File+": "+err.Error())
			}
			continue
		}

		// Record a reversible checkpoint before every change so a bad edit
		// can be rolled back. Snapshot failure is non-fatal (degraded mode).
		if err := a.sandbox.Snapshot("edit " + step.File); err != nil {
			a.sess.Log(StateEditing, "snapshot", "warn: "+err.Error())
		}
		args, err := json.Marshal(map[string]string{"path": step.File, "content": step.Content})
		if err != nil {
			return err
		}
		res, err := a.tools.edit.Run(ctx, tools.Args(args))
		if err != nil {
			// A validation rejection means this plan is broken at the symbol
			// layer BEFORE any test runs. Feed the reason back and repair —
			// milliseconds of symbol-engine checking instead of a full
			// compiler/test round.
			var rejected *tools.EditRejectedError
			if errors.As(err, &rejected) {
				a.ctxMgr.RecordOutcome("Your edit to " + step.File + " was rejected before tests ran:\n" + rejected.Reason)
				a.sess.TestOutput = "edit rejected: " + rejected.Reason
				a.sess.State = StateRepairing
				a.sess.Log(StateRepairing, "edit_rejected", rejected.Reason)
				return nil
			}
			return fmt.Errorf("edit %s: %w", step.File, err)
		}
		if !res.Ok {
			return fmt.Errorf("edit %s: rejected", step.File)
		}
		a.sess.Log(StateEditing, "edit_file", step.File+": "+step.Desc)
		// after_edit is observation-only: the change already happened, so a
		// hook error is logged as an event, never fatal.
		if err := a.fire(ctx, HookAfterEdit, Event{State: StateEditing, Tool: "edit_file", Detail: step.File + ": " + step.Desc}); err != nil {
			a.sess.Log(StateEditing, "hook_after_edit", step.File+": "+err.Error())
		}
	}
	a.sess.State = StateExecuting
	return nil
}

func (a *Agent) executing(ctx context.Context) {
	// Permission gate on running tests too — an operator may want to review the
	// edits before any command executes. nil gate = automatic (headless).
	if !a.confirm(PermissionAction{Op: "run_test", Detail: a.testCmdDisplay}) {
		a.sess.TestOutput = "run_test denied by permission gate"
		a.sess.lastTestOK = false
		a.sess.Log(StateExecuting, "test_denied", "user denied run_test")
		a.sess.State = StateRepairing
		return
	}
	// A before_test hook can veto the run (e.g. "don't run tests on a build
	// server during peak"). A veto is treated like a test failure so the
	// agent repairs rather than silently skipping verification.
	if err := a.fire(ctx, HookBeforeTest, Event{State: StateExecuting, Tool: "run_test", Detail: a.testCmdDisplay}); err != nil {
		a.sess.TestOutput = "run_test blocked by hook: " + err.Error()
		a.sess.lastTestOK = false
		a.sess.Log(StateExecuting, "test_blocked", err.Error())
		a.sess.State = StateRepairing
		return
	}
	res, err := a.tools.run.Run(ctx, tools.Args(`{}`))
	if err != nil {
		a.sess.TestOutput = "run_test error: " + err.Error()
		a.sess.lastTestOK = false
	} else {
		a.sess.TestOutput = res.Output
		a.sess.lastTestOK = res.Ok
	}
	a.sess.Log(StateExecuting, "run_test", truncate(a.sess.TestOutput, 2000))
	if err := a.fire(ctx, HookAfterTest, Event{State: StateExecuting, Tool: "run_test", Detail: fmt.Sprintf("ok=%v", a.sess.lastTestOK)}); err != nil {
		a.sess.Log(StateExecuting, "hook_after_test", err.Error())
	}
	// Deliverable verification (P23): after go test passes, read back the files
	// THIS task produced for consistency (reference-integrity) and, when the task
	// built a server, boot it and probe real HTTP — mirroring Claude Code's "run
	// it and observe" scoped to what the task built, not the whole repository.
	// The diff against verifyBaseline yields exactly the touched files (parent +
	// executor children commit into the same sandbox repo), so a pre-existing
	// server the task never touched is never booted and a false failure can't
	// be manufactured from unrelated code. Issues are fed into TestOutput so the
	// repair loop treats them exactly like a test failure. No-ops when the task
	// produced no web assets and no port-binding server.
	if !a.child && a.sess.lastTestOK {
		touched, _ := a.sandbox.ChangedFilesSince(a.verifyBaseline)
		if issues := verify.VerifyDeliverable(ctx, a.sess.RepoPath, touched, a.sandbox, verifyTimeout); len(issues) > 0 {
			a.sess.TestOutput = "deliverable verification failed:\n- " + strings.Join(issues, "\n- ")
			a.sess.lastTestOK = false
			a.sess.Log(StateExecuting, "verify", truncate(a.sess.TestOutput, 2000))
		}
	}
	a.sess.State = StateValidating
}

// verifyTimeout bounds the deliverable-verification gate (build + boot + probe
// of a produced server). It runs only when the task actually generated a
// port-binding server, and the smoke check self-limits to a third of this.
const verifyTimeout = 30 * time.Second

func (a *Agent) validating() {
	if a.sess.lastTestOK {
		a.sess.State = StateDone
		a.sess.Log(StateDone, "", "tests passed")
		return
	}
	// Record what happened after the last plan: "the edits were applied but
	// the tests failed, here's the output." This is the memory that lets the
	// next repair round avoid repeating the same mistake.
	a.ctxMgr.RecordOutcome("The edits from the last plan were applied, but `"+a.testCmdDisplay+"` failed:\n" + a.sess.TestOutput)
	a.sess.State = StateRepairing
	a.sess.Log(StateRepairing, "", "tests failed, entering repair")
}

func (a *Agent) repairing(ctx context.Context) error {
	a.sess.Round++
	if a.sess.Round > a.sess.MaxRounds {
		a.sess.State = StateFailed
		return nil
	}
	// Feed the failure (test output OR an edit rejection) + the requirement
	// back, but now WITH the conversation history so the LLM knows what it
	// already tried. The repair plan goes through the same evaluator review as
	// the first plan (obtainPlan).
	question := "The previous attempt did not succeed. Here is the output:\n" + a.sess.TestOutput + "\n\nFix the root cause with a new plan."
	return a.obtainPlan(ctx, question)
}

func (a *Agent) result(st State, note string) Result {
	a.sess.State = st
	if note != "" {
		a.sess.Log(st, "", note)
	}
	if st == StateFailed {
		_ = a.fire(context.Background(), HookOnError, Event{State: st, Tool: "", Detail: note})
	}
	return Result{
		TaskID:     a.sess.TaskID,
		State:      st,
		Rounds:     a.sess.Round,
		ToolCalls:  a.sess.ToolCalls(),
		LLMCalls:   a.sess.LLMCalls,
		TestOutput: a.sess.TestOutput,
		Note:       note,
		Events:     a.sess.History,
		Usage:      a.sess.Usage,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
