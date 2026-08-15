package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPermissionGateDenyThenAllow is the interactive flow: the operator denies
// the first edit, the agent repairs and re-plans, the second edit is allowed,
// and tests run. The gate must be consulted for every edit and for the test.
func TestPermissionGateDenyThenAllow(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	plan1 := `{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`
	mock := newMockChat([]string{plan1, plan1})
	var calls []PermissionAction
	var gates = []bool{false, true, true} // edit denied → edit allowed → test allowed
	i := 0
	a := New(mock, Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function.",
		MaxRounds:   3,
		Confirm: func(pa PermissionAction) bool {
			calls = append(calls, pa)
			ok := gates[i%len(gates)]
			i++
			return ok
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("run failed after deny→allow: %s", res.Note)
	}
	if len(calls) != 3 {
		t.Fatalf("permission gate consulted %d times, want 3 (2 edits + 1 test)", len(calls))
	}
	if calls[0].Op != "edit_file" || calls[1].Op != "edit_file" || calls[2].Op != "run_test" {
		t.Fatalf("gate order = %+v, want edit_file, edit_file, run_test", calls)
	}
	// The denied first attempt never touched disk — the approved second plan did.
	src, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "return a + b") {
		t.Fatalf("calc.go was not edited after approval: %q", src)
	}
}

// TestPermissionGateDenyEverything verifies a fully-restrictive gate blocks
// every edit and the run eventually fails via MaxRounds — never a silent pass.
func TestPermissionGateDenyEverything(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	plan1 := `{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`
	mock := newMockChat([]string{plan1})
	a := New(mock, Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function.",
		MaxRounds:   2,
		Confirm: func(PermissionAction) bool {
			return false
		},
	})
	res := a.Run(context.Background())
	if res.Passed() {
		t.Fatal("run passed with every edit denied")
	}
	if !strings.Contains(res.TestOutput, "denied") {
		t.Fatalf("testOutput = %q, want a denied marker", res.TestOutput)
	}
}
