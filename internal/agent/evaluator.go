package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/zlx05/go-coding/pkg/llm"
)

// The Evaluator–Optimizer role split (Anthropic's building-effective-agents
// workflow). This is the PLAN-level variant: the producer (optimizer) emits a
// plan of edits, and a separate evaluator reviews that plan BEFORE any edit
// lands. A rejected plan costs a rewrite, not a wasted edit+test round — the
// evaluation target is the generated content (the plan), matching the official
// pattern, and nothing touches disk until it is approved.
//
// Two gates, applied in order (following the official guidance "use a
// deterministic tool when one exists — don't spend tokens on something a
// parser can decide"):
//  1. symbol-integrity gate (objective, zero-token): the package declarations
//     + top-level symbols at task start are compared against what the plan
//     would produce. A plan that removes an existing symbol or renames a
//     package is rejected without any LLM call. This catches the failure mode
//     we saw live with a local 7B model: it "fixed" a bug by rewriting a
//     194-line file into 49 lines and renaming the package — tests happened to
//     pass, but the structure was destroyed.
//  2. LLM review (subjective): an independent chat with its own transcript
//     judges whether the proposed plan fully satisfies the requirement.

// SymbolInfo is one top-level declaration in one file.
type SymbolInfo struct {
	File string // path relative to the workspace root
	Pkg  string // package name
	Name string // declared name (func / type / var / const)
}

// SymbolInventory is the objective baseline/current snapshot for the gate.
type SymbolInventory struct {
	Symbols []SymbolInfo
}

// symbolsFromSource parses one Go source snippet and returns its top-level
// symbols. Shared by the workspace inventory and the plan gate, so both judge
// structure with the same parser.
func symbolsFromSource(src string) []SymbolInfo {
	var syms []SymbolInfo
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "step.go", src, 0)
	if err != nil {
		return nil // unparseable content — the syntax gate will catch it later
	}
	pkg := f.Name.Name
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			syms = append(syms, SymbolInfo{Pkg: pkg, Name: d.Name.Name})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					syms = append(syms, SymbolInfo{Pkg: pkg, Name: sp.Name.Name})
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						syms = append(syms, SymbolInfo{Pkg: pkg, Name: n.Name})
					}
				}
			}
		}
	}
	return syms
}

// symbolInventory scans every .go file under root (skipping hidden dirs) and
// records its package name and top-level declarations. A pure parser pass, no
// LLM — the baseline captured at task start for the plan gate.
func symbolInventory(root string) (*SymbolInventory, error) {
	inv := &SymbolInventory{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, relerr := filepath.Rel(root, path)
		if relerr != nil {
			rel = path
		}
		for _, s := range symbolsFromSource(string(src)) {
			s.File = rel
			inv.Symbols = append(inv.Symbols, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// checkSymbolRegression diffs the baseline inventory against the current one
// and reports concrete regressions: a package renamed in a file, or a
// top-level symbol that existed at task start and is now gone. The producer
// may add symbols freely; it may not remove existing ones.
func checkSymbolRegression(base, cur *SymbolInventory) []string {
	var problems []string
	basePkg := map[string]string{} // file -> package
	curPkg := map[string]string{}
	baseSym := map[string]bool{} // file|pkg|name -> present
	curSym := map[string]bool{}
	for _, s := range base.Symbols {
		basePkg[s.File] = s.Pkg
		baseSym[s.File+"|"+s.Pkg+"|"+s.Name] = true
	}
	for _, s := range cur.Symbols {
		curPkg[s.File] = s.Pkg
		curSym[s.File+"|"+s.Pkg+"|"+s.Name] = true
	}
	for f, p := range basePkg {
		if q, ok := curPkg[f]; ok && q != p && q != "" {
			problems = append(problems, fmt.Sprintf("package changed in %s: %q -> %q", f, p, q))
		}
	}
	for k := range baseSym {
		if !curSym[k] {
			parts := strings.SplitN(k, "|", 3)
			problems = append(problems, fmt.Sprintf("symbol %s.%s removed from %s", parts[1], parts[2], parts[0]))
		}
	}
	return problems
}

// symbolPlanGate reviews a proposed plan BEFORE any edit lands: it builds the
// symbol inventory the plan would produce (files the plan touches are replaced
// by the plan's content; untouched files keep their baseline) and runs the
// objective regression check. A plan that would delete an existing symbol or
// rename a package is rejected without any LLM call — the gate costs zero
// tokens and cannot be fooled by a model that "fixes" by deleting code.
func (a *Agent) symbolPlanGate(steps []Step) []string {
	if a.sess.SymbolBase == nil {
		return nil
	}
	planned := &SymbolInventory{}
	covered := map[string]bool{}
	for _, st := range steps {
		for _, s := range symbolsFromSource(st.Content) {
			s.File = st.File
			planned.Symbols = append(planned.Symbols, s)
		}
		// An outline step (executor mode) carries no content, so it must not
		// "cover" the file either — otherwise every baseline symbol in it is
		// reported as removed and the whole outline plan is falsely rejected.
		// Structural protection for those steps lives in the executor's
		// edit-time call-graph gate instead.
		if st.Content != "" {
			covered[st.File] = true
		}
	}
	for _, s := range a.sess.SymbolBase.Symbols {
		if !covered[s.File] {
			planned.Symbols = append(planned.Symbols, s)
		}
	}
	return checkSymbolRegression(a.sess.SymbolBase, planned)
}

// Verdict is the evaluator's structured judgment, aligned with the official
// pattern: PASS / NEEDS_IMPROVEMENT / FAIL plus concrete feedback.
type Verdict struct {
	Evaluation string   `json:"evaluation"`
	Feedback   string   `json:"feedback"`
	Findings   []string `json:"findings"`
}

// Approves reports whether the evaluator considers the plan acceptable.
func (v Verdict) Approves() bool { return strings.EqualFold(v.Evaluation, "PASS") }

// parseVerdict is tolerant to the same deviations parseSteps is — code fences,
// trailing garbage, small-model JSON typos — by reusing the shared extraction
// and repair layer in prompt.go.
func parseVerdict(content string) (Verdict, error) {
	jsonStr, ok := extractFirstJSON(stripCodeFence(content))
	if !ok {
		return Verdict{}, fmt.Errorf("agent: no JSON object in evaluator reply")
	}
	var v Verdict
	if err := json.Unmarshal([]byte(repairJSON(jsonStr)), &v); err != nil {
		return Verdict{}, fmt.Errorf("agent: parse verdict JSON: %w", err)
	}
	if v.Evaluation == "" {
		return Verdict{}, fmt.Errorf("agent: evaluator returned no evaluation field")
	}
	v.Evaluation = strings.ToUpper(v.Evaluation)
	return v, nil
}

// Evaluator is the second role in the loop. It keeps its own small transcript
// (separate from the producer's ContextManager) so each review can reference
// what the previous review rejected. No retrieval: it judges the proposed plan
// against the requirement, not the symbol index.
type Evaluator struct {
	client  llm.ChatClient
	stream  func(string) // optional live renderer, shared with the producer
	history []llm.Message
}

func newEvaluator(lc llm.ChatClient, stream func(string)) *Evaluator {
	return &Evaluator{client: lc, stream: stream}
}

// chat runs one review LLM call, streaming deltas when the client supports it
// and a renderer is wired — same shape as Agent.chat, for the evaluator role.
func (e *Evaluator) chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	if sc, ok := e.client.(llm.ChatStreamer); ok && e.stream != nil {
		return sc.ChatStream(ctx, req, e.stream)
	}
	return e.client.Chat(ctx, req)
}

func evaluatorSystemPrompt() string {
	return `You are an evaluator for a coding agent's plan. You do NOT write code. You review a proposed plan against the original requirement and decide whether it should be applied.

Judge ONLY whether the plan fully satisfies the requirement without breaking existing code. Report concrete problems, not style nitpicks.

Respond with STRICT JSON only:
{"evaluation":"PASS|NEEDS_IMPROVEMENT|FAIL","feedback":"what must change and why","findings":["concrete issue 1", "..."]}
PASS only if the plan fully meets the requirement and nothing concrete remains.`
}

// messages assembles the evaluator's prompt: system, requirement, past
// reviews (its own transcript), then the plan under review.
func (e *Evaluator) messages(requirement, plan string) []llm.Message {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: evaluatorSystemPrompt()},
		{Role: llm.RoleUser, Content: "Requirement:\n" + requirement},
	}
	msgs = append(msgs, e.history...)
	msgs = append(msgs, llm.Message{
		Role:    llm.RoleUser,
		Content: "Review the proposed plan. Will it fully satisfy the requirement?\n\nProposed changes:\n" + truncate(plan, 4000),
	})
	return msgs
}

// evalPlan asks the evaluator role for a verdict over a proposed plan. The
// plan is summarized as file:desc per step plus the full content (truncated),
// so the reviewer can judge coverage, not just structure.
func (a *Agent) evalPlan(ctx context.Context, steps []Step) (Verdict, error) {
	var b strings.Builder
	for _, st := range steps {
		// Outline steps (executor mode) carry no content; show the acceptance
		// criteria instead of an empty code block so the reviewer still judges
		// each step on the evidence available.
		if st.Content == "" {
			fmt.Fprintf(&b, "### %s — %s\nAcceptance: %s\n", st.File, st.Desc, st.Accept)
			continue
		}
		fmt.Fprintf(&b, "### %s — %s\n```go\n%s\n```\n", st.File, st.Desc, truncate(st.Content, 800))
	}
	resp, err := a.evaluator.chat(ctx, llm.ChatRequest{
		Messages: a.evaluator.messages(a.sess.Requirement, b.String()),
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("evaluator: %w", err)
	}
	a.sess.AddUsage(resp.Usage)
	a.sess.LLMCalls++
	if resp.Truncated {
		return Verdict{}, fmt.Errorf("evaluator: response truncated by max_tokens")
	}
	// Remember this review so the next one can reference it.
	a.evaluator.history = append(a.evaluator.history, llm.Message{
		Role:    llm.RoleAssistant,
		Content: resp.Message.Content,
	})
	return parseVerdict(resp.Message.Content)
}
