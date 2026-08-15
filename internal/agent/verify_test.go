package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pickPortForAgent(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// brokenServer reproduces the mytest-go bug: it rewrites "/" to "/index.html",
// which trips http.FileServer's canonical `/index.html → ./` redirect into an
// infinite loop. The P23 deliverable gate must catch this and route to repair
// even though go test reports "[no test files]" and exits 0.
const brokenServer = `package main

import (
	"net/http"
)

const serverAddr = "127.0.0.1:PORT"

func main() {
	fs := http.FileServer(http.Dir("static"))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			r.URL.Path = "/index.html"
		}
		fs.ServeHTTP(w, r)
	})
	http.ListenAndServe(serverAddr, nil)
}
`

// fixedServer serves an inline 200 page — the round-2 repair that passes the
// smoke probe (GET / returns 200, no external assets to chase).
const fixedServer = `package main

import (
	"io"
	"net/http"
)

const serverAddr = "127.0.0.1:PORT"

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body>calculator</body></html>")
	})
	http.ListenAndServe(serverAddr, nil)
}
`

// TestDeliverableVerifyRoutesToRepair proves the P23 closed loop end-to-end
// on a deterministic mock script: round 1 the executor writes a broken server
// (go test passes emptily), the deliverable gate boots it and reports the
// redirect loop, the parent repairs, round 2 writes a fixed server, the gate
// goes green, the run ends Done. LLM calls: planner(1) + executor(1) +
// planner(2) + executor(2) = 4. No real LLM, no external tokens.
func TestDeliverableVerifyRoutesToRepair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module smoke.test\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	port := pickPortForAgent(t)
	broken := strings.ReplaceAll(brokenServer, "PORT", port)
	fixed := strings.ReplaceAll(fixedServer, "PORT", port)

	script := []string{
		`{"steps":[{"file":"main.go","desc":"write the calculator server","accept":"GET / returns 200"}]}`,
		`{"steps":[{"file":"main.go","content":` + strconv.Quote(broken) + `,"desc":"write the server"}]}`,
		`{"steps":[{"file":"main.go","desc":"fix the server: GET / must return 200","accept":"GET / returns 200"}]}`,
		`{"steps":[{"file":"main.go","content":` + strconv.Quote(fixed) + `,"desc":"fix the server"}]}`,
	}
	a := New(newMockChat(script), Config{
		RepoPath:    dir,
		Requirement: "Build a calculator web server at 127.0.0.1:" + port + ".",
		MaxRounds:   2,
		RunTimeout:  60 * time.Second,
		Executor:    true,
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected run to pass after repair, state=%s note=%s", res.State, res.Note)
	}
	if res.LLMCalls != 4 {
		t.Errorf("LLMCalls = %d, want 4 (2 planner + 2 executor)", res.LLMCalls)
	}
	if res.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2 (one repair round)", res.Rounds)
	}
	// The gate must have fired on the broken server in round 1.
	var sawVerify bool
	for _, ev := range res.Events {
		if ev.Tool == "verify" {
			sawVerify = true
			if !strings.Contains(ev.Detail, "deliverable verification failed") {
				t.Errorf("verify event should mention deliverable failure, got: %s", ev.Detail)
			}
			if !strings.Contains(ev.Detail, "redirect") {
				t.Errorf("verify event should carry the redirect-loop detail, got: %s", ev.Detail)
			}
		}
	}
	if !sawVerify {
		t.Error("expected a verify event when the broken server was caught")
	}
	// The fixed server must be on disk (round-2 executor wrote it).
	b, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "w.Header().Set") {
		t.Errorf("expected the fixed server on disk, got:\n%s", b)
	}
}

// TestUnrelatedEditSkipsVerify is the false-positive guard at the agent level:
// the workspace contains a server — even a broken one with the 301-loop bug the
// P23 gate exists to catch — but the task only touches a library file. The
// deliverable gate must NO-OP (touched = [lib.go], no server produced), the run
// must finish Done on round 1, and the pre-existing server must stay untouched.
// With whole-workspace verification this fixture would boot the broken server,
// report a redirect loop, and manufacture a repair round for a task that had
// nothing to do with it — exactly the failure the scoping removes.
func TestUnrelatedEditSkipsVerify(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module smoke.test\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	port := pickPortForAgent(t)
	// A pre-existing broken server the task never touches (port is never bound —
	// the gate must not even boot it). The task's lib lives in its own package so
	// the workspace has valid Go (one package per directory).
	server := strings.ReplaceAll(brokenServer, "PORT", port)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(server), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "calc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calc", "lib.go"), []byte("package calc\nfunc Add(a, b int) int { return a + b + 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := []string{
		`{"steps":[{"file":"calc/lib.go","desc":"fix the Add function","accept":"Add(a,b) returns a+b"}]}`,
		`{"steps":[{"file":"calc/lib.go","content":` + strconv.Quote("package calc\nfunc Add(a, b int) int { return a + b }") + `,"desc":"fix Add"}]}`,
	}
	a := New(newMockChat(script), Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function so it returns a+b.",
		MaxRounds:   2,
		RunTimeout:  60 * time.Second,
		Executor:    true,
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected the library-only task to pass without repair, state=%s note=%s", res.State, res.Note)
	}
	if res.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1 (no repair round manufactured by an unrelated server)", res.Rounds)
	}
	for _, ev := range res.Events {
		if ev.Tool == "verify" {
			t.Errorf("unexpected verify event: the pre-existing server must not be booted for a library-only task:\n%s", ev.Detail)
		}
	}
	// The pre-existing server was never rewritten by a bogus repair round.
	b, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "r.URL.Path = \"/index.html\"") {
		t.Errorf("the untouched server must stay as-is, got:\n%s", b)
	}
}

// TestRunTurnServerScoping: the REPL's follow-up turn must NOT re-verify a
// server a previous turn built. Turn 1 produces a working server (the gate
// boot-probes it and goes green); turn 2 is an unrelated library edit — its
// diff against the turn-start commit is [calc/lib.go], no server was touched,
// so the gate no-ops and no verify event is emitted. Without per-turn scoping,
// turn 2 would boot the turn-1 server and could manufacture a fake failure on
// an unrelated request.
func TestRunTurnServerScoping(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module smoke.test\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "calc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calc", "lib.go"), []byte("package calc\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	port := pickPortForAgent(t)
	server := strings.ReplaceAll(fixedServer, "PORT", port)
	planServer := `{"steps":[{"file":"main.go","content":` + strconv.Quote(server) + `,"desc":"write the calculator server"}]}`
	planLib := `{"steps":[{"file":"calc/lib.go","content":"package calc\nfunc Sub(a, b int) int { return a - b }\n","desc":"add Sub"}]}`

	a := New(newMockChat([]string{planServer, planLib}), Config{
		RepoPath:   dir,
		Requirement: "first turn",
		MaxRounds:  3,
		RunTimeout: 60 * time.Second,
	})
	r1 := a.RunTurn(context.Background(), "Build a calculator web server at 127.0.0.1:"+port+".")
	if !r1.Passed() {
		t.Fatalf("turn 1 expected pass, state=%s note=%s", r1.State, r1.Note)
	}
	r2 := a.RunTurn(context.Background(), "Add a Sub function to calc.")
	if !r2.Passed() {
		t.Fatalf("turn 2 expected pass, state=%s note=%s", r2.State, r2.Note)
	}
	for _, ev := range r2.Events {
		if ev.Tool == "verify" {
			t.Errorf("turn 2 must not re-verify the turn-1 server, got verify event:\n%s", ev.Detail)
		}
	}
}
