package llm

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds LLM connection settings. All fields have env-var-driven
// defaults so the binary runs out of the box against a local gateway.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// ExecutorModel is the per-role LLM model executor workers run on in P22
	// hierarchical plan-and-execute (env OPENAI_EXECUTOR_MODEL). Empty means a
	// worker inherits the planner's Model. Like Model, it is a convenience
	// default: the cmd layer lets an explicit -executor-model flag win over it.
	ExecutorModel string
	Timeout       time.Duration
	MaxRetries    int
	Stream        bool
}

// ConfigFromEnv reads configuration from environment variables.
func ConfigFromEnv() Config {
	return Config{
		BaseURL:        envOr("OPENAI_BASE_URL", "http://localhost:11434/v1"),
		APIKey:         os.Getenv("OPENAI_API_KEY"),
		Model:          envOr("OPENAI_MODEL", ""),
		ExecutorModel:  envOr("OPENAI_EXECUTOR_MODEL", ""),
		Timeout:        envDur("LLM_TIMEOUT", 60*time.Second),
		MaxRetries:     envInt("LLM_MAX_RETRIES", 3),
		Stream:         envBool("LLM_STREAM", true),
	}
}

// LoadEnvFile reads KEY=VALUE lines from each path (if it exists) and sets any
// environment variable that is not already set. Real shell env vars always win:
// a key already in the environment is never overwritten, so the root .env is
// convenience sugar for live runs, not a way to clobber an explicit export.
// Missing paths and malformed lines are silently skipped — the file is optional.
// Call it before ConfigFromEnv, e.g. LoadEnvFile(".env").
func LoadEnvFile(paths ...string) {
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			// "Already set" means present in the environment, even when the value
			// is the empty string — an explicit `export FOO=""` is a real choice
			// and must not be clobbered by the file. os.Getenv alone cannot tell
			// empty-but-set from unset, so use LookupEnv.
			if k != "" {
				if _, set := os.LookupEnv(k); !set {
					_ = os.Setenv(k, v)
				}
			}
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
