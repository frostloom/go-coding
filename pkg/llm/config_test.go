package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnvFile(t *testing.T, root, content string) string {
	t.Helper()
	p := filepath.Join(root, "envfile")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadEnvFileSetsUnset: KEY=VALUE lines are loaded, comments and blank
// lines skipped, values trimmed.
func TestLoadEnvFileSetsUnset(t *testing.T) {
	root := t.TempDir()
	p := writeEnvFile(t, root, "# comment\n\nOPENAI_API_KEY=sk-test-123\n  OPENAI_MODEL = gpt-5.6-luna  \nBADLINE\n")
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("OPENAI_MODEL")
	t.Cleanup(func() {
		os.Unsetenv("OPENAI_API_KEY")
		os.Unsetenv("OPENAI_MODEL")
	})

	LoadEnvFile(p)
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-test-123" {
		t.Errorf("API key not loaded: %q", got)
	}
	if got := os.Getenv("OPENAI_MODEL"); got != "gpt-5.6-luna" {
		t.Errorf("model not loaded (or not trimmed): %q", got)
	}
}

// TestLoadEnvFileDoesNotClobber: a key already in the environment wins over the
// file — the .env is sugar, never an override.
func TestLoadEnvFileDoesNotClobber(t *testing.T) {
	root := t.TempDir()
	p := writeEnvFile(t, root, "OPENAI_API_KEY=sk-from-file\n")
	os.Setenv("OPENAI_API_KEY", "sk-from-shell")
	t.Cleanup(func() { os.Unsetenv("OPENAI_API_KEY") })

	LoadEnvFile(p)
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-from-shell" {
		t.Errorf("file overrode the shell env var: %q", got)
	}
}

// TestLoadEnvFileEmptyEnvValueWins: a key exported with an EMPTY value is still
// "set" — the file must not fill it. An explicit `export FOO=""` is the
// operator's choice of empty, and .env is only sugar for unset vars. (os.Getenv
// returns "" for both unset and empty-but-set; this is the LookupEnv distinction.)
func TestLoadEnvFileEmptyEnvValueWins(t *testing.T) {
	root := t.TempDir()
	p := writeEnvFile(t, root, "OPENAI_API_KEY=sk-from-file\n")
	t.Setenv("OPENAI_API_KEY", "") // present, but explicitly empty

	LoadEnvFile(p)
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Errorf("file overwrote an empty-but-set env var: %q", got)
	}
}

// TestLoadEnvFileMissingPathIgnored: a missing .env is not an error.
func TestLoadEnvFileMissingPathIgnored(t *testing.T) {
	LoadEnvFile(filepath.Join(t.TempDir(), "does-not-exist"))
}

// TestConfigFromEnvExecutorModel: OPENAI_EXECUTOR_MODEL feeds the per-role
// executor model (P22), empty when unset — planner and executor tiering live in
// the same env config, defaulting to a worker inheriting the planner's model.
func TestConfigFromEnvExecutorModel(t *testing.T) {
	t.Setenv("OPENAI_EXECUTOR_MODEL", "gpt-5.6-terra")
	if got := ConfigFromEnv().ExecutorModel; got != "gpt-5.6-terra" {
		t.Errorf("ExecutorModel = %q, want %q", got, "gpt-5.6-terra")
	}
	t.Setenv("OPENAI_EXECUTOR_MODEL", "")
	if got := ConfigFromEnv().ExecutorModel; got != "" {
		t.Errorf("ExecutorModel = %q, want empty when unset", got)
	}
}
