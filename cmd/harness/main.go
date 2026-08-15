// Command harness runs the full M5 evaluation set and reports capability +
// performance metrics, in either deterministic mock mode (no LLM, CI-safe)
// or live mode against a real LLM.
//
//	harness -mode mock -out harness-out/mock.json
//	harness -mode live -out harness-out/live.json      # needs OPENAI_* env
//	harness -mode live -mcp "compat ./mcp-demo-server.exe"   # + external tools
//
// -mcp registers remote MCP server tools (namespaced "mcp:<label>:<tool>")
// into every case's agent, same wiring as cmd/agent — the knowledge tool that
// makes fix_legacy_time solvable (its hidden layout lives only there).
//
// The report is machine-readable JSON (mode + per-case results + summary) so
// iteration can compare runs over time.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zlx05/go-coding/internal/harness"
	"github.com/zlx05/go-coding/internal/mcpclient"
	"github.com/zlx05/go-coding/internal/registry"
	"github.com/zlx05/go-coding/pkg/llm"
)

func main() {
	mode := flag.String("mode", "mock", "run mode: mock (deterministic, no LLM) | live (real LLM)")
	out := flag.String("out", "", "path to write the report JSON (default: print summary only)")
	maxRounds := flag.Int("max-rounds", 5, "max repair rounds per case")
	runTimeout := flag.Duration("run-timeout", 60*time.Second, "run tool timeout per case")
	price := flag.Float64("price-per-1m", 0, "USD per 1M total tokens; prints estimated cost per case (0 = skip)")
	executorModel := flag.String("executor-model", "", "P22 per-role model tiering: LLM model executor workers run on (default: OPENAI_EXECUTOR_MODEL env, else the case's main model)")
	arch := flag.String("arch", "single", "architecture: single (classic one-agent loop) | pe (P6 evaluator-optimizer)")
	caseName := flag.String("case", "", "run only this case (default: all)")
	fixBudget := flag.Int("max-tokens-fix", 0, "P14 per-type cost budget: stop a bugfix case once the session passes this many tokens (0 = no gate)")
	implBudget := flag.Int("max-tokens-impl", 0, "P14 per-type cost budget: stop an implementation case once the session passes this many tokens (0 = no gate)")
	selfBudget := flag.Int("max-tokens-self", 0, "P14 per-type cost budget: stop a self-hosted case once the session passes this many tokens (0 = no gate)")
	var mcpServers mcpListFlag
	flag.Var(&mcpServers, "mcp", "spawn a stdio MCP server as \"label cmd args...\" and register its tools (repeatable)")
	flag.Parse()

	// Repo-root .env (the user's live keys) feeds ConfigFromEnv below. Missing
	// file or a shell export wins either way — .env only fills unset vars.
	llm.LoadEnvFile(".env")
	cfg := llm.ConfigFromEnv()

	// MCP servers (-mcp "label cmd args..."): spawn a stdio server, discover
	// its tools via tools/list and register them namespaced "mcp:<label>:<t>"
	// as the external tools every case's agent can call_tool. Same wiring as
	// cmd/agent — this is how a case like fix_legacy_time becomes solvable:
	// its hidden acceptance format lives only in the remote knowledge tool.
	// Mock mode never emits call_tool, so the deterministic gate is unchanged
	// by Tools being present.
	var extTools []registry.ToolDef
	for _, spec := range mcpServers {
		parts := strings.Fields(spec)
		if len(parts) < 2 {
			fmt.Fprintln(os.Stderr, "warn: -mcp wants \"label cmd args...\", got:", spec)
			continue
		}
		mc, err := mcpclient.DialStdio(context.Background(), parts[0], parts[1], parts[2:], nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "warn: -mcp", parts[0]+":", err)
			continue
		}
		defer mc.Close()
		defs, err := mc.AsToolDefs(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "warn: -mcp", parts[0], "tools/list:", err)
			continue
		}
		fmt.Printf("mcp %s: %d remote tool(s)\n", parts[0], len(defs))
		extTools = append(extTools, defs...)
	}

	h := harness.New()
	h.MaxRounds = *maxRounds
	h.RunTimeout = *runTimeout
	h.UsePE = *arch == "pe"
	// Model tiering default lives in the same env as the planner's model
	// (OPENAI_EXECUTOR_MODEL); an explicit -executor-model flag wins over it.
	h.ExecutorModel = *executorModel
	if h.ExecutorModel == "" {
		h.ExecutorModel = cfg.ExecutorModel
	}
	h.Tools = extTools
	// P14 per-task-type cost budgets (all opt-in: 0 flags = no gate, exactly
	// the P13 run). Each case gets its own type's ceiling; the cheapest class
	// (bugfix) and the priciest (self-hosted) no longer share one flat number.
	if *fixBudget > 0 || *implBudget > 0 || *selfBudget > 0 {
		h.MaxTokens = map[harness.CaseType]int{}
		if *fixBudget > 0 {
			h.MaxTokens[harness.TypeBugfix] = *fixBudget
		}
		if *implBudget > 0 {
			h.MaxTokens[harness.TypeImpl] = *implBudget
		}
		if *selfBudget > 0 {
			h.MaxTokens[harness.TypeSelf] = *selfBudget
		}
	}
	for _, c := range harness.Cases() {
		if *caseName != "" && c.Name != *caseName {
			continue
		}
		h.AddCase(c)
	}

	ctx := context.Background()
	var results []harness.CaseResult
	switch *mode {
	case "mock":
		results = h.RunMockNoTest(ctx)
	case "live":
		lc, err := llm.NewOpenAI(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		results = h.RunLiveNoTest(ctx, lc)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (use mock|live)\n", *mode)
		os.Exit(2)
	}

	summary := h.Summarize(results)
	fmt.Println("==", *mode, "summary ==")
	for _, r := range results {
		mark := "PASS"
		if !r.Passed {
			mark = "FAIL"
		}
		cost := ""
		if *price > 0 {
			cost = fmt.Sprintf(" cost=$%.4f", float64(r.Tokens)*(*price)/1e6)
		}
		fmt.Printf("  [%s] %-16s type=%-6s rounds=%d toolCalls=%d llmCalls=%d tokens=%d dur=%s note=%s%s\n",
			mark, r.Name, r.Type, r.Rounds, r.ToolCalls, r.LLMCalls, r.Tokens, r.Duration.Round(time.Millisecond), r.Note, cost)
	}
	fmt.Println(summary.String())

	if *out != "" {
		if err := harness.WriteReport(*out, *mode, results, summary); err != nil {
			fmt.Fprintln(os.Stderr, "error writing report:", err)
			os.Exit(1)
		}
		fmt.Println("report ->", *out)
	}

	if summary.Passed != summary.Total {
		os.Exit(1)
	}
}

// mcpListFlag collects repeated -mcp "label cmd args..." values.
type mcpListFlag []string

func (m *mcpListFlag) String() string { return strings.Join(*m, "; ") }
func (m *mcpListFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
