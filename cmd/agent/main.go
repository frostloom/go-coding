// Command agent is a Claude-Code-style interactive coding agent for a Go
// project. It copies the target directory into a sandbox workspace (so the
// source is never modified in place) and drives the plan→edit→test loop.
//
//	agent                      # interactive REPL in the current directory
//	agent -repo testdata/sample -req "fix the Add function"
//	agent -repo some/project -interactive -arch pe
//
// LLM connection is configured via environment, optionally loaded from a
// KEY=VALUE file (default ~/.go-coding.env) so the built binary starts
// straight into a conversation without re-typing the model settings.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zlx05/go-coding/internal/agent"
	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/internal/mcpclient"
	"github.com/zlx05/go-coding/internal/registry"
	"github.com/zlx05/go-coding/internal/skill"
	"github.com/zlx05/go-coding/internal/subagent"
	"github.com/zlx05/go-coding/pkg/llm"
)

func main() {
	repo := flag.String("repo", ".", "target project directory (default: current directory)")
	req := flag.String("req", "", "task requirement (or the first turn in -interactive mode)")
	maxRounds := flag.Int("max-rounds", 5, "max repair rounds")
	runTimeout := flag.Duration("run-timeout", 60*time.Second, "run tool timeout")
	testCmd := flag.String("test-cmd", "", "test command override for run_test, e.g. \"python -m pytest\" (default: go test ./...)")
	allowCmds := flag.String("allow", "", "comma-separated extra commands the sandbox may run, e.g. \"python,pytest\" for non-Go projects (widens the Go+git allowlist)")
	workdir := flag.String("workdir", ".agent-workspace", "sandbox working directory (ignored with -direct)")
	direct := flag.Bool("direct", false, "edit the target project in place, like Claude Code — no sandbox copy; git snapshots still allow the agent to undo")
	blockPaths := flag.String("block-paths", "", "comma-separated path prefixes the agent may never edit (demo of a before_edit hook)")
	maxTokens := flag.Int("max-tokens", 0, "stop the agent once the session reaches this many tokens (demo of a before_chat cost-gate hook; 0 = no budget)")
	interactive := flag.Bool("interactive", false, "interactive REPL: keep sending requests to one agent in the same workspace")
	arch := flag.String("arch", "single", "architecture: single (classic one-agent loop) | pe (P6 evaluator-optimizer)")
	executor := flag.Bool("executor", true, "plan-and-execute: the parent outlines per-file steps and a worker executor implements each one (P22; pass -executor=false for the legacy atomic full-content path)")
	executorModel := flag.String("executor-model", "", "LLM model executor workers run on — per-role tiering: strong planner, cheaper executor (default: OPENAI_EXECUTOR_MODEL env, else the planner's model)")
	config := flag.String("config", "", "KEY=VALUE env file (default ~/.go-coding.env)")
	var mcpServers mcpListFlag
	flag.Var(&mcpServers, "mcp", "spawn a stdio MCP server as \"label cmd args...\" and register its tools (repeatable)")
	flag.Parse()

	// Without an explicit requirement, drop straight into the REPL.
	if *req == "" {
		*interactive = true
	}
	// Repo-root .env first (the live keys the user filled in), then the
	// -config file (default ~/.go-coding.env). Shell exports win over both.
	llm.LoadEnvFile(".env")
	loadConfigFile(*config)

	cfg := llm.ConfigFromEnv()
	lc, err := llm.NewOpenAI(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "set OPENAI_BASE_URL / OPENAI_MODEL / OPENAI_API_KEY, or write them as KEY=VALUE lines into ~/.go-coding.env")
		os.Exit(1)
	}

	// Default: copy the target into a sandbox workspace so the agent's edits
	// stay isolated from the source repository. When the target IS the current
	// directory, skip our own output dir, VCS metadata and common junk — a
	// recursive copy must not pull in the sandbox it is writing into.
	// -direct: no copy — the agent edits the operator's real project in place,
	// like Claude Code. Safety shifts from "work on a throwaway copy" to the
	// permission gate (interactive confirm before each edit) + git snapshots
	// that still let the agent undo and show its diff.
	ws := *repo
	if !*direct {
		if err := os.MkdirAll(*workdir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		ws = filepath.Join(*workdir, "task-"+fmt.Sprint(time.Now().UnixMilli()))
		if err := fsutil.CopyDirFiltered(*repo, ws, []string{".git", ".agent-workspace", "node_modules", ".idea", ".vscode", "__pycache__"}); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}

	// termRenderer turns the agent's live events and streamed LLM deltas into
	// a Claude-Code-like terminal: you see the model "think" and each tool act
	// as it happens, not one result line after the run finishes.
	tr := &termRenderer{}
	acfg := agent.Config{
		RepoPath:    ws,
		Requirement: *req,
		MaxRounds:   *maxRounds,
		RunTimeout:  *runTimeout,
		OnStream:    tr.delta,
		OnEvent:     tr.event,
		InPlace:     *direct,
	}
	// -test-cmd lets the agent drive a non-Go project's test suite (pytest,
	// npm test, …). Empty keeps the Go default. P20: de-hardcodes Go from the
	// formal test phase so "other languages like Claude" actually holds.
	if *testCmd != "" {
		acfg.TestCommand = strings.Fields(*testCmd)
	}
	// -allow lets the same project's tooling through the sandbox whitelist
	// (python to run pytest, node to run npm test, …). Each name is granted any
	// invocation; the strict Go+git default is untouched unless this is set.
	if *allowCmds != "" {
		allowed := map[string][]string{}
		for _, name := range strings.Split(*allowCmds, ",") {
			if name = strings.TrimSpace(name); name != "" {
				allowed[name] = []string{"*"}
			}
		}
		if len(allowed) > 0 {
			acfg.AllowedCommands = allowed
		}
	}
	// -arch pe wires a second role (the evaluator) on the same model: tests
	// passing is no longer sufficient — the evaluator must approve, or its
	// feedback drives another repair round.
	if *arch == "pe" {
		acfg.Evaluator = lc
	}
	// P22 hierarchical plan-and-execute: on by default (the CLI is the direct-cutover
	// entry point). The parent plans an outline; each step is implemented by a worker
	// executor — on a cheaper model when -executor-model is set. -executor=false is
	// the escape hatch back to the legacy atomic whole-content path.
	if *executor {
		acfg.Executor = true
		// Model tiering default lives in the same env as the planner's model
		// (OPENAI_EXECUTOR_MODEL); an explicit -executor-model flag wins over it.
		acfg.ExecutorModel = *executorModel
		if acfg.ExecutorModel == "" {
			acfg.ExecutorModel = cfg.ExecutorModel
		}
	}
	// -block-paths demonstrates a before_edit veto hook: any edit touching a
	// blocked path is rejected before it lands, and the agent repairs instead.
	// -max-tokens demonstrates a before_chat cost-gate hook: once the session
	// budget is exhausted, every further LLM call is refused and the task stops
	// — the ONE veto point that can stop the spend, because repairing would
	// spend more. Both hooks are pure opt-in; headless runs leave Hooks nil.
	if *blockPaths != "" || *maxTokens > 0 {
		acfg.Hooks = map[agent.HookPoint][]agent.Hook{}
	}
	if *blockPaths != "" {
		acfg.Hooks[agent.HookBeforeEdit] = append(acfg.Hooks[agent.HookBeforeEdit], func(_ context.Context, ev agent.Event) error {
			for _, p := range strings.Split(*blockPaths, ",") {
				if strings.HasPrefix(ev.Detail, p) {
					return errors.New("policy: path prefix " + p + " is protected")
				}
			}
			return nil
		})
	}
	if *maxTokens > 0 {
		acfg.Hooks[agent.HookBeforeChat] = append(acfg.Hooks[agent.HookBeforeChat], func(_ context.Context, ev agent.Event) error {
			if ev.Tokens >= *maxTokens {
				return fmt.Errorf("cost budget exceeded: session at %d tokens (limit %d)", ev.Tokens, *maxTokens)
			}
			return nil
		})
	}
	// Interactive mode wires the permission gate — the defense-in-depth layer
	// where write actions and test runs ask the operator before they execute
	// ("dangerous command asks you", like Claude Code). Headless single-shot
	// runs leave it nil, so everything stays fully automatic.
	if *interactive {
		acfg.Confirm = interactiveConfirm
	}
	// Cross-session memory: user-level ~/.go-coding/memory.md + project-level
	// go-coding.md load into the system prompt (Claude Code's CLAUDE.md
	// layering). Empty when neither file exists, so behavior is unchanged. The
	// REPL additionally records each turn and flushes it to the project file on
	// exit, so the NEXT session remembers this one.
	absRepo, _ := filepath.Abs(*repo)
	home, _ := os.UserHomeDir()
	mem := agent.NewMemory(filepath.Join(home, ".go-coding", "memory.md"), filepath.Join(absRepo, "go-coding.md"))
	if blk := mem.Block(); blk != "" {
		acfg.SessionMemory = blk
	}
	// Dynamic external tools — skills, MCP servers, subagents. All three are
	// inert until the LLM emits {"call_tool":...}: a model that never calls
	// them behaves exactly as before, so this wiring is pure opt-in.
	//
	// Skills (.go-coding/skills) surface as "skill:<name>"; calling one feeds
	// its instructions (body + resources) into context.
	var extTools []registry.ToolDef
	if skills, err := skill.Load(absRepo, home); err != nil {
		fmt.Fprintln(os.Stderr, "warn: skills:", err)
	} else {
		for _, s := range skills {
			sd := s
			extTools = append(extTools, registry.ToolDef{
				Name:        "skill:" + sd.Name,
				Description: sd.Description,
				Schema:      map[string]any{"type": "object"},
				Run: func(_ context.Context, _ map[string]any) (any, error) {
					return sd.Render(), nil
				},
			})
		}
		if len(skills) > 0 {
			names := make([]string, 0, len(skills))
			for _, s := range skills {
				names = append(names, s.Name)
			}
			fmt.Printf("skills: %s\n", strings.Join(names, ", "))
		}
	}
	// MCP servers (-mcp "label cmd args..."): spawn a stdio server, discover
	// its tools via tools/list and register them namespaced "mcp:<label>:<t>".
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
	if len(extTools) > 0 {
		acfg.Tools = extTools
	}
	// Subagents (.go-coding/agents) surface as "subagent:<name>": the model
	// dispatches a task, the child runs in the same workspace and reports back.
	if subs, err := subagent.Load(absRepo, home); err != nil {
		fmt.Fprintln(os.Stderr, "warn: subagents:", err)
	} else if len(subs) > 0 {
		acfg.SubAgents = subs
		for _, s := range subs {
			role := "worker"
			if s.ReadOnly {
				role = "read-only"
			}
			fmt.Printf("subagent: %s (%s)\n", s.Name, role)
		}
	}

	a := agent.New(lc, acfg)
	// Claude-Code-style auto-compact: once the conversation history passes
	// ~24k runes, the oldest rounds are summarized by one LLM call instead of
	// truncated, so a long REPL session keeps its memory. Live sessions only —
	// harness/mock runs never enable it, keeping the baseline deterministic.
	a.SetAutoCompact(24_000)

	if abs, err := filepath.Abs(*repo); err == nil {
		fmt.Printf("working on: %s\n", abs)
	}
	if cfg.Model != "" {
		fmt.Printf("model: %s (%s)\n", cfg.Model, cfg.BaseURL)
	}
	if *direct {
		fmt.Printf("mode: in-place — edits hit %s directly (git snapshots let the agent undo)\n", ws)
	} else {
		fmt.Printf("sandbox: %s (your source is untouched; the agent edits this copy)\n", ws)
	}

	if *interactive {
		runREPL(a, *req, tr, mem, *direct)
		return
	}

	res := a.Run(context.Background())
	tr.endStream()
	fmt.Printf("== result ==\nstate=%s rounds=%d toolCalls=%d tokens=%d note=%s\nworkspace=%s\n",
		res.State, res.Rounds, res.ToolCalls, res.Usage.TotalTokens, res.Note, ws)
	if res.TestOutput != "" {
		fmt.Println("== test output ==")
		fmt.Println(res.TestOutput)
	}
	if res.Passed() {
		os.Exit(0)
	}
	os.Exit(1)
}

// runREPL keeps chatting with one agent across turns: each user request is a
// fresh RunTurn that resets round state but reuses the workspace and the
// conversation memory, so follow-ups build on prior edits. Each turn's outcome
// is recorded into the cross-session memory and flushed on exit, so a future
// session in the same project remembers what this one did.
func runREPL(a *agent.Agent, first string, tr *termRenderer, mem *agent.Memory, direct bool) {
	fmt.Println()
	fmt.Println("== go-coding agent REPL ==")
	if direct {
		fmt.Println("type a request; the agent edits your project in place. exit/quit to leave.")
	} else {
		fmt.Println("type a request; the agent edits the sandbox copy. exit/quit to leave.")
	}
	defer func() {
		if err := mem.Flush(); err != nil {
			fmt.Fprintln(os.Stderr, "warn: could not write session memory:", err)
		}
	}()
	record := func(req string, res agent.Result) {
		mem.AddNote(fmt.Sprintf("- [%s] %q → %s (%d rounds, %d tokens)", time.Now().Format("15:04"), truncate(req, 60), res.State, res.Rounds, res.Usage.TotalTokens))
	}
	var total int
	if first != "" {
		res := a.RunTurn(context.Background(), first)
		total = printResult(res, 0)
		record(first, res)
		tr.endStream()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("you> ")
		if !sc.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			// Slash command: built-in or a user command file (markdown in
			// ./.go-coding/commands/ or ~/.go-coding/commands/). Unknown
			// commands are rejected, not sent to the model.
			if exit, handled := handleSlash(a, line, tr, &total); handled {
				if exit {
					return
				}
				continue
			}
			fmt.Printf("unknown command %q — /help lists what's available\n", line)
			continue
		}
		res := a.RunTurn(context.Background(), line)
		total = printResult(res, total)
		record(line, res)
		tr.endStream()
	}
}

// interactiveConfirm is the permission gate wired into interactive mode:
// write actions (edit_file) and test runs ask the operator [y/N] before they
// execute — the "dangerous command asks you" layer of defense in depth. It
// reads a fresh line from stdin (the REPL's own scanner only consumes the
// typed request, so a confirmation reads the next line cleanly). No is a
// veto: the agent repairs and proposes a different change.
func interactiveConfirm(pa agent.PermissionAction) bool {
	verb := map[string]string{"edit_file": "edit", "run_test": "run tests"}[pa.Op]
	if verb == "" {
		verb = "run " + pa.Op
	}
	fmt.Printf("🔐 allow %s: %s? [y/N] ", verb, pa.Detail)
	ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// handleSlash dispatches a "/command [args]" line. Returns (exit, handled);
// handled=false means the command is unknown. Custom commands are markdown
// files named "<cmd>.md" in ./.go-coding/commands/ (project) or
// ~/.go-coding/commands/ (user); an optional frontmatter "description" feeds
// /help, the body is the prompt injected as a new turn.
func handleSlash(a *agent.Agent, line string, tr *termRenderer, total *int) (bool, bool) {
	cmd, args, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	switch cmd {
	case "help":
		fmt.Println("commands: /help /status /tokens /clear /exit")
		for _, c := range customCommands() {
			fmt.Printf("  /%s — %s\n", c.name, c.description)
		}
		return false, true
	case "status":
		fmt.Printf("model configured via environment; workspace=%s\n", workspaceOf(a))
		fmt.Printf("session tokens: %d\n", tokensOf(a))
		return false, true
	case "tokens":
		fmt.Printf("session tokens (cumulative): %d\n", tokensOf(a))
		return false, true
	case "clear":
		a.ClearMemory()
		fmt.Println("conversation memory cleared (workspace and edits kept)")
		return false, true
	case "exit", "quit":
		return true, true
	}
	for _, c := range customCommands() {
		if c.name == cmd {
			req := c.prompt
			if args != "" {
				req += "\n\nArgs: " + args
			}
			*total = printResult(a.RunTurn(context.Background(), req), *total)
			tr.endStream()
			return false, true
		}
	}
	return false, false
}

// customCommands lists user-defined slash commands from the project and user
// command directories (a project dir is resolved from the sandbox copy, so a
// command written next to the real project is picked up).
func customCommands() []cmdInfo {
	var out []cmdInfo
	dirs := []string{filepath.Join(".go-coding", "commands")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".go-coding", "commands"))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				out = append(out, parseCommandFile(name, string(b)))
			}
		}
	}
	return out
}

// cmdInfo is one user-defined slash command.
type cmdInfo struct {
	name        string
	description string
	prompt      string
}

// parseCommandFile reads a command markdown file: optional frontmatter
// (---…---) with a "description" key, then the body is the prompt.
func parseCommandFile(name, content string) cmdInfo {
	c := cmdInfo{name: name}
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "---") {
		if end := strings.Index(s[3:], "---"); end >= 0 {
			fm := s[3 : 3+end]
			rest := s[3+end+3:]
			for _, l := range strings.Split(fm, "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && strings.TrimSpace(k) == "description" {
					c.description = strings.TrimSpace(v)
				}
			}
			s = rest
		}
	}
	c.prompt = strings.TrimSpace(s)
	if c.description == "" {
		c.description = "custom command"
	}
	return c
}

// mcpListFlag collects repeated -mcp "label cmd args..." values.
type mcpListFlag []string

func (m *mcpListFlag) String() string { return strings.Join(*m, "; ") }
func (m *mcpListFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// workspaceOf / tokensOf expose agent state for /status — read through small
// accessors so the REPL stays decoupled from agent internals.
func workspaceOf(a *agent.Agent) string { return a.Workspace() }
func tokensOf(a *agent.Agent) int       { return a.SessionTokens() }

// printResult prints one turn's outcome and returns the session token total.
// Since Result.Usage accumulates across turns, it reports the delta too.
func printResult(res agent.Result, prevTotal int) int {
	delta := res.Usage.TotalTokens - prevTotal
	fmt.Printf("== result ==\nstate=%s rounds=%d toolCalls=%d tokens=%d total=%d note=%s\n",
		res.State, res.Rounds, res.ToolCalls, delta, res.Usage.TotalTokens, res.Note)
	if res.TestOutput != "" {
		fmt.Println("== test output ==")
		fmt.Println(res.TestOutput)
	}
	return res.Usage.TotalTokens
}

// termRenderer renders the agent's live progress to a terminal. Both callbacks
// fire synchronously from the agent's own goroutine during RunTurn, so no
// locking is needed: the streaming line is always "settled" before the next
// event line starts.
type termRenderer struct {
	streaming bool // an LLM stream line is currently being written
	tick      int
}

// delta is called once per streamed token. The streamed bytes are the LLM's
// JSON protocol ({"steps":…} plans, evaluator verdicts) — echoing them raw
// would dump a multi-KB single line that wraps and floods the scrollback in
// plain consoles. So we render a compact activity tick instead; the real
// progress (plan step count, edits, test output) is shown by event() as each
// action completes.
func (t *termRenderer) delta(string) {
	if !t.streaming {
		t.streaming = true
		fmt.Print("\n  ⚙️ thinking")
		return
	}
	t.tick++
	if t.tick%40 == 39 {
		fmt.Print(".")
	}
}

// endStream settles a finished streaming line. Called once per RunTurn after
// it returns, so the prompt and events never interleave mid-line.
func (t *termRenderer) endStream() {
	if t.streaming {
		t.streaming = false
		fmt.Println()
	}
}

// event renders one live session event (explore / edit / test / reject...).
func (t *termRenderer) event(ev agent.Event) {
	t.endStream()
	if line := renderEvent(ev); line != "" {
		fmt.Println(line)
	}
}

// renderEvent maps a session event to one terminal line with a hint of what
// the agent is doing. Tool-less state transitions are skipped.
func renderEvent(ev agent.Event) string {
	switch ev.Tool {
	case "explore":
		return "  \U0001F50D explore: " + truncate(ev.Detail, 140)
	case "edit_file":
		return "  ✏️ " + ev.Detail
	case "run_test":
		first := ev.Detail
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		return "  \U0001F9EA go test → " + truncate(first, 140)
	case "edit_rejected", "edit_blocked", "symbol_gate_reject", "eval_reject":
		return "  ⛔ " + truncate(ev.Detail, 200)
	case "plan":
		return "  \U0001F4CB " + ev.Detail
	case "":
		return "" // state transition, not an action
	}
	if ev.Detail == "" {
		return "  · " + ev.Tool
	}
	return "  · " + ev.Tool + ": " + truncate(ev.Detail, 140)
}

// loadConfigFile sources KEY=VALUE lines from the given file, defaulting to
// ~/.go-coding.env, into the environment when the key is not already set (real
// env always wins). Missing file = no-op.
func loadConfigFile(explicit string) {
	path := explicit
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		path = filepath.Join(home, ".go-coding.env")
	}
	llm.LoadEnvFile(path)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
