package agent

import (
	"context"
	"fmt"

	"github.com/zlx05/go-coding/internal/registry"
	"github.com/zlx05/go-coding/internal/subagent"
)

// executorPrompt is the role prompt of the executor worker: an implementation
// agent that owns exactly one plan step (one file, or one logical change). It
// reads the existing code, produces the file's full content itself, and runs
// tests until they pass. It shares the parent's workspace and symbol index, so
// its edits are immediately visible to sibling steps and to the parent — the
// same shared state as any subagent dispatch.
const executorPrompt = `You are an implementation worker in a plan-and-execute agent. You own exactly ONE file (or one logical change). Your task tells you what to implement and the acceptance criteria.

WORKING STYLE:
1. Read the existing file and related code first — you have explore tools (list_dir, grep, read_file). Match the project's style and keep everything else intact.
2. Produce a plan with a SINGLE step carrying the FULL NEW FILE CONTENT for your file. Creating a file that does not exist yet is fine.
3. Tests run automatically after your edit; if they fail, read the failure output and repair.
4. If the acceptance criteria require a test (e.g. an HTTP check on a server you wrote), write that test as part of your edit and make it pass — an acceptance gate that has no test will not hold your deliverable to its stated behaviour.

You share the parent's workspace and symbol index: your edits are immediately visible to the rest of the task. Do not touch files outside your step unless the task explicitly requires it.`

// runExecutor dispatches one plan step to an executor worker. It is the
// execution half of hierarchical plan-and-execute (P22): the planner outlined
// the step, and a fresh worker — potentially on a cheaper model — implements
// the file and verifies it with its own test loop. Mirrors runSubagent's
// merge/record/log pattern (internal/agent/subagent.go) but is driven
// programmatically from editing() rather than via call_tool.
func (a *Agent) runExecutor(ctx context.Context, step Step) Result {
	def := subagent.Def{
		Name:     "executor",
		Prompt:   executorPrompt,
		ReadOnly: false,
		Model:    a.executorModel,
	}
	task := fmt.Sprintf("Implement %s in %s.\nAcceptance: %s", step.Desc, step.File, step.Accept)
	child := a.newChildAgent(def, task)
	a.sess.Log(StateEditing, "executor", "dispatch "+step.File)
	res := child.run(ctx)
	// The worker's token usage and LLM calls merge into the parent session so
	// cost accounting stays whole across the whole hierarchical run.
	a.sess.AddUsage(res.Usage)
	a.sess.LLMCalls += res.LLMCalls

	report := res.Note
	if report == "" {
		report = res.TestOutput
	}
	a.ctxMgr.RecordExternal("executor:"+step.File, registry.ToJSON(map[string]any{
		"state":  string(res.State),
		"rounds": res.Rounds,
		"report": report,
		"usage":  res.Usage,
	}))
	a.sess.Log(StateEditing, "executor", fmt.Sprintf("%s done: state=%s rounds=%d report=%s",
		step.File, res.State, res.Rounds, truncate(report, 200)))
	return res
}
