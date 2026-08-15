package sandbox

import "fmt"

// builtinAllowed maps each permitted binary to the subcommands the agent may
// invoke. Everything else is rejected. go build/test/vet/fmt covers the build
// loop; git add/diff/status/log/reset/checkout/init/commit covers snapshotting,
// rollback and inspection. Network or mutating commands (go run, go get,
// git push/fetch/pull) are deliberately absent.
var builtinAllowed = map[string][]string{
	"go":  {"build", "test", "vet", "fmt"},
	"git": {"init", "add", "commit", "reset", "diff", "status", "log", "checkout"},
}

// CheckCommand validates a command against the whitelist. It must be called
// before any execution: a non-whitelisted command never touches the shell. The
// special subcommand "*" (added via AllowCommands) permits ANY invocation of
// that binary — the operator's explicit opt-in for test runners like pytest
// that have no fixed subcommand set.
func (s *Sandbox) CheckCommand(name string, args []string) error {
	subs, ok := s.allowedFor(name)
	if !ok {
		return fmt.Errorf("sandbox: command %q is not allowed", name)
	}
	for _, sub := range subs {
		if sub == "*" {
			return nil
		}
	}
	if len(args) == 0 {
		return fmt.Errorf("sandbox: command %q requires a subcommand", name)
	}
	for _, sub := range subs {
		if args[0] == sub {
			return nil
		}
	}
	return fmt.Errorf("sandbox: subcommand %q is not allowed for %q", args[0], name)
}

// allowedFor returns the per-instance allowlist entry for name, falling back to
// the built-in Go+git set when no AllowCommands call has been made. A bare
// &Sandbox{Root: ...} therefore behaves exactly like the historical sandbox.
func (s *Sandbox) allowedFor(name string) ([]string, bool) {
	if s.allowed != nil {
		subs, ok := s.allowed[name]
		return subs, ok
	}
	subs, ok := builtinAllowed[name]
	return subs, ok
}

// AllowCommands extends this sandbox's command allowlist with operator-approved
// binaries (P20: the multi-language escape hatch). A value of "*" permits any
// invocation; otherwise only the listed subcommands become permitted, MERGED
// into the existing set — so the strict Go+git default is never silently
// weakened. nil extra is a no-op.
func (s *Sandbox) AllowCommands(extra map[string][]string) {
	if len(extra) == 0 {
		return
	}
	if s.allowed == nil {
		s.allowed = map[string][]string{}
		for name, subs := range builtinAllowed {
			s.allowed[name] = subs
		}
	}
	for name, subs := range extra {
		cur := s.allowed[name]
		for _, sub := range subs {
			if !containsSub(cur, sub) {
				cur = append(cur, sub)
			}
		}
		s.allowed[name] = cur
	}
}

func containsSub(subs []string, sub string) bool {
	for _, s := range subs {
		if s == sub {
			return true
		}
	}
	return false
}
