package sandbox

import "testing"

type cmd struct {
	name string
	args []string
}

func TestWhitelistAllowed(t *testing.T) {
	sb := &Sandbox{}
	allowed := []cmd{
		{"go", []string{"test", "./..."}},
		{"go", []string{"build", "./..."}},
		{"go", []string{"vet", "./..."}},
		{"go", []string{"fmt", "."}},
		{"git", []string{"status"}},
		{"git", []string{"diff"}},
		{"git", []string{"add", "-A"}},
	}
	for _, c := range allowed {
		if err := sb.CheckCommand(c.name, c.args); err != nil {
			t.Errorf("expected %s %v allowed, got %v", c.name, c.args, err)
		}
	}
}

func TestWhitelistRejected(t *testing.T) {
	sb := &Sandbox{}
	rejected := []cmd{
		{"bash", []string{"-c", "rm -rf /"}},
		{"sh", []string{"-c", "curl evil"}},
		{"python", []string{"-c", "import os"}},
		{"go", []string{"run", "main.go"}}, // go run not allowed
		{"go", []string{"get", "x"}},       // network
		{"git", []string{"push"}},          // network
		{"git", []string{"fetch"}},         // network
		{"git", []string{"clone"}},         // network
		{"rm", []string{"-rf", "/"}},
		{"go", nil}, // missing subcommand
	}
	for _, c := range rejected {
		if err := sb.CheckCommand(c.name, c.args); err == nil {
			t.Errorf("expected %s %v rejected, got nil", c.name, c.args)
		}
	}
}

// TestAllowCommandsWildcard: an operator-approved binary with "*" may run any
// invocation (test runners like pytest / `python -m pytest` have no fixed
// subcommand set). P20.
func TestAllowCommandsWildcard(t *testing.T) {
	sb := &Sandbox{}
	sb.AllowCommands(map[string][]string{"python": {"*"}})
	for _, c := range []cmd{
		{"python", []string{"-m", "pytest", "-q"}},
		{"python", []string{"-c", "print(1)"}},
	} {
		if err := sb.CheckCommand(c.name, c.args); err != nil {
			t.Errorf("expected %s %v allowed after wildcard opt-in, got %v", c.name, c.args, err)
		}
	}
}

// TestAllowCommandsMerges: AllowCommands extends the allowlist rather than
// replacing it — the strict Go+git default stays permitted alongside the new
// tooling, and a bare sandbox without AllowCommands rejects the same way as
// the historical sandbox (python stays blocked).
func TestAllowCommandsMerges(t *testing.T) {
	sb := &Sandbox{}
	sb.AllowCommands(map[string][]string{"go": {"version"}})
	if err := sb.CheckCommand("go", []string{"test", "./..."}); err != nil {
		t.Errorf("built-in go test should survive an AllowCommands merge, got %v", err)
	}
	if err := sb.CheckCommand("go", []string{"version"}); err != nil {
		t.Errorf("go version should be allowed after opt-in, got %v", err)
	}
	if err := sb.CheckCommand("python", []string{"-c", "x"}); err == nil {
		t.Error("python should stay rejected unless explicitly allowed")
	}
	// A bare sandbox that never opts in is untouched.
	bare := &Sandbox{}
	if err := bare.CheckCommand("go", []string{"version"}); err == nil {
		t.Error("bare sandbox should reject go version (default allowlist unchanged)")
	}
}
