package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zlx05/go-coding/internal/codeindex"
	"github.com/zlx05/go-coding/internal/registry"
	"github.com/zlx05/go-coding/internal/sandbox"
	"github.com/zlx05/go-coding/internal/subagent"
	"github.com/zlx05/go-coding/pkg/llm"
)

// agentShared carries the state a child agent inherits from its parent. nil
// means "top-level agent" — fresh sandbox + symbol index.
type agentShared struct {
	sb            *sandbox.Sandbox
	builder       *codeindex.Builder
	rolePrompt    string // the subagent's role instructions, prepended to its system prompt
	readOnly      bool   // researcher: explore-only, reports instead of editing
	modelOverride string // per-role LLM model (subagent.Def.Model); empty = inherit the parent's
}

// researchMaxTurns caps how many explore/tool rounds a researcher may take
// before being forced to write its report. Prevents a research loop that
// never converges.
const researchMaxTurns = 12

// runSubagent dispatches a task to a registered subagent. It is the Run
// handler of the dynamic tool "subagent:<name>" registered in newAgent. The
// child runs in the SAME sandbox workspace and symbol index as the parent, so
// its edits are immediately visible to the parent's retrieval; the child's
// Result is returned as structured JSON for RecordExternal to feed back to
// the parent LLM.
func (a *Agent) runSubagent(ctx context.Context, def subagent.Def, args map[string]any) (any, error) {
	task, _ := args["task"].(string)
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, fmt.Errorf("subagent %q requires a \"task\" argument", def.Name)
	}
	child := a.newChildAgent(def, task)
	a.sess.Log(StatePlanning, "subagent", fmt.Sprintf("dispatch %s (read_only=%v)", def.Name, def.ReadOnly))
	var res Result
	if def.ReadOnly {
		res = child.runResearch(ctx)
	} else {
		res = child.run(ctx)
	}
	// The child's token usage and LLM call counts merge into the parent's
	// session so cost accounting stays whole across the whole dispatch.
	a.sess.AddUsage(res.Usage)
	a.sess.LLMCalls += res.LLMCalls

	report := res.Note
	if report == "" {
		report = res.TestOutput
	}
	a.sess.Log(StatePlanning, "subagent", fmt.Sprintf("%s done: state=%s rounds=%d report=%s", def.Name, res.State, res.Rounds, truncate(report, 200)))

	return json.RawMessage(registry.ToJSON(map[string]any{
		"subagent": def.Name,
		"state":    string(res.State),
		"rounds":   res.Rounds,
		"report":   report,
		"usage":    res.Usage,
	})), nil
}

// newChildAgent builds a fresh agent for one subagent dispatch: its own
// Session / ContextManager / tools, but the parent's sandbox workspace and
// symbol index (the shared state). Read-only children get only explore tools;
// workers get the full toolset. Subagent tools are NOT re-seeded (the child's
// external tools are the parent's extTools — skills / MCP only), so a child
// never dispatches a grandchild.
func (a *Agent) newChildAgent(def subagent.Def, task string) *Agent {
	cfg := Config{
		RepoPath:    a.sandbox.Root,
		Requirement: task,
		MaxRounds:   2, // subagent workers are scoped: a bounded attempt, then report back
		Tools:       a.extTools,
		// Lifecycle hooks govern subagent work too: an operator's before_chat
		// cost gate or before_edit block-list must also apply to a worker's
		// calls and edits, or the default executor path would bypass the very
		// governance P11/P14 exist to enforce. Hooks fire with the child's own
		// session, so a per-call budget reads that worker's spend.
		Hooks: a.hooks,
	}
	return newAgent(a.llm, cfg, &agentShared{
		sb:            a.sandbox,
		builder:       a.builder,
		rolePrompt:    def.Prompt,
		readOnly:      def.ReadOnly,
		modelOverride: def.Model,
	})
}

// runResearch is the researcher subagent loop: explore and call external tools
// until the model produces free text — that free text IS the report. There is
// no plan/edit/test state machine here; the researcher can only read. Bounded
// by researchMaxTurns so it always terminates.
func (a *Agent) runResearch(ctx context.Context) Result {
	for turns := 0; ; turns++ {
		select {
		case <-ctx.Done():
			return a.result(StateFailed, "research: "+ctx.Err().Error())
		default:
		}
		if turns >= researchMaxTurns {
			return a.result(StateFailed, "research: reached max turns without a report")
		}
		a.maybeCompact(ctx)
		resp, err := a.chat(ctx, llm.ChatRequest{
			Messages: a.ctxMgr.Messages(ctx, a.sess, llm.Message{Role: llm.RoleUser, Content: "Investigate and report back."}),
		})
		if err != nil {
			return a.result(StateFailed, "research: "+err.Error())
		}
		a.sess.AddUsage(resp.Usage)
		a.sess.LLMCalls++
		if resp.Truncated {
			return a.result(StateFailed, "research: response truncated by max_tokens")
		}
		content := strings.TrimSpace(resp.Message.Content)

		// The researcher has the same two look-then-act sub-loops as planning:
		// explore requests and external tool calls are executed and fed back.
		if exp, ok := parseExplore(content); ok {
			results := a.execExplore(ctx, exp)
			a.ctxMgr.RecordExplore(results)
			a.sess.Log(StatePlanning, "explore", truncate(results, 500))
			continue
		}
		if tc, ok := parseToolCall(content); ok {
			result := a.execToolCall(ctx, tc)
			a.ctxMgr.RecordExternal(tc.Name, result)
			a.sess.Log(StatePlanning, "call_tool", tc.Name)
			continue
		}
		// Anything else is the report.
		return a.result(StateDone, content)
	}
}
