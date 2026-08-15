package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/internal/sandbox"
	"github.com/zlx05/go-coding/internal/tools"
	"github.com/zlx05/go-coding/pkg/llm"
)

// runOne executes one case end to end:
//  1. copy fixture into a fresh sandbox;
//  2. stash the hidden acceptance test OUT of the agent's view;
//  3. run the agent on the real loop;
//  4. restore the hidden test and run it as the acceptance check.
//
// Step 2 is what prevents "self-fulfilling prophecy": the agent cannot
// cheat by editing the very test that grades it.
//
// runOne is independent of *testing.T so both the test wrappers (RunMock /
// RunLive) and the CLI (cmd/harness) drive the exact same closed loop.
// Setup failures are folded into the result's Note rather than failing hard.
// evl is the evaluator-role client for P6; nil keeps the single-agent path.
func (h *Harness) runOne(ctx context.Context, c Case, lc, evl llm.ChatClient) CaseResult {
	fail := func(note string) CaseResult {
		return CaseResult{Name: c.Name, Type: c.Type, WantPass: c.WantPass, AgentState: agent.StateFailed, Note: note}
	}

	dir, err := os.MkdirTemp("", "harness-case-*")
	if err != nil {
		return fail("setup: " + err.Error())
	}
	defer os.RemoveAll(dir)

	if err := fsutil.CopyDir(c.FixtureDir, dir); err != nil {
		return fail("copy fixture: " + err.Error())
	}

	stash, err := os.MkdirTemp("", "harness-stash-*")
	if err != nil {
		return fail("setup stash: " + err.Error())
	}
	defer os.RemoveAll(stash)
	hidden, err := stashAcceptance(dir, stash)
	if err != nil {
		return fail("stash acceptance tests: " + err.Error())
	}

	cfg := agent.Config{
		RepoPath:       dir,
		Requirement:    c.Requirement,
		MaxRounds:      h.MaxRounds,
		RunTimeout:     h.RunTimeout,
		Evaluator:      evl,
		Tools:          h.Tools,
		Executor:       true,
		ExecutorModel:  h.ExecutorModel,
	}
	// P14 per-task-type cost budget: when this case's type has a ceiling,
	// register the same before_chat cost-gate hook as cmd/agent -max-tokens,
	// keyed by case complexity instead of one flat CLI number. The veto fails
	// the case at the first call that would push it over the budget — no
	// REPAIRING (repairing would spend more). With no budget set (the default)
	// Hooks stays nil and the run is exactly the P13 baseline.
	if budget, ok := h.MaxTokens[c.Type]; ok && budget > 0 {
		typ := c.Type
		name := c.Name
		cfg.Hooks = map[agent.HookPoint][]agent.Hook{
			agent.HookBeforeChat: {func(_ context.Context, ev agent.Event) error {
				if ev.Tokens >= budget {
					return fmt.Errorf("cost budget exceeded for %s case %s: at %d (limit %d)", typ, name, ev.Tokens, budget)
				}
				return nil
			}},
		}
	}
	a := agent.New(lc, cfg)
	start := time.Now()
	res := a.Run(ctx)
	dur := time.Since(start)

	if err := restoreAcceptance(stash, dir, hidden); err != nil {
		return fail("restore acceptance tests: " + err.Error())
	}

	// Acceptance = the hidden test passes, not just "agent said done".
	acceptOK := false
	if res.Passed() {
		sb, err := sandbox.New(dir)
		if err == nil {
			at := &tools.RunTestTool{Sandbox: sb, Timeout: h.RunTimeout}
			if ar, err := at.Run(ctx, tools.Args(`{}`)); err == nil {
				acceptOK = ar.Ok
			}
		}
	}

	return CaseResult{
		Name:       c.Name,
		Type:       c.Type,
		Passed:     res.Passed() && acceptOK,
		WantPass:   c.WantPass,
		Rounds:     res.Rounds,
		ToolCalls:  res.ToolCalls,
		LLMCalls:   res.LLMCalls,
		Duration:   dur,
		AgentState: res.State,
		Note:       res.Note,
		Tokens:     res.Usage.TotalTokens,
	}
}

// stashAcceptance moves *_acceptance_test.go out of the sandbox and returns
// the moved filenames (basenames) so they can be restored later.
func stashAcceptance(dir, stash string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*_acceptance_test.go"))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		if err := os.Rename(m, filepath.Join(stash, base)); err != nil {
			return names, err
		}
		names = append(names, base)
	}
	return names, nil
}

func restoreAcceptance(stash, dir string, names []string) error {
	for _, n := range names {
		// The Go toolchain ignores files whose names start with "_" or ".", so
		// the stashed "_acceptance_test.go" must be restored under a runnable
		// name — otherwise the post-run grading silently skips the hidden test
		// and the acceptance check only reflects the visible tests.
		runName := "zz_" + strings.TrimPrefix(n, "_")
		if err := os.Rename(filepath.Join(stash, n), filepath.Join(dir, runName)); err != nil {
			return err
		}
	}
	return nil
}
