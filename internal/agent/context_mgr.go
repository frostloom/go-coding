package agent

import (
	"context"
	"unicode/utf8"

	"github.com/zlx05/go-coding/pkg/llm"
)

// ContextManager owns the conversation history and the token budget for
// each prompt. Without it, every repair round would rebuild the prompt from
// scratch — the LLM would have no memory of what it tried, why it failed,
// and what changed. That is the difference between a "coding agent that
// remembers" and one that "restarts every round".
//
// Budgeting is layered so the most decision-relevant signal is preserved:
//  1. failure output (what went wrong) — kept in full (capped)
//  2. retrieval context (symbols + call graph) — most recent
//  3. conversation history (past round summaries) — trimmed oldest-first
type ContextManager struct {
	ctxProv      *ContextProvider
	history      []llm.Message
	historyCap   int // max rounds of history kept before trimming
	failCap      int // max runes of failure output retained
	exploreCap   int // max runes of an explore round's results retained
	retrievalCap int // max tokens of retrieval context injected
	externalTools string // prompt block listing dynamically-registered tools
	// auto-compact (Claude Code policy): when history runs past compactRunes,
	// the oldest rounds are moved into pendingCompact instead of dropped. The
	// agent summarizes them with one LLM call (Agent.maybeCompact) before the
	// next prompt. compactRunes <= 0 keeps the classic truncate-oldest behavior.
	compactRunes   int
	pendingCompact []llm.Message
	// compactedSummary is the LLM summary of dropped rounds. It lives OUTSIDE
	// the history slice so trim never evicts it again — the summary is the
	// permanent memory of everything that happened before compaction.
	compactedSummary string
	// sessionMemory is the cross-session memory block (user + project memory
	// files, see Memory in session_memory.go) injected into the system prompt.
	// Empty means nothing is injected — headless and mock runs see the same
	// prompt, so the 8-case gate stays byte-identical.
	sessionMemory string
	// rolePrompt is a subagent's role instructions (the body of its agents/*.md
	// file), prepended to the system prompt so the child's identity comes first.
	rolePrompt string
	// research switches the system prompt to the researcher variant (explore +
	// report, never edit) for read-only subagents.
	research bool
	// executorMode switches the system prompt to the PLANNER variant: the
	// parent produces an outline plan (per-file desc + acceptance criteria) and
	// worker executors implement each step. Only the top-level agent sets it;
	// children stay on defaultSystemPrompt so they produce full content.
	executorMode bool
}

// NewContextManager builds a manager with sensible defaults.
func NewContextManager(cp *ContextProvider) *ContextManager {
	return &ContextManager{
		ctxProv:      cp,
		historyCap:   4,
		failCap:      3000,
		exploreCap:   20000,
		retrievalCap: 2500,
	}
}

// RecordPlan pushes the assistant's plan (the reply to planning) so later
// repair rounds can see what was originally intended.
func (cm *ContextManager) RecordPlan(assistantMsg llm.Message) {
	cm.history = append(cm.history, assistantMsg)
	cm.trim()
}

// RecordOutcome appends a user-style message summarizing what happened
// after the last plan: the tool was applied, the test failed, here's the
// output. This is what gives the LLM memory of "what I did → what result".
func (cm *ContextManager) RecordOutcome(summary string) {
	cm.history = append(cm.history, llm.Message{
		Role:    llm.RoleUser,
		Content: truncate(summary, cm.failCap),
	})
	cm.trim()
}

// RecordExplore appends an exploration round's results. Exploration results
// use a much larger cap than failure output: the model needs to actually SEE
// the files it asked to read. With the failure cap (3000 runes) a real
// multi-hundred-line file got truncated, so the model could never find the
// bug it was exploring for and looped forever (observed live: gpt-5.6-terra
// burned 50k tokens on self_codeindex never reaching a plan).
func (cm *ContextManager) RecordExplore(results string) {
	cm.history = append(cm.history, llm.Message{
		Role:    llm.RoleUser,
		Content: truncate(results, cm.exploreCap),
	})
	cm.trim()
}

// RecordExternal appends the result of a dynamically-registered external tool
// call — a skill's instructions, an MCP tool's answer, a subagent's report.
// Tool outputs are like explore output: they can be long and the model needs
// to actually read them, so they use the explore cap (20000 runes), not the
// failure cap (3000 runes) that RecordOutcome applies.
func (cm *ContextManager) RecordExternal(toolName, text string) {
	cm.history = append(cm.history, llm.Message{
		Role:    llm.RoleUser,
		Content: truncate("External tool "+toolName+" returned:\n"+text, cm.exploreCap),
	})
	cm.trim()
}

// SetSessionMemory stores the cross-session memory block (user + project
// memory files) to inject into the system prompt. Empty keeps the prompt
// unchanged.
func (cm *ContextManager) SetSessionMemory(block string) {
	cm.sessionMemory = block
}

// SetExternalTools stores the prompt block describing dynamically-registered
// tools (skills / MCP / subagents), injected into the system prompt.
func (cm *ContextManager) SetExternalTools(desc string) {
	cm.externalTools = desc
}

// SetRolePrompt stores a subagent's role instructions, prepended to the system
// prompt so the child's identity comes first. Empty keeps the prompt unchanged.
func (cm *ContextManager) SetRolePrompt(p string) {
	cm.rolePrompt = p
}

// SetResearch switches the system prompt to the researcher variant (explore +
// report, never edit) — used only by read-only subagents.
func (cm *ContextManager) SetResearch(on bool) {
	cm.research = on
}

// SetExecutorMode switches the system prompt to the PLANNER variant for
// hierarchical plan-and-execute (P22): the parent outlines per-file steps
// (desc + acceptance criteria, no content) and worker executors implement each
// one. Off keeps the classic full-content plan prompt.
func (cm *ContextManager) SetExecutorMode(on bool) {
	cm.executorMode = on
}

// externalToolsBlock returns the external-tools section of the system prompt,
// including the call protocol. Empty when nothing is registered, so headless
// and mock runs see an identical prompt.
func (cm *ContextManager) externalToolsBlock() string {
	if cm.externalTools == "" {
		return ""
	}
	return "\n\nEXTERNAL TOOLS — call one with STRICT JSON only when it is useful:\n" +
		cm.externalTools +
		`{"call_tool":{"name":"<tool>","args":{...}}}
The system executes it and feeds the result back. Continue planning or exploring afterwards.`
}

// Reset drops the conversation history. The workspace and symbol index are
// untouched — this is "start fresh but keep the files", the /clear command.
func (cm *ContextManager) Reset() {
	cm.history = nil
	cm.pendingCompact = nil
	cm.compactedSummary = ""
}

// SetCompactor enables Claude-Code-style auto-compaction: once the history
// exceeds thresholdRunes, the oldest rounds are set aside instead of dropped,
// and the agent summarizes them with one LLM call before the next prompt
// (see Agent.maybeCompact). thresholdRunes <= 0 keeps the classic
// truncate-oldest behavior.
func (cm *ContextManager) SetCompactor(thresholdRunes int) {
	cm.compactRunes = thresholdRunes
}

// PendingCount reports how many old messages are awaiting compaction.
func (cm *ContextManager) PendingCount() int { return len(cm.pendingCompact) }

// PendingMessages returns the messages set aside for compaction.
func (cm *ContextManager) PendingMessages() []llm.Message { return cm.pendingCompact }

// DiscardPending drops the set-aside messages without summarizing them — the
// fallback when the summary LLM call fails (equivalent to classic truncation).
func (cm *ContextManager) DiscardPending() { cm.pendingCompact = nil }

// InsertSummary stores the LLM summary of the set-aside rounds so the agent
// keeps remembering the gist of earlier attempts after compaction, then clears
// the pending queue. The summary lives OUTSIDE the history slice — trim never
// evicts it, and Messages injects it in the right position.
func (cm *ContextManager) InsertSummary(summary string) {
	cm.compactedSummary = truncate(summary, cm.failCap*2)
	cm.pendingCompact = nil
}

// defaultSystemPrompt drives the full coding agent: explore → plan → edit →
// test. Workers (read-only=false subagents) prepend their role instructions
// to it; top-level agents use it verbatim.
const defaultSystemPrompt = `You are a coding agent. You work iteratively: you explore the codebase, then plan edits, they are applied, tests run, and you see the results before your next move.

WORKING STYLE — mirror Claude Code's agent loop:
1. GATHER CONTEXT first. When the requirement is vague (e.g. "handle concurrency"), do not jump to editing. Study the project: list the directory structure, grep for relevant patterns (e.g. "sync.", "WaitGroup", "channel"), read 1-2 key files to learn the style. Use the conversation history to avoid repeating failed approaches.
2. Then TAKE ACTION: produce a plan of whole-file edits that implements the requirement.
3. Then VERIFY: the edits are applied and tests run automatically.

If you need to explore before editing, respond with a special exploration request:
{"explore":[{"op":"list_dir"},{"op":"grep","pattern":"..."},{"op":"read_file","path":"..."}]}
The system will execute it and give you results, then you continue.

Once you are ready to edit, respond with STRICT JSON only:
{"steps":[{"file":"relative/path","content":"FULL NEW FILE CONTENT","desc":"what and why"}]}
Do not add commentary outside the JSON.`

// executorPlanPrompt drives the PLANNER role of hierarchical plan-and-execute
// (P22). Unlike defaultSystemPrompt it does NOT ask for inline file content:
// each step is an outline (file + description + acceptance criteria) that a
// worker executor implements separately. This is what lets a large task be
// decomposed per-file and executed incrementally instead of emitted as one
// giant full-content JSON.
const executorPlanPrompt = `You are the PLANNER of a plan-and-execute coding agent. You explore the codebase and produce an OUTLINE plan; a separate executor agent implements each step. You do NOT write file content.

WORKING STYLE:
1. GATHER CONTEXT first. When the requirement is vague, study the project before planning: list the directory structure, grep for relevant patterns, read 1-2 key files to learn the style.
2. Then produce the plan: one step per file (or per logical change), in dependency order — steps run in order in ONE shared workspace, so put dependencies before dependents.
3. Each step MUST carry concrete acceptance criteria: how a correct implementation is verified (e.g. "the function returns 1245789 for input 5489712", "go test ./... passes", "the page loads and streams messages"). The executor derives the file content from the description + acceptance.
4. When a step's deliverable is a server or web page (it binds a port or serves HTML/CSS/JS), its acceptance MUST be an HTTP test the executor can write and run: assert GET / returns 200 and every referenced static asset (script src / link href) resolves. An acceptance that only says "go test passes" will not stop an empty-test deliverable — the framework boots the server and probes it anyway, so specify the real requests.

If you need to explore before planning, respond with a special exploration request:
{"explore":[{"op":"list_dir"},{"op":"grep","pattern":"..."},{"op":"read_file","path":"..."}]}
The system will execute it and give you results, then you continue.

Once you are ready to plan, respond with STRICT JSON only:
{"steps":[{"file":"relative/path","desc":"what and why","accept":"how correctness is verified"}]}
Do not inline file content — leave it to the executor. Do not add commentary outside the JSON.`

// researchSystemPrompt drives a read-only researcher subagent: it gathers
// context and produces a report, but has no edit/run tools and never touches
// disk. Its final answer IS the report.
const researchSystemPrompt = `You are a research subagent. Your job is to gather information and produce a report — you do NOT edit files and you have no edit, run_test, or run_command tools.

Work iteratively:
1. Explore the codebase: list the directory structure, grep for relevant patterns, read key files. Use {"explore":[...]} to request exploration.
2. Call an external tool (skill, MCP) when it would help answer the question.
3. When you have enough information, write your final report as plain prose — no JSON wrapper. Include concrete file paths, what you found, and a clear recommendation.

You must finish with a report, not with another exploration request.`

// Messages returns the full ordered conversation for the next LLM call:
// system, original requirement, accumulated history, and the current
// question (with live retrieval context refreshed for this round).
func (cm *ContextManager) Messages(ctx context.Context, s *Session, current llm.Message) []llm.Message {
	system := defaultSystemPrompt
	if cm.research {
		system = researchSystemPrompt
	} else if cm.executorMode {
		system = executorPlanPrompt
	}
	if cm.rolePrompt != "" {
		system = cm.rolePrompt + "\n\n" + system
	}

	out := make([]llm.Message, 0, 4)
	sys := system + cm.externalToolsBlock()
	if cm.sessionMemory != "" {
		sys += "\n\nSESSION MEMORY (loaded at startup; do not treat it as this task's requirement):\n" + cm.sessionMemory
	}
	out = append(out, llm.Message{Role: llm.RoleSystem, Content: sys})
	out = append(out, llm.Message{Role: llm.RoleUser, Content: "Requirement:\n" + s.Requirement})
	// A compaction summary sits right after the requirement so the LLM sees the
	// permanent gist of earlier attempts before the live round-by-round history.
	if cm.compactedSummary != "" {
		out = append(out, llm.Message{Role: llm.RoleUser, Content: "[auto-compacted summary of earlier conversation]\n" + cm.compactedSummary})
	}
	out = append(out, cm.history...)

	// Inject fresh retrieval context for this round so the LLM sees the
	// current code, not a stale snapshot.
	ctxText := ""
	if cm.ctxProv != nil {
		ctxText = cm.ctxProv.Context(ctx, s.Requirement)
	}
	prefix := "Relevant code context:\n" + ctxText
	prefix += "\n\nProject files:\n" + joinFiles(cm.ctxProv)

	// The current message carries the failure output + question; prepend
	// the retrieval context to it so it stays ordered.
	cur := current
	cur.Content = prefix + "\n\n" + cur.Content
	out = append(out, cur)
	return out
}

// trim keeps history within historyCap rounds (pairs of plan+outcome) and,
// when auto-compact is enabled, within compactRunes. Overflowing rounds are
// moved into pendingCompact (to be summarized) rather than silently dropped —
// the memory that separates a "coding agent that remembers" from one that
// "restarts every round", extended from rounds to tokens.
func (cm *ContextManager) trim() {
	// Runes-based compaction (auto-compact enabled): evict the oldest messages
	// until the remaining history fits the budget. The newest message is always
	// kept — it is the just-recorded outcome the next round depends on.
	if cm.compactRunes > 0 && totalRunes(cm.history) > cm.compactRunes {
		var evicted []llm.Message
		for len(cm.history) > 1 && totalRunes(cm.history) > cm.compactRunes {
			evicted = append(evicted, cm.history[0])
			cm.history = cm.history[1:]
		}
		cm.pendingCompact = append(cm.pendingCompact, evicted...)
	}
	// Count-based cap: keep at most historyCap rounds regardless of budget.
	// Without a compactor this is the classic truncate-oldest behavior.
	if len(cm.history) > cm.historyCap*2 {
		drop := len(cm.history) - cm.historyCap*2
		if cm.compactRunes > 0 {
			cm.pendingCompact = append(cm.pendingCompact, cm.history[:drop]...)
		}
		cm.history = cm.history[drop:]
	}
}

func totalRunes(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += utf8.RuneCountInString(m.Content)
	}
	return n
}

func joinFiles(cp *ContextProvider) string {
	if cp == nil {
		return ""
	}
	files := cp.FileList()
	out := ""
	for _, f := range files {
		out += f + "\n"
	}
	return out
}
